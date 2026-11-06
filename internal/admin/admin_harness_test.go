package admin

// Test harness for the admin HTTP surface.
//
// Everything runs against a real SQLite store in t.TempDir() and a fake QQ /
// OpenAI upstream served by httptest, so no test touches the network and none
// of them need real credentials. The wiring mirrors cmd/bot-harness/main.go
// (settings in the database, dispatcher built around the real store), because
// the point of these tests is the contract the panel actually sees.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/bus"
	"github.com/dynesshely/bot-harness/internal/config"
	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/logging"
	"github.com/dynesshely/bot-harness/internal/media"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
	"github.com/dynesshely/bot-harness/internal/testpg"
)

const (
	adminTestToken     = "test-admin-token"
	adminTestGroup     = "G-ADMIN-1"
	adminTestGroupAlt  = "G-ADMIN-2"
	adminTestGroupNoPr = "G-ADMIN-NOPRO"
)

// adminTestQQ is the fake QQ OpenAPI, recording what the bot actually sent.
type adminTestQQ struct {
	mu sync.Mutex

	states    map[string]qq.BotState
	stateErrs map[string]bool

	tokenFails bool
	tokenCalls int

	sendStatus int
	sendCalls  int
	sends      []qq.SendMessageRequest
	sendPaths  []string
	recalls    []string

	// 指令面板：只实现同步用到的 list/create/update，够 admin 动作测试使用。
	panels       map[string][]qq.PanelRecord
	panelNextID  int
	panelStatus  int
	panelCode    int
	panelMessage string
	panelCalls   int
}

func newAdminTestQQ() *adminTestQQ {
	return &adminTestQQ{
		states:    map[string]qq.BotState{},
		stateErrs: map[string]bool{},
		panels:    map[string][]qq.PanelRecord{},
	}
}

func (q *adminTestQQ) setState(group string, st qq.BotState) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.states[group] = st
}

func (q *adminTestQQ) failState(group string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stateErrs[group] = true
}

// setPanelsStatus makes every /v2/panels call fail with a platform error, the
// way a missing permission or a content-risk refusal does in production.
func (q *adminTestQQ) setPanelsStatus(status, code int, message string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.panelStatus = status
	q.panelCode = code
	q.panelMessage = message
}

// panelCallCount reports how many /v2/panels requests reached the fake.
func (q *adminTestQQ) panelCallCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.panelCalls
}

func (q *adminTestQQ) setSendStatus(status int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sendStatus = status
}

func (q *adminTestQQ) setTokenFails(fail bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tokenFails = fail
}

func (q *adminTestQQ) sent() []qq.SendMessageRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]qq.SendMessageRequest, len(q.sends))
	copy(out, q.sends)
	return out
}

func (q *adminTestQQ) paths() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, len(q.sendPaths))
	copy(out, q.sendPaths)
	return out
}

func (q *adminTestQQ) tokenCallCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.tokenCalls
}

func (q *adminTestQQ) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/app/getAppAccessToken", func(w http.ResponseWriter, _ *http.Request) {
		q.mu.Lock()
		q.tokenCalls++
		fail := q.tokenFails
		q.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail {
			_, _ = io.WriteString(w, `{"code":1000,"message":"bad credentials"}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"test-token","expires_in":"7200"}`)
	})
	mux.HandleFunc("/users/@me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"BOT-ID","username":"test-bot"}`)
	})
	mux.HandleFunc("/v2/groups/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v2/groups/")
		group, action, _ := strings.Cut(rest, "/")
		if decoded, err := url.PathUnescape(group); err == nil {
			group = decoded
		}
		if action == "bot_state" {
			q.mu.Lock()
			st, ok := q.states[group]
			failed := q.stateErrs[group]
			q.mu.Unlock()
			if failed {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"code":22001,"message":"upstream unavailable"}`)
				return
			}
			if !ok {
				http.NotFound(w, r)
				return
			}
			st.JoinedAt = "2026-01-01T00:00:00+08:00"
			st.MemberRole = "member"
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(st)
			return
		}
		if action == "info" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"group_openid": group,
				"group_name":   "测试群",
			})
			return
		}
		if strings.HasPrefix(action, "messages/") {
			id, _ := url.PathUnescape(strings.TrimPrefix(action, "messages/"))
			q.mu.Lock()
			q.recalls = append(q.recalls, id)
			q.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "{}")
			return
		}
		if action == "messages" && r.Method == http.MethodPost {
			var body qq.SendMessageRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			q.mu.Lock()
			q.sendCalls++
			status := q.sendStatus
			q.sends = append(q.sends, body)
			q.sendPaths = append(q.sendPaths, r.URL.Path)
			n := q.sendCalls
			q.mu.Unlock()
			if status != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"code":40054,"message":"platform refused"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":        fmt.Sprintf("MSG-%d", n),
				"timestamp": "2026-01-01T00:00:00+08:00",
				"ext_info":  map[string]any{"ref_idx": fmt.Sprintf("REFIDX-%d", n)},
			})
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/v2/panels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q.mu.Lock()
		q.panelCalls++
		status, code, message := q.panelStatus, q.panelCode, q.panelMessage
		q.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"code":%d,"message":%q}`, code, message)
			return
		}
		switch r.Method {
		case http.MethodGet:
			scope := r.URL.Query().Get("scope")
			q.mu.Lock()
			records := make([]qq.PanelRecord, 0, len(q.panels[scope]))
			for _, rec := range q.panels[scope] {
				rec.Panel.Items = append([]qq.PanelItem(nil), rec.Panel.Items...)
				records = append(records, rec)
			}
			q.mu.Unlock()
			_ = json.NewEncoder(w).Encode(qq.PanelList{Records: records, IsEnd: true})
		case http.MethodPost:
			var req qq.CreatePanelRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			q.mu.Lock()
			q.panelNextID++
			id := fmt.Sprintf("PANEL-%d", q.panelNextID)
			rec := qq.PanelRecord{
				PanelID: id, Scope: req.Scope, TargetType: req.TargetType,
				Panel:   qq.Panel{Items: req.Panel.Items, Remark: req.Panel.Remark, Version: 1},
				Version: 1,
			}
			q.panels[req.Scope] = append(q.panels[req.Scope], rec)
			q.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"panel_id": id})
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/v2/panels/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v2/panels/")
		var body struct {
			Panel qq.Panel `json:"panel"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		q.mu.Lock()
		version := 0
		for scope := range q.panels {
			for i := range q.panels[scope] {
				rec := &q.panels[scope][i]
				if rec.PanelID != id {
					continue
				}
				rec.Panel.Items = body.Panel.Items
				rec.Panel.Remark = body.Panel.Remark
				rec.Version++
				rec.Panel.Version = rec.Version
				version = rec.Version
			}
		}
		q.mu.Unlock()
		if version == 0 {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": version})
	})
	mux.HandleFunc("/v2/users/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v2/users/")
		if strings.HasSuffix(rest, "/messages") && r.Method == http.MethodPost {
			var body qq.SendMessageRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			q.mu.Lock()
			q.sendCalls++
			status := q.sendStatus
			q.sends = append(q.sends, body)
			q.sendPaths = append(q.sendPaths, r.URL.Path)
			n := q.sendCalls
			q.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"code":40054,"message":"platform refused"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       fmt.Sprintf("C2C-MSG-%d", n),
				"ext_info": map[string]any{"ref_idx": fmt.Sprintf("C2C-REFIDX-%d", n)},
			})
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

// adminTestAI is the fake OpenAI-compatible endpoint.
type adminTestAI struct {
	mu sync.Mutex

	modelsStatus int
	chatStatus   int
	reply        string
	chatCalls    int
	modelCalls   int
	lastBody     map[string]any
}

func (a *adminTestAI) setModelsStatus(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.modelsStatus = status
}

func (a *adminTestAI) setChatStatus(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.chatStatus = status
}

func (a *adminTestAI) setReply(reply string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reply = reply
}

func (a *adminTestAI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		status := a.modelsStatus
		a.modelCalls++
		a.mu.Unlock()
		if status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"model catalogue unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"test-model"},{"id":"another-model"}]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.mu.Lock()
		status := a.chatStatus
		a.chatCalls++
		a.lastBody = body
		reply := a.reply
		a.mu.Unlock()
		if reply == "" {
			reply = "这是测试回复。"
		}
		if status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"upstream model failed"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(map[string]any{
			"model": "test-model",
			"choices": []map[string]any{{
				"delta":         map[string]string{"content": reply},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	return mux
}

// adminTestOpts switches individual collaborators off so the handlers' nil
// guards are exercised too.
type adminTestOpts struct {
	noWS         bool
	noTokens     bool
	noMedia      bool
	noAI         bool
	noBus        bool
	noDispatcher bool
	// pgDSN switches the harness's store from a temp SQLite file to a real
	// PostgreSQL database. It is empty by default, so the whole suite keeps
	// running on SQLite; only the engine-specific test sets it (from
	// PG_TEST_DSN), which keeps the shared database untouched by every other
	// test in this package.
	pgDSN string
}

type adminTestHarness struct {
	t *testing.T

	store    *store.Store
	settings *settings.Manager
	bus      *bus.Bus
	ring     *logging.Ring
	logger   *slog.Logger
	dis      *core.Dispatcher
	aiClient *ai.Client
	qqClient *qq.Client
	tokens   *qq.TokenManager
	ws       *qq.WSClient
	media    *media.Bundle
	cfg      *config.Config
	server   *Server
	ts       *httptest.Server

	qq *adminTestQQ
	ai *adminTestAI
}

func newAdminTestHarness(t *testing.T, tweaks ...func(*adminTestOpts)) *adminTestHarness {
	t.Helper()

	opts := &adminTestOpts{}
	for _, tweak := range tweaks {
		tweak(opts)
	}

	h := &adminTestHarness{t: t, qq: newAdminTestQQ(), ai: &adminTestAI{}}
	h.qq.setState(adminTestGroup, qq.BotState{
		MemberOpenID: "BOT-MEMBER-1", AllowProactiveMsg: true, RecvMsgSetting: "all",
	})
	h.qq.setState(adminTestGroupAlt, qq.BotState{
		MemberOpenID: "BOT-MEMBER-2", AllowProactiveMsg: true, RecvMsgSetting: "all",
	})
	h.qq.setState(adminTestGroupNoPr, qq.BotState{
		MemberOpenID: "BOT-MEMBER-3", AllowProactiveMsg: false, RecvMsgSetting: "only_mention",
	})

	qqServer := httptest.NewServer(h.qq.handler())
	t.Cleanup(qqServer.Close)
	aiServer := httptest.NewServer(h.ai.handler())
	t.Cleanup(aiServer.Close)

	dsn := t.TempDir() + "/bot.db"
	if opts.pgDSN != "" {
		dsn = opts.pgDSN
	}
	// The PostgreSQL path comes from PG_TEST_DSN; refuse production before the
	// connection is opened (this is the suite that polluted it in the incident).
	testpg.Guard(t, dsn)
	st, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h.store = st

	// DBPath is the *bootstrap* value and stays set even when the store opened
	// a PostgreSQL DSN, which is exactly the stale value the config contract
	// must not report any more.
	h.cfg = &config.Config{Version: "test", DBPath: t.TempDir() + "/bot.db"}

	h.tokens = qq.NewTokenManager("test-app-id", "test-app-secret", qqServer.URL, nil)
	h.qqClient = qq.NewClient(qqServer.URL, h.tokens, nil)
	sender := qq.NewSender(h.qqClient, slog.New(slog.NewTextHandler(io.Discard, nil)))

	h.ring = logging.NewRing(500)
	h.bus = bus.New()
	handler, _ := logging.NewHandler(logging.Options{
		Writer: io.Discard,
		Level:  slog.LevelDebug,
		Ring:   h.ring,
		Bus:    h.bus,
	})
	h.logger = slog.New(handler)

	h.aiClient = ai.NewClient("", "", &http.Client{Timeout: 10 * time.Second})

	h.media = media.NewBundle(media.NewStore(st.DB()), nil, h.logger, 2)

	sm, err := settings.New(context.Background(), st, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	h.settings = sm
	h.aiClient.SetBaseURLResolver(func() string { return sm.Get().OpenAIBaseURL })
	h.aiClient.SetAPIKeyResolver(func() string { return sm.Get().OpenAIAPIKey })
	h.applySettings(t, map[string]any{
		"admin_token":          adminTestToken,
		"qq_app_id":            "test-app-id",
		"qq_app_secret":        "test-app-secret",
		"openai_base_url":      aiServer.URL + "/v1",
		"openai_api_key":       "test-key",
		"model":                "test-model",
		"markdown_enabled":     false,
		"humanize_level":       "off",
		"reply_footer_enabled": false,
		"early_reply_enabled":  false,
	})

	h.dis = core.New(core.Options{
		Settings: sm,
		Store:    st,
		AI:       h.aiClient,
		Sender:   sender,
		Bus:      h.bus,
		Logger:   h.logger,
		Prompt:   "你是测试助手",
		QQ:       h.qqClient,
		Media:    h.media,
	})
	h.dis.SetSilenceRoll(func() float64 { return 1 })
	h.dis.Start(context.Background())

	h.ws = qq.NewWSClient(qq.WSOptions{Client: h.qqClient, Intents: settings.DefaultIntents, Logger: h.logger})

	deps := Deps{
		Config:     h.cfg,
		Settings:   sm,
		Store:      st,
		Dispatcher: h.dis,
		AI:         h.aiClient,
		QQ:         h.qqClient,
		Tokens:     h.tokens,
		WS:         h.ws,
		Bus:        h.bus,
		Logger:     h.logger,
		LogRing:    h.ring,
		Media:      h.media,
		StartedAt:  time.Now(),
	}
	if opts.noWS {
		deps.WS = nil
	}
	if opts.noTokens {
		deps.Tokens = nil
	}
	if opts.noMedia {
		deps.Media = nil
	}
	if opts.noAI {
		deps.AI = nil
	}
	if opts.noBus {
		deps.Bus = nil
	}
	if opts.noDispatcher {
		deps.Dispatcher = nil
	}
	h.server = New(deps)
	h.ts = httptest.NewServer(h.server.Handler())
	t.Cleanup(h.ts.Close)
	return h
}

// applySettings writes a JSON patch into the runtime settings.
func (h *adminTestHarness) applySettings(t *testing.T, patch map[string]any) {
	t.Helper()
	raw := make(map[string]json.RawMessage, len(patch))
	for k, v := range patch {
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal setting %q: %v", k, err)
		}
		raw[k] = encoded
	}
	if _, _, err := h.settings.Apply(context.Background(), raw); err != nil {
		t.Fatalf("apply settings: %v", err)
	}
}

// adminTestResponse is one decoded admin API response.
type adminTestResponse struct {
	status  int
	header  http.Header
	body    []byte
	ok      bool
	payload json.RawMessage
	code    string
	message string
}

func (r *adminTestResponse) decodeEnvelope() {
	var env map[string]any
	if err := json.Unmarshal(r.body, &env); err != nil {
		return
	}
	if v, ok := env["ok"].(bool); ok {
		r.ok = v
	}
	if d, ok := env["data"]; ok {
		r.payload, _ = json.Marshal(d)
	}
	if e, ok := env["error"].(map[string]any); ok {
		r.code, _ = e["code"].(string)
		r.message, _ = e["message"].(string)
	}
}

// data decodes the success envelope's payload into a generic map.
func (r *adminTestResponse) data(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.payload, &out); err != nil {
		t.Fatalf("decode data (status %d, body %s): %v", r.status, string(r.body), err)
	}
	return out
}

// request issues one admin call. Authorization defaults to the admin bearer
// token; mutate the request (or set auth=false) for the auth tests.
func (h *adminTestHarness) request(t *testing.T, method, path string, body any, mutate ...func(*http.Request)) *adminTestResponse {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, h.ts.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+adminTestToken)
	for _, fn := range mutate {
		fn(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	out := &adminTestResponse{status: resp.StatusCode, header: resp.Header, body: raw}
	if len(raw) > 0 && strings.Contains(resp.Header.Get("Content-Type"), "json") {
		out.decodeEnvelope()
	}
	return out
}

// rawRequest sends a body verbatim (for malformed-JSON branches).
func (h *adminTestHarness) rawRequest(t *testing.T, method, path, body string) *adminTestResponse {
	t.Helper()
	req, err := http.NewRequest(method, h.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminTestToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	out := &adminTestResponse{status: resp.StatusCode, header: resp.Header, body: raw}
	out.decodeEnvelope()
	return out
}

func (h *adminTestHarness) get(t *testing.T, path string) *adminTestResponse {
	t.Helper()
	return h.request(t, http.MethodGet, path, nil)
}

func (h *adminTestHarness) post(t *testing.T, path string, body any) *adminTestResponse {
	t.Helper()
	return h.request(t, http.MethodPost, path, body)
}

func (h *adminTestHarness) put(t *testing.T, path string, body any) *adminTestResponse {
	t.Helper()
	return h.request(t, http.MethodPut, path, body)
}

func (h *adminTestHarness) del(t *testing.T, path string) *adminTestResponse {
	t.Helper()
	return h.request(t, http.MethodDelete, path, nil)
}

func (h *adminTestHarness) wantStatus(t *testing.T, resp *adminTestResponse, want int) *adminTestResponse {
	t.Helper()
	if resp.status != want {
		t.Fatalf("status = %d, want %d (body %s)", resp.status, want, string(resp.body))
	}
	return resp
}

func (h *adminTestHarness) wantOK(t *testing.T, resp *adminTestResponse) map[string]any {
	t.Helper()
	h.wantStatus(t, resp, http.StatusOK)
	if !resp.ok {
		t.Fatalf("ok = false (body %s)", string(resp.body))
	}
	return resp.data(t)
}

func (h *adminTestHarness) wantError(t *testing.T, resp *adminTestResponse, status int, code string) {
	t.Helper()
	h.wantStatus(t, resp, status)
	if resp.ok {
		t.Fatalf("expected an error envelope, got ok=true (body %s)", string(resp.body))
	}
	if code != "" && resp.code != code {
		t.Fatalf("error.code = %q, want %q (body %s)", resp.code, code, string(resp.body))
	}
	if resp.message == "" {
		t.Fatalf("error.message must explain the failure (body %s)", string(resp.body))
	}
}

// seedConversation inserts one conversation fixture.
func (h *adminTestHarness) seedConversation(t *testing.T, c store.Conversation) {
	t.Helper()
	if err := h.store.EnsureConversation(context.Background(), &c); err != nil {
		t.Fatalf("seed conversation %q: %v", c.ID, err)
	}
}

// seedMessage inserts one timeline row and returns its id.
func (h *adminTestHarness) seedMessage(t *testing.T, in store.MessageInput) int64 {
	t.Helper()
	id, err := h.store.AppendMessageID(context.Background(), in)
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}
	return id
}

func (h *adminTestHarness) seedRecord(t *testing.T, r store.Record) {
	t.Helper()
	if _, err := h.store.AddRecord(context.Background(), &r); err != nil {
		t.Fatalf("seed record: %v", err)
	}
}

func adminTestContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%q does not contain %q", haystack, needle)
	}
}
