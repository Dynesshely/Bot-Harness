package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/config"
	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/httpx"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// liveAISettings reads the model endpoint and key straight out of the bot's own
// database. They used to live in .env; now the panel/DB is the only source, so a
// live test has to read the same place the process does. Read-only, no
// migrations, so it is safe to run while the bot is up.
// liveConfig is what a live run needs from the bot's database.
type liveConfig struct {
	Endpoint    string
	APIKey      string
	Model       string
	ExtraCAs    []string
	InsecureTLS bool
}

func liveAISettings(t *testing.T, dbPath string) (string, string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer func() { _ = db.Close() }()

	read := func(key string) string {
		var raw string
		if err := db.QueryRow(`SELECT v FROM settings WHERE k = ?`, key).Scan(&raw); err != nil {
			return ""
		}
		var out string
		if json.Unmarshal([]byte(raw), &out) != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}
	return read("openai_base_url"), read("openai_api_key")
}

// liveSettings reads every knob a live run needs, straight from the bot database.
func liveSettings(t *testing.T, dbPath string) liveConfig {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer func() { _ = db.Close() }()

	var out liveConfig
	read := func(key string, dst any) {
		var raw string
		if err := db.QueryRow(`SELECT v FROM settings WHERE k = ?`, key).Scan(&raw); err != nil {
			return
		}
		_ = json.Unmarshal([]byte(raw), dst)
	}
	read("openai_base_url", &out.Endpoint)
	read("openai_api_key", &out.APIKey)
	read("model", &out.Model)
	read("tls_extra_ca_files", &out.ExtraCAs)
	read("tls_insecure_verify", &out.InsecureTLS)
	return out
}

// TestLivePipeline exercises the real dispatcher against the REAL model
// endpoint configured in the admin panel (database), with only the QQ send call
// stubbed out.
//
// It is opt-in because it costs tokens and needs network access:
//
//	QQBOT_LIVE_TEST=1 ENV_FILE=../../.env go test ./internal/e2e -run TestLivePipeline -v
func TestLivePipeline(t *testing.T) {
	if os.Getenv("QQBOT_LIVE_TEST") != "1" {
		t.Skip("set QQBOT_LIVE_TEST=1 to run against the real model endpoint")
	}
	if os.Getenv("ENV_FILE") == "" {
		_ = os.Setenv("ENV_FILE", filepath.Join("..", "..", ".env"))
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	// 端点、密钥、TLS、模型都只存在数据库里，所以从机器人自己的库里读。
	live := liveSettings(t, cfg.DBPath)
	endpoint, apiKey := live.Endpoint, live.APIKey
	if endpoint == "" || apiKey == "" {
		t.Skipf("机器人数据库（%s）里还没配置模型端点/密钥，先在后台「系统设置」里填写", cfg.DBPath)
	}

	// --- stub only the QQ outbound call ---------------------------------
	var (
		mu   sync.Mutex
		sent []qq.SendMessageRequest
	)
	qqServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/getAppAccessToken":
			_, _ = w.Write([]byte(`{"access_token":"live-test","expires_in":"7200"}`))
		case strings.Contains(r.URL.Path, "/messages"):
			var body qq.SendMessageRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			sent = append(sent, body)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"LIVE-1","timestamp":"2026-07-21T10:00:00+08:00"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer qqServer.Close()

	// --- real model client, real TLS trust settings ----------------------
	// Paths in .env are relative to the directory the process runs from (the
	// project root); the test's working directory is internal/e2e, so resolve
	// them against the .env location first.
	envDir, _ := filepath.Abs(filepath.Dir(os.Getenv("ENV_FILE")))
	extraCAs := make([]string, 0, len(live.ExtraCAs))
	for _, p := range live.ExtraCAs {
		if !filepath.IsAbs(p) {
			p = filepath.Join(envDir, p)
		}
		extraCAs = append(extraCAs, p)
	}
	tlsOpts := httpx.TLSOptions{
		ExtraCAFiles:       extraCAs,
		InsecureSkipVerify: live.InsecureTLS,
	}
	aiHTTP, err := tlsOpts.Client(0)
	if err != nil {
		t.Fatalf("tls: %v", err)
	}
	aiClient := ai.NewClient(endpoint, apiKey, aiHTTP)

	// Confirm the catalogue is reachable before spending a completion.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := aiClient.ListModels(ctx)
	if err != nil {
		t.Fatalf("list models against the real endpoint: %v", err)
	}
	ids := map[string]bool{}
	for _, m := range models {
		ids[m.ID] = true
	}
	if !ids[live.Model] {
		t.Errorf("configured model %q is not offered by the endpoint", live.Model)
	}
	t.Logf("endpoint offers %d models; using %s", len(models), live.Model)

	// --- real dispatcher -------------------------------------------------
	st, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer func() { _ = st.Close() }()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sm, err := settings.New(context.Background(), st, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}

	tokens := qq.NewTokenManager("live", "live", qqServer.URL, nil)
	sender := qq.NewSender(qq.NewClient(qqServer.URL, tokens, nil), logger)

	dispatcher := core.New(core.Options{
		Settings: sm,
		Store:    st,
		AI:       aiClient,
		Sender:   sender,
		Logger:   logger,
		Prompt:   "你是一个测试助手，回答保持在一句话以内。",
	})
	dispatcher.Start(context.Background())

	payload := groupAtMessage("LIVE-1", "G-LIVE", "U-LIVE", "测试用户", "用一句话打招呼，并说明你是什么模型")
	dispatcher.HandlePayload(context.Background(), payload)

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sent) == 0 {
		t.Fatal("the real model produced no reply")
	}
	for i, m := range sent {
		t.Logf("reply segment %d (msg_id=%s msg_seq=%d):\n%s", i+1, m.MsgID, m.MsgSeq, m.Content)
	}
	if sent[0].MsgID != "LIVE-1" {
		t.Errorf("reply must be a passive reply to LIVE-1, got %q", sent[0].MsgID)
	}
	if dispatcher.Metrics().AIErrors.Load() != 0 {
		t.Errorf("model errors recorded: %s", dispatcher.LastAIError())
	}
}
