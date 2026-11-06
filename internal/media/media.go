// Package media stores binary attachments (currently images) referenced by the
// conversation timeline.
//
// QQ serves attachments from a URL whose rkey expires, so the bytes are fetched
// once at ingest and kept in SQLite (or PostgreSQL, once DB_PATH is a DSN). The
// admin panel then serves them from /api/media/{id}, which keeps old
// conversations looking intact.
//
// The package owns its *sql.DB directly instead of going through
// internal/store's helpers, so it carries its own copy of the bind-placeholder
// dialect (see dialect.go): every statement is written with "?" and rewritten
// with rebind before it reaches the driver.
package media

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	pgxstdlib "github.com/jackc/pgx/v5/stdlib" // driver type used by detectDriver
)

// ErrTooLarge is returned when a download exceeds the configured cap.
var ErrTooLarge = errors.New("media: attachment exceeds the size limit")

// ErrUnsupportedType is returned for content types we refuse to store.
var ErrUnsupportedType = errors.New("media: unsupported content type")

// Store persists media blobs.
//
// The dialect field decides how "?" placeholders are spelled. Its zero value
// means SQLite, so a hand-built &Store{} — or one whose dialect was never
// resolved — keeps the historical "?" behaviour instead of emitting "$1".
type Store struct {
	db      *sql.DB
	dialect dialect
}

// NewStore wraps an existing database handle, detecting the placeholder dialect
// from the driver the handle was opened with.
//
// Detection exists so the historical wiring — media.NewStore(st.DB()),
// written before this package knew about PostgreSQL — keeps working unchanged
// when store.Open was handed a postgres:// DSN; without it that call would keep
// emitting "?" into pgx and every statement would fail with SQLSTATE 42601.
// Only the two drivers the harness registers are recognised: pgx (PostgreSQL)
// and everything else, including modernc.org/sqlite, maps to SQLite.
//
// NewStoreWithDriver / SetDialect remain the authoritative way to state the
// dialect explicitly; they override whatever was detected.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, dialect: newDialect(detectDriver(db))}
}

// detectDriver names the dialect a handle speaks by looking at its driver.
//
// database/sql hands back the driver.Driver value that was registered, so a
// type assertion is enough and no DSN or connection is involved. A nil handle,
// a nil driver or an unknown driver all fall back to SQLite, which is both the
// historical default and the safer one: "?" on SQLite works, "?" on PostgreSQL
// fails loudly at the first statement rather than silently corrupting anything.
func detectDriver(db *sql.DB) string {
	if db == nil {
		return DriverSQLite
	}
	switch db.Driver().(type) {
	case *pgxstdlib.Driver:
		return DriverPostgres
	default:
		return DriverSQLite
	}
}

// NewStoreWithDriver wraps db and installs the placeholder dialect of driver.
//
// driver is a database/sql driver name; store.DriverSQLite / store.DriverPostgres
// (or anything SetDialect accepts: "pg", "postgresql", "pgx", "pgx/v5") work.
// Unknown or empty names fall back to SQLite, mirroring store.NewDialect.
func NewStoreWithDriver(db *sql.DB, driver string) *Store {
	return &Store{db: db, dialect: newDialect(driver)}
}

// SetDialect installs the placeholder dialect for driver.
//
// It is the media counterpart of store.(*Store).SetDialect: call it right after
// construction (or after sql.Open) and before the first query, so every
// statement that follows is rewritten consistently. An empty driver keeps the
// current dialect, so a wiring bug cannot silently switch a live store back to
// "?" markers.
func (s *Store) SetDialect(driver string) {
	if strings.TrimSpace(driver) == "" {
		return
	}
	s.dialect = newDialect(driver)
}

// DB returns the handle the store writes through, for callers that need to run
// their own statement on the same connection pool.
func (s *Store) DB() *sql.DB { return s.db }

// Driver reports the dialect this store speaks ("sqlite" or "postgres").
func (s *Store) Driver() string {
	if s.dialect.isPostgres() {
		return DriverPostgres
	}
	return DriverSQLite
}

// Placeholder rewrites query for this store's dialect. It is exported so callers
// that build their own SQL against the same handle stay consistent.
func (s *Store) Placeholder(query string) string { return s.dialect.rebind(query) }

// execContext / queryContext / queryRowContext mirror the wrappers in
// internal/store/dialect.go: every statement in this package goes through them,
// so the dialect is applied in exactly one place.
func (s *Store) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.dialect.rebind(query), args...)
}

func (s *Store) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.dialect.rebind(query), args...)
}

func (s *Store) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.dialect.rebind(query), args...)
}

// Record is one stored blob.
type Record struct {
	ID          string
	ContentType string
	Size        int64
	Data        []byte
	CreatedAt   string
}

// Downloader fetches attachments from the platform CDN.
type Downloader struct {
	http *http.Client
	// AllowedHostSuffixes restricts where we will fetch from. Attachment URLs
	// come from the platform events, not from user input, but an allowlist keeps
	// a malformed or hostile event from turning the bot into a fetcher.
	AllowedHostSuffixes []string
	MaxBytes            int64
	Timeout             time.Duration
}

// NewDownloader builds a downloader with sane defaults.
func NewDownloader(hc *http.Client, maxBytes int64, timeout time.Duration) *Downloader {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	if maxBytes <= 0 {
		maxBytes = 4 << 20
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Downloader{
		http:                hc,
		AllowedHostSuffixes: []string{"qq.com.cn", "qq.com", "qpic.cn", "gtimg.cn"},
		MaxBytes:            maxBytes,
		Timeout:             timeout,
	}
}

// Fetch downloads url after validating the host and content type.
func (d *Downloader) Fetch(ctx context.Context, rawURL string) (contentType string, data []byte, err error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", nil, fmt.Errorf("media: bad attachment url: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", nil, fmt.Errorf("media: refusing scheme %q", parsed.Scheme)
	}
	if !d.hostAllowed(parsed.Hostname()) {
		return "", nil, fmt.Errorf("media: refusing host %q", parsed.Hostname())
	}

	if d.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("media: HTTP %d", resp.StatusCode)
	}

	// Read one byte past the cap so an oversized body is detected, not silently
	// truncated into a corrupt image.
	limited := io.LimitReader(resp.Body, d.MaxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", nil, err
	}
	if int64(len(body)) > d.MaxBytes {
		return "", nil, ErrTooLarge
	}

	contentType = strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = http.DetectContentType(body)
	}
	if !strings.HasPrefix(contentType, "image/") {
		return "", nil, fmt.Errorf("%w: %s", ErrUnsupportedType, contentType)
	}
	return contentType, body, nil
}

func (d *Downloader) hostAllowed(host string) bool {
	host = strings.ToLower(host)
	for _, suffix := range d.AllowedHostSuffixes {
		suffix = strings.ToLower(strings.TrimSpace(suffix))
		if suffix == "" {
			continue
		}
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// Put stores a blob and returns its content-addressed id.
//
// The id is the sha256 of the bytes, so re-uploading the same image is a no-op
// and the admin can cache it forever.
func (s *Store) Put(ctx context.Context, contentType string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	_, err := s.execContext(ctx, `
INSERT INTO media (id, content_type, size, sha256, data, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`, id, contentType, int64(len(data)), id, data, time.Now().Format(time.RFC3339))
	if err != nil {
		return "", err
	}
	return id, nil
}

// Get returns the stored content type and bytes of one blob.
func (s *Store) Get(ctx context.Context, id string) (string, []byte, error) {
	var (
		contentType string
		data        []byte
	)
	err := s.queryRowContext(ctx,
		`SELECT content_type, data FROM media WHERE id = ?`, id).Scan(&contentType, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, sql.ErrNoRows
	}
	if err != nil {
		return "", nil, err
	}
	return contentType, data, nil
}

// Stats reports the number of blobs and the bytes they occupy.
func (s *Store) Stats(ctx context.Context) (count int, bytes int64, err error) {
	err = s.queryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM media`).Scan(&count, &bytes)
	return count, bytes, err
}

// pruneBatchSize bounds how many rows are held in memory at once: the messages
// scan (attachments are a few hundred bytes each) and the expired-media delete
// both page at this size, so Prune never builds an unbounded IN (...) list —
// SQLite caps bind variables at 32766 and PostgreSQL's wire protocol at 65535,
// and a long-lived deployment could otherwise reach either.
const pruneBatchSize = 500

// Prune drops blobs older than retention that are no longer referenced by any
// message, keeping storage bounded.
//
// The referenced ids are collected in Go rather than in SQL: SQLite reads the
// attachments JSON with json1 (json_each / json_extract) while PostgreSQL would
// need jsonb_array_elements / jsonb_path_query, and the two disagree on a bad
// document (json_extract yields NULL, jsonb raises). Doing the parse here keeps
// one code path that behaves identically on both backends, and the only SQL
// left is a plain "id IN (...)" both engines understand.
//
// messages.attachments is the JSON array written by store.AppendMessageID. It is
// an empty string for the vast majority of rows (no attachments) and may in
// principle hold anything a future migration left behind, so empty text and
// unparseable documents are skipped instead of failing the whole prune.
//
// The delete also pages: expired rows are read in id order, filtered against the
// referenced set in Go, and removed a batch at a time. A message that starts
// referencing a blob in the microseconds between the two passes can still lose
// it — the same race the previous single-statement DELETE had, and it is bounded
// by the fact that a reference to an already-expired blob is itself stale.
func (s *Store) Prune(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention).Format(time.RFC3339)

	referenced, err := s.referencedMediaIDs(ctx)
	if err != nil {
		return 0, err
	}

	// Fast path: nothing references any blob, so one cutoff-only DELETE clears
	// every expired row without building an id list at all.
	if len(referenced) == 0 {
		res, err := s.execContext(ctx, `DELETE FROM media WHERE created_at < ?`, cutoff)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}

	var deleted int64
	cursor := ""
	for {
		expired, err := s.expiredMediaIDs(ctx, cutoff, cursor, pruneBatchSize)
		if err != nil {
			return deleted, err
		}
		if len(expired) == 0 {
			break
		}
		cursor = expired[len(expired)-1]

		doomed := make([]string, 0, len(expired))
		for _, id := range expired {
			if _, live := referenced[id]; !live {
				doomed = append(doomed, id)
			}
		}
		if len(doomed) == 0 {
			continue
		}

		// One placeholder per id, written positionally: the dialect turns them
		// into $1..$n on PostgreSQL. The ids themselves only ever travel as bind
		// arguments, never as interpolated text.
		marks := make([]string, len(doomed))
		args := make([]any, 0, len(doomed))
		for i, id := range doomed {
			marks[i] = "?"
			args = append(args, id)
		}
		res, err := s.execContext(ctx,
			`DELETE FROM media WHERE id IN (`+strings.Join(marks, ", ")+`)`, args...)
		if err != nil {
			return deleted, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return deleted, err
		}
		deleted += n
	}
	return deleted, nil
}

// expiredMediaIDs returns up to limit media ids older than cutoff whose id sorts
// after cursor, in ascending id order. The id cursor (rather than OFFSET) keeps
// the scan stable while the caller deletes the rows it already read.
func (s *Store) expiredMediaIDs(ctx context.Context, cutoff, cursor string, limit int) ([]string, error) {
	rows, err := s.queryContext(ctx, `
SELECT id FROM media
WHERE created_at < ? AND id > ?
ORDER BY id
LIMIT ?`, cutoff, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("media: scan expired: %w", err)
	}
	defer rows.Close()

	out := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("media: scan expired: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("media: scan expired: %w", err)
	}
	return out, nil
}

// referencedMediaIDs walks messages.attachments page by page and returns the
// set of media ids that at least one message still points at.
//
// Paging is keyed on the integer messages.id rather than OFFSET so a concurrent
// insert cannot make a row be skipped; prune and ingest racing is harmless
// either way, but a stable cursor keeps the scan honest.
//
// The result is a set because the same blob is routinely referenced from
// several messages (a re-sent image) and the caller only asks "is it live?".
func (s *Store) referencedMediaIDs(ctx context.Context) (map[string]struct{}, error) {
	seen := make(map[string]struct{})
	lastID := int64(-1 << 62)

	for {
		rows, err := s.queryContext(ctx, `
SELECT id, attachments FROM messages
WHERE id > ? AND attachments <> ''
ORDER BY id
LIMIT ?`, lastID, pruneBatchSize)
		if err != nil {
			return nil, fmt.Errorf("media: scan attachments: %w", err)
		}

		batch := 0
		for rows.Next() {
			var (
				id          int64
				attachments string
			)
			if err := rows.Scan(&id, &attachments); err != nil {
				rows.Close()
				return nil, fmt.Errorf("media: scan attachments: %w", err)
			}
			batch++
			lastID = id
			addMediaIDs(seen, attachments)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("media: scan attachments: %w", err)
		}
		rows.Close()

		if batch < pruneBatchSize {
			break
		}
	}

	return seen, nil
}

// addMediaIDs parses one attachments column and records every non-empty
// media_id it contains.
//
// A row that does not parse is skipped: the blob it might reference then looks
// unreferenced, but a corrupt attachments column must not stop housekeeping.
func addMediaIDs(seen map[string]struct{}, attachments string) {
	attachments = strings.TrimSpace(attachments)
	if attachments == "" || attachments == "null" {
		return
	}
	var items []struct {
		MediaID string `json:"media_id"`
	}
	if err := json.Unmarshal([]byte(attachments), &items); err != nil {
		return
	}
	for _, item := range items {
		if id := strings.TrimSpace(item.MediaID); id != "" {
			seen[id] = struct{}{}
		}
	}
}
