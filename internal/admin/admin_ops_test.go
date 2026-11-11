package admin

// Operations surface: runtime status, the SSE stream, and the action endpoints
// (reconnect / reload-token / test-ai / test-send). Nothing here reaches a real
// network: the upstarts are the harness's fake QQ and OpenAI servers.

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdminStatusEndpoint(t *testing.T) {
	h := newAdminTestHarness(t)
	seedAdminFixture(t, h)

	data := h.wantOK(t, h.get(t, "/api/status"))
	for _, key := range []string{
		"version", "started_at", "uptime_seconds", "transport", "trigger_mode",
		"context_mode", "full_group_detected", "platform", "ws", "webhook",
		"ai", "qq", "store", "counters",
	} {
		if _, present := data[key]; !present {
			t.Fatalf("status is missing %q: %#v", key, data)
		}
	}
	if data["version"] != "test" || data["trigger_mode"] != "at_only" || data["context_mode"] != "per_member" {
		t.Fatalf("status basics = %#v", data)
	}
	if data["uptime_seconds"].(float64) < 0 {
		t.Fatalf("uptime_seconds = %v", data["uptime_seconds"])
	}

	ws := data["ws"].(map[string]any)
	if ws["enabled"] != true || ws["state"] != "idle" {
		t.Fatalf("ws status = %#v", ws)
	}
	if shard, _ := ws["shard"].([]any); len(shard) != 2 {
		t.Fatalf("ws shard = %#v", ws["shard"])
	}
	hook := data["webhook"].(map[string]any)
	if hook["enabled"] != false || hook["path"] != "/qq/webhook" {
		t.Fatalf("webhook status = %#v", hook)
	}

	ai := data["ai"].(map[string]any)
	if !strings.HasSuffix(ai["base_url"].(string), "/v1") || ai["base_url_set"] != true {
		t.Fatalf("ai status = %#v", ai)
	}
	if ai["api_key_set"] != true || ai["model"] != "test-model" {
		t.Fatalf("ai status = %#v", ai)
	}
	if ai["in_flight"] != float64(0) || ai["max_concurrent"] != float64(4) {
		t.Fatalf("ai concurrency = %#v", ai)
	}

	qqBlock := data["qq"].(map[string]any)
	if qqBlock["app_id_masked"] != "test****" || qqBlock["sandbox"] != false {
		t.Fatalf("qq status = %#v", qqBlock)
	}
	if qqBlock["token_valid"] != false {
		t.Fatalf("token_valid should be false before the first fetch: %#v", qqBlock)
	}

	storeBlock := data["store"].(map[string]any)
	if storeBlock["path"] == "" || storeBlock["size_bytes"].(float64) <= 0 {
		t.Fatalf("store status = %#v", storeBlock)
	}
	if storeBlock["conversations"] != float64(3) {
		t.Fatalf("store conversations = %v", storeBlock["conversations"])
	}
	counters := data["counters"].(map[string]any)
	for _, key := range []string{
		"events_received", "events_deduped", "events_ignored", "replies_sent", "replies_failed",
		// 两阶段生成的两个计数器：后台概览直接读这两个键。
		"polish_requests", "polish_fallbacks",
		"reply_debounced_batches", "reply_debounced_messages",
		// 群聊消息合并：批次窗口数与被并入批次的后续消息数。
		"reply_debounced_batches", "reply_debounced_messages",
	} {
		if _, present := counters[key]; !present {
			t.Fatalf("counters is missing %q: %#v", key, counters)
		}
	}

	// Once a group has been probed, the platform summary reports it.
	h.wantOK(t, h.get(t, "/api/groups/"+adminTestGroup+"/state"))
	platform := h.wantOK(t, h.get(t, "/api/status"))["platform"].(map[string]any)
	if platform["known_groups"] != float64(1) {
		t.Fatalf("platform known_groups = %v", platform["known_groups"])
	}
	full, _ := platform["full_access_groups"].([]any)
	if len(full) != 1 {
		t.Fatalf("platform full groups = %#v", platform["full_access_groups"])
	}

	// Optional collaborators absent: the status must still render.
	noWS := newAdminTestHarness(t, func(o *adminTestOpts) { o.noWS = true })
	ws = noWS.wantOK(t, noWS.get(t, "/api/status"))["ws"].(map[string]any)
	if ws["enabled"] != false || ws["state"] != "disabled" {
		t.Fatalf("noWS status = %#v", ws)
	}
	noTokens := newAdminTestHarness(t, func(o *adminTestOpts) { o.noTokens = true })
	qqBlock = noTokens.wantOK(t, noTokens.get(t, "/api/status"))["qq"].(map[string]any)
	if qqBlock["token_valid"] != false || qqBlock["bot_id"] != "" {
		t.Fatalf("noTokens qq status = %#v", qqBlock)
	}
	noAI := newAdminTestHarness(t, func(o *adminTestOpts) { o.noAI = true })
	ai = noAI.wantOK(t, noAI.get(t, "/api/status"))["ai"].(map[string]any)
	if ai["base_url"] != "" || ai["base_url_set"] != false || ai["api_key_set"] != false {
		t.Fatalf("noAI status = %#v", ai)
	}
}

// adminTestReadSSEEvent reads one SSE frame with its event name.
func adminTestReadSSEEvent(t *testing.T, reader *bufio.Reader) (string, string) {
	t.Helper()
	name, data := "", ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE stream: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if name != "" || data != "" {
				return name, data
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "event: "); ok {
			name = rest
			continue
		}
		if rest, ok := strings.CutPrefix(line, "data: "); ok {
			data = rest
		}
	}
}

func TestAdminEventStream(t *testing.T) {
	h := newAdminTestHarness(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(10*time.Second, cancel)
	defer timer.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.ts.URL+"/api/events/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminTestToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type = %q", resp.Header.Get("Content-Type"))
	}

	reader := bufio.NewReader(resp.Body)
	name, payload := adminTestReadSSEEvent(t, reader)
	if name != "status" {
		t.Fatalf("first event = %q", name)
	}
	adminTestContains(t, payload, "counters")

	h.bus.Publish("message", map[string]any{"id": 1, "content": "hello"})
	name, payload = adminTestReadSSEEvent(t, reader)
	if name != "message" {
		t.Fatalf("second event = %q", name)
	}
	adminTestContains(t, payload, "hello")

	h.bus.Publish("log", map[string]any{"id": 1, "message": "ring entry"})
	name, payload = adminTestReadSSEEvent(t, reader)
	if name != "log" {
		t.Fatalf("third event = %q", name)
	}
	adminTestContains(t, payload, "ring entry")

	// The handler refuses to stream without a bus or without a flusher.
	noBus := newAdminTestHarness(t, func(o *adminTestOpts) { o.noBus = true })
	noBus.wantError(t, noBus.get(t, "/api/events/stream"), http.StatusInternalServerError, "not_configured")

	writer := &adminTestNoFlushWriter{header: http.Header{}}
	h.server.handleEventStream(writer, httptest.NewRequest(http.MethodGet, "/api/events/stream", nil))
	if writer.status != http.StatusInternalServerError {
		t.Fatalf("no-flusher status = %d", writer.status)
	}
	adminTestContains(t, writer.body.String(), "unsupported")
}

type adminTestNoFlushWriter struct {
	header http.Header
	body   strings.Builder
	status int
}

func (w *adminTestNoFlushWriter) Header() http.Header         { return w.header }
func (w *adminTestNoFlushWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *adminTestNoFlushWriter) WriteHeader(status int)      { w.status = status }

func TestAdminReconnectAction(t *testing.T) {
	h := newAdminTestHarness(t)
	data := h.wantOK(t, h.post(t, "/api/actions/reconnect", nil))
	if data["requested"] != true {
		t.Fatalf("reconnect response = %#v", data)
	}

	noWS := newAdminTestHarness(t, func(o *adminTestOpts) { o.noWS = true })
	noWS.wantError(t, noWS.post(t, "/api/actions/reconnect", nil), http.StatusBadRequest, "not_configured")
}

func TestAdminReloadTokenAction(t *testing.T) {
	h := newAdminTestHarness(t)
	data := h.wantOK(t, h.post(t, "/api/actions/reload-token", nil))
	if data["token_valid"] != true {
		t.Fatalf("reload response = %#v", data)
	}
	if adminTestNumber(t, data, "token_expires_in_seconds") <= 0 {
		t.Fatalf("token_expires_in_seconds = %v", data["token_expires_in_seconds"])
	}
	if h.qq.tokenCallCount() == 0 {
		t.Fatal("reload-token never reached the fake platform")
	}

	// The platform refusing credentials is a 502, not a silent success.
	h.qq.setTokenFails(true)
	h.wantError(t, h.post(t, "/api/actions/reload-token", nil), http.StatusBadGateway, "upstream_error")

	noTokens := newAdminTestHarness(t, func(o *adminTestOpts) { o.noTokens = true })
	noTokens.wantError(t, noTokens.post(t, "/api/actions/reload-token", nil), http.StatusBadRequest, "not_configured")
}

func TestAdminTestAIAction(t *testing.T) {
	h := newAdminTestHarness(t)

	data := h.wantOK(t, h.post(t, "/api/actions/test-ai", map[string]any{"prompt": "你好"}))
	if data["text"] != "这是测试回复。" || data["model"] != "test-model" {
		t.Fatalf("test-ai = %#v", data)
	}
	if data["finish_reason"] != "stop" || data["tools_advertised"] != false {
		t.Fatalf("test-ai = %#v", data)
	}
	usage := data["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(3) || usage["completion_tokens"] != float64(4) || usage["total_tokens"] != float64(7) {
		t.Fatalf("usage = %#v", usage)
	}
	if calls, _ := data["tool_calls"].([]any); len(calls) != 0 {
		t.Fatalf("tool_calls = %#v", data["tool_calls"])
	}

	// An empty prompt falls back to the built-in probe, and tools=true advertises
	// a probe tool so the operator can verify function calling.
	data = h.wantOK(t, h.post(t, "/api/actions/test-ai", map[string]any{"tools": true}))
	if data["tools_advertised"] != true {
		t.Fatalf("tools_advertised = %v", data["tools_advertised"])
	}
	h.ai.mu.Lock()
	lastBody := h.ai.lastBody
	h.ai.mu.Unlock()
	if tools, _ := lastBody["tools"].([]any); len(tools) != 1 {
		t.Fatalf("the tools parameter never reached the endpoint: %#v", lastBody)
	}

	// Malformed / unknown-field bodies are rejected before any upstream call.
	h.wantError(t, h.rawRequest(t, http.MethodPost, "/api/actions/test-ai", "not-json"),
		http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.post(t, "/api/actions/test-ai", map[string]any{"nope": 1}),
		http.StatusBadRequest, "invalid_request")

	h.ai.setChatStatus(http.StatusInternalServerError)
	h.wantError(t, h.post(t, "/api/actions/test-ai", map[string]any{"prompt": "hi"}),
		http.StatusBadGateway, "upstream_error")

	noAI := newAdminTestHarness(t, func(o *adminTestOpts) { o.noAI = true })
	noAI.wantError(t, noAI.post(t, "/api/actions/test-ai", nil), http.StatusBadRequest, "not_configured")
}

func TestAdminTestSendAction(t *testing.T) {
	h := newAdminTestHarness(t)

	data := h.wantOK(t, h.post(t, "/api/actions/test-send", map[string]any{
		"group_openid": adminTestGroup, "content": "测试主动消息",
	}))
	ids, _ := data["message_ids"].([]any)
	if len(ids) != 1 || ids[0] != "MSG-1" {
		t.Fatalf("message_ids = %#v", data["message_ids"])
	}
	if data["message_id"] != "MSG-1" || data["segments"] != float64(1) || data["active"] != true {
		t.Fatalf("test-send = %#v", data)
	}
	sent := h.qq.sent()
	if len(sent) != 1 || sent[0].Content != "测试主动消息" || sent[0].MsgID != "" {
		t.Fatalf("platform request = %#v", sent)
	}
	paths := h.qq.paths()
	if len(paths) != 1 || paths[0] != "/v2/groups/"+adminTestGroup+"/messages" {
		t.Fatalf("platform path = %#v", paths)
	}

	h.wantError(t, h.post(t, "/api/actions/test-send", map[string]any{"group_openid": adminTestGroup}),
		http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.post(t, "/api/actions/test-send", map[string]any{"content": "x"}),
		http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.post(t, "/api/actions/test-send", map[string]any{"group_openid": "   ", "content": "x"}),
		http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.rawRequest(t, http.MethodPost, "/api/actions/test-send", "not-json"),
		http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.post(t, "/api/actions/test-send", map[string]any{
		"group_openid": adminTestGroup, "content": "x", "extra": 1,
	}), http.StatusBadRequest, "invalid_request")

	// The per-minute limiter caps test sends at 10.
	limited := newAdminTestHarness(t)
	for i := 0; i < 10; i++ {
		limited.wantOK(t, limited.post(t, "/api/actions/test-send", map[string]any{
			"group_openid": adminTestGroup, "content": "第几条",
		}))
	}
	limited.wantError(t, limited.post(t, "/api/actions/test-send", map[string]any{
		"group_openid": adminTestGroup, "content": "再来一条",
	}), http.StatusTooManyRequests, "rate_limited")

	// A platform refusal is reported as an upstream error.
	failing := newAdminTestHarness(t)
	failing.qq.setSendStatus(http.StatusBadGateway)
	failing.wantError(t, failing.post(t, "/api/actions/test-send", map[string]any{
		"group_openid": adminTestGroup, "content": "会失败",
	}), http.StatusBadGateway, "upstream_error")
}

// TestSyncCommandsAction pins the manual escape hatch: the endpoint is
// authenticated like every other action, creates both scopes once, and reports
// "unchanged" on the next run instead of writing again.
func TestSyncCommandsAction(t *testing.T) {
	h := newAdminTestHarness(t)

	h.wantError(t, h.request(t, http.MethodPost, "/api/actions/sync-commands", nil,
		func(r *http.Request) { r.Header.Del("Authorization") }),
		http.StatusUnauthorized, "unauthorized")

	first := h.wantOK(t, h.post(t, "/api/actions/sync-commands", nil))
	results, _ := first["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %#v", first["results"])
	}
	scopes := []string{"group", "c2c"}
	for i, want := range scopes {
		res, _ := results[i].(map[string]any)
		if res["scope"] != want || res["action"] != "created" || res["panel_id"] == "" {
			t.Fatalf("result[%d] = %#v", i, res)
		}
	}

	// 第二次必须识别 marker 并报 unchanged，不重复创建。
	second := h.wantOK(t, h.post(t, "/api/actions/sync-commands", nil))
	results, _ = second["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("second results = %#v", second["results"])
	}
	for i, want := range scopes {
		res, _ := results[i].(map[string]any)
		if res["scope"] != want || res["action"] != "unchanged" {
			t.Fatalf("second result[%d] = %#v", i, res)
		}
	}

	h.qq.mu.Lock()
	defer h.qq.mu.Unlock()
	if got := len(h.qq.panels["group"]) + len(h.qq.panels["c2c"]); got != 2 {
		t.Fatalf("expected exactly two panels, got %d", got)
	}
	for _, scope := range scopes {
		rec := h.qq.panels[scope][0]
		if rec.Panel.Remark != "bot-harness:commands" {
			t.Fatalf("%s remark = %q", scope, rec.Panel.Remark)
		}
		if len(rec.Panel.Items) < 2 || rec.Panel.Items[0].Type != "command" {
			t.Fatalf("%s items = %#v", scope, rec.Panel.Items)
		}
	}
}

// TestSyncCommandsActionUpstreamError pins the failure contract of the manual
// sync: a platform refusal is 502 upstream_error (not 200, not 500) with the
// platform text preserved, so the operator can tell "the platform said no" from
// "the admin server broke". 400 not_configured covers the other side.
func TestSyncCommandsActionUpstreamError(t *testing.T) {
	failing := newAdminTestHarness(t)
	failing.qq.setPanelsStatus(http.StatusBadRequest, 40030013, "面板内容不合规")
	failing.wantError(t, failing.post(t, "/api/actions/sync-commands", nil),
		http.StatusBadGateway, "upstream_error")

	// 调度器未初始化是 400 not_configured，不是 502：这两种失败的原因不同。
	noDispatcher := newAdminTestHarness(t, func(o *adminTestOpts) { o.noDispatcher = true })
	noDispatcher.wantError(t, noDispatcher.post(t, "/api/actions/sync-commands", nil),
		http.StatusBadRequest, "not_configured")
}
