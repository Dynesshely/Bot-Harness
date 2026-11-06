package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestNewDialect(t *testing.T) {
	cases := []struct {
		driver string
		wantPG bool
	}{
		{"sqlite", false},
		{"", false},
		{"SQLite", false},
		{"postgres", true},
		{"PostgreSQL", true},
		{" pg ", true},
		{"pgx", true},
		{"pgx/v5", true},
		{"mysql", false},
	}
	for _, c := range cases {
		d := NewDialect(c.driver)
		if got := d.IsPostgres(); got != c.wantPG {
			t.Errorf("NewDialect(%q).IsPostgres() = %v, want %v", c.driver, got, c.wantPG)
		}
	}
	if NewDialect("").Name() != DriverSQLite {
		t.Errorf("零值 dialect 的 Name() 应为 sqlite，实际 %q", NewDialect("").Name())
	}
	if (&Store{}).dialect.Name() != DriverSQLite {
		t.Error("未设置 dialect 的 Store 应退化为 sqlite")
	}
}

func TestPlaceholder(t *testing.T) {
	sq := NewDialect("sqlite")
	if got := sq.Placeholder(1); got != "?" {
		t.Errorf("sqlite Placeholder(1) = %q, want ?", got)
	}
	if got := sq.Placeholder(7); got != "?" {
		t.Errorf("sqlite Placeholder(7) = %q, want ?", got)
	}
	pg := NewDialect("postgres")
	for n, want := range map[int]string{1: "$1", 2: "$2", 10: "$10", 42: "$42"} {
		if got := pg.Placeholder(n); got != want {
			t.Errorf("pg Placeholder(%d) = %q, want %q", n, got, want)
		}
	}
	// n < 1 不应产生 "$0" 这类非法占位符。
	if got := pg.Placeholder(0); got != "$1" {
		t.Errorf("pg Placeholder(0) = %q, want $1", got)
	}
}

func TestRebindSQLiteIsIdentity(t *testing.T) {
	sq := NewDialect("sqlite")
	for _, q := range []string{
		"",
		"SELECT 1",
		"SELECT * FROM t WHERE a = ? AND b = ?",
		"SELECT '?' FROM t WHERE a = ?",
	} {
		if got := sq.Rebind(q); got != q {
			t.Errorf("sqlite Rebind(%q) = %q，应原样返回", q, got)
		}
	}
	// 零值 dialect 也必须原样返回（Open 未设置时的历史行为）。
	var zero dialect
	if got := zero.Rebind("SELECT ? , ?"); got != "SELECT ? , ?" {
		t.Errorf("零值 dialect Rebind 应原样返回，实际 %q", got)
	}
}

func TestRebindPostgres(t *testing.T) {
	pg := NewDialect("postgres")
	cases := []struct{ in, want string }{
		// 无占位符
		{"", ""},
		{"SELECT 1", "SELECT 1"},
		{"SELECT * FROM t", "SELECT * FROM t"},
		// 单个 / 多个
		{"SELECT * FROM t WHERE id = ?", "SELECT * FROM t WHERE id = $1"},
		{
			"INSERT INTO t (a, b, c) VALUES (?, ?, ?)",
			"INSERT INTO t (a, b, c) VALUES ($1, $2, $3)",
		},
		{
			"UPDATE t SET a = ?, b = ? WHERE id = ? AND x = ?",
			"UPDATE t SET a = $1, b = $2 WHERE id = $3 AND x = $4",
		},
		// 跨越 9 → 10 的编号（确认两位数没有被截断）
		{strings.Repeat("?,", 11) + "?", "$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12"},
		// 字符串字面量里的 ? 不参与编号
		{
			"SELECT * FROM t WHERE a = '?' AND b = ?",
			"SELECT * FROM t WHERE a = '?' AND b = $1",
		},
		{
			"SELECT 'a?b?c' AS s, ? AS p, 'd?' AS q",
			"SELECT 'a?b?c' AS s, $1 AS p, 'd?' AS q",
		},
		// 引号转义：'' 是字面量内的转义，字符串到此还没结束
		{
			"SELECT 'it''s ? here' AS s, ? AS p",
			"SELECT 'it''s ? here' AS s, $1 AS p",
		},
		{
			"SELECT 'a''''b?' AS s, ? AS p",
			"SELECT 'a''''b?' AS s, $1 AS p",
		},
		// 双引号标识符里的 ? 不参与编号
		{
			"SELECT \"weird?col\" FROM t WHERE a = ?",
			"SELECT \"weird?col\" FROM t WHERE a = $1",
		},
		{
			"SELECT \"a\"\"b?c\" FROM t WHERE a = ?",
			"SELECT \"a\"\"b?c\" FROM t WHERE a = $1",
		},
		// 单行注释里的 ? 不参与编号
		{
			"SELECT * FROM t -- why? because ?\nWHERE a = ?",
			"SELECT * FROM t -- why? because ?\nWHERE a = $1",
		},
		// LIKE 前缀拼接（store 里真实的写法）照常编号
		{
			"SELECT * FROM t WHERE text LIKE '%' || ? || '%' AND id = ?",
			"SELECT * FROM t WHERE text LIKE '%' || $1 || '%' AND id = $2",
		},
	}
	for _, c := range cases {
		if got := pg.Rebind(c.in); got != c.want {
			t.Errorf("Rebind(%q)\n  got  %q\n  want %q", c.in, got, c.want)
		}
	}
}

// 未闭合的引号 / 未换行的注释不能让扫描器 panic 或吞掉后面的占位符。
func TestRebindUnterminatedInput(t *testing.T) {
	pg := NewDialect("postgres")
	if got := pg.Rebind("SELECT 'unterminated ? FROM t"); got != "SELECT 'unterminated ? FROM t" {
		t.Errorf("未闭合引号内的 ? 应保持原样，实际 %q", got)
	}
	if got := pg.Rebind("SELECT 1 -- trailing comment with ?"); !strings.HasSuffix(got, "?") {
		t.Errorf("行注释内的 ? 应保持原样，实际 %q", got)
	}
}

func TestSetDialect(t *testing.T) {
	s, err := Open(t.TempDir() + "/d.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()

	if s.dialect.IsPostgres() {
		t.Fatal("Open 出来的 Store 默认必须是 sqlite 方言")
	}
	ctx := context.Background()
	if _, err := s.AddRecord(ctx, &Record{Direction: "in", Scene: SceneC2C, Content: "x"}); err != nil {
		t.Fatalf("add record: %v", err)
	}
	s.SetDialect(NewDialect("postgres"))
	if !s.dialect.IsPostgres() {
		t.Fatal("SetDialect 没有生效")
	}
	s.SetDialect(NewDialect("sqlite"))
	if _, err := s.AddRecord(ctx, &Record{Direction: "in", Scene: SceneC2C, Content: "y"}); err != nil {
		t.Fatalf("切回 sqlite 后 add record 失败: %v", err)
	}
}

// RETURNING id 改造后的返回值语义：正常插入返回非 0 且递增。
func TestInsertReturningID(t *testing.T) {
	s, err := Open(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	id1, err := s.AddRecord(ctx, &Record{Direction: "in", Scene: SceneC2C, Content: "a"})
	if err != nil {
		t.Fatalf("add record: %v", err)
	}
	id2, err := s.AddRecord(ctx, &Record{Direction: "out", Scene: SceneC2C, Content: "b"})
	if err != nil {
		t.Fatalf("add record: %v", err)
	}
	if id1 <= 0 || id2 <= id1 {
		t.Fatalf("RETURNING id 异常: id1=%d id2=%d", id1, id2)
	}
	if got, err := s.AddToolCall(ctx, ToolCallInput{Tool: "run_shell", Status: "ok"}); err != nil {
		t.Fatalf("add tool call: %v", err)
	} else if got <= 0 {
		t.Fatalf("tool_calls 的 RETURNING id 应 > 0，实际 %d", got)
	}
}

// --- 包装层真的把 Rebind 的结果交给了驱动 -----------------------------------

// recordingDriver 记录每条经过 database/sql 的语句文本，用来断言包装层的改写。
type recordingDriver struct{ queries []string }

// The driver name is process-global in database/sql, and `go test -count=N`
// runs the same test N times in one process. Register exactly once and reuse
// the instance; registering on every run panics with "Register called twice".
var (
	registerRecordingDriverOnce sync.Once
	recordingDriverInstance     = &recordingDriver{}
)

func (d *recordingDriver) reset() { d.queries = nil }

func (d *recordingDriver) Open(string) (driver.Conn, error) { return &recordingConn{d: d}, nil }

func (d *recordingDriver) last() string {
	if len(d.queries) == 0 {
		return ""
	}
	return d.queries[len(d.queries)-1]
}

type recordingConn struct{ d *recordingDriver }

func (c *recordingConn) Prepare(query string) (driver.Stmt, error) {
	c.d.queries = append(c.d.queries, query)
	return &recordingStmt{}, nil
}
func (c *recordingConn) Close() error { return nil }
func (c *recordingConn) Begin() (driver.Tx, error) {
	return recordingTx{}, nil
}

type recordingTx struct{}

func (recordingTx) Commit() error   { return nil }
func (recordingTx) Rollback() error { return nil }

type recordingStmt struct{}

func (s *recordingStmt) Close() error  { return nil }
func (s *recordingStmt) NumInput() int { return -1 }
func (s *recordingStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (s *recordingStmt) Query([]driver.Value) (driver.Rows, error) { return emptyRows{}, nil }

type emptyRows struct{}

func (emptyRows) Columns() []string         { return []string{"x"} }
func (emptyRows) Close() error              { return nil }
func (emptyRows) Next([]driver.Value) error { return io.EOF }

// sqlite 驱动同样接受 $n，所以"报错"不能证明改写发生；改为在驱动层记录语句。
func TestWrappersApplyDialect(t *testing.T) {
	registerRecordingDriverOnce.Do(func() { sql.Register("store-dialect-recorder", recordingDriverInstance) })
	rec := recordingDriverInstance
	rec.reset()
	db, err := sql.Open("store-dialect-recorder", "")
	if err != nil {
		t.Fatalf("open recorder: %v", err)
	}
	defer func() { _ = db.Close() }()

	s := &Store{db: db}
	ctx := context.Background()

	if _, err := s.execContext(ctx, "UPDATE t SET a = ? WHERE b = ?", 1, 2); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got, want := rec.last(), "UPDATE t SET a = ? WHERE b = ?"; got != want {
		t.Errorf("sqlite exec 语句被改写: got %q want %q", got, want)
	}

	if _, err := s.queryContext(ctx, "SELECT * FROM t WHERE a = ?", 1); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got, want := rec.last(), "SELECT * FROM t WHERE a = ?"; got != want {
		t.Errorf("sqlite query 语句被改写: got %q want %q", got, want)
	}

	s.SetDialect(NewDialect("postgres"))
	if _, err := s.execContext(ctx, "UPDATE t SET a = ? WHERE b = ?", 1, 2); err != nil {
		t.Fatalf("exec pg: %v", err)
	}
	if got, want := rec.last(), "UPDATE t SET a = $1 WHERE b = $2"; got != want {
		t.Errorf("PG exec 语句未改写: got %q want %q", got, want)
	}

	var x int
	_ = s.queryRowContext(ctx, "SELECT * FROM t WHERE a = ? AND b = '?'", 1).Scan(&x)
	if got, want := rec.last(), "SELECT * FROM t WHERE a = $1 AND b = '?'"; got != want {
		t.Errorf("PG queryRow 语句未改写: got %q want %q", got, want)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.txExecContext(ctx, tx, "DELETE FROM t WHERE a = ?", 1); err != nil {
		t.Fatalf("tx exec: %v", err)
	}
	if got, want := rec.last(), "DELETE FROM t WHERE a = $1"; got != want {
		t.Errorf("PG tx exec 语句未改写: got %q want %q", got, want)
	}
}

// 五处 INSERT ... RETURNING id 改造后，语义必须与旧的 Exec+LastInsertId 一致：
// 插入成功返回非 0 且写进了行，重复键只更新不新增。
func TestReturningIDCallSites(t *testing.T) {
	s, err := Open(t.TempDir() + "/ret.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	// 1) AppendMessageID
	msgID, err := s.AppendMessageID(ctx, MessageInput{ConversationID: "c1", Role: "user", Content: "hi"})
	if err != nil || msgID <= 0 {
		t.Fatalf("AppendMessageID: id=%d err=%v", msgID, err)
	}
	if _, err := s.GetMessage(ctx, msgID); err != nil {
		t.Fatalf("AppendMessageID 返回的 id 查不到行: %v", err)
	}

	// 2) AddRecord
	recID, err := s.AddRecord(ctx, &Record{Direction: "in", Scene: SceneC2C, Content: "x"})
	if err != nil || recID <= 0 {
		t.Fatalf("AddRecord: id=%d err=%v", recID, err)
	}

	// 3) AddTask
	taskID, err := s.AddTask(ctx, TaskInput{ConversationID: "c1", Kind: "reminder", Text: "t", DueAt: nowRFC3339()})
	if err != nil || taskID <= 0 {
		t.Fatalf("AddTask: id=%d err=%v", taskID, err)
	}

	// 4) AddToolCall
	callID, err := s.AddToolCall(ctx, ToolCallInput{ConversationID: "c1", Tool: "web_search", Status: "ok"})
	if err != nil || callID <= 0 {
		t.Fatalf("AddToolCall: id=%d err=%v", callID, err)
	}

	// 5) AddMemory：首次插入与 ON CONFLICT DO UPDATE 两条路径都要吐 id，
	// 且重复写入指向同一行。
	in := MemoryInput{Scope: MemoryGroup, GroupOpenID: "G1", Text: "周五发版"}
	first, err := s.AddMemory(ctx, in)
	if err != nil || first <= 0 {
		t.Fatalf("AddMemory 首次: id=%d err=%v", first, err)
	}
	second, err := s.AddMemory(ctx, in)
	if err != nil || second != first {
		t.Fatalf("AddMemory 重复应返回同一 id: first=%d second=%d err=%v", first, second, err)
	}
	if n, err := s.CountMemories(ctx, MemoryGroup, "G1", ""); err != nil || n != 1 {
		t.Fatalf("重复记忆不应新增行: n=%d err=%v", n, err)
	}
}

// dedup 用 ON CONFLICT (msg_id) DO NOTHING 之后的语义：首次 true，重复 false。
func TestMarkSeenIsIdempotent(t *testing.T) {
	s, err := Open(t.TempDir() + "/dedup.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	if seen, err := s.MarkSeen(ctx, "msg-1"); err != nil || !seen {
		t.Fatalf("首次 MarkSeen 应为新消息: seen=%v err=%v", seen, err)
	}
	if seen, err := s.MarkSeen(ctx, "msg-1"); err != nil || seen {
		t.Fatalf("重复 MarkSeen 应为旧消息: seen=%v err=%v", seen, err)
	}
	// 空 msg_id 视为新消息，不写库、不报错（原有语义）。
	if seen, err := s.MarkSeen(ctx, ""); err != nil || !seen {
		t.Fatalf("空 msg_id 应直接判新: seen=%v err=%v", seen, err)
	}
}
