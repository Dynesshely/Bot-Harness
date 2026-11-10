package pgmigrate

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/testpg"
	_ "modernc.org/sqlite"
)

// fixtureSchema is a compact SQLite schema that exercises every feature the
// migrator has to handle: AUTOINCREMENT identities, TEXT/INTEGER/REAL/BLOB
// columns, NOT NULL, column defaults, a UNIQUE constraint, a plain index and
// a composite text primary key.
const fixtureSchema = `
CREATE TABLE conversations (
  id             TEXT PRIMARY KEY,
  scene          TEXT NOT NULL,
  group_openid   TEXT NOT NULL DEFAULT '',
  enabled        INTEGER NOT NULL DEFAULT 1,
  message_count  INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL,
  last_active_at TEXT NOT NULL
);
CREATE INDEX idx_conversations_last_active ON conversations(last_active_at DESC);

CREATE TABLE messages (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL,
  role            TEXT NOT NULL,
  content         TEXT NOT NULL,
  tokens          INTEGER NOT NULL DEFAULT 0,
  kind            TEXT NOT NULL DEFAULT 'dialogue',
  attachments     TEXT NOT NULL DEFAULT '',
  addressed       INTEGER NOT NULL DEFAULT 1,
  created_at      TEXT NOT NULL
);
CREATE INDEX idx_messages_conversation ON messages(conversation_id, id);

CREATE TABLE records (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  direction       TEXT NOT NULL,
  conversation_id TEXT NOT NULL DEFAULT '',
  latency_ms      INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL
);

CREATE TABLE tool_calls (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL DEFAULT '',
  tool            TEXT NOT NULL DEFAULT '',
  latency_ms      INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL
);

CREATE TABLE memories (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  scope         TEXT NOT NULL DEFAULT '',
  group_openid  TEXT NOT NULL DEFAULT '',
  member_openid TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL DEFAULT '',
  text          TEXT NOT NULL,
  salience      REAL NOT NULL DEFAULT 1,
  hits          INTEGER NOT NULL DEFAULT 0,
  last_used_at  TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  UNIQUE (scope, group_openid, member_openid, text)
);
CREATE INDEX idx_memories_scope ON memories(scope, group_openid, member_openid, id DESC);

CREATE TABLE proactive_state (
  group_openid  TEXT PRIMARY KEY,
  last_sent_at  TEXT NOT NULL DEFAULT '',
  silent_streak INTEGER NOT NULL DEFAULT 0,
  updated_at    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE scheduled_tasks (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL DEFAULT '',
  text            TEXT NOT NULL,
  due_at          TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'pending',
  lease_until     TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  sent_at         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_tasks_due ON scheduled_tasks(status, due_at);

CREATE TABLE dedup (
  msg_id     TEXT PRIMARY KEY,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_dedup_created ON dedup(created_at);

CREATE TABLE settings (
  k          TEXT PRIMARY KEY,
  v          TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE media (
  id           TEXT PRIMARY KEY,
  content_type TEXT NOT NULL,
  size         INTEGER NOT NULL,
  sha256       TEXT NOT NULL,
  data         BLOB NOT NULL,
  created_at   TEXT NOT NULL
);
CREATE INDEX idx_media_created ON media(created_at);

CREATE TABLE schema_migrations (
  name       TEXT PRIMARY KEY,
  applied_at TEXT NOT NULL
);

CREATE TABLE stats (
  bucket     TEXT PRIMARY KEY,
  in_count   INTEGER NOT NULL DEFAULT 0,
  out_count  INTEGER NOT NULL DEFAULT 0,
  tokens     INTEGER NOT NULL DEFAULT 0
);
`

// buildFixture writes a SQLite database with representative rows.
func buildFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(fixtureSchema); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("fixture exec %q: %v", q, err)
		}
	}
	now := "2026-11-01T12:00:00+08:00"
	mustExec("INSERT INTO conversations (id, scene, group_openid, enabled, message_count, created_at, last_active_at) VALUES (?,?,?,?,?,?,?)",
		"conv-1", "group", "g1", 1, 3, now, now)
	mustExec("INSERT INTO conversations (id, scene, group_openid, enabled, message_count, created_at, last_active_at) VALUES (?,?,?,?,?,?,?)",
		"conv-2", "private", "", 0, 0, now, now)
	for i := 1; i <= 50; i++ {
		mustExec("INSERT INTO messages (conversation_id, role, content, tokens, kind, created_at) VALUES (?,?,?,?,?,?)",
			"conv-1", "user", fmt.Sprintf("消息 %d — ünïcode", i), int64(i), "dialogue", now)
	}
	mustExec("DELETE FROM messages WHERE id IN (7, 19)") // leave id gaps
	for i := 1; i <= 300; i++ {
		mustExec("INSERT INTO records (direction, conversation_id, latency_ms, created_at) VALUES (?,?,?,?)",
			"in", "conv-1", int64(i), now)
	}
	mustExec("INSERT INTO tool_calls (conversation_id, tool, created_at) VALUES (?,?,?)", "conv-1", "now", now)
	mustExec("INSERT INTO proactive_state (group_openid, updated_at) VALUES (?,?)", "g1", now)
	mustExec("INSERT INTO scheduled_tasks (conversation_id, text, due_at, lease_until, created_at) VALUES (?,?,?,?,?)", "conv-1", "提醒", now, now, now)
	mustExec("INSERT INTO dedup (msg_id, created_at) VALUES (?,?)", "m-1", now)
	mustExec("INSERT INTO dedup (msg_id, created_at) VALUES (?,?)", "m-2", now)
	mustExec("INSERT INTO settings (k, v, updated_at) VALUES (?,?,?)", "model", `"gpt-4o-mini"`, now)
	mustExec("INSERT INTO settings (k, v, updated_at) VALUES (?,?,?)", "tools_enabled", "true", now)
	mustExec("INSERT INTO settings (k, v, updated_at) VALUES (?,?,?)", "limits", `{"a":1,"b":[1,2,3]}`, now)
	mustExec("INSERT INTO schema_migrations (name, applied_at) VALUES (?,?)", "0001_x", now)
	mustExec("INSERT INTO stats (bucket, in_count, out_count, tokens) VALUES (?,?,?,?)", now, 9, 4, 1234)
	// REAL column: a fractional value and a whole number (SQLite stores the
	// latter as an integer, which must still compare equal after the copy).
	mustExec("INSERT INTO memories (scope, group_openid, member_openid, kind, text, salience, hits, created_at) VALUES (?,?,?,?,?,?,?,?)",
		"group", "g1", "u1", "fact", "喜欢抹茶", 0.75, 3, now)
	mustExec("INSERT INTO memories (scope, group_openid, member_openid, kind, text, salience, hits, last_used_at, created_at) VALUES (?,?,?,?,?,?,?,?,?)",
		"group", "g1", "u1", "fact", "整点睡眠", 1, 0, now, now)
	mustExec("INSERT INTO memories (scope, group_openid, member_openid, kind, text, salience, hits, created_at) VALUES (?,?,?,?,?,?,?,?)",
		"group", "g1", "u1", "fact", "极小值", 1e-12, 1, now)

	blob := bytes.Repeat([]byte{0x00, 0xff, 0x10, 0x7f}, 512)
	blob[0] = 0x00 // leading NUL must survive BYTEA untouched
	sum := blobDigest(blob)
	mustExec("INSERT INTO media (id, content_type, size, sha256, data, created_at) VALUES (?,?,?,?,?,?)",
		sum, "image/png", int64(len(blob)), sum, blob, now)
	// Second blob that is not valid UTF-8 at all.
	raw := []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01}
	rawSum := blobDigest(raw)
	mustExec("INSERT INTO media (id, content_type, size, sha256, data, created_at) VALUES (?,?,?,?,?,?)",
		rawSum, "application/octet-stream", int64(len(raw)), rawSum, raw, now)
	return path
}

// testDSN returns the PostgreSQL DSN used by the integration tests, or "".
// It refuses the production database before any connection is opened.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("BOT_HARNESS_TEST_PG_DSN"))
	if dsn != "" {
		testpg.Guard(t, dsn)
	}
	return dsn
}

// dsnForDatabase rewrites the database name inside a PostgreSQL URL DSN.
func dsnForDatabase(t *testing.T, dsn, database string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse test DSN: %v", err)
	}
	u.Path = "/" + database
	return u.String()
}

// freshDatabase creates a scratch database and returns a DSN pointing at it.
// Each test gets its own database so runs never share state.
func freshDatabase(t *testing.T, dsn string) string {
	t.Helper()
	base := dsnForDatabase(t, dsn, "postgres")
	name := fmt.Sprintf("pgmigrate_test_%d", time.Now().UnixNano())
	ctx := context.Background()

	admin, err := OpenTarget(ctx, base)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}
	if _, err := admin.DB.Exec(ctx, "CREATE DATABASE "+quoteIdent(name)); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create scratch database: %v", err)
	}
	_ = admin.Close(ctx)
	t.Cleanup(func() {
		ctx := context.Background()
		c, err := OpenTarget(ctx, base)
		if err != nil {
			return
		}
		defer func() { _ = c.Close(ctx) }()
		_, _ = c.DB.Exec(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(name)+" WITH (FORCE)")
	})
	return dsnForDatabase(t, dsn, name)
}

func TestTableOrderCoversEveryTable(t *testing.T) {
	if len(TableOrder) != 19 {
		t.Fatalf("TableOrder has %d tables, want 19", len(TableOrder))
	}
	seen := map[string]bool{}
	for _, name := range TableOrder {
		if seen[name] {
			t.Fatalf("duplicate table %q in TableOrder", name)
		}
		seen[name] = true
	}
	if TableOrder[len(TableOrder)-1] != MediaTable {
		t.Fatalf("media must be copied last, order is %v", TableOrder)
	}
	pos := map[string]int{}
	for i, n := range TableOrder {
		pos[n] = i
	}
	for _, before := range []string{"settings", "conversations"} {
		for _, after := range []string{"messages", "records", "scheduled_tasks", "commitments", "human_status", "memories", "media"} {
			if pos[before] > pos[after] {
				t.Fatalf("%s must be copied before %s", before, after)
			}
		}
	}
	// The join-gate tables were added after the affect work and must stay after
	// the tables whose rows they reference indirectly.
	for _, before := range []string{"human_status", "affect_states", "affect_events", "affect_history"} {
		for _, after := range []string{"join_decisions", "join_topics"} {
			if pos[before] > pos[after] {
				t.Fatalf("%s must be copied before %s", before, after)
			}
		}
	}
}

func TestMapType(t *testing.T) {
	cases := map[string]string{
		"TEXT":          "TEXT",
		"VARCHAR(32)":   "TEXT",
		"INTEGER":       "INTEGER",
		"BIGINT":        "INTEGER",
		"REAL":          "DOUBLE PRECISION",
		"DOUBLE":        "DOUBLE PRECISION",
		"BLOB":          "BYTEA",
		"BOOLEAN":       "INTEGER",
		"NUMERIC(10,2)": "TEXT",
		"":              "TEXT",
	}
	for in, want := range cases {
		if got := mapType(in).SQL; got != want {
			t.Errorf("mapType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderDDLUsesIdentityAndNoForeignKeys(t *testing.T) {
	tbl := &Table{
		Name:          "messages",
		PK:            []string{"id"},
		AutoIncrement: true,
		Columns: []Column{
			{Name: "id", DeclType: "INTEGER", PKOrder: 1},
			{Name: "content", DeclType: "TEXT", NotNull: true},
			{Name: "kind", DeclType: "TEXT", NotNull: true, HasDefault: true, DefaultExpr: "'dialogue'"},
			{Name: "data", DeclType: "BLOB", NotNull: true},
		},
	}
	ddl := renderDDL(tbl)
	for _, want := range []string{
		"\"id\" BIGINT GENERATED BY DEFAULT AS IDENTITY",
		"\"content\" TEXT NOT NULL",
		"\"kind\" TEXT NOT NULL DEFAULT 'dialogue'",
		"\"data\" BYTEA NOT NULL",
		"PRIMARY KEY (\"id\")",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("DDL missing %q:\n%s", want, ddl)
		}
	}
	upper := strings.ToUpper(ddl)
	if strings.Contains(upper, "FOREIGN KEY") || strings.Contains(upper, "REFERENCES") {
		t.Errorf("DDL must not declare foreign keys:\n%s", ddl)
	}
}

func TestRenderSequenceFixup(t *testing.T) {
	// memories: empty table, counter 1 -> setval(2, false), so the next id is 2,
	// which is exactly what SQLite would have produced.
	mem := &Table{Name: MemoriesTable, PK: []string{"id"}, AutoIncrement: true, HasSeq: true, SeqValue: 1}
	stmts := renderSequenceFixup(mem, "id", 1)
	if len(stmts) != 1 {
		t.Fatalf("memories fixup must be a single statement, got %v", stmts)
	}
	for _, want := range []string{"setval", "GREATEST", "), 1, 0) + 1, false)"} {
		if !strings.Contains(stmts[0], want) {
			t.Fatalf("memories fixup missing %q: %s", want, stmts[0])
		}
	}

	// messages: max(id) and the counter agree; the sequence continues after
	// them, and GREATEST still protects against a stale counter.
	msgs := &Table{Name: "messages", PK: []string{"id"}, AutoIncrement: true, HasSeq: true, SeqValue: 13343}
	stmt := renderSequenceFixup(msgs, "id", 13343)[0]
	if !strings.Contains(stmt, "GREATEST") || !strings.Contains(stmt, "13343") {
		t.Fatalf("messages fixup must use the source counter, got %s", stmt)
	}

	// A stale counter must never make the sequence reissue an id.
	stale := renderSequenceFixup(msgs, "id", 5)[0]
	if !strings.Contains(stale, "MAX(") {
		t.Fatalf("fixup must fall back to max(pk), got %s", stale)
	}

	// A table that never saw a row: setval(1, false) -> the next id is 1.
	// setval(0) would be rejected by PostgreSQL (sequences start at 1).
	empty := renderSequenceFixup(msgs, "id", -3)[0]
	if !strings.Contains(empty, "), 0, 0) + 1, false)") {
		t.Fatalf("empty/negative counters must clamp to setval(1, false), got %s", empty)
	}
	if strings.Contains(empty, ", 0), true)") {
		t.Fatalf("setval(0) is out of bounds on PostgreSQL, got %s", empty)
	}
}

func TestEncodeValueIsLossless(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"null", nil, "0:N:"},
		{"int", int64(-42), "3:I:-42"},
		{"int32", int32(7), "1:I:7"},
		{"empty string", "", "0:S:"},
		{"string", "hi", "2:S:hi"},
		{"unicode length is bytes", "ü", "2:S:ü"},
		{"bool true", true, "1:I:1"},
		{"bool false", false, "1:I:0"},
		{"integral float promotes to int", float64(3), "1:I:3"},
	}
	for _, c := range cases {
		got, err := encodeValue(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("%s: encodeValue(%v) = %q, want %q", c.name, c.in, got, c.want)
		}
	}

	// A fractional float is encoded as its bit pattern, so 0.1 and -0.1
	// cannot collide.
	pos, _ := encodeValue(0.1)
	neg, _ := encodeValue(-0.1)
	if bytes.Equal(pos, neg) {
		t.Fatal("0.1 and -0.1 must not encode identically")
	}
	if !strings.HasPrefix(string(pos), "8:F:") {
		t.Fatalf("fractional float must use the F tag, got %q", pos)
	}
	if !bytes.Equal(pos, mustEncode(t, math.Float64frombits(math.Float64bits(0.1)))) {
		t.Fatal("float encoding is not bit-exact")
	}

	// A BLOB whose bytes happen to spell "ABC" must not collide with the
	// TEXT value "ABC".
	blobEnc, _ := encodeValue([]byte("ABC"))
	textEnc, _ := encodeValue("ABC")
	if bytes.Equal(blobEnc, textEnc) {
		t.Fatal("byte string and text must not encode identically")
	}
}

func mustEncode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := encodeValue(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJSONSemanticEquality(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"object key order is irrelevant", "{\"a\":1,\"b\":2}", "{\"b\":2,\"a\":1}", true},
		{"string", "\"x\"", "\"x\"", true},
		{"integer vs float", "1", "1.0", true},
		{"booleans differ", "true", "false", false},
		{"object value differs", "{\"a\":1}", "{\"a\":2}", false},
		{"non-JSON compares as text", "not-json", "not-json", true},
		{"non-JSON vs JSON", "not-json", "{\"a\":1}", false},
		{"array order matters", "[1,2]", "[2,1]", false},
	}
	for _, c := range cases {
		if got := jsonEqual(c.a, c.b); got != c.want {
			t.Errorf("%s: jsonEqual(%q, %q) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
	}
}

func TestMediaBatchIsCapped(t *testing.T) {
	media := &Table{Name: MediaTable}
	if got := media.BatchSize(1000); got != MediaBatchMax {
		t.Fatalf("media batch = %d, want %d", got, MediaBatchMax)
	}
	if got := media.BatchSize(4); got != 4 {
		t.Fatalf("a smaller explicit batch must be honoured, got %d", got)
	}
	other := &Table{Name: "records"}
	if got := other.BatchSize(500); got != 500 {
		t.Fatalf("records batch = %d, want 500", got)
	}
	if got := other.BatchSize(0); got != DefaultBatch {
		t.Fatalf("zero batch must fall back to the default, got %d", got)
	}
}

func TestSourceDiscoversSchema(t *testing.T) {
	src, err := OpenSource(buildFixture(t))
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer func() { _ = src.Close() }()

	if len(src.AllTables()) != 12 {
		t.Fatalf("discovered %d tables, want 12", len(src.AllTables()))
	}
	msgs := findTable(t, src, "messages")
	// Column order must come from PRAGMA table_info, not a hardcoded list.
	want := "id,conversation_id,role,content,tokens,kind,attachments,addressed,created_at"
	if got := strings.Join(msgs.ColumnNames(), ","); got != want {
		t.Fatalf("messages columns = %s, want %s", got, want)
	}
	if !msgs.AutoIncrement {
		t.Fatal("messages must be detected as AUTOINCREMENT")
	}
	if msgs.SeqValue != 50 {
		t.Fatalf("messages AUTOINCREMENT counter = %d, want 50", msgs.SeqValue)
	}
	mem := findTable(t, src, "memories")
	if len(mem.Uniques) != 1 {
		t.Fatalf("memories UNIQUE constraints = %d, want 1", len(mem.Uniques))
	}
	if want := "memories_scope_group_openid_member_openid_text_key"; mem.Uniques[0].Name != want {
		t.Fatalf("UNIQUE name = %q, want %q", mem.Uniques[0].Name, want)
	}
	if len(msgs.Indexes) != 1 || msgs.Indexes[0].Name != "idx_messages_conversation" {
		t.Fatalf("messages indexes = %+v", msgs.Indexes)
	}
	conv := findTable(t, src, "conversations")
	def, ok := conv.Column("enabled")
	if !ok || !def.HasDefault || def.DefaultExpr != "1" {
		t.Fatalf("conversations.enabled default not discovered: %+v", def)
	}
	if pks := strings.Join(msgs.PK, ","); pks != "id" {
		t.Fatalf("messages primary key = %s, want id", pks)
	}
}

func TestSourceOpenIsReadOnly(t *testing.T) {
	src, err := OpenSource(buildFixture(t))
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer func() { _ = src.Close() }()
	if _, err := src.db.Exec("DELETE FROM messages"); err == nil {
		t.Fatal("the source handle must be read-only")
	}
}

func TestHexOfDigest(t *testing.T) {
	sum := rowDigest([]byte("abc"))
	if got := hexOf(sum); len(got) != 64 {
		t.Fatalf("digest hex length = %d, want 64", len(got))
	}
}

func findTable(t *testing.T, src *Source, name string) *Table {
	t.Helper()
	for _, tbl := range src.AllTables() {
		if tbl.Name == name {
			return tbl
		}
	}
	t.Fatalf("table %s not found", name)
	return nil
}

// TestRoundTripAgainstPostgres is the end-to-end acceptance test: copy,
// refuse a second import, verify, rerun with --replace, then check that the
// identity sequences, defaults and the UNIQUE constraint survived.
func TestRoundTripAgainstPostgres(t *testing.T) {
	dsn := testDSN(t)
	if dsn == "" {
		t.Skip("set BOT_HARNESS_TEST_PG_DSN to run the PostgreSQL integration tests")
	}
	ctx := context.Background()
	db := freshDatabase(t, dsn)
	fixture := buildFixture(t)

	var out strings.Builder
	if err := Run(ctx, Options{From: fixture, To: db, Batch: 64}, &out); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "verification PASSED: 12/12") {
		t.Fatalf("expected a clean verification:\n%s", out.String())
	}

	out.Reset()
	err := Run(ctx, Options{From: fixture, To: db, Batch: 64}, &out)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("a second import must be refused, got err=%v\n%s", err, out.String())
	}

	out.Reset()
	if err := Run(ctx, Options{From: fixture, To: db, VerifyOnly: true}, &out); err != nil {
		t.Fatalf("verify-only: %v\n%s", err, out.String())
	}

	out.Reset()
	if err := Run(ctx, Options{From: fixture, To: db, Replace: true, Batch: 64}, &out); err != nil {
		t.Fatalf("replace rerun: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "verification PASSED: 12/12") {
		t.Fatalf("replace rerun did not verify:\n%s", out.String())
	}

	tgt, err := OpenTarget(ctx, db)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer func() { _ = tgt.Close(ctx) }()

	// A BLOB with a leading NUL and non-UTF-8 bytes must survive verbatim.
	blob := bytes.Repeat([]byte{0x00, 0xff, 0x10, 0x7f}, 512)
	var n int64
	if err := tgt.DB.QueryRow(ctx, "SELECT COUNT(*) FROM media WHERE data = $1", blob).Scan(&n); err != nil {
		t.Fatalf("query blob: %v", err)
	}
	if n != 1 {
		t.Fatalf("blob round trip: found %d rows, want 1", n)
	}

	// memories holds three fixture rows, so the sequence must continue at 4
	// and the REAL salience values must have survived bit-exactly.
	seqs, err := tgt.NextIdentityIDs(ctx, []*Table{{Name: MemoriesTable, PK: []string{"id"}, AutoIncrement: true}})
	if err != nil {
		t.Fatalf("read memories sequence: %v", err)
	}
	if len(seqs) != 1 || seqs[0].Next != 4 {
		t.Fatalf("memories next id = %+v, want 4", seqs)
	}
	var id int64
	var scope string
	var salience float64
	var hits int64
	if err := tgt.DB.QueryRow(ctx, "INSERT INTO memories (text, created_at) VALUES ($1, $2) RETURNING id, scope, salience, hits",
		"probe", "2026-11-02T00:00:00+08:00").Scan(&id, &scope, &salience, &hits); err != nil {
		t.Fatalf("insert into memories with defaults: %v", err)
	}
	if id != 4 || scope != "" || salience != 1 || hits != 0 {
		t.Fatalf("memories defaults: id=%d scope=%q salience=%v hits=%d", id, scope, salience, hits)
	}
	rows, err := tgt.DB.Query(ctx, "SELECT salience FROM memories WHERE text = $1", "喜欢抹茶")
	if err != nil {
		t.Fatalf("read salience: %v", err)
	}
	var got float64
	if rows.Next() {
		if err := rows.Scan(&got); err != nil {
			t.Fatalf("scan salience: %v", err)
		}
	}
	rows.Close()
	if got != 0.75 {
		t.Fatalf("salience round trip = %v, want 0.75", got)
	}
	if _, err := tgt.DB.Exec(ctx, "INSERT INTO memories (text, created_at) VALUES ($1, $2)", "probe", "2026-11-02T00:00:00+08:00"); err == nil {
		t.Fatal("the UNIQUE (scope, group_openid, member_openid, text) constraint is missing on the target")
	}
}

// TestDryRunWritesNothing proves --dry-run never creates a table.
func TestDryRunWritesNothing(t *testing.T) {
	dsn := testDSN(t)
	if dsn == "" {
		t.Skip("set BOT_HARNESS_TEST_PG_DSN to run the PostgreSQL integration tests")
	}
	ctx := context.Background()
	db := freshDatabase(t, dsn)
	fixture := buildFixture(t)

	var out strings.Builder
	if err := Run(ctx, Options{From: fixture, To: db, DryRun: true}, &out); err != nil {
		t.Fatalf("dry run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "would CREATE 12 table(s)") {
		t.Fatalf("dry run did not describe the plan:\n%s", out.String())
	}

	tgt, err := OpenTarget(ctx, db)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer func() { _ = tgt.Close(ctx) }()
	if len(tgt.Tables) != 0 {
		t.Fatalf("dry run created %d table(s): %v", len(tgt.Tables), tgt.Tables)
	}
}

// tamperMediaSHA256 rewrites one media row's declared digest so that it cannot
// possibly equal sha256(data) (and cannot equal the row id, which IS
// sha256(data)). The row itself stays byte-identical, so a copy of it is
// lossless: only a checker that recomputes the digest can notice.
func tamperMediaSHA256(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	bogus := strings.Repeat("0", 64)
	res, err := db.Exec("UPDATE media SET sha256 = ? WHERE rowid = (SELECT MIN(rowid) FROM media)", bogus)
	if err != nil {
		t.Fatalf("tamper media.sha256: %v", err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		t.Fatalf("tamper media.sha256 rewrote %d row(s) (err=%v), want 1", n, err)
	}
	return bogus
}

// TestVerifierRejectsMediaSha256Drift is the regression test for the media
// content check.
//
// Why it exists: the copy path is byte-exact, so a media row whose sha256 column
// no longer matches sha256(data) round-trips perfectly and the generic row hash
// (source vs target) is identical on both sides. Everything a copy can observe
// is fine, so a verifier that only compares the two sides reports PASS — the
// media-specific check is the only thing that can catch a corrupt or
// hand-edited blob store before it becomes the production database. Deleting
// the sha256(data)==id comparison from verify.go used to leave the whole suite
// green; this test is what makes that deletion fail loudly.
func TestVerifierRejectsMediaSha256Drift(t *testing.T) {
	dsn := testDSN(t)
	if dsn == "" {
		t.Skip("set BOT_HARNESS_TEST_PG_DSN to run the PostgreSQL integration tests")
	}
	ctx := context.Background()
	db := freshDatabase(t, dsn)
	fixture := buildFixture(t)
	bogus := tamperMediaSHA256(t, fixture)

	var out strings.Builder
	err := Run(ctx, Options{From: fixture, To: db, Batch: 64}, &out)
	report := out.String()
	if err == nil {
		t.Fatalf("a media row whose sha256 is %s instead of sha256(data) must fail the migration:\n%s", bogus, report)
	}
	if !strings.Contains(report, "media sha256 mismatch") {
		t.Fatalf("report does not name the media sha256 mismatch:\n%s", report)
	}
	if !strings.Contains(report, "MISMATCH") {
		t.Fatalf("report does not flag media as MISMATCH:\n%s", report)
	}
	// The damaged row is the only defect: the copy itself is lossless, so the
	// failure has to be attributed to media alone (the returned error names the
	// failing tables; runVerify prints the table, the note and this error).
	if !strings.Contains(err.Error(), "verification FAILED for 1 table(s): media") {
		t.Fatalf("failure is not attributed to media alone: %v\n%s", err, report)
	}
}

// TestVerifierDetectsTampering proves a single changed character fails the
// check with a located difference.
func TestVerifierDetectsTampering(t *testing.T) {
	dsn := testDSN(t)
	if dsn == "" {
		t.Skip("set BOT_HARNESS_TEST_PG_DSN to run the PostgreSQL integration tests")
	}
	ctx := context.Background()
	db := freshDatabase(t, dsn)
	fixture := buildFixture(t)

	var out strings.Builder
	if err := Run(ctx, Options{From: fixture, To: db, Batch: 128}, &out); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out.String())
	}

	tgt, err := OpenTarget(ctx, db)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	if _, err := tgt.DB.Exec(ctx, "UPDATE messages SET content = content || '!' WHERE id = 3"); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	_ = tgt.Close(ctx)

	out.Reset()
	if err := Run(ctx, Options{From: fixture, To: db, VerifyOnly: true}, &out); err == nil {
		t.Fatalf("tampered data must fail verification:\n%s", out.String())
	}
	report := out.String()
	if !strings.Contains(report, "MISMATCH") || !strings.Contains(report, "first difference") {
		t.Fatalf("report does not describe the difference:\n%s", report)
	}
	if !strings.Contains(report, "id=3") {
		t.Fatalf("report does not name the offending primary key:\n%s", report)
	}
}

// TestNextIDFromState pins the one sequence formula shared by the dry-run plan,
// the copy and the verifier. memories is deliberately in the table: it used to
// be hardcoded to 2 in the dry run, which disagreed with the copy for any
// memories table that actually holds rows.
func TestNextIDFromState(t *testing.T) {
	cases := []struct {
		name       string
		maxID, seq int64
		want       int64
	}{
		{"empty table, no counter", 0, 0, 1},
		{"empty table, counter already used once (real memories)", 0, 1, 2},
		{"three rows, counter 4 (one row deleted)", 3, 4, 5},
		{"counter lags max(pk) (explicit ids)", 5, -3, 6},
		{"messages: rows and counter agree", 13343, 13343, 13344},
		{"counter ahead of rows", 2, 9, 10},
	}
	for _, c := range cases {
		if got := nextIDFromState(c.maxID, c.seq); got != c.want {
			t.Errorf("%s: nextIDFromState(%d, %d) = %d, want %d", c.name, c.maxID, c.seq, got, c.want)
		}
	}
}

// bumpMemoriesSeq leaves memories with three rows but a counter of 4, the state
// SQLite itself produces after a row was deleted: three rows, next id 5.
func bumpMemoriesSeq(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	now := "2026-11-01T12:00:00+08:00"
	if _, err := db.Exec("INSERT INTO memories (scope, group_openid, member_openid, kind, text, salience, hits, created_at) VALUES (?,?,?,?,?,?,?,?)",
		"group", "g1", "u1", "fact", "临时记忆", 1.0, 0, now); err != nil {
		t.Fatalf("insert 4th memory: %v", err)
	}
	if _, err := db.Exec("DELETE FROM memories WHERE text = ?", "临时记忆"); err != nil {
		t.Fatalf("delete 4th memory: %v", err)
	}
	var rows, seq int64
	if err := db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&rows); err != nil {
		t.Fatalf("count memories: %v", err)
	}
	if err := db.QueryRow("SELECT seq FROM sqlite_sequence WHERE name = ?", "memories").Scan(&seq); err != nil {
		t.Fatalf("read memories counter: %v", err)
	}
	if rows != 3 || seq != 4 {
		t.Fatalf("fixture state = %d rows / counter %d, want 3 rows / counter 4", rows, seq)
	}
}

// TestEmptySourceTableSequenceStartsAtOne is the migration-level regression for
// an empty table that never had a sqlite_sequence row (commitments /
// human_status on a fresh source). The old fixup emitted setval(0), which
// PostgreSQL rejects because sequences start at 1; the migration must succeed
// and the first identity value must be 1, matching what SQLite would hand out.
func TestEmptySourceTableSequenceStartsAtOne(t *testing.T) {
	dsn := testDSN(t)
	if dsn == "" {
		t.Skip("set BOT_HARNESS_TEST_PG_DSN to run the PostgreSQL integration tests")
	}
	ctx := context.Background()

	// buildFixture covers the original schema; add the two optional tables with
	// no rows and no sqlite_sequence entry, which is exactly the state that used
	// to produce setval(0).
	path := buildFixture(t)
	srcDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	for _, ddl := range []string{
		`CREATE TABLE commitments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			text TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE human_status (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			status TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	} {
		if _, err := srcDB.Exec(ddl); err != nil {
			_ = srcDB.Close()
			t.Fatalf("create empty source table: %v", err)
		}
	}
	if err := srcDB.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	db := freshDatabase(t, dsn)
	var out strings.Builder
	if err := Run(ctx, Options{From: path, To: db, Batch: 64}, &out); err != nil {
		t.Fatalf("migrate an empty source table: %v\n%s", err, out.String())
	}
	for _, want := range []string{"commitments=1", "human_status=1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("empty table sequence %s missing (want first id 1):\n%s", want, out.String())
		}
	}

	tgt, err := OpenTarget(ctx, db)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer func() { _ = tgt.Close(ctx) }()
	var id int64
	if err := tgt.DB.QueryRow(ctx,
		"INSERT INTO commitments (text, created_at) VALUES ($1, $2) RETURNING id",
		"first", "2026-11-02T00:00:00+08:00").Scan(&id); err != nil {
		t.Fatalf("insert into the empty target table: %v", err)
	}
	if id != 1 {
		t.Fatalf("first identity value = %d, want 1", id)
	}
}

// TestDryRunSequencePlanMatchesTheRun is the regression for the dry-run plan
// lying about memories: the plan hardcoded next id 2 while the real run applies
// setval(GREATEST(max(pk), sqlite_sequence)), which for three rows and a
// counter of 4 is 5. A plan that disagrees with the run is worse than no plan.
func TestDryRunSequencePlanMatchesTheRun(t *testing.T) {
	dsn := testDSN(t)
	if dsn == "" {
		t.Skip("set BOT_HARNESS_TEST_PG_DSN to run the PostgreSQL integration tests")
	}
	ctx := context.Background()
	db := freshDatabase(t, dsn)
	fixture := buildFixture(t)
	bumpMemoriesSeq(t, fixture)

	var out strings.Builder
	if err := Run(ctx, Options{From: fixture, To: db, DryRun: true}, &out); err != nil {
		t.Fatalf("dry run: %v\n%s", err, out.String())
	}
	plan := out.String()
	if !strings.Contains(plan, "memories.id=5") {
		t.Fatalf("dry-run plan must predict memories.id=5 (3 rows, counter 4):\n%s", plan)
	}
	if strings.Contains(plan, "memories.id=2") {
		t.Fatalf("dry-run plan still hardcodes memories.id=2:\n%s", plan)
	}

	// The run it describes must agree, both in its own report and afterwards.
	out.Reset()
	if err := Run(ctx, Options{From: fixture, To: db, Batch: 64}, &out); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out.String())
	}
	if report := out.String(); !strings.Contains(report, "memories=5") {
		t.Fatalf("the real run disagrees with the dry-run plan (want memories=5):\n%s", report)
	}
}

// TestTargetTimestampColumnsAreTimestamptz pins the target DDL: every column
// the harness treats as a timestamp must be a native timestamptz, and the ones
// whose source spelling of "unset" is the empty string must be nullable.
func TestTargetTimestampColumnsAreTimestamptz(t *testing.T) {
	dsn := testDSN(t)
	if dsn == "" {
		t.Skip("set BOT_HARNESS_TEST_PG_DSN to run the PostgreSQL integration tests")
	}
	ctx := context.Background()
	db := freshDatabase(t, dsn)
	fixture := buildFixture(t)

	var out strings.Builder
	if err := Run(ctx, Options{From: fixture, To: db, Batch: 64}, &out); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out.String())
	}

	tgt, err := OpenTarget(ctx, db)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer func() { _ = tgt.Close(ctx) }()

	rows, err := tgt.DB.Query(ctx, "SELECT table_name, column_name, data_type, is_nullable\n"+
		"  FROM information_schema.columns WHERE table_schema = current_schema()")
	if err != nil {
		t.Fatalf("read information_schema.columns: %v", err)
	}
	defer rows.Close()

	type columnState struct{ dataType, nullable string }
	have := map[string]columnState{}
	for rows.Next() {
		var table, column, dataType, nullable string
		if err := rows.Scan(&table, &column, &dataType, &nullable); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		have[table+"."+column] = columnState{dataType: dataType, nullable: nullable}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read information_schema.columns: %v", err)
	}

	for key := range timestampColumns {
		state, ok := have[key]
		if !ok {
			// An old source file may predate the optional tables; the runtime
			// store creates them (with the same timestamptz types) on next open.
			if optionalTables[strings.SplitN(key, ".", 2)[0]] {
				continue
			}
			t.Errorf("%s: column is missing on the target", key)
			continue
		}
		if state.dataType != "timestamp with time zone" {
			t.Errorf("%s: data_type = %q, want timestamp with time zone", key, state.dataType)
		}
		wantNullable := "NO"
		if nullableTimestampColumns[key] {
			wantNullable = "YES"
		}
		if state.nullable != wantNullable {
			t.Errorf("%s: is_nullable = %q, want %q", key, state.nullable, wantNullable)
		}
	}
}

// TestNullableTimestampsBecomeNull is the migration-side proof that the empty
// string the SQLite source uses for "unset" does not survive as an invalid
// timestamptz: the copy turns it into NULL, and the same projection the store
// uses reads it back as the empty string.
func TestNullableTimestampsBecomeNull(t *testing.T) {
	dsn := testDSN(t)
	if dsn == "" {
		t.Skip("set BOT_HARNESS_TEST_PG_DSN to run the PostgreSQL integration tests")
	}
	ctx := context.Background()
	db := freshDatabase(t, dsn)
	fixture := buildFixture(t)

	var out strings.Builder
	if err := Run(ctx, Options{From: fixture, To: db, Batch: 64}, &out); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out.String())
	}

	tgt, err := OpenTarget(ctx, db)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer func() { _ = tgt.Close(ctx) }()

	const projection = `COALESCE(to_char(%s AT TIME ZONE 'Asia/Shanghai', 'YYYY-MM-DD"T"HH24:MI:SS') || '+08:00', '')`

	var lastSentNull bool
	var lastSent string
	if err := tgt.DB.QueryRow(ctx,
		"SELECT last_sent_at IS NULL, "+fmt.Sprintf(projection, "last_sent_at")+" FROM proactive_state WHERE group_openid = $1",
		"g1").Scan(&lastSentNull, &lastSent); err != nil {
		t.Fatalf("read proactive_state.last_sent_at: %v", err)
	}
	if !lastSentNull {
		t.Error("proactive_state.last_sent_at: the empty source value did not become NULL")
	}
	if lastSent != "" {
		t.Errorf("proactive_state.last_sent_at read back %q, want the empty string", lastSent)
	}

	var sentNull bool
	var sent string
	if err := tgt.DB.QueryRow(ctx,
		"SELECT sent_at IS NULL, "+fmt.Sprintf(projection, "sent_at")+" FROM scheduled_tasks WHERE text = $1",
		"提醒").Scan(&sentNull, &sent); err != nil {
		t.Fatalf("read scheduled_tasks.sent_at: %v", err)
	}
	if !sentNull {
		t.Error("scheduled_tasks.sent_at: the empty source value did not become NULL")
	}
	if sent != "" {
		t.Errorf("scheduled_tasks.sent_at read back %q, want the empty string", sent)
	}

	var leaseNull bool
	var lease string
	if err := tgt.DB.QueryRow(ctx,
		"SELECT lease_until IS NULL, "+fmt.Sprintf(projection, "lease_until")+" FROM scheduled_tasks WHERE text = $1",
		"提醒").Scan(&leaseNull, &lease); err != nil {
		t.Fatalf("read scheduled_tasks.lease_until: %v", err)
	}
	if leaseNull {
		t.Error("scheduled_tasks.lease_until: a non-empty source value became NULL")
	}
	if want := "2026-11-01T12:00:00+08:00"; lease != want {
		t.Errorf("scheduled_tasks.lease_until read back %q, want %q", lease, want)
	}

	var usedNull bool
	var used string
	if err := tgt.DB.QueryRow(ctx,
		"SELECT last_used_at IS NULL, "+fmt.Sprintf(projection, "last_used_at")+" FROM memories WHERE text = $1",
		"喜欢抹茶").Scan(&usedNull, &used); err != nil {
		t.Fatalf("read memories.last_used_at: %v", err)
	}
	if !usedNull || used != "" {
		t.Errorf("memories.last_used_at (empty source): is_null=%v value=%q, want true and \"\"", usedNull, used)
	}

	var touchedNull bool
	var touched string
	if err := tgt.DB.QueryRow(ctx,
		"SELECT last_used_at IS NULL, "+fmt.Sprintf(projection, "last_used_at")+" FROM memories WHERE text = $1",
		"整点睡眠").Scan(&touchedNull, &touched); err != nil {
		t.Fatalf("read memories.last_used_at: %v", err)
	}
	if touchedNull || !strings.HasSuffix(touched, "+08:00") {
		t.Errorf("memories.last_used_at (non-empty source): is_null=%v value=%q, want a +08:00 timestamp", touchedNull, touched)
	}
}
