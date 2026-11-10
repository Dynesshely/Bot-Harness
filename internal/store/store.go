// Package store persists conversations, message history, audit records,
// deduplication keys, runtime settings overrides and statistics in SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, keeps the container cgo-free
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store owns the SQLite handle.
type Store struct {
	db   *sql.DB
	path string
	// dialect decides how bind placeholders are spelled. The zero value is
	// treated as SQLite, so a Store that never had SetDialect called on it
	// keeps the historical "?" behaviour.
	dialect dialect
}

// Open creates or opens the database at path and applies the schema.
//
// The signature is unchanged, but path now selects the backend: a value starting
// with postgres:// or postgresql:// is a PostgreSQL DSN (see pg.go), and anything
// else keeps the historical SQLite behaviour — same file, same PRAGMAs, same
// single connection. Existing deployments and existing tests therefore stay on
// the exact code path they were on before.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	if isPostgresDSN(path) {
		return openPostgres(path)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create data dir: %w", err)
		}
	}

	dsn := "file:" + path + "?" + url.Values{
		"_pragma": []string{
			"busy_timeout(5000)",
			"journal_mode(WAL)",
			"synchronous(NORMAL)",
			"foreign_keys(1)",
		},
	}.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// A QQ bot writes far less than SQLite can handle; serialising access
	// removes every SQLITE_BUSY failure mode at negligible cost.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db, path: path}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	// SQLite honours the process umask (usually 0644). The file holds chat
	// history and the admin token, so clamp it (and WAL/SHM sidecars) to the
	// owner only, both for a fresh database and for one created by an older
	// build.
	if err := restrictSQLitePermissions(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// restrictSQLitePermissions makes the database and its sidecars readable and
// writable only by their owner. Missing sidecars are fine: SQLite creates them
// lazily.
func restrictSQLitePermissions(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("store: stat %s: %w", candidate, err)
		}
		if info.Mode().Perm() == 0o600 {
			continue
		}
		if err := os.Chmod(candidate, 0o600); err != nil {
			return fmt.Errorf("store: chmod %s: %w", candidate, err)
		}
	}
	return nil
}

// DB exposes the underlying handle for packages that own their own tables
// (currently the media store).
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Path() string { return s.path }

// Driver names the engine behind this handle: "sqlite" or "postgres". It is
// what the startup log prints, so an operator can confirm at a glance which
// database the process actually opened.
func (s *Store) Driver() string { return s.dialect.Name() }

// Target describes where the handle points and is always safe to log.
//
// For SQLite that is the file path; for PostgreSQL it is the DSN with the
// password replaced by "redacted", because openPostgres masks it before the DSN
// is ever stored on the handle (see redactDSN).
func (s *Store) Target() string { return s.path }

// IsPostgres reports whether this handle speaks PostgreSQL.
func (s *Store) IsPostgres() bool { return s.dialect.IsPostgres() }

// SizeBytes reports how much space the database occupies, in bytes.
//
// SQLite: the main database file (WAL files excluded).
// PostgreSQL: pg_database_size(current_database()), i.e. the on-disk size of
// the current database as the server accounts for it. There is no local file in
// that case, so the file stat is skipped entirely.
//
// Failures return 0 rather than an error: the admin status endpoint renders a
// missing number as "unknown", and a size probe must never fail a request.
func (s *Store) SizeBytes() int64 {
	if s.IsPostgres() {
		return s.postgresSizeBytes()
	}
	fi, err := os.Stat(s.path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

const schema = `
CREATE TABLE IF NOT EXISTS conversations (
  id             TEXT PRIMARY KEY,
  scene          TEXT NOT NULL,
  group_openid   TEXT NOT NULL DEFAULT '',
  member_openid  TEXT NOT NULL DEFAULT '',
  group_name     TEXT NOT NULL DEFAULT '',
  member_name    TEXT NOT NULL DEFAULT '',
  enabled        INTEGER NOT NULL DEFAULT 1,
  message_count  INTEGER NOT NULL DEFAULT 0,
  turn_count     INTEGER NOT NULL DEFAULT 0,
  token_estimate INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL,
  last_active_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversations_last_active ON conversations(last_active_at DESC);
CREATE INDEX IF NOT EXISTS idx_conversations_group ON conversations(group_openid);

CREATE TABLE IF NOT EXISTS messages (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL,
  role            TEXT NOT NULL,
  content         TEXT NOT NULL,
  tokens          INTEGER NOT NULL DEFAULT 0,
  kind            TEXT NOT NULL DEFAULT 'dialogue',
  speaker         TEXT NOT NULL DEFAULT '',
  speaker_openid  TEXT NOT NULL DEFAULT '',
  quote_index     TEXT NOT NULL DEFAULT '',
  attachments     TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, id);
CREATE INDEX IF NOT EXISTS idx_messages_quote_index ON messages(quote_index);

CREATE TABLE IF NOT EXISTS records (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  direction       TEXT NOT NULL,
  scene           TEXT NOT NULL DEFAULT '',
  conversation_id TEXT NOT NULL DEFAULT '',
  group_openid    TEXT NOT NULL DEFAULT '',
  member_openid   TEXT NOT NULL DEFAULT '',
  member_name     TEXT NOT NULL DEFAULT '',
  content         TEXT NOT NULL DEFAULT '',
  msg_id          TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT '',
  error           TEXT NOT NULL DEFAULT '',
  latency_ms      INTEGER NOT NULL DEFAULT 0,
  delivery        TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_records_created ON records(id DESC);
CREATE INDEX IF NOT EXISTS idx_records_direction ON records(direction, id DESC);
CREATE INDEX IF NOT EXISTS idx_records_group ON records(group_openid, id DESC);

CREATE TABLE IF NOT EXISTS tool_calls (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL DEFAULT '',
  group_openid    TEXT NOT NULL DEFAULT '',
  member_openid   TEXT NOT NULL DEFAULT '',
  tool            TEXT NOT NULL DEFAULT '',
  args            TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT '',
  error           TEXT NOT NULL DEFAULT '',
  latency_ms      INTEGER NOT NULL DEFAULT 0,
  result_chars    INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tool_calls_created ON tool_calls(id DESC);
CREATE INDEX IF NOT EXISTS idx_tool_calls_group ON tool_calls(group_openid, id DESC);

CREATE TABLE IF NOT EXISTS memories (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  scope         TEXT NOT NULL DEFAULT '',
  group_openid  TEXT NOT NULL DEFAULT '',
  member_openid TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL DEFAULT '',
  text          TEXT NOT NULL,
  source        TEXT NOT NULL DEFAULT '',
  salience      REAL NOT NULL DEFAULT 1,
  hits          INTEGER NOT NULL DEFAULT 0,
  last_used_at  TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  UNIQUE (scope, group_openid, member_openid, text)
);
CREATE INDEX IF NOT EXISTS idx_memories_scope ON memories(scope, group_openid, member_openid, id DESC);

CREATE TABLE IF NOT EXISTS proactive_state (
  group_openid  TEXT PRIMARY KEY,
  last_sent_at  TEXT NOT NULL DEFAULT '',
  silent_streak INTEGER NOT NULL DEFAULT 0,
  sends         INTEGER NOT NULL DEFAULT 0,
  last_reason   TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS scheduled_tasks (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL DEFAULT '',
  group_openid    TEXT NOT NULL DEFAULT '',
  member_openid   TEXT NOT NULL DEFAULT '',
  creator_openid  TEXT NOT NULL DEFAULT '',
  kind            TEXT NOT NULL DEFAULT 'reminder',
  text            TEXT NOT NULL,
  due_at          TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'pending',
  attempts        INTEGER NOT NULL DEFAULT 0,
  lease_until     TEXT NOT NULL DEFAULT '',
  error           TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  sent_at         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_tasks_due ON scheduled_tasks(status, due_at);
-- creator_openid 是 0009 迁移才加的列，索引必须建在迁移里：
-- 老库上基础 schema 会先于迁移执行，此时建索引会直接报 no such column。

CREATE TABLE IF NOT EXISTS commitments (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL DEFAULT '',
  group_openid    TEXT NOT NULL DEFAULT '',
  member_openid   TEXT NOT NULL DEFAULT '',
  creator_openid  TEXT NOT NULL DEFAULT '',
  text            TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'waiting',
  priority        INTEGER NOT NULL DEFAULT 0,
  deadline_at     TEXT NOT NULL DEFAULT '',
  next_check_at   TEXT NOT NULL,
  last_nudge_at   TEXT NOT NULL DEFAULT '',
  nudge_count     INTEGER NOT NULL DEFAULT 0,
  ack_at          TEXT NOT NULL DEFAULT '',
  ack_note        TEXT NOT NULL DEFAULT '',
  meta            TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_commitments_due ON commitments(status, next_check_at);
CREATE INDEX IF NOT EXISTS idx_commitments_creator ON commitments(creator_openid, status);

CREATE TABLE IF NOT EXISTS human_status (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  scope           TEXT NOT NULL,
  conversation_id TEXT NOT NULL,
  group_openid    TEXT NOT NULL DEFAULT '',
  member_openid   TEXT NOT NULL,
  display_name    TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL,
  confidence      REAL NOT NULL DEFAULT 0,
  evidence        TEXT NOT NULL DEFAULT '',
  updated_at      TEXT NOT NULL,
  expires_at      TEXT NOT NULL,
  UNIQUE (scope, conversation_id, member_openid)
);

CREATE TABLE IF NOT EXISTS affect_states (
  conversation_id TEXT PRIMARY KEY,
  pack_id         TEXT NOT NULL DEFAULT 'daily',
  dims            TEXT NOT NULL DEFAULT '{}',
  events          TEXT NOT NULL DEFAULT '[]',
  paused_until    TEXT NOT NULL DEFAULT '',
  ticked_at       TEXT NOT NULL DEFAULT '',
  updated_at      TEXT NOT NULL,
  version         INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS affect_events (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL,
  event_id        TEXT NOT NULL,
  tone            TEXT NOT NULL DEFAULT '',
  source          TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_affect_events_conversation ON affect_events(conversation_id, created_at DESC);

CREATE TABLE IF NOT EXISTS affect_history (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL,
  at              TEXT NOT NULL,
  dims            TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_affect_history_conversation ON affect_history(conversation_id, at DESC);

CREATE TABLE IF NOT EXISTS join_decisions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  group_openid    TEXT NOT NULL DEFAULT '',
  conversation_id TEXT NOT NULL DEFAULT '',
  message_id      TEXT NOT NULL DEFAULT '',
  speaker         TEXT NOT NULL DEFAULT '',
  preview         TEXT NOT NULL DEFAULT '',
  joined          INTEGER NOT NULL DEFAULT 0,
  action          TEXT NOT NULL DEFAULT '',
  confidence      REAL NOT NULL DEFAULT 0,
  reason          TEXT NOT NULL DEFAULT '',
  topics          TEXT NOT NULL DEFAULT '',
  model           TEXT NOT NULL DEFAULT '',
  latency_ms      INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_join_decisions_group ON join_decisions(group_openid, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_join_decisions_conversation ON join_decisions(conversation_id);

CREATE TABLE IF NOT EXISTS join_topics (
  group_openid TEXT PRIMARY KEY,
  topics       TEXT NOT NULL DEFAULT '',
  analyzed_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS dedup (
  msg_id     TEXT PRIMARY KEY,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_dedup_created ON dedup(created_at);

CREATE TABLE IF NOT EXISTS settings (
  k          TEXT PRIMARY KEY,
  v          TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS media (
  id           TEXT PRIMARY KEY,
  content_type TEXT NOT NULL,
  size         INTEGER NOT NULL,
  sha256       TEXT NOT NULL,
  data         BLOB NOT NULL,
  created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_media_created ON media(created_at);

CREATE TABLE IF NOT EXISTS schema_migrations (
  name       TEXT PRIMARY KEY,
  applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS stats (
  bucket         TEXT PRIMARY KEY,
  in_count       INTEGER NOT NULL DEFAULT 0,
  out_count      INTEGER NOT NULL DEFAULT 0,
  errors         INTEGER NOT NULL DEFAULT 0,
  latency_sum_ms INTEGER NOT NULL DEFAULT 0,
  latency_count  INTEGER NOT NULL DEFAULT 0,
  tokens         INTEGER NOT NULL DEFAULT 0
);
`

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.execContext(ctx, schema); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	// CREATE TABLE IF NOT EXISTS does not add columns to an existing table, and
	// some fixes must run exactly once (for example the speaker backfill), so
	// schema changes go through named, recorded migrations.
	return s.runMigrations(ctx)
}

// runMigrations applies each pending migration exactly once.
func (s *Store) runMigrations(ctx context.Context) error {
	migrations := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"0001_messages_kind_speaker", s.migrationMessagesKindSpeaker},
		{"0002_backfill_message_speakers", s.backfillSpeakers},
		{"0003_backfill_shared_speakers", s.backfillSharedSpeakers},
		{"0004_messages_attachments", s.migrationMessagesAttachments},
		{"0005_messages_speaker_openid", s.migrationMessagesSpeakerOpenID},
		{"0006_backfill_message_speaker_openid", s.backfillMessageSpeakerOpenID},
		{"0007_records_delivery", s.migrationRecordsDelivery},
		{"0008_messages_quote_index", s.migrationMessagesQuoteIndex},
		{"0009_tasks_creator", s.migrationTasksCreator},
		{"0010_messages_addressed", s.migrationMessagesAddressed},
		{"0011_commitments", s.migrationCommitments},
		{"0012_human_status", s.migrationHumanStatus},
		{"0013_affect", s.migrationAffect},
		{"0014_join_gate", s.migrationJoinGate},
	}
	for _, m := range migrations {
		var applied int
		if err := s.queryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, m.name).Scan(&applied); err != nil {
			return fmt.Errorf("store: check migration %s: %w", m.name, err)
		}
		if applied > 0 {
			continue
		}
		if err := m.fn(ctx); err != nil {
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
		if _, err := s.execContext(ctx,
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, m.name, nowRFC3339()); err != nil {
			return fmt.Errorf("store: record migration %s: %w", m.name, err)
		}
	}
	return nil
}

// migrationMessagesSpeakerOpenID adds the speaker's openid column to an older
// database. The display name is not unique inside a group (two members can both
// be called "小王"), so the timeline needs the openid to be unambiguous.
func (s *Store) migrationMessagesSpeakerOpenID(ctx context.Context) error {
	have, err := s.tableColumns(ctx, "messages")
	if err != nil {
		return err
	}
	if have["speaker_openid"] {
		return nil
	}
	if _, err := s.execContext(ctx,
		`ALTER TABLE messages ADD COLUMN speaker_openid TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add messages.speaker_openid: %w", err)
	}
	return nil
}

// backfillMessageSpeakerOpenID recovers the speaker openid of rows written
// before the column existed.
//
// The audit table is the only surviving source: it stores the same content
// together with the sender's member_openid. A row is only filled in when every
// matching audit entry names the same member, so a repeated line ("好的" from
// three different people) stays blank instead of being attributed to whoever
// happened to match first. Better an empty cell than a wrong OpenID.
func (s *Store) backfillMessageSpeakerOpenID(ctx context.Context) error {
	_, err := s.execContext(ctx, `
UPDATE messages SET speaker_openid = (
  SELECT CASE WHEN COUNT(DISTINCT r.member_openid) = 1
              THEN MIN(r.member_openid)
              ELSE '' END
  FROM records r
  WHERE r.conversation_id = messages.conversation_id
    AND r.direction = 'in'
    AND r.content = messages.content
    AND r.member_openid <> ''
)
WHERE speaker_openid = '' AND role = 'user'`)
	if err != nil {
		return fmt.Errorf("backfill messages.speaker_openid: %w", err)
	}
	return nil
}

// migrationMessagesAttachments adds the attachment column to an older database.
func (s *Store) migrationMessagesAttachments(ctx context.Context) error {
	have, err := s.tableColumns(ctx, "messages")
	if err != nil {
		return err
	}
	if have["attachments"] {
		return nil
	}
	if _, err := s.execContext(ctx,
		`ALTER TABLE messages ADD COLUMN attachments TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add messages.attachments: %w", err)
	}
	return nil
}

// migrationMessagesKindSpeaker adds the timeline columns to an older database.
func (s *Store) migrationMessagesKindSpeaker(ctx context.Context) error {
	have, err := s.tableColumns(ctx, "messages")
	if err != nil {
		return err
	}
	additions := map[string]string{
		"kind":    "TEXT NOT NULL DEFAULT 'dialogue'",
		"speaker": "TEXT NOT NULL DEFAULT ''",
	}
	for column, decl := range additions {
		if have[column] {
			continue
		}
		if _, err := s.execContext(ctx,
			fmt.Sprintf("ALTER TABLE messages ADD COLUMN %s %s", column, decl)); err != nil {
			return fmt.Errorf("add messages.%s: %w", column, err)
		}
	}
	return nil
}

// migrationMessagesQuoteIndex adds the platform quote index to an older
// database. It is a REFIDX_… token: quoting somebody else's message requires it
// (message_reference.message_id), and the timeline's "right click → quote" needs
// it to be stored per message.
func (s *Store) migrationMessagesQuoteIndex(ctx context.Context) error {
	have, err := s.tableColumns(ctx, "messages")
	if err != nil {
		return err
	}
	if have["quote_index"] {
		return nil
	}
	if _, err := s.execContext(ctx,
		`ALTER TABLE messages ADD COLUMN quote_index TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add messages.quote_index: %w", err)
	}
	return nil
}

// migrationRecordsDelivery adds the delivery column to an older database.
//
// The column records whether an outbound message was a passive reply (bound to
// an inbound msg_id, capped at 5 per message) or an active message (spending the
// group's daily proactive quota). Without it the quota cannot be counted.
func (s *Store) migrationRecordsDelivery(ctx context.Context) error {
	have, err := s.tableColumns(ctx, "records")
	if err != nil {
		return err
	}
	if have["delivery"] {
		return nil
	}
	if _, err := s.execContext(ctx,
		`ALTER TABLE records ADD COLUMN delivery TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add records.delivery: %w", err)
	}
	// The index cannot live in the base schema: that block runs before the
	// migrations, so on an old database it would reference a column that does
	// not exist yet and abort startup.
	if _, err := s.execContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_records_delivery ON records(direction, delivery, created_at)`); err != nil {
		return fmt.Errorf("index records.delivery: %w", err)
	}
	// Historical rows: early acknowledgement notices always went out as active
	// messages; otherwise a msg_id means a passive reply.
	if _, err := s.execContext(ctx, `
UPDATE records SET delivery = CASE
    WHEN status = 'early_reply' THEN 'active'
    WHEN msg_id <> '' THEN 'passive'
    ELSE 'active' END
WHERE direction = 'out' AND delivery = ''`); err != nil {
		return fmt.Errorf("backfill records.delivery: %w", err)
	}
	return nil
}

// backfillSharedSpeakers recovers the speaker of shared-context rows, whose
// content is stored as "昵称：内容".
func (s *Store) backfillSharedSpeakers(ctx context.Context) error {
	rows, err := s.queryContext(ctx, `
SELECT m.id, m.content FROM messages m
JOIN conversations c ON c.id = m.conversation_id
WHERE m.speaker = '' AND m.role = 'user' AND c.scene = 'group_shared'`)
	if err != nil {
		return fmt.Errorf("scan shared messages: %w", err)
	}
	type pending struct {
		id      int64
		speaker string
	}
	var updates []pending
	for rows.Next() {
		var (
			id      int64
			content string
		)
		if err := rows.Scan(&id, &content); err != nil {
			rows.Close()
			return err
		}
		idx := strings.IndexAny(content, "：:")
		if idx <= 0 || idx > 32 {
			continue
		}
		prefix := content[:idx]
		if strings.ContainsAny(prefix, "\n\r") {
			continue
		}
		updates = append(updates, pending{id: id, speaker: prefix})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, u := range updates {
		if _, err := s.execContext(ctx,
			`UPDATE messages SET speaker = ? WHERE id = ?`, u.speaker, u.id); err != nil {
			return err
		}
	}
	return nil
}

// backfillSpeakers fills the speaker name of pre-existing rows from the
// conversation they belong to, so conversations recorded before the column
// existed still show a nickname in the admin timeline.
func (s *Store) backfillSpeakers(ctx context.Context) error {
	_, err := s.execContext(ctx, `
UPDATE messages
SET speaker = COALESCE((
      SELECT c.member_name FROM conversations c
      WHERE c.id = messages.conversation_id AND c.member_name <> ''
    ), '')
WHERE speaker = ''
  AND role = 'user'
  AND conversation_id IN (
    SELECT id FROM conversations WHERE scene IN ('group_member', 'c2c')
  )`)
	if err != nil {
		return fmt.Errorf("backfill message speakers: %w", err)
	}
	return nil
}

// tableColumns lists the column names of one table, through the dialect (see
// dialect.columnsQuery): SQLite answers from pragma_table_info, PostgreSQL from
// information_schema.columns. The table name is a bind parameter, so it never
// becomes part of the SQL text.
func (s *Store) tableColumns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.queryContext(ctx, s.dialect.columnsQuery(), table)
	if err != nil {
		return nil, fmt.Errorf("store: inspect %s: %w", table, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// --- time helpers ---

func nowRFC3339() string { return time.Now().Format(time.RFC3339) }

// nullableTimeText scans one nullable timestamp column, selected raw (no SQL
// projection), into the RFC3339 string the rest of the application speaks.
//
// SQLite hands back the stored TEXT ("" for unset); PostgreSQL hands back a
// time.Time, or NULL for unset. Using sql.NullTime for the native value and
// formatting it in scanLocation (+08:00) keeps the two engines identical from
// the caller's point of view: unset → "", set → "...+08:00". Doing the
// formatting here rather than in SQL is deliberate — see the timestamp note in
// dialect.go.
type nullableTimeText struct{ Value string }

func (n *nullableTimeText) Scan(src any) error {
	var native sql.NullTime
	if err := native.Scan(src); err == nil {
		if !native.Valid {
			n.Value = ""
			return nil
		}
		n.Value = native.Time.In(scanLocation).Format(time.RFC3339)
		return nil
	}
	// SQLite: the column is TEXT, so nil is impossible but a string arrives.
	var text sql.NullString
	if err := text.Scan(src); err != nil {
		return fmt.Errorf("scan nullable timestamp %T: %w", src, err)
	}
	n.Value = text.String
	return nil
}

// --- conversations ---

// Conversation is a stored dialogue scope.
type Conversation struct {
	ID            string `json:"id"`
	Scene         string `json:"scene"`
	GroupOpenID   string `json:"group_openid"`
	GroupName     string `json:"group_name"`
	MemberOpenID  string `json:"member_openid"`
	MemberName    string `json:"member_name"`
	Enabled       bool   `json:"enabled"`
	MessageCount  int    `json:"message_count"`
	TurnCount     int    `json:"turn_count"`
	TokenEstimate int    `json:"token_estimate"`
	CreatedAt     string `json:"created_at"`
	LastActiveAt  string `json:"last_active_at"`
}

// ConversationFilter narrows ListConversations.
type ConversationFilter struct {
	Query    string
	Scene    string
	Enabled  *bool
	Page     int
	PageSize int
	Sort     string
}

// EnsureConversation inserts the conversation when absent and refreshes the
// display names of an existing one.
func (s *Store) EnsureConversation(ctx context.Context, c *Conversation) error {
	if c.ID == "" {
		return errors.New("store: conversation id required")
	}
	now := nowRFC3339()
	if c.CreatedAt == "" {
		c.CreatedAt = now
	}
	if c.LastActiveAt == "" {
		c.LastActiveAt = now
	}
	_, err := s.execContext(ctx, `
INSERT INTO conversations (id, scene, group_openid, member_openid, group_name, member_name, enabled, created_at, last_active_at)
VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  group_name  = CASE WHEN excluded.group_name  <> '' THEN excluded.group_name  ELSE conversations.group_name  END,
  member_name = CASE WHEN excluded.member_name <> '' THEN excluded.member_name ELSE conversations.member_name END,
  scene       = excluded.scene`,
		c.ID, c.Scene, c.GroupOpenID, c.MemberOpenID, c.GroupName, c.MemberName, c.CreatedAt, c.LastActiveAt)
	return err
}

// GetConversation loads one conversation by id.
func (s *Store) GetConversation(ctx context.Context, id string) (*Conversation, error) {
	row := s.queryRowContext(ctx, `
SELECT id, scene, group_openid, member_openid, group_name, member_name, enabled,
       message_count, turn_count, token_estimate, created_at, last_active_at
FROM conversations WHERE id = ?`, id)
	c, err := scanConversation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ListConversations returns a page of conversations plus the total count.
func (s *Store) ListConversations(ctx context.Context, f ConversationFilter) ([]Conversation, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 200 {
		f.PageSize = 20
	}

	where := []string{"1=1"}
	args := []any{}
	if f.Query != "" {
		where = append(where, "(group_name LIKE ? OR member_name LIKE ? OR group_openid LIKE ? OR member_openid LIKE ?)")
		like := "%" + f.Query + "%"
		args = append(args, like, like, like, like)
	}
	if f.Scene != "" {
		where = append(where, "scene = ?")
		args = append(args, f.Scene)
	}
	if f.Enabled != nil {
		where = append(where, "enabled = ?")
		if *f.Enabled {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	clause := strings.Join(where, " AND ")

	var total int
	if err := s.queryRowContext(ctx, "SELECT COUNT(*) FROM conversations WHERE "+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	order := "last_active_at DESC"
	switch f.Sort {
	case "last_active_asc":
		order = "last_active_at ASC"
	case "messages_desc":
		order = "message_count DESC"
	case "created_desc":
		order = "created_at DESC"
	}

	q := `SELECT id, scene, group_openid, member_openid, group_name, member_name, enabled,
	             message_count, turn_count, token_estimate, created_at, last_active_at
	      FROM conversations WHERE ` + clause + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)

	rows, err := s.queryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]Conversation, 0, f.PageSize)
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *c)
	}
	return out, total, rows.Err()
}

// TouchConversation records activity and token usage for a conversation.
func (s *Store) TouchConversation(ctx context.Context, id string, tokens int) error {
	_, err := s.execContext(ctx, `
UPDATE conversations
SET message_count  = message_count + 1,
    turn_count     = turn_count + CASE WHEN ? > 0 THEN 1 ELSE 0 END,
    token_estimate = token_estimate + ?,
    last_active_at = ?
WHERE id = ?`, tokens, tokens, nowRFC3339(), id)
	return err
}

// TouchObservedMessage records a witnessed message without counting a turn.
func (s *Store) TouchObservedMessage(ctx context.Context, id string, tokens int) error {
	_, err := s.execContext(ctx, `
UPDATE conversations
SET message_count  = message_count + 1,
    token_estimate = token_estimate + ?,
    last_active_at = ?
WHERE id = ?`, tokens, nowRFC3339(), id)
	return err
}

// ConversationsForGroup lists the conversations belonging to one group.
//
// Another bot's message is group-level ambient content rather than something
// one member said, so it is mirrored into every conversation of that group
// (in C1 that is each member's own conversation; in C2 the single shared one).
func (s *Store) ConversationsForGroup(ctx context.Context, groupOpenID string, limit int) ([]string, error) {
	if groupOpenID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.queryContext(ctx, `
SELECT id FROM conversations WHERE group_openid = ? ORDER BY last_active_at DESC LIMIT ?`,
		groupOpenID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GroupName returns the cached display name of a group, if known.
func (s *Store) GroupName(ctx context.Context, groupOpenID string) string {
	var name string
	if err := s.queryRowContext(ctx,
		`SELECT group_name FROM conversations WHERE group_openid = ? AND group_name <> '' LIMIT 1`,
		groupOpenID).Scan(&name); err != nil {
		return ""
	}
	return name
}

// DistinctGroupOpenIDs lists the groups we have conversations with.
func (s *Store) DistinctGroupOpenIDs(ctx context.Context) ([]string, error) {
	rows, err := s.queryContext(ctx,
		`SELECT DISTINCT group_openid FROM conversations WHERE group_openid <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetGroupName fills in the display name of every conversation in a group.
func (s *Store) SetGroupName(ctx context.Context, groupOpenID, name string) (int64, error) {
	if groupOpenID == "" || name == "" {
		return 0, nil
	}
	res, err := s.execContext(ctx, `
UPDATE conversations SET group_name = ? WHERE group_openid = ? AND group_name = ?`,
		name, groupOpenID, "")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetConversationEnabled pauses or resumes a conversation.
func (s *Store) SetConversationEnabled(ctx context.Context, id string, enabled bool) error {
	res, err := s.execContext(ctx, `UPDATE conversations SET enabled = ? WHERE id = ?`, boolToInt(enabled), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ConversationEnabled reports whether a conversation exists and is enabled.
// A missing conversation counts as enabled.
func (s *Store) ConversationEnabled(ctx context.Context, id string) bool {
	var v int
	err := s.queryRowContext(ctx, `SELECT enabled FROM conversations WHERE id = ?`, id).Scan(&v)
	if err != nil {
		return true
	}
	return v == 1
}

// ClearConversationMessages deletes stored history but keeps metadata.
func (s *Store) ClearConversationMessages(ctx context.Context, id string) error {
	if _, err := s.execContext(ctx, `DELETE FROM messages WHERE conversation_id = ?`, id); err != nil {
		return err
	}
	// Affect rows are per-conversation and carry user-derived state; clearing a
	// conversation must not leave them behind.
	if err := s.ClearAffectForConversation(ctx, id); err != nil {
		return err
	}
	// Join decisions are conversation-scoped audit rows. The group topic cache is
	// group-scoped and is left to ClearAllJoin (see ClearJoinForConversation).
	if err := s.ClearJoinForConversation(ctx, id); err != nil {
		return err
	}
	_, err := s.execContext(ctx, `
UPDATE conversations SET message_count = 0, turn_count = 0, token_estimate = 0 WHERE id = ?`, id)
	return err
}

// ClearAllConversations removes every conversation and message.
func (s *Store) ClearAllConversations(ctx context.Context) error {
	if _, err := s.execContext(ctx, `DELETE FROM messages`); err != nil {
		return err
	}
	if err := s.ClearAllAffect(ctx); err != nil {
		return err
	}
	if err := s.ClearAllJoin(ctx); err != nil {
		return err
	}
	_, err := s.execContext(ctx, `DELETE FROM conversations`)
	return err
}

// --- messages ---

// Message kinds.
const (
	// KindDialogue is a turn the bot took part in (it is fed to the model).
	KindDialogue = "dialogue"
	// KindObserved is group chatter the bot saw but was not addressed by. It is
	// kept so the conversation view is a complete timeline, but it is never
	// sent to the model.
	KindObserved = "observed"
	// KindObservedBot is a message written by another bot in the group.
	KindObservedBot = "observed_bot"
	// KindManual is a message an operator sent from the admin panel as the bot.
	// It is a real message in the conversation, so the model must see it as one
	// of its own turns; the distinct kind is only there for the timeline badge.
	KindManual = "manual"
)

// Attachment is one file carried by a message (images are cached locally so
// the timeline keeps working after the platform URL expires).
type Attachment struct {
	// Kind is a coarse category: image | voice | video | file.
	Kind string `json:"kind"`
	// MediaID addresses the cached blob in the media table (empty when we did
	// not manage to download it).
	MediaID     string `json:"media_id,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Filename    string `json:"filename,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Size        int64  `json:"size,omitempty"`
	// ASRText is the platform's speech-to-text hint for voice messages.
	ASRText string `json:"asr_text,omitempty"`
}

// Message is one stored conversation entry.
type Message struct {
	ID      int64  `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
	Tokens  int    `json:"tokens"`
	Kind    string `json:"kind"`
	Speaker string `json:"speaker"`
	// SpeakerOpenID identifies the speaker unambiguously: display names repeat
	// inside a group, openids do not. Empty for assistant rows in C2C (the bot
	// has no per-user openid there) and for pre-migration rows we could not
	// attribute with certainty.
	SpeakerOpenID string `json:"speaker_openid,omitempty"`
	// QuoteIndex lets this message be quoted later: the platform's REFIDX_… for
	// it. Empty when the platform did not report one (older rows, or a send
	// response without ext_info.ref_idx).
	QuoteIndex  string       `json:"quote_index,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	CreatedAt   string       `json:"created_at"`
}

// MessageInput describes one row to store.
type MessageInput struct {
	ConversationID string
	Role           string
	Content        string
	Speaker        string
	SpeakerOpenID  string
	QuoteIndex     string
	Kind           string
	Tokens         int
	Attachments    []Attachment
	// Addressed marks a user message that was aimed at the bot: in a group that
	// means an @ (Inbound.AtBot). Proactive credits only count the opposite —
	// natural chatter nobody addressed to the bot. C2C messages are stored with
	// addressed=false too (the dispatcher's @-detection is group-only); they are
	// kept out of the credit query by the group_openid filter, not by this flag.
	// Assistant rows and observed rows leave it false.
	Addressed bool
}

// AppendMessage stores one conversation entry.
//
// kind is KindDialogue for turns the bot took part in and KindObserved for
// group chatter it merely witnessed; speaker is the display name to show and
// speakerOpenID is the openid that disambiguates it.
func (s *Store) AppendMessage(ctx context.Context, in MessageInput) error {
	_, err := s.AppendMessageID(ctx, in)
	return err
}

// AppendMessageID is AppendMessage that also reports the new row id, which the
// admin panel returns so the UI can point at the row it just created.
func (s *Store) AppendMessageID(ctx context.Context, in MessageInput) (int64, error) {
	if in.Kind == "" {
		in.Kind = KindDialogue
	}
	encoded := ""
	if len(in.Attachments) > 0 {
		raw, err := json.Marshal(in.Attachments)
		if err != nil {
			return 0, err
		}
		encoded = string(raw)
	}
	// RETURNING id rather than LastInsertId: PostgreSQL's driver has no
	// LastInsertId, and both engines accept INSERT ... RETURNING (SQLite 3.35+).
	var id int64
	err := s.queryRowContext(ctx, `
INSERT INTO messages (conversation_id, role, content, tokens, kind, speaker, speaker_openid, quote_index, attachments, addressed, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id`,
		in.ConversationID, in.Role, in.Content, in.Tokens, in.Kind, in.Speaker, in.SpeakerOpenID,
		in.QuoteIndex, encoded, boolToInt(in.Addressed), nowRFC3339()).Scan(&id)
	if err != nil {
		// Nothing inserted: report 0 together with the error, as before.
		return 0, err
	}
	return id, nil
}

// SetMessageQuoteIndex records the platform reference index of a message we
// already stored, so it can be quoted later. The assistant row has to be written
// before delivery (a failed send must still leave coherent context) while the
// index only arrives with the send response.
func (s *Store) SetMessageQuoteIndex(ctx context.Context, id int64, quoteIndex string) error {
	if id <= 0 || quoteIndex == "" {
		return nil
	}
	_, err := s.execContext(ctx, `UPDATE messages SET quote_index = ? WHERE id = ?`, quoteIndex, id)
	return err
}

// RecentMessages returns the newest messages that may feed the model, in
// chronological order.
//
// includeUnaddressed controls whether group chatter the bot merely witnessed
// (and other bots' messages) is returned as well. Callers pass the configured
// context scope: answering and reading are separate decisions, so an operator
// can keep the bot quiet while still letting it follow the room.
func (s *Store) RecentMessages(ctx context.Context, conversationID string, limit int, includeUnaddressed bool) ([]Message, error) {
	if limit <= 0 {
		limit = 40
	}
	query := `
SELECT id, role, content, tokens, kind, speaker, speaker_openid, quote_index, attachments, created_at FROM (
  SELECT id, role, content, tokens, kind, speaker, speaker_openid, quote_index, attachments, created_at FROM messages
  WHERE conversation_id = ? AND kind IN (?, ?) ORDER BY id DESC LIMIT ?
) ORDER BY id ASC`
	// Manual messages are the bot's own words, so they are context, not ambience.
	args := []any{conversationID, KindDialogue, KindManual, limit}
	if includeUnaddressed {
		query = `
SELECT id, role, content, tokens, kind, speaker, speaker_openid, quote_index, attachments, created_at FROM (
  SELECT id, role, content, tokens, kind, speaker, speaker_openid, quote_index, attachments, created_at FROM messages
  WHERE conversation_id = ? AND kind IN (?, ?, ?, ?) ORDER BY id DESC LIMIT ?
) ORDER BY id ASC`
		args = []any{conversationID, KindDialogue, KindManual, KindObserved, KindObservedBot, limit}
	}
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// Messages returns a page of messages ending before beforeID (0 = newest).
func (s *Store) Messages(ctx context.Context, conversationID string, limit int, beforeID int64) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `SELECT id, role, content, tokens, kind, speaker, speaker_openid, quote_index, attachments, created_at FROM messages WHERE conversation_id = ?`
	args := []any{conversationID}
	if beforeID > 0 {
		q += ` AND id < ?`
		args = append(args, beforeID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.queryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	// Return chronological order for display.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// PruneMessages removes messages older than the retention window.
func (s *Store) PruneMessages(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention).Format(time.RFC3339)
	res, err := s.execContext(ctx, `DELETE FROM messages WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- records ---

// Record is an audit row for one inbound or outbound message.
type Record struct {
	ID             int64  `json:"id"`
	Direction      string `json:"direction"`
	Scene          string `json:"scene"`
	ConversationID string `json:"conversation_id"`
	GroupOpenID    string `json:"group_openid"`
	MemberOpenID   string `json:"member_openid"`
	MemberName     string `json:"member_name"`
	Content        string `json:"content"`
	MsgID          string `json:"msg_id"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	LatencyMS      int64  `json:"latency_ms"`
	// Delivery is "passive" (a reply bound to an inbound msg_id, at most 5 per
	// message) or "active" (a proactive message, spending the group's daily
	// quota). Empty for inbound rows.
	Delivery  string `json:"delivery,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Snapshot returns an immutable copy of the record for the live event bus.
//
// A Record is mutable until finishRecord runs; SSE subscribers may still be
// encoding an event published earlier. Publishing this copy (the bus's
// Snapshotter hook calls it on every *Record) guarantees the subscriber sees a
// frozen value and never races with finishRecord's later field writes.
func (r *Record) Snapshot() any {
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

// Delivery values for Record.Delivery.
const (
	DeliveryPassive = "passive"
	DeliveryActive  = "active"
)

// StatusManual marks an outbound record produced by an operator in the panel
// rather than by the bot itself. Other statuses stay literal at their call sites
// because nothing filters on them by name.
const StatusManual = "manual"

// RecordFilter narrows ListRecords.
type RecordFilter struct {
	Direction string
	Status    string
	Query     string
	Since     string
	Until     string
	Page      int
	PageSize  int
}

// AddRecord inserts an audit row and returns its id.
func (s *Store) AddRecord(ctx context.Context, r *Record) (int64, error) {
	if r.CreatedAt == "" {
		r.CreatedAt = nowRFC3339()
	}
	var id int64
	err := s.queryRowContext(ctx, `
INSERT INTO records (direction, scene, conversation_id, group_openid, member_openid, member_name,
                     content, msg_id, status, error, latency_ms, delivery, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id`,
		r.Direction, r.Scene, r.ConversationID, r.GroupOpenID, r.MemberOpenID, r.MemberName,
		r.Content, r.MsgID, r.Status, r.Error, r.LatencyMS, r.Delivery, r.CreatedAt).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// UpdateRecord finalises an audit row once processing finished.
func (s *Store) UpdateRecord(ctx context.Context, id int64, status, errMsg string, latencyMS int64) error {
	_, err := s.execContext(ctx, `
UPDATE records SET status = ?, error = ?, latency_ms = ? WHERE id = ?`, status, errMsg, latencyMS, id)
	return err
}

// ListRecords returns a page of audit rows, newest first.
func (s *Store) ListRecords(ctx context.Context, f RecordFilter) ([]Record, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 200 {
		f.PageSize = 50
	}
	where := []string{"1=1"}
	args := []any{}
	if f.Direction != "" {
		where = append(where, "direction = ?")
		args = append(args, f.Direction)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Query != "" {
		where = append(where, "(content LIKE ? OR member_name LIKE ? OR group_openid LIKE ? OR msg_id LIKE ?)")
		like := "%" + f.Query + "%"
		args = append(args, like, like, like, like)
	}
	if f.Since != "" {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until != "" {
		where = append(where, "created_at <= ?")
		args = append(args, f.Until)
	}
	clause := strings.Join(where, " AND ")

	var total int
	if err := s.queryRowContext(ctx, "SELECT COUNT(*) FROM records WHERE "+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := `SELECT id, direction, scene, conversation_id, group_openid, member_openid, member_name,
	             content, msg_id, status, error, latency_ms, delivery, created_at
	      FROM records WHERE ` + clause + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)

	rows, err := s.queryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]Record, 0, f.PageSize)
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.ID, &r.Direction, &r.Scene, &r.ConversationID, &r.GroupOpenID,
			&r.MemberOpenID, &r.MemberName, &r.Content, &r.MsgID, &r.Status, &r.Error,
			&r.LatencyMS, &r.Delivery, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// CountActiveMessages counts the active (proactive) messages sent to a group
// since a point in time.
//
// The platform quota is shared by everything proactive we do — manual sends and
// the passive-window fallback — so the panel needs one honest number to watch.
func (s *Store) CountActiveMessages(ctx context.Context, groupOpenID string, since time.Time) (int, error) {
	if strings.TrimSpace(groupOpenID) == "" {
		return 0, nil
	}
	var n int
	err := s.queryRowContext(ctx, `
SELECT COUNT(*) FROM records
WHERE direction = 'out' AND delivery = ? AND group_openid = ? AND created_at >= ?`,
		DeliveryActive, groupOpenID, since.Format(time.RFC3339)).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// --- dedup ---

// MarkSeen records msgID and reports whether it is new. Duplicate platform
// pushes of the same msg_id return false.
func (s *Store) MarkSeen(ctx context.Context, msgID string) (bool, error) {
	if msgID == "" {
		return true, nil
	}
	// ON CONFLICT DO NOTHING is the portable spelling of INSERT OR IGNORE
	// (SQLite 3.24+, PostgreSQL 9.5+); RowsAffected is 0 on both when the key
	// was already known.
	res, err := s.execContext(ctx, `INSERT INTO dedup (msg_id, created_at) VALUES (?, ?) ON CONFLICT (msg_id) DO NOTHING`,
		msgID, nowRFC3339())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// PruneDedup drops dedup keys older than the retention window.
func (s *Store) PruneDedup(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention).Format(time.RFC3339)
	res, err := s.execContext(ctx, `DELETE FROM dedup WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneRecords trims the audit log to its newest keep rows.
func (s *Store) PruneRecords(ctx context.Context, keep int) (int64, error) {
	res, err := s.execContext(ctx, `
DELETE FROM records WHERE id <= (SELECT MAX(id) - ? FROM records)`, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- settings ---

// LoadSettings returns every persisted runtime override.
func (s *Store) LoadSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.queryContext(ctx, `SELECT k, v FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SaveSettings writes overrides in one transaction.
func (s *Store) SaveSettings(ctx context.Context, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := nowRFC3339()
	for k, v := range values {
		if _, err := s.txExecContext(ctx, tx, `
INSERT INTO settings (k, v, updated_at) VALUES (?, ?, ?)
ON CONFLICT(k) DO UPDATE SET v = excluded.v, updated_at = excluded.updated_at`, k, v, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteAllSettings removes every persisted override.
func (s *Store) DeleteAllSettings(ctx context.Context) error {
	_, err := s.execContext(ctx, `DELETE FROM settings`)
	return err
}

// --- stats ---

// StatBucket is one aggregated time bucket.
type StatBucket struct {
	Bucket       string `json:"ts"`
	In           int    `json:"in"`
	Out          int    `json:"out"`
	Errors       int    `json:"errors"`
	LatencySumMS int64  `json:"-"`
	LatencyCount int64  `json:"-"`
	Tokens       int64  `json:"tokens"`
}

// LoadStats returns buckets at or after since (RFC3339), oldest first.
func (s *Store) LoadStats(ctx context.Context, since string) ([]StatBucket, error) {
	rows, err := s.queryContext(ctx, `
SELECT bucket, in_count, out_count, errors, latency_sum_ms, latency_count, tokens
FROM stats WHERE bucket >= ? ORDER BY bucket ASC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatBucket{}
	for rows.Next() {
		var b StatBucket
		if err := rows.Scan(&b.Bucket, &b.In, &b.Out, &b.Errors, &b.LatencySumMS, &b.LatencyCount, &b.Tokens); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SaveStats upserts buckets.
func (s *Store) SaveStats(ctx context.Context, buckets []StatBucket) error {
	if len(buckets) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, b := range buckets {
		if _, err := s.txExecContext(ctx, tx, `
INSERT INTO stats (bucket, in_count, out_count, errors, latency_sum_ms, latency_count, tokens)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(bucket) DO UPDATE SET
  in_count = excluded.in_count, out_count = excluded.out_count, errors = excluded.errors,
  latency_sum_ms = excluded.latency_sum_ms, latency_count = excluded.latency_count, tokens = excluded.tokens`,
			b.Bucket, b.In, b.Out, b.Errors, b.LatencySumMS, b.LatencyCount, b.Tokens); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneStats removes buckets older than the cutoff.
// --- proactive state (spontaneous messages) ---

// ProactiveState is the per-group memory of the spontaneous-message loop.
type ProactiveState struct {
	GroupOpenID  string `json:"group_openid"`
	LastSentAt   string `json:"last_sent_at"`
	SilentStreak int    `json:"silent_streak"`
	Sends        int    `json:"sends"`
	LastReason   string `json:"last_reason"`
	UpdatedAt    string `json:"updated_at"`
}

// GetProactiveState returns the stored state, or a zero value when the group has
// never spoken on its own.
func (s *Store) GetProactiveState(ctx context.Context, groupOpenID string) (ProactiveState, error) {
	out := ProactiveState{GroupOpenID: groupOpenID}
	var lastSent, updated nullableTimeText
	err := s.queryRowContext(ctx, `
SELECT group_openid, last_sent_at, silent_streak, sends, last_reason, updated_at
  FROM proactive_state WHERE group_openid = ?`, groupOpenID).Scan(
		&out.GroupOpenID, &lastSent, &out.SilentStreak, &out.Sends, &out.LastReason, &updated)
	out.LastSentAt, out.UpdatedAt = lastSent.Value, updated.Value
	if err != nil {
		return ProactiveState{GroupOpenID: groupOpenID}, nil // 没有记录就是初始状态
	}
	return out, nil
}

// SaveProactiveState upserts one group's state.
func (s *Store) SaveProactiveState(ctx context.Context, st ProactiveState) error {
	_, err := s.execContext(ctx, `
INSERT INTO proactive_state (group_openid, last_sent_at, silent_streak, sends, last_reason, updated_at)
VALUES (?, `+s.dialect.NullableTimeArg()+`, ?, ?, ?, `+s.dialect.NullableTimeArg()+`)
ON CONFLICT (group_openid) DO UPDATE SET
  last_sent_at = excluded.last_sent_at,
  silent_streak = excluded.silent_streak,
  sends = excluded.sends,
  last_reason = excluded.last_reason,
  updated_at = excluded.updated_at`,
		st.GroupOpenID, st.LastSentAt, st.SilentStreak, st.Sends, st.LastReason, nowRFC3339())
	return err
}

// ListProactiveStates returns every group the bot has spoken in unprompted.
func (s *Store) ListProactiveStates(ctx context.Context) ([]ProactiveState, error) {
	rows, err := s.queryContext(ctx, `
SELECT group_openid, last_sent_at, silent_streak, sends, last_reason, updated_at
  FROM proactive_state ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]ProactiveState, 0, 8)
	for rows.Next() {
		var st ProactiveState
		var lastSent, updated nullableTimeText
		if err := rows.Scan(&st.GroupOpenID, &lastSent, &st.SilentStreak, &st.Sends,
			&st.LastReason, &updated); err != nil {
			return nil, err
		}
		st.LastSentAt, st.UpdatedAt = lastSent.Value, updated.Value
		out = append(out, st)
	}
	return out, rows.Err()
}

// CountHumanMessagesSince counts the *ambient* human chatter in a group after
// `since`, which is what the proactive credit budget is denominated in.
//
// Credits must come from natural conversation, never from people talking to the
// bot: a question asked of it should not become a licence to speak unprompted.
// Hence addressed messages are excluded. The rule is:
//
//   - role='user'                 → the bot's own replies and manual sends never count
//   - addressed=0                 → in a group, a message that @s the bot is stored as
//     dialogue with addressed=1 and does not count; ambient chatter has addressed=0
//   - kind IN (dialogue, observed) → ambient group chatter is dialogue(addressed=0)
//     when the bot also answered it (all_messages), and observed when it merely
//     witnessed it; observed_bot is another robot, not a human
//
// Private (C2C) messages are stored with addressed=0 as well — the dispatcher's
// @-detection only fires in groups — but this query is scoped to one group_openid
// and a C2C conversation has an empty group_openid, so a private chat can never
// earn proactive credits.
//
// A consequence worth knowing: an at_only group only ever receives addressed
// messages, so it earns zero credits here and proactive messaging stays dormant
// by construction (D-034).
func (s *Store) CountHumanMessagesSince(ctx context.Context, groupOpenID string, since time.Time) (int, error) {
	var n int
	err := s.queryRowContext(ctx, `
SELECT COUNT(*) FROM messages m
  JOIN conversations c ON c.id = m.conversation_id
 WHERE c.group_openid = ?
   AND m.role = 'user'
   AND m.addressed = 0
   AND m.kind IN (?, ?)
   AND m.created_at > ?`,
		groupOpenID, KindDialogue, KindObserved, since.Format(time.RFC3339)).Scan(&n)
	return n, err
}

// migrationTasksCreator adds the per-member owner of a reminder. Before this,
// group tasks carried no member at all, so anyone in the group could list and
// cancel everyone else's reminders.
// migrationCommitments creates the open-loop table on databases that predate
// it. The base schema already creates it for a fresh database; this migration is
// what makes an upgrade work.
func (s *Store) migrationCommitments(ctx context.Context) error {
	_, err := s.execContext(ctx, `
CREATE TABLE IF NOT EXISTS commitments (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL DEFAULT '',
  group_openid    TEXT NOT NULL DEFAULT '',
  member_openid   TEXT NOT NULL DEFAULT '',
  creator_openid  TEXT NOT NULL DEFAULT '',
  text            TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'waiting',
  priority        INTEGER NOT NULL DEFAULT 0,
  deadline_at     TEXT NOT NULL DEFAULT '',
  next_check_at   TEXT NOT NULL,
  last_nudge_at   TEXT NOT NULL DEFAULT '',
  nudge_count     INTEGER NOT NULL DEFAULT 0,
  ack_at          TEXT NOT NULL DEFAULT '',
  ack_note        TEXT NOT NULL DEFAULT '',
  meta            TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_commitments_due ON commitments(status, next_check_at);
CREATE INDEX IF NOT EXISTS idx_commitments_creator ON commitments(creator_openid, status);
`)
	if err != nil {
		return fmt.Errorf("create commitments: %w", err)
	}
	return nil
}

// migrationHumanStatus creates the inferred human-state cache and its unique key.
func (s *Store) migrationHumanStatus(ctx context.Context) error {
	_, err := s.execContext(ctx, `
CREATE TABLE IF NOT EXISTS human_status (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  scope           TEXT NOT NULL,
  conversation_id TEXT NOT NULL,
  group_openid    TEXT NOT NULL DEFAULT '',
  member_openid   TEXT NOT NULL,
  display_name    TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL,
  confidence      REAL NOT NULL DEFAULT 0,
  evidence        TEXT NOT NULL DEFAULT '',
  updated_at      TEXT NOT NULL,
  expires_at      TEXT NOT NULL,
  UNIQUE (scope, conversation_id, member_openid)
);
`)
	if err != nil {
		return fmt.Errorf("create human_status: %w", err)
	}
	return nil
}

// migrationAffect creates the three affect tables on databases that predate
// them. The base schema already creates them for a fresh database; this
// migration is what makes an upgrade work, and it is idempotent because
// CREATE ... IF NOT EXISTS never touches an existing table.
func (s *Store) migrationAffect(ctx context.Context) error {
	_, err := s.execContext(ctx, `
CREATE TABLE IF NOT EXISTS affect_states (
  conversation_id TEXT PRIMARY KEY,
  pack_id         TEXT NOT NULL DEFAULT 'daily',
  dims            TEXT NOT NULL DEFAULT '{}',
  events          TEXT NOT NULL DEFAULT '[]',
  paused_until    TEXT NOT NULL DEFAULT '',
  ticked_at       TEXT NOT NULL DEFAULT '',
  updated_at      TEXT NOT NULL,
  version         INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS affect_events (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL,
  event_id        TEXT NOT NULL,
  tone            TEXT NOT NULL DEFAULT '',
  source          TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_affect_events_conversation ON affect_events(conversation_id, created_at DESC);
CREATE TABLE IF NOT EXISTS affect_history (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL,
  at              TEXT NOT NULL,
  dims            TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_affect_history_conversation ON affect_history(conversation_id, at DESC);
`)
	if err != nil {
		return fmt.Errorf("create affect tables: %w", err)
	}
	return nil
}

// migrationJoinGate creates the join-gate tables on databases that predate
// them. The base schema already creates them for a fresh database; this
// migration is what makes an upgrade work, and it is idempotent because
// CREATE ... IF NOT EXISTS never touches an existing table.
func (s *Store) migrationJoinGate(ctx context.Context) error {
	_, err := s.execContext(ctx, `
CREATE TABLE IF NOT EXISTS join_decisions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  group_openid    TEXT NOT NULL DEFAULT '',
  conversation_id TEXT NOT NULL DEFAULT '',
  message_id      TEXT NOT NULL DEFAULT '',
  speaker         TEXT NOT NULL DEFAULT '',
  preview         TEXT NOT NULL DEFAULT '',
  joined          INTEGER NOT NULL DEFAULT 0,
  action          TEXT NOT NULL DEFAULT '',
  confidence      REAL NOT NULL DEFAULT 0,
  reason          TEXT NOT NULL DEFAULT '',
  topics          TEXT NOT NULL DEFAULT '',
  model           TEXT NOT NULL DEFAULT '',
  latency_ms      INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_join_decisions_group ON join_decisions(group_openid, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_join_decisions_conversation ON join_decisions(conversation_id);
CREATE TABLE IF NOT EXISTS join_topics (
  group_openid TEXT PRIMARY KEY,
  topics       TEXT NOT NULL DEFAULT '',
  analyzed_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_quote_index ON messages(quote_index);
`)
	if err != nil {
		return fmt.Errorf("create join_gate tables: %w", err)
	}
	// A database that already ran an earlier build of this migration has the
	// table but not the column: CREATE TABLE IF NOT EXISTS does nothing there.
	have, err := s.tableColumns(ctx, "join_decisions")
	if err != nil {
		return err
	}
	if !have["action"] {
		if _, err := s.execContext(ctx,
			`ALTER TABLE join_decisions ADD COLUMN action TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add join_decisions.action: %w", err)
		}
	}
	return nil
}

func (s *Store) migrationTasksCreator(ctx context.Context) error {
	have, err := s.tableColumns(ctx, "scheduled_tasks")
	if err != nil {
		return err
	}
	if !have["creator_openid"] {
		if _, err := s.execContext(ctx,
			`ALTER TABLE scheduled_tasks ADD COLUMN creator_openid TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add scheduled_tasks.creator_openid: %w", err)
		}
		// 存量：私聊任务能从 member_openid 恢复归属；群聊任务当年没记创建者，
		// 只能保持空（空 = 仅后台可见，聊天里谁都查不到）。
		if _, err := s.execContext(ctx,
			`UPDATE scheduled_tasks SET creator_openid = member_openid WHERE creator_openid = ''`); err != nil {
			return fmt.Errorf("backfill scheduled_tasks.creator_openid: %w", err)
		}
	}
	_, err = s.execContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_tasks_creator ON scheduled_tasks(creator_openid, status)`)
	return err
}

// migrationMessagesAddressed records whether a user message was aimed at the bot.
//
// Proactive credits must be earned by natural conversation, and "a human asked
// the bot something" and "humans were chatting among themselves" are both stored
// as kind=dialogue; only this flag tells them apart (see CountHumanMessagesSince).
//
// Existing rows default to addressed=1 on purpose: after an upgrade the bot must
// not suddenly bank credits from old @-history. Witnessed chatter (kind=observed)
// is ambient by definition, so it is backfilled to 0.
//
// The column is deliberately NOT in the base schema: new databases get it through
// this migration, so there is exactly one place that adds it.
func (s *Store) migrationMessagesAddressed(ctx context.Context) error {
	have, err := s.tableColumns(ctx, "messages")
	if err != nil {
		return err
	}
	if have["addressed"] {
		return nil
	}
	if _, err := s.execContext(ctx,
		`ALTER TABLE messages ADD COLUMN addressed INTEGER NOT NULL DEFAULT 1`); err != nil {
		return fmt.Errorf("add messages.addressed: %w", err)
	}
	if _, err := s.execContext(ctx,
		`UPDATE messages SET addressed = 0 WHERE kind = ?`, KindObserved); err != nil {
		return fmt.Errorf("backfill messages.addressed: %w", err)
	}
	return nil
}

// --- scheduled tasks (delegated proactivity) ---

// Task statuses. A task is leased while a worker is sending it, so two workers
// (or two containers) can never deliver the same reminder twice.
const (
	TaskPending  = "pending"
	TaskLeased   = "leased"
	TaskSent     = "sent"
	TaskFailed   = "failed"
	TaskCanceled = "canceled"
)

// TaskInput is one scheduled reminder.
type TaskInput struct {
	ConversationID string `json:"conversation_id"`
	GroupOpenID    string `json:"group_openid"`
	MemberOpenID   string `json:"member_openid"`
	// CreatorOpenID is who asked for the reminder. Only this person may list or
	// cancel it from chat.
	CreatorOpenID string `json:"creator_openid"`
	Kind          string `json:"kind"`
	Text          string `json:"text"`
	DueAt         string `json:"due_at"`
}

// ScheduledTask is a stored reminder.
type ScheduledTask struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"conversation_id"`
	GroupOpenID    string `json:"group_openid"`
	MemberOpenID   string `json:"member_openid"`
	CreatorOpenID  string `json:"creator_openid"`
	Kind           string `json:"kind"`
	Text           string `json:"text"`
	DueAt          string `json:"due_at"`
	Status         string `json:"status"`
	Attempts       int    `json:"attempts"`
	LeaseUntil     string `json:"lease_until"`
	Error          string `json:"error"`
	CreatedAt      string `json:"created_at"`
	SentAt         string `json:"sent_at"`
}

// AddTask stores a pending reminder.
func (s *Store) AddTask(ctx context.Context, in TaskInput) (int64, error) {
	if in.Kind == "" {
		in.Kind = "reminder"
	}
	var id int64
	err := s.queryRowContext(ctx, `
INSERT INTO scheduled_tasks (conversation_id, group_openid, member_openid, creator_openid, kind, text, due_at, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id`,
		in.ConversationID, in.GroupOpenID, in.MemberOpenID, in.CreatorOpenID, in.Kind, in.Text, in.DueAt,
		TaskPending, nowRFC3339()).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// LeaseDueTasks atomically claims tasks that are due, returning them to the
// caller. Claiming happens inside one transaction so a second worker sees
// nothing to do.
func (s *Store) LeaseDueTasks(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]ScheduledTask, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	nowStr := now.Format(time.RFC3339)
	leaseStr := now.Add(lease).Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := s.txQueryContext(ctx, tx, `
SELECT id FROM scheduled_tasks
 WHERE status = ? AND due_at <= ? AND (`+s.dialect.TimeIsUnset("lease_until")+` OR lease_until < ?)
 ORDER BY due_at ASC LIMIT ?`, TaskPending, nowStr, nowStr, limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if len(ids) == 0 {
		return nil, tx.Commit()
	}

	claimed := make([]ScheduledTask, 0, len(ids))
	for _, id := range ids {
		if _, err := s.txExecContext(ctx, tx, `UPDATE scheduled_tasks SET status = ?, lease_until = `+s.dialect.NullableTimeArg()+`, attempts = attempts + 1 WHERE id = ?`,
			TaskLeased, leaseStr, id); err != nil {
			return nil, err
		}
		var t ScheduledTask
		var leaseUntil, sentAt nullableTimeText
		if err := s.txQueryRowContext(ctx, tx, `
SELECT id, conversation_id, group_openid, member_openid, creator_openid, kind, text, due_at, status, attempts,
       lease_until, error, created_at, sent_at
  FROM scheduled_tasks WHERE id = ?`, id).Scan(
			&t.ID, &t.ConversationID, &t.GroupOpenID, &t.MemberOpenID, &t.CreatorOpenID, &t.Kind, &t.Text, &t.DueAt,
			&t.Status, &t.Attempts, &leaseUntil, &t.Error, &t.CreatedAt, &sentAt); err != nil {
			return nil, err
		}
		t.LeaseUntil, t.SentAt = leaseUntil.Value, sentAt.Value
		claimed = append(claimed, t)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

// FinishTask marks a leased task as sent or failed.
func (s *Store) FinishTask(ctx context.Context, id int64, status, errMsg string) error {
	sentAt := ""
	if status == TaskSent {
		sentAt = nowRFC3339()
	}
	_, err := s.execContext(ctx, `
UPDATE scheduled_tasks SET status = ?, error = ?, sent_at = `+s.dialect.NullableTimeArg()+`, lease_until = `+s.dialect.EmptyTimeLiteral()+` WHERE id = ?`,
		status, errMsg, sentAt, id)
	return err
}

// CancelTaskAs cancels a pending task owned by creatorOpenID. A task owned by
// someone else is not cancelled (and not even reported as existing).
func (s *Store) CancelTaskAs(ctx context.Context, id int64, creatorOpenID string) (bool, error) {
	if creatorOpenID == "" {
		return false, nil
	}
	res, err := s.execContext(ctx, `
UPDATE scheduled_tasks SET status = ?, lease_until = `+s.dialect.EmptyTimeLiteral()+`
 WHERE id = ? AND creator_openid = ? AND status IN (?, ?)`,
		TaskCanceled, id, creatorOpenID, TaskPending, TaskLeased)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CancelTask cancels any pending task (admin panel; no ownership check).
func (s *Store) CancelTask(ctx context.Context, id int64) (bool, error) {
	res, err := s.execContext(ctx, `
UPDATE scheduled_tasks SET status = ?, lease_until = `+s.dialect.EmptyTimeLiteral()+` WHERE id = ? AND status IN (?, ?)`,
		TaskCanceled, id, TaskPending, TaskLeased)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ListTasks pages tasks, newest first; empty status means every status.
func (s *Store) ListTasks(ctx context.Context, status string, limit, offset int) ([]ScheduledTask, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT id, conversation_id, group_openid, member_openid, creator_openid, kind, text, due_at, status,
	                 attempts, lease_until, error, created_at, sent_at
	          FROM scheduled_tasks`
	args := []any{}
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]ScheduledTask, 0, limit)
	for rows.Next() {
		var t ScheduledTask
		var leaseUntil, sentAt nullableTimeText
		if err := rows.Scan(&t.ID, &t.ConversationID, &t.GroupOpenID, &t.MemberOpenID, &t.CreatorOpenID,
			&t.Kind, &t.Text, &t.DueAt, &t.Status, &t.Attempts, &leaseUntil, &t.Error,
			&t.CreatedAt, &sentAt); err != nil {
			return nil, err
		}
		t.LeaseUntil, t.SentAt = leaseUntil.Value, sentAt.Value
		out = append(out, t)
	}
	return out, rows.Err()
}

// PendingTasksOfCreator counts the pending reminders one person owns (limit).
func (s *Store) PendingTasksOfCreator(ctx context.Context, creatorOpenID string) (int, error) {
	if creatorOpenID == "" {
		return 0, nil
	}
	var n int
	err := s.queryRowContext(ctx, `
SELECT COUNT(*) FROM scheduled_tasks
 WHERE status IN (?, ?) AND creator_openid = ?`,
		TaskPending, TaskLeased, creatorOpenID).Scan(&n)
	return n, err
}

// ListPendingTasksOfCreator returns one person's pending reminders, soonest
// first. `creatorOpenID` is empty only for legacy rows and yields nothing.
func (s *Store) ListPendingTasksOfCreator(ctx context.Context, creatorOpenID string, limit int) ([]ScheduledTask, error) {
	if creatorOpenID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := s.queryContext(ctx, `
SELECT id, conversation_id, group_openid, member_openid, creator_openid, kind, text, due_at, status,
       attempts, lease_until, error, created_at, sent_at
  FROM scheduled_tasks
 WHERE status IN (?, ?) AND creator_openid = ?
 ORDER BY due_at ASC LIMIT ?`, TaskPending, TaskLeased, creatorOpenID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]ScheduledTask, 0, limit)
	for rows.Next() {
		var t ScheduledTask
		var leaseUntil, sentAt nullableTimeText
		if err := rows.Scan(&t.ID, &t.ConversationID, &t.GroupOpenID, &t.MemberOpenID, &t.CreatorOpenID,
			&t.Kind, &t.Text, &t.DueAt, &t.Status, &t.Attempts, &leaseUntil, &t.Error,
			&t.CreatedAt, &sentAt); err != nil {
			return nil, err
		}
		t.LeaseUntil, t.SentAt = leaseUntil.Value, sentAt.Value
		out = append(out, t)
	}
	return out, rows.Err()
}

// PruneTasks keeps the newest `keep` finished tasks (sent/failed/canceled).
func (s *Store) PruneTasks(ctx context.Context, keep int) (int64, error) {
	if keep <= 0 {
		keep = 5000
	}
	res, err := s.execContext(ctx, `
DELETE FROM scheduled_tasks
 WHERE status IN (?, ?, ?)
   AND id <= (SELECT MAX(id) - ? FROM scheduled_tasks)`,
		TaskSent, TaskFailed, TaskCanceled, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- memories (long-term facts) ---

// Memory scopes. A group memory is only visible inside its group, a member
// memory only in that member's private chat, and a global memory everywhere.
const (
	MemoryGlobal = "global"
	MemoryGroup  = "group"
	MemoryMember = "member"
)

// MemoryInput is one fact to store. Re-storing the same text in the same scope
// bumps its salience instead of duplicating the row.
type MemoryInput struct {
	Scope        string  `json:"scope"`
	GroupOpenID  string  `json:"group_openid"`
	MemberOpenID string  `json:"member_openid"`
	Kind         string  `json:"kind"`
	Text         string  `json:"text"`
	Source       string  `json:"source"`
	Salience     float64 `json:"salience"`
}

// Memory is a stored fact.
type Memory struct {
	ID           int64   `json:"id"`
	Scope        string  `json:"scope"`
	GroupOpenID  string  `json:"group_openid"`
	MemberOpenID string  `json:"member_openid"`
	Kind         string  `json:"kind"`
	Text         string  `json:"text"`
	Source       string  `json:"source"`
	Salience     float64 `json:"salience"`
	Hits         int     `json:"hits"`
	LastUsedAt   string  `json:"last_used_at"`
	CreatedAt    string  `json:"created_at"`
}

// AddMemory stores a fact, returning the row id. Repeats raise salience so a
// fact mentioned twice outranks one mentioned once.
func (s *Store) AddMemory(ctx context.Context, in MemoryInput) (int64, error) {
	if in.Salience <= 0 {
		in.Salience = 1
	}
	now := nowRFC3339()
	// One statement instead of INSERT + SELECT: RETURNING also answers the
	// conflicting (DO UPDATE) case on both engines, so the read-back can go.
	var id int64
	err := s.queryRowContext(ctx, `
INSERT INTO memories (scope, group_openid, member_openid, kind, text, source, salience, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (scope, group_openid, member_openid, text)
DO UPDATE SET salience = memories.salience + 0.5, kind = excluded.kind, source = excluded.source
RETURNING id`,
		in.Scope, in.GroupOpenID, in.MemberOpenID, in.Kind, in.Text, in.Source, in.Salience, now).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// VisibleMemories returns the candidates a conversation may recall: its own
// scope plus global. `query` narrows by substring when non-empty.
func (s *Store) VisibleMemories(ctx context.Context, scope, groupOpenID, memberOpenID, query string, limit int) ([]Memory, error) {
	if limit <= 0 || limit > 500 {
		limit = 60
	}
	sqlText := `SELECT id, scope, group_openid, member_openid, kind, text, source, salience, hits,
	                  last_used_at, created_at
	           FROM memories
	           WHERE ((scope = 'global')
	                  OR (scope = 'group' AND group_openid = ?)
	                  OR (scope = 'member' AND member_openid = ?))`
	args := []any{groupOpenID, memberOpenID}
	if strings.TrimSpace(query) != "" {
		sqlText += ` AND text LIKE '%' || ? || '%'`
		args = append(args, query)
	}
	sqlText += ` ORDER BY salience DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.queryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]Memory, 0, limit)
	for rows.Next() {
		var m Memory
		var lastUsed nullableTimeText
		if err := rows.Scan(&m.ID, &m.Scope, &m.GroupOpenID, &m.MemberOpenID, &m.Kind, &m.Text,
			&m.Source, &m.Salience, &m.Hits, &lastUsed, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.LastUsedAt = lastUsed.Value
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListMemories pages the admin view; empty filters mean "everything".
func (s *Store) ListMemories(ctx context.Context, scope, groupOpenID, memberOpenID string, limit, offset int) ([]Memory, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	query := `SELECT id, scope, group_openid, member_openid, kind, text, source, salience, hits,
	                 last_used_at, created_at
	          FROM memories WHERE 1=1`
	args := []any{}
	if scope != "" {
		query += ` AND scope = ?`
		args = append(args, scope)
	}
	if groupOpenID != "" {
		query += ` AND group_openid = ?`
		args = append(args, groupOpenID)
	}
	if memberOpenID != "" {
		query += ` AND member_openid = ?`
		args = append(args, memberOpenID)
	}
	query += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]Memory, 0, limit)
	for rows.Next() {
		var m Memory
		var lastUsed nullableTimeText
		if err := rows.Scan(&m.ID, &m.Scope, &m.GroupOpenID, &m.MemberOpenID, &m.Kind, &m.Text,
			&m.Source, &m.Salience, &m.Hits, &lastUsed, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.LastUsedAt = lastUsed.Value
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMemory removes one fact.
func (s *Store) DeleteMemory(ctx context.Context, id int64) (bool, error) {
	res, err := s.execContext(ctx, `DELETE FROM memories WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteMemoriesForGroup forgets everything scoped to one group.
func (s *Store) DeleteMemoriesForGroup(ctx context.Context, groupOpenID string) (int64, error) {
	res, err := s.execContext(ctx,
		`DELETE FROM memories WHERE scope = 'group' AND group_openid = ?`, groupOpenID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TouchMemories records that these facts were actually used.
func (s *Store) TouchMemories(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := nowRFC3339()
	for _, id := range ids {
		if _, err := s.execContext(ctx,
			`UPDATE memories SET hits = hits + 1, last_used_at = `+s.dialect.NullableTimeArg()+` WHERE id = ?`, now, id); err != nil {
			return err
		}
	}
	return nil
}

// CountMemories reports how many facts a scope holds (cap enforcement).
func (s *Store) CountMemories(ctx context.Context, scope, groupOpenID, memberOpenID string) (int, error) {
	var n int
	err := s.queryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE scope = ? AND group_openid = ? AND member_openid = ?`,
		scope, groupOpenID, memberOpenID).Scan(&n)
	return n, err
}

// DropWeakestMemory evicts the lowest-salience fact of a scope, used when the
// cap is reached.
func (s *Store) DropWeakestMemory(ctx context.Context, scope, groupOpenID, memberOpenID string) error {
	_, err := s.execContext(ctx, `
DELETE FROM memories WHERE id = (
  SELECT id FROM memories WHERE scope = ? AND group_openid = ? AND member_openid = ?
  ORDER BY salience ASC, hits ASC, id ASC LIMIT 1)`, scope, groupOpenID, memberOpenID)
	return err
}

// --- tool calls (agent audit trail) ---

// Tool call statuses.
const (
	ToolCallOK       = "ok"
	ToolCallError    = "error"
	ToolCallRejected = "rejected" // unknown tool, or not allowed in this group
)

// ToolCallInput is one recorded tool invocation.
type ToolCallInput struct {
	ConversationID string `json:"conversation_id"`
	GroupOpenID    string `json:"group_openid"`
	MemberOpenID   string `json:"member_openid"`
	Tool           string `json:"tool"`
	Args           string `json:"args"`
	Status         string `json:"status"`
	Error          string `json:"error"`
	LatencyMS      int64  `json:"latency_ms"`
	ResultChars    int    `json:"result_chars"`
}

// ToolCallRecord is a stored invocation.
type ToolCallRecord struct {
	ID int64 `json:"id"`
	ToolCallInput
	CreatedAt string `json:"created_at"`
}

// AddToolCall appends one audit row.
func (s *Store) AddToolCall(ctx context.Context, in ToolCallInput) (int64, error) {
	var id int64
	err := s.queryRowContext(ctx, `
INSERT INTO tool_calls (conversation_id, group_openid, member_openid, tool, args, status, error,
                        latency_ms, result_chars, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id`,
		in.ConversationID, in.GroupOpenID, in.MemberOpenID, in.Tool, in.Args, in.Status,
		in.Error, in.LatencyMS, in.ResultChars, nowRFC3339()).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// ListToolCalls returns the newest invocations (beforeID 0 = from the top).
func (s *Store) ListToolCalls(ctx context.Context, limit int, beforeID int64) ([]ToolCallRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	query := `SELECT id, conversation_id, group_openid, member_openid, tool, args, status, error,
	                 latency_ms, result_chars, created_at
	          FROM tool_calls`
	args := []any{}
	if beforeID > 0 {
		query += ` WHERE id < ?`
		args = append(args, beforeID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]ToolCallRecord, 0, limit)
	for rows.Next() {
		var r ToolCallRecord
		if err := rows.Scan(&r.ID, &r.ConversationID, &r.GroupOpenID, &r.MemberOpenID, &r.Tool,
			&r.Args, &r.Status, &r.Error, &r.LatencyMS, &r.ResultChars, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneToolCalls keeps the newest `keep` audit rows.
func (s *Store) PruneToolCalls(ctx context.Context, keep int) (int64, error) {
	if keep <= 0 {
		keep = 20000
	}
	res, err := s.execContext(ctx,
		`DELETE FROM tool_calls WHERE id <= (SELECT MAX(id) - ? FROM tool_calls)`, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) PruneStats(ctx context.Context, before string) error {
	_, err := s.execContext(ctx, `DELETE FROM stats WHERE bucket < ?`, before)
	return err
}

// TopGroups returns the busiest groups over the retained audit window.
func (s *Store) TopGroups(ctx context.Context, since string, limit int) ([]TopGroup, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.queryContext(ctx, `
SELECT r.group_openid,
       COALESCE((SELECT c.group_name FROM conversations c
                 WHERE c.group_openid = r.group_openid AND c.group_name <> '' LIMIT 1), '') AS group_name,
       COUNT(*) AS n
FROM records r
WHERE r.direction = 'in' AND r.group_openid <> '' AND r.created_at >= ?
GROUP BY r.group_openid
ORDER BY n DESC
LIMIT ?`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopGroup{}
	for rows.Next() {
		var g TopGroup
		if err := rows.Scan(&g.GroupOpenID, &g.GroupName, &g.Messages); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// TopGroup is one row of the busiest-groups report.
type TopGroup struct {
	GroupOpenID string `json:"group_openid"`
	GroupName   string `json:"group_name"`
	Messages    int    `json:"messages"`
}

// Counts summarises stored data for /api/status.
func (s *Store) Counts(ctx context.Context) (conversations, messages int, err error) {
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM conversations`).Scan(&conversations); err != nil {
		return 0, 0, err
	}
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&messages); err != nil {
		return 0, 0, err
	}
	return conversations, messages, nil
}

// --- scanning helpers ---

type scanner interface {
	Scan(dest ...any) error
}

func scanConversation(sc scanner) (*Conversation, error) {
	var c Conversation
	var enabled int
	if err := sc.Scan(&c.ID, &c.Scene, &c.GroupOpenID, &c.MemberOpenID, &c.GroupName, &c.MemberName,
		&enabled, &c.MessageCount, &c.TurnCount, &c.TokenEstimate, &c.CreatedAt, &c.LastActiveAt); err != nil {
		return nil, err
	}
	c.Enabled = enabled == 1
	return &c, nil
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	out := []Message{}
	for rows.Next() {
		var (
			m        Message
			rawAttrs string
		)
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.Tokens, &m.Kind, &m.Speaker, &m.SpeakerOpenID, &m.QuoteIndex, &rawAttrs, &m.CreatedAt); err != nil {
			return nil, err
		}
		if rawAttrs != "" {
			_ = json.Unmarshal([]byte(rawAttrs), &m.Attachments)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetMessage loads one message row by id.
func (s *Store) GetMessage(ctx context.Context, id int64) (*Message, error) {
	rows, err := s.queryContext(ctx, `
SELECT id, role, content, tokens, kind, speaker, speaker_openid, quote_index, attachments, created_at
FROM messages WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, ErrNotFound
	}
	return &msgs[0], nil
}

// MessageInConversation reports whether a message row lives in the conversation.
// The admin panel sends a row id when quoting a message, so the server has to
// check that it may not reach across conversations.
func (s *Store) MessageInConversation(ctx context.Context, messageID int64, conversationID string) (bool, error) {
	var n int
	err := s.queryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE id = ? AND conversation_id = ?`,
		messageID, conversationID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
