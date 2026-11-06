package media

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/store"
	"github.com/dynesshely/bot-harness/internal/testpg"
)

func newTestBundle(t *testing.T, srvURL string) *Bundle {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	dl := NewDownloader(&http.Client{Timeout: 5 * time.Second}, 1<<20, 5*time.Second)
	if srvURL != "" {
		// 测试服务器用的是 127.0.0.1，临时放开白名单
		dl.AllowedHostSuffixes = []string{"127.0.0.1"}
	}
	return NewBundle(NewStore(st.DB()), dl, nil, 2)
}

func pngBytes() []byte {
	// 最小合法 PNG（1x1 透明）
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
}

func TestDownloaderRejectsForeignHosts(t *testing.T) {
	dl := NewDownloader(nil, 1<<20, time.Second)
	for _, raw := range []string{
		"https://evil.example.com/x.png",
		"https://127.0.0.1/x.png",
		"http://169.254.169.254/latest/meta-data/",
		"file:///etc/passwd",
	} {
		if _, _, err := dl.Fetch(context.Background(), raw); err == nil {
			t.Fatalf("%s 应被拒绝", raw)
		}
	}
}

func TestIngestCachesImagesAndSkipsNonImages(t *testing.T) {
	png := pngBytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	b := newTestBundle(t, srv.URL)
	got := b.Ingest(context.Background(), []qq.MessageAttachment{
		{URL: srv.URL + "/a.png", ContentType: "image/png", Filename: "a.png", Width: 1, Height: 1},
		{URL: srv.URL + "/v.mp4", ContentType: "video/mp4", Filename: "v.mp4"},
	})
	if len(got) != 2 {
		t.Fatalf("应有 2 个附件，实际 %d", len(got))
	}
	if got[0].Kind != "image" || got[0].MediaID == "" {
		t.Fatalf("图片应被缓存，实际 %#v", got[0])
	}
	if got[1].Kind != "video" || got[1].MediaID != "" {
		t.Fatalf("视频只记录元数据，实际 %#v", got[1])
	}

	contentType, data, err := b.Load(context.Background(), got[0].MediaID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if contentType != "image/png" || len(data) != len(png) {
		t.Fatalf("缓存内容不符：%s %d", contentType, len(data))
	}
}

func TestIngestSkipsOversizedAndFailingDownloads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "huge") {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(make([]byte, 2<<20))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := newTestBundle(t, srv.URL)
	got := b.Ingest(context.Background(), []qq.MessageAttachment{
		{URL: srv.URL + "/huge.png", ContentType: "image/png"},
		{URL: srv.URL + "/missing.png", ContentType: "image/png"},
	})
	if len(got) != 2 {
		t.Fatalf("失败的下载也应保留占位记录，实际 %d", len(got))
	}
	for _, a := range got {
		if a.MediaID != "" {
			t.Fatalf("失败的下载不应有 media id：%#v", a)
		}
	}
}

func TestBundleRespectsImageCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes())
	}))
	defer srv.Close()

	b := newTestBundle(t, srv.URL) // MaxImages = 2
	atts := []qq.MessageAttachment{}
	for i := 0; i < 4; i++ {
		atts = append(atts, qq.MessageAttachment{URL: srv.URL + "/a.png", ContentType: "image/png"})
	}
	got := b.Ingest(context.Background(), atts)
	cached := 0
	for _, a := range got {
		if a.MediaID != "" {
			cached++
		}
	}
	if cached != 2 {
		t.Fatalf("最多缓存 2 张，实际 %d", cached)
	}
}

// --- dialect ---------------------------------------------------------------

// TestDialectMatchesStoreRebind pins media's private copy of the placeholder
// rewriter against the real implementation in internal/store: the two must
// produce identical output for every shape of query the package can meet.
func TestDialectMatchesStoreRebind(t *testing.T) {
	queries := []string{
		"",
		"SELECT 1",
		"SELECT * FROM media WHERE id = ?",
		"INSERT INTO media (id, data) VALUES (?, ?)",
		"SELECT * FROM messages WHERE id > ? AND attachments <> '' ORDER BY id LIMIT ?",
		"SELECT '?' AS q, ? AS v",
		"SELECT 'it''s ?' AS q, ? AS v",
		"SELECT \"weird?column\" FROM t WHERE a = ?",
		"SELECT 1 -- what?\nWHERE a = ?",
		"SELECT 'a?b', \"c?d\", ? FROM t",
		"-- leading ? comment\nSELECT ?",
	}

	pg := newDialect(DriverPostgres)
	want := store.NewDialect(store.DriverPostgres)
	for _, q := range queries {
		if got, exp := pg.rebind(q), want.Rebind(q); got != exp {
			t.Fatalf("rebind mismatch for %q:\n media: %q\n store: %q", q, got, exp)
		}
	}
}

func TestDialectRebindPostgres(t *testing.T) {
	pg := newDialect(DriverPostgres)
	cases := []struct{ in, want string }{
		{"SELECT * FROM media WHERE id = ?", "SELECT * FROM media WHERE id = $1"},
		{"VALUES (?, ?, ?)", "VALUES ($1, $2, $3)"},
		// 引号内的问号不是占位符
		{"SELECT '?' , ?", "SELECT '?' , $1"},
		{"SELECT 'it''s ?', ?", "SELECT 'it''s ?', $1"},
		{"SELECT \"q?\" , ?", "SELECT \"q?\" , $1"},
		{"-- what?\nSELECT ?", "-- what?\nSELECT $1"},
		// 没有问号时不复制
		{"SELECT 1", "SELECT 1"},
	}
	for _, c := range cases {
		if got := pg.rebind(c.in); got != c.want {
			t.Fatalf("rebind(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	if got := newDialect(DriverSQLite).rebind("SELECT * FROM media WHERE id = ?"); got != "SELECT * FROM media WHERE id = ?" {
		t.Fatalf("sqlite 不应改写占位符，得到 %q", got)
	}
}

// TestNewStoreDetectsDriver pins the compatibility shim: the historical
// media.NewStore(st.DB()) call must pick the right placeholders on its own, so
// the cmd wiring keeps working on PostgreSQL without being touched.
func TestNewStoreDetectsDriver(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/detect.db")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if got := NewStore(st.DB()).Driver(); got != DriverSQLite {
		t.Fatalf("NewStore(sqlite) = %q, want sqlite", got)
	}

	// PostgreSQL 的检测需要真连接，走 PG_TEST_DSN 的 gated 分支。
	if os.Getenv("PG_TEST_DSN") == "" {
		t.Log("PG_TEST_DSN 未设置，跳过 PostgreSQL 侧的驱动检测")
		return
	}
	pg := pgMediaStore(t)
	if got := NewStore(pg.DB()).Driver(); got != DriverPostgres {
		t.Fatalf("NewStore(pgx) = %q, want postgres", got)
	}

	// 端到端：自动检测出来的 store 必须真的能写能读。
	img := uniquePNG(t)
	id, err := NewStore(pg.DB()).Put(context.Background(), "image/png", img)
	if err != nil {
		t.Fatalf("自动检测的 store 写入失败：%v", err)
	}
	_, data, err := pg.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if len(data) != len(img) {
		t.Fatalf("读回长度 %d，want %d", len(data), len(img))
	}
}

func TestNewStoreWithDriverSelectsDialect(t *testing.T) {
	for _, driver := range []string{"postgres", "postgresql", "pg", "pgx", "pgx/v5", " PGX "} {
		s := NewStoreWithDriver(nil, driver)
		if got := s.Driver(); got != DriverPostgres {
			t.Fatalf("driver %q -> %q, want postgres", driver, got)
		}
		if got := s.Placeholder("SELECT ?"); got != "SELECT $1" {
			t.Fatalf("driver %q placeholder = %q", driver, got)
		}
	}
	for _, driver := range []string{"", "sqlite", "sqlite3", "modernc"} {
		s := NewStoreWithDriver(nil, driver)
		if got := s.Driver(); got != DriverSQLite {
			t.Fatalf("driver %q -> %q, want sqlite", driver, got)
		}
	}

	// NewStore 保持历史行为：零值方言 = SQLite。
	if got := NewStore(nil).Driver(); got != DriverSQLite {
		t.Fatalf("NewStore 默认方言 = %q, want sqlite", got)
	}

	// SetDialect 可以在构造后切换，空值不改变现状。
	s := NewStore(nil)
	s.SetDialect(DriverPostgres)
	if s.Driver() != DriverPostgres {
		t.Fatalf("SetDialect(postgres) 未生效")
	}
	s.SetDialect("")
	if s.Driver() != DriverPostgres {
		t.Fatalf("SetDialect(空串) 不应重置方言")
	}
}

// --- prune (SQLite) --------------------------------------------------------

// insertMessageSQL is the statement mustAppendAttachments runs; it is kept as a
// "?" query so the same helper works on both backends.
const insertMessageSQL = "INSERT INTO messages (conversation_id, role, content, tokens, kind, speaker, " +
	"speaker_openid, quote_index, attachments, addressed, created_at) " +
	"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"

// sqliteStore opens a throwaway SQLite store and wraps its handle the way the
// production wiring does.
func sqliteStore(t *testing.T) *Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/prune.db")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewStoreWithDriver(st.DB(), store.DriverSQLite)
}

func mustPut(t *testing.T, s *Store, data []byte) string {
	t.Helper()
	id, err := s.Put(context.Background(), "image/png", data)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	return id
}

func mustAppendAttachments(t *testing.T, db *sql.DB, s *Store, attachments string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), s.Placeholder(insertMessageSQL),
		"c1", "user", "x", 0, "dialogue", "", "", "", attachments, 1, time.Now().Format(time.RFC3339)); err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

func backdateMedia(t *testing.T, db *sql.DB, s *Store, ids ...string) {
	t.Helper()
	old := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	for _, id := range ids {
		if _, err := db.ExecContext(context.Background(),
			s.Placeholder("UPDATE media SET created_at = ? WHERE id = ?"), old, id); err != nil {
			t.Fatalf("backdate media: %v", err)
		}
	}
}

func mediaExists(t *testing.T, db *sql.DB, s *Store, id string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		s.Placeholder("SELECT COUNT(*) FROM media WHERE id = ?"), id).Scan(&n); err != nil {
		t.Fatalf("count media: %v", err)
	}
	return n == 1
}

// TestPruneHandlesAttachmentEncodings covers the three shapes Prune meets in
// messages.attachments: the empty string (the overwhelming majority of rows),
// a document that does not parse, and a normal JSON array.
func TestPruneHandlesAttachmentEncodings(t *testing.T) {
	ctx := context.Background()
	s := sqliteStore(t)
	db := s.DB()

	live := mustPut(t, s, append(pngBytes(), 0x01))
	emptyRow := mustPut(t, s, append(pngBytes(), 0x02))
	badJSON := mustPut(t, s, append(pngBytes(), 0x03))
	orphan := mustPut(t, s, append(pngBytes(), 0x04))
	fresh := mustPut(t, s, append(pngBytes(), 0x05))

	normal, err := json.Marshal([]store.Attachment{{Kind: "image", MediaID: live}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mustAppendAttachments(t, db, s, string(normal))
	mustAppendAttachments(t, db, s, "")                      // 空串：没有附件
	mustAppendAttachments(t, db, s, "[{\"kind\":\"image\",") // 坏 JSON，文本里带着 badJSON 的 id
	mustAppendAttachments(t, db, s, "null")                  // JSON null
	mustAppendAttachments(t, db, s, "[]")                    // 空数组

	// 把坏 JSON 指向的 id 写进那段文本（截断后的文档仍然是坏的）。
	if _, err := db.ExecContext(ctx, s.Placeholder(
		"UPDATE messages SET attachments = ? WHERE attachments = ?"),
		fmt.Sprintf("[{\"kind\":\"image\",\"media_id\":%q}", badJSON),
		"[{\"kind\":\"image\","); err != nil {
		t.Fatalf("corrupt attachments: %v", err)
	}

	backdateMedia(t, db, s, live, emptyRow, badJSON, orphan)

	n, err := s.Prune(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 3 {
		t.Fatalf("prune 删除 %d 行，want 3（空串/坏 JSON/无引用）", n)
	}

	if !mediaExists(t, db, s, live) {
		t.Fatalf("正常 JSON 引用的 blob 被误删")
	}
	if !mediaExists(t, db, s, fresh) {
		t.Fatalf("未过期的 blob 被误删")
	}
	for name, id := range map[string]string{
		"空串 attachments":       emptyRow,
		"坏 JSON 的 attachments": badJSON,
		"完全没有引用":               orphan,
	} {
		if mediaExists(t, db, s, id) {
			t.Fatalf("%s 的 blob 应被删除", name)
		}
	}

	// 幂等：再跑一次没有可删的。
	if n, err := s.Prune(ctx, 24*time.Hour); err != nil || n != 0 {
		t.Fatalf("第二次 prune = %d, %v; want 0, nil", n, err)
	}
}

// TestPrunePagesThroughMessages proves the Go-side scan crosses the batch
// boundary: a reference that only appears after pruneBatchSize rows must still
// keep its blob alive.
func TestPrunePagesThroughMessages(t *testing.T) {
	ctx := context.Background()
	s := sqliteStore(t)
	db := s.DB()

	live := mustPut(t, s, append(pngBytes(), 0x11))
	unused := mustPut(t, s, append(pngBytes(), 0x12))

	for i := 0; i < pruneBatchSize+25; i++ {
		mustAppendAttachments(t, db, s, "")
	}
	encoded, err := json.Marshal([]store.Attachment{{Kind: "image", MediaID: live}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mustAppendAttachments(t, db, s, string(encoded))

	backdateMedia(t, db, s, live, unused)

	n, err := s.Prune(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("prune 删除 %d 行，want 1", n)
	}
	if !mediaExists(t, db, s, live) {
		t.Fatalf("分页之后的引用没被看到，blob 被误删")
	}
	if mediaExists(t, db, s, unused) {
		t.Fatalf("无引用的 blob 未被删除")
	}
}

// TestPruneDeletesExpiredInBatches covers the delete side of the paging: more
// expired rows than one batch, so Prune must walk them without ever building an
// IN (...) list longer than pruneBatchSize (SQLite caps bind variables, and so
// does PostgreSQL's wire protocol).
func TestPruneDeletesExpiredInBatches(t *testing.T) {
	ctx := context.Background()
	s := sqliteStore(t)
	db := s.DB()

	const expired = pruneBatchSize + 137
	old := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	for i := 0; i < expired; i++ {
		if _, err := db.ExecContext(ctx, s.Placeholder(
			"INSERT INTO media (id, content_type, size, sha256, data, created_at) VALUES (?, ?, ?, ?, ?, ?)"),
			fmt.Sprintf("expired-%05d", i), "image/png", 1, "sha", []byte{0}, old); err != nil {
			t.Fatalf("insert expired media: %v", err)
		}
	}
	live := mustPut(t, s, append(pngBytes(), 0x31))
	encoded, err := json.Marshal([]store.Attachment{{Kind: "image", MediaID: live}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mustAppendAttachments(t, db, s, string(encoded))
	backdateMedia(t, db, s, live)

	n, err := s.Prune(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != expired {
		t.Fatalf("prune 删除 %d 行，want %d", n, expired)
	}
	if !mediaExists(t, db, s, live) {
		t.Fatalf("被引用的 blob 不应删除")
	}
	if got, _, err := s.Stats(ctx); err != nil || got != 1 {
		t.Fatalf("剩余 media = %d, %v; want 1", got, err)
	}
}

// TestPruneWithoutReferences covers the other Prune branch: when no message
// references any blob at all, Prune takes the cutoff-only fast path instead of
// building an id list.
//
// This branch is the one the "id NOT IN (...)" statement never runs on, so a
// regression test that only ever seeds a reference (see the tests above) cannot
// see a malformed placeholder list — the historical "... IN (?, ?@)" typo lived
// exactly here and only fired once a photo had actually been referenced.
func TestPruneWithoutReferences(t *testing.T) {
	ctx := context.Background()
	s := sqliteStore(t)
	db := s.DB()

	orphanA := mustPut(t, s, append(pngBytes(), 0x41))
	orphanB := mustPut(t, s, append(pngBytes(), 0x42))
	fresh := mustPut(t, s, append(pngBytes(), 0x43))

	// Messages exist but carry no attachments at all: attachments is '' for the
	// overwhelming majority of production rows.
	for i := 0; i < 3; i++ {
		mustAppendAttachments(t, db, s, "")
	}
	mustAppendAttachments(t, db, s, "[]")

	backdateMedia(t, db, s, orphanA, orphanB)

	n, err := s.Prune(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 2 {
		t.Fatalf("prune 删除 %d 行，want 2", n)
	}
	for name, id := range map[string]string{"过期孤儿 A": orphanA, "过期孤儿 B": orphanB} {
		if mediaExists(t, db, s, id) {
			t.Fatalf("%s 应被删除", name)
		}
	}
	if !mediaExists(t, db, s, fresh) {
		t.Fatalf("未过期的 blob 被误删")
	}

	// 幂等：没有可删的就返回 0，不报错。
	if n, err := s.Prune(ctx, 24*time.Hour); err != nil || n != 0 {
		t.Fatalf("第二次 prune = %d, %v; want 0, nil", n, err)
	}
}

func TestStatsAndGetMissing(t *testing.T) {
	ctx := context.Background()
	s := sqliteStore(t)
	if _, _, err := s.Get(ctx, "nope"); err != sql.ErrNoRows {
		t.Fatalf("Get(missing) = %v, want sql.ErrNoRows", err)
	}
	mustPut(t, s, append(pngBytes(), 0x21))
	count, bytes, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if count != 1 || bytes != int64(len(pngBytes())+1) {
		t.Fatalf("stats = %d, %d", count, bytes)
	}
}

// --- prune helpers ---------------------------------------------------------

func TestAddMediaIDs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"spaces", "   ", nil},
		{"null", "null", nil},
		{"empty array", "[]", nil},
		{"one", "[{\"media_id\":\"a\"}]", []string{"a"}},
		{"two", "[{\"media_id\":\"a\"},{\"media_id\":\"b\"}]", []string{"a", "b"}},
		{"no media id", "[{\"kind\":\"image\"}]", nil},
		{"blank media id", "[{\"media_id\":\"   \"}]", nil},
		{"object not array", "{\"media_id\":\"a\"}", nil},
		{"truncated", "[{\"media_id\":\"a\"}", nil},
	}
	for _, c := range cases {
		seen := map[string]struct{}{}
		addMediaIDs(seen, c.in)
		if len(seen) != len(c.want) {
			t.Fatalf("%s: seen = %v, want %v", c.name, seen, c.want)
		}
		for _, id := range c.want {
			if _, ok := seen[id]; !ok {
				t.Fatalf("%s: 缺少 %q（seen=%v）", c.name, id, seen)
			}
		}
	}
}

// --- PostgreSQL (gated) ----------------------------------------------------

// pgMediaStore opens PG_TEST_DSN through the real store, which creates the
// media/messages tables with the production schema (the same DDL pgmigrate
// targets), and wraps the handle with the PostgreSQL dialect.
func pgMediaStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN not set; skipping PostgreSQL media test")
	}
	testpg.Guard(t, dsn)
	st, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// store.Open 建的是生产 schema；这里只确认 media 列与预期一致。
	rows, err := st.DB().QueryContext(context.Background(),
		"SELECT column_name FROM information_schema.columns WHERE table_name = 'media'")
	if err != nil {
		t.Fatalf("inspect media columns: %v", err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		cols[c] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("scan columns: %v", err)
	}
	for _, want := range []string{"id", "content_type", "size", "sha256", "data", "created_at"} {
		if !cols[want] {
			t.Fatalf("media 缺列 %q（有 %v）", want, cols)
		}
	}
	return NewStoreWithDriver(st.DB(), store.DriverPostgres)
}

// uniquePNG returns pngBytes with a random suffix, so this run's content hash
// cannot collide with rows a previous run left behind in the scratch database.
func uniquePNG(t *testing.T) []byte {
	t.Helper()
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return append(pngBytes(), []byte(hex.EncodeToString(suffix))...)
}

// TestPostgresMediaRoundTripAndPrune is the PostgreSQL counterpart of the
// SQLite tests: Put stores a BLOB, Get returns it byte for byte, and Prune copes
// with empty, corrupt and well-formed attachments JSON.
func TestPostgresMediaRoundTripAndPrune(t *testing.T) {
	ctx := context.Background()
	s := pgMediaStore(t)
	db := s.DB()

	if got := s.Driver(); got != DriverPostgres {
		t.Fatalf("dialect = %q, want postgres", got)
	}

	img := uniquePNG(t)
	id := mustPut(t, s, img)

	contentType, data, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if contentType != "image/png" {
		t.Fatalf("content type = %q", contentType)
	}
	if len(data) != len(img) {
		t.Fatalf("len(data) = %d, want %d", len(data), len(img))
	}
	for i := range img {
		if data[i] != img[i] {
			t.Fatalf("第 %d 字节不一致：%#x != %#x", i, data[i], img[i])
		}
	}

	// 幂等：同一张图再 Put 一次不报错、id 不变。
	if again, err := s.Put(ctx, "image/png", img); err != nil || again != id {
		t.Fatalf("重复 Put = %q, %v; want %q, nil", again, err, id)
	}

	// 三种 attachments 输入：正常 JSON 保活，空串与坏 JSON 不保活。
	live := id
	emptyRow := mustPut(t, s, uniquePNG(t))
	badJSON := mustPut(t, s, uniquePNG(t))
	orphan := mustPut(t, s, uniquePNG(t))
	fresh := mustPut(t, s, uniquePNG(t))

	normal, err := json.Marshal([]store.Attachment{{Kind: "image", MediaID: live}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mustAppendAttachments(t, db, s, string(normal))
	mustAppendAttachments(t, db, s, "")
	mustAppendAttachments(t, db, s, fmt.Sprintf("[{\"kind\":\"image\",\"media_id\":%q}", badJSON))
	mustAppendAttachments(t, db, s, "null")

	backdateMedia(t, db, s, live, emptyRow, badJSON, orphan)

	if _, err := s.Prune(ctx, 24*time.Hour); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if !mediaExists(t, db, s, live) {
		t.Fatalf("正常 JSON 引用的 blob 被误删")
	}
	if !mediaExists(t, db, s, fresh) {
		t.Fatalf("未过期的 blob 被误删")
	}
	for name, mid := range map[string]string{
		"空串 attachments":       emptyRow,
		"坏 JSON 的 attachments": badJSON,
		"完全没有引用":               orphan,
	} {
		if mediaExists(t, db, s, mid) {
			t.Fatalf("%s 的 blob 应被删除", name)
		}
	}

	// 清理本次插入的 messages 行，避免 scratch 库越跑越大。
	if _, err := db.ExecContext(ctx, s.Placeholder("DELETE FROM messages WHERE conversation_id = ?"), "c1"); err != nil {
		t.Fatalf("cleanup messages: %v", err)
	}
}

// TestPostgresPruneWithoutReferences is the PostgreSQL counterpart of
// TestPruneWithoutReferences, and the reason it exists is the same: the fast
// path (no message references any blob) is a different statement from the id
// list one, so both branches need a row of their own on each backend.
func TestPostgresPruneWithoutReferences(t *testing.T) {
	ctx := context.Background()
	s := pgMediaStore(t)
	db := s.DB()

	orphan := mustPut(t, s, uniquePNG(t))
	fresh := mustPut(t, s, uniquePNG(t))

	// 有消息行，但一条附件都没有引用。
	mustAppendAttachments(t, db, s, "")
	mustAppendAttachments(t, db, s, "[]")

	backdateMedia(t, db, s, orphan)

	// 计数不写成 1：PG_TEST_DSN 指向的是共享的 scratch 库，先前几轮跑剩下的
	// 过期 media 行也会在这条 DELETE 里被回收（这正是 fast path 的语义）。
	n, err := s.Prune(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n < 1 {
		t.Fatalf("prune 删除 %d 行，want >= 1", n)
	}
	if mediaExists(t, db, s, orphan) {
		t.Fatalf("过期孤儿 blob 应被删除")
	}
	if !mediaExists(t, db, s, fresh) {
		t.Fatalf("未过期的 blob 被误删")
	}

	if _, err := db.ExecContext(ctx, s.Placeholder("DELETE FROM messages WHERE conversation_id = ?"), "c1"); err != nil {
		t.Fatalf("cleanup messages: %v", err)
	}
	for _, id := range []string{orphan, fresh} {
		if _, err := db.ExecContext(ctx, s.Placeholder("DELETE FROM media WHERE id = ?"), id); err != nil {
			t.Fatalf("cleanup media: %v", err)
		}
	}
}
