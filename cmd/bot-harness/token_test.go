package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
	"github.com/dynesshely/bot-harness/internal/testpg"
)

// seedTokenDatabase creates a database with the given settings patch and points
// DB_PATH at it (config.Load reads the environment, so the subcommand sees it).
func seedTokenDatabase(t *testing.T, patch map[string]json.RawMessage) {
	t.Helper()
	path := t.TempDir() + "/bot.db"
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if patch != nil {
		mgr, err := settings.New(context.Background(), st, settings.Defaults())
		if err != nil {
			t.Fatalf("settings: %v", err)
		}
		if _, _, err := mgr.Apply(context.Background(), patch); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	t.Setenv("DB_PATH", path)
}

// token 子命令只输出库里的后台口令，别的密钥一个字都不能带出来。
func TestAdminTokenCmdPrintsOnlyTheToken(t *testing.T) {
	seedTokenDatabase(t, map[string]json.RawMessage{
		"admin_token":    json.RawMessage(`"rotated-token-42"`),
		"qq_app_secret":  json.RawMessage(`"qq-secret-value"`),
		"openai_api_key": json.RawMessage(`"sk-secret-value"`),
	})

	var stdout, stderr bytes.Buffer
	if code := adminTokenCmd(&stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if got := stdout.String(); got != "rotated-token-42\n" {
		t.Fatalf("stdout 只应有口令一行，实际 %q", got)
	}
	for _, secret := range []string{"qq-secret-value", "sk-secret-value"} {
		if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
			t.Fatalf("不能打印其它密钥：%q", secret)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("成功时不该有 stderr：%q", stderr.String())
	}
}

// 数据库还没生成口令时：明确告诉人怎么产生它，而不是打印空行让人以为拿到了 token。
func TestAdminTokenCmdExplainsAnEmptyDatabase(t *testing.T) {
	seedTokenDatabase(t, nil)

	var stdout, stderr bytes.Buffer
	if code := adminTokenCmd(&stdout, &stderr); code != 1 {
		t.Fatalf("没有口令时应返回 1，实际 %d", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("没有口令时 stdout 必须为空，实际 %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "bot-harness") {
		t.Fatalf("应说明口令由谁生成：%q", stderr.String())
	}
}

// 数据库打不开时也要老实报错，不能静默成功。
func TestAdminTokenCmdReportsAnUnopenableDatabase(t *testing.T) {
	// 一个目录当数据库文件：SQLite 打不开它。
	t.Setenv("DB_PATH", t.TempDir())

	var stdout, stderr bytes.Buffer
	if code := adminTokenCmd(&stdout, &stderr); code != 1 {
		t.Fatalf("打不开数据库时应返回 1，实际 %d", code)
	}
	if stderr.Len() == 0 {
		t.Fatal("应说明失败原因")
	}
}

// 数据库文件不存在时不能顺手建一个空库：那会让「路径写错」看起来像「全新部署」，
// 而且 token 子命令本该是只读的。
func TestAdminTokenCmdDoesNotCreateADatabase(t *testing.T) {
	path := t.TempDir() + "/missing/bot.db"
	t.Setenv("DB_PATH", path)

	var stdout, stderr bytes.Buffer
	if code := adminTokenCmd(&stdout, &stderr); code != 1 {
		t.Fatalf("库不存在时应返回 1，实际 %d", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("失败时 stdout 必须为空，实际 %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "不存在") {
		t.Fatalf("应说明数据库不存在：%q", stderr.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("token 子命令不该创建数据库文件：err=%v", err)
	}
}

// tokenFingerprint is the data the token subcommand must not disturb.
type tokenFingerprint struct {
	settingsRows int
	settingsKeys string
	migrations   string
	messages     int
	token        string
	model        string
}

func fingerprintTokenDatabase(t *testing.T, path string) tokenFingerprint {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("fingerprint open: %v", err)
	}
	defer func() { _ = st.Close() }()

	var out tokenFingerprint
	db := st.DB()
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&out.settingsRows); err != nil {
		t.Fatalf("settings count: %v", err)
	}
	rows, err := db.Query(`SELECT k FROM settings ORDER BY k`)
	if err != nil {
		t.Fatalf("settings keys: %v", err)
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan key: %v", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close keys: %v", err)
	}
	out.settingsKeys = strings.Join(keys, ",")

	migRows, err := db.Query(`SELECT name FROM schema_migrations ORDER BY name`)
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}
	var names []string
	for migRows.Next() {
		var name string
		if err := migRows.Scan(&name); err != nil {
			t.Fatalf("scan migration: %v", err)
		}
		names = append(names, name)
	}
	if err := migRows.Close(); err != nil {
		t.Fatalf("close migrations: %v", err)
	}
	out.migrations = strings.Join(names, ",")

	if err := db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&out.messages); err != nil {
		t.Fatalf("messages count: %v", err)
	}
	mgr, err := settings.New(ctx, st, settings.Defaults())
	if err != nil {
		t.Fatalf("fingerprint settings: %v", err)
	}
	out.token = mgr.Effective().AdminToken
	out.model = mgr.Effective().Model
	return out
}

// 对「已存在且已是最新 schema」的库执行 token：只读，业务数据一个字节都不该动。
// 命令内部打开的是正常读写连接（会执行 schema / 待应用迁移）；本用例证明对已迁移
// 的库没有新增迁移记录，settings 与 messages 也都不变。
func TestAdminTokenCmdLeavesAnExistingDatabaseUnchanged(t *testing.T) {
	path := t.TempDir() + "/bot.db"
	ctx := context.Background()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mgr, err := settings.New(ctx, st, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if _, _, err := mgr.Apply(ctx, map[string]json.RawMessage{
		"admin_token": json.RawMessage(`"existing-token"`),
		"model":       json.RawMessage(`"some-model"`),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := st.EnsureConversation(ctx, &store.Conversation{
		ID: "group:G1:U1", Scene: store.SceneGroupMember, GroupOpenID: "G1", MemberOpenID: "U1",
	}); err != nil {
		t.Fatalf("conv: %v", err)
	}
	if err := st.AppendMessage(ctx, store.MessageInput{
		ConversationID: "group:G1:U1", Role: "user", Content: "hi", Kind: store.KindDialogue,
	}); err != nil {
		t.Fatalf("msg: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	before := fingerprintTokenDatabase(t, path)
	if before.token != "existing-token" || before.model != "some-model" || before.messages != 1 {
		t.Fatalf("种子数据不对：%+v", before)
	}
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read db: %v", err)
	}

	t.Setenv("DB_PATH", path)
	var stdout, stderr bytes.Buffer
	if code := adminTokenCmd(&stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "existing-token" {
		t.Fatalf("stdout 应是库里的 token，实际 %q", got)
	}

	after := fingerprintTokenDatabase(t, path)
	if after != before {
		t.Fatalf("token 子命令改了库内容：before %+v / after %+v", before, after)
	}
	// 更严格的一条（同机同版本实测零写入）：文件字节也不变。这里只记日志不硬断言，
	// 避免不同 SQLite/文件系统实现因 checkpoint 时机而误报。
	afterBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read db: %v", err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Logf("注意：库文件字节有变化（数据未变）——命令走正常读写连接，可能触发 WAL 恢复")
	}
}

// PostgreSQL mode: there is no local file to stat, so the command must go
// straight to the database. DATABASE_URL points at PG_TEST_DSN while DB_PATH
// deliberately names a missing directory — a stat on it would fail the run.
func TestAdminTokenCmdWorksInPostgresMode(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN not set; skipping the PostgreSQL token test")
	}
	// This test rewrites admin_token; never let it reach the live database.
	testpg.Guard(t, dsn)
	ctx := context.Background()
	st, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	mgr, err := settings.New(ctx, st, settings.Defaults())
	if err != nil {
		_ = st.Close()
		t.Fatalf("settings: %v", err)
	}
	previous := mgr.Effective().AdminToken
	if _, _, err := mgr.Apply(ctx, map[string]json.RawMessage{
		"admin_token": json.RawMessage(`"pg-mode-token"`),
	}); err != nil {
		_ = st.Close()
		t.Fatalf("seed token: %v", err)
	}
	_ = st.Close()
	t.Cleanup(func() {
		cleanup, err := store.Open(dsn)
		if err != nil {
			return
		}
		defer func() { _ = cleanup.Close() }()
		m, err := settings.New(context.Background(), cleanup, settings.Defaults())
		if err != nil {
			return
		}
		if previous == "" {
			_, _ = cleanup.DB().Exec(`DELETE FROM settings WHERE k = 'admin_token'`)
			return
		}
		_, _, _ = m.Apply(context.Background(), map[string]json.RawMessage{
			"admin_token": json.RawMessage(strconv.Quote(previous)),
		})
	})

	t.Setenv("DATABASE_URL", dsn)
	// A missing SQLite path must be irrelevant in PostgreSQL mode.
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "missing", "bot.db"))

	var stdout, stderr bytes.Buffer
	if code := adminTokenCmd(&stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "pg-mode-token" {
		t.Fatalf("stdout = %q, want the token stored in PostgreSQL", got)
	}
}
