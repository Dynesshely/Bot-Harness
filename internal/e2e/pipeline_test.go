// Package e2e drives the real dispatcher against fake QQ and OpenAI servers.
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/admin"
	"github.com/dynesshely/bot-harness/internal/affect"
	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/bus"
	"github.com/dynesshely/bot-harness/internal/config"
	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/media"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
	"github.com/dynesshely/bot-harness/internal/tools"
)

// sentMessage records one outbound platform call.
type sentMessage struct {
	Path string
	Body qq.SendMessageRequest
}

// uploadedFile records one POST /files call (the rich-media upload that must
// precede a msg_type=7 message).
type uploadedFile struct {
	Path string
	Body map[string]any
}

type harness struct {
	t        *testing.T
	bus      *bus.Bus
	dis      *core.Dispatcher
	st       *store.Store
	settings *settings.Manager
	affect   *affect.Service
	cfg      *config.Config
	qqClient *qq.Client
	media    *media.Bundle
	sender   *qq.Sender
	client   *ai.Client
	logger   *slog.Logger

	mu         sync.Mutex
	botStates  map[string]string
	botSetting map[string]string
	recalled   []string
	lastAIBody map[string]any
	imageURL   string
	sent       []sentMessage
	uploads    []uploadedFile
	// uploadFail makes POST /files answer 500, for the image→text fallback.
	uploadFail bool
	aiReply    string
	aiFail     bool
	// aiFailStatus / aiFailMessage let a test drive the user-facing error path:
	// the fake upstream answers this HTTP status with this provider message.
	aiFailStatus  int
	aiFailMessage string
	aiDelay       time.Duration
	// aiReasoningOnly makes the fake provider imitate a reasoning model that
	// burns its whole budget thinking and never writes a final answer.
	aiReasoningOnly bool
	// aiBaseURL is the fake upstream the harness seeds into the database
	// settings (the endpoint no longer comes from .env).
	aiBaseURL string
	// allowProactive mirrors the platform's 主动消息 switch in bot_state.
	allowProactive bool
	// aiToolScript makes the fake model request these tools in order before it
	// finally answers, which is what drives the agent loop.
	aiToolScript []string
	// aiProactiveReply is what the fake model returns for the spontaneous-message
	// reflection prompt.
	aiProactiveReply string
	// aiExtractReply is what the fake model returns for the memory-extraction prompt.
	aiExtractReply string
	// commitmentReply is the JSON verdict for a commitment nudge decision.
	commitmentReply string
	// humanStatusReply is the JSON array returned for a human-status inference.
	humanStatusReply string
	// humanStatusImageTool makes the fake model request analyze_image on the
	// first human-status round. The second round (role=tool present) answers with
	// humanStatusReply, which exercises the whole tool loop.
	humanStatusImageTool   bool
	humanStatusImageID     string
	humanStatusVision      string
	commitmentCalls        int
	humanStatusCalls       int
	humanStatusVisionCalls int
	// joinGateReply is the JSON verdict returned for the 自主加入判断 prompt;
	// joinGateCalls counts those requests. The fake provider recognises them by
	// the system-message marker, exactly like the polish/commitment calls.
	joinGateReply    string
	joinGateCalls    int
	lastJoinGateBody map[string]any
	// aiRejectTools makes the fake upstream answer HTTP 400 whenever the request
	// carries a tools array (a gateway that does not support tool calling).
	aiRejectTools bool
	// polish* drive stage 2 (the two-stage pipeline). The fake provider tells
	// the calls apart by the system message: only the polish call carries the
	// polisher instruction, so no extra routing state is needed.
	polishReply string
	// polishStatus, when non-zero, makes stage 2 fail with that HTTP status.
	polishStatus int
	// polishEmpty makes stage 2 answer with a blank completion.
	polishEmpty bool
	// polishDelay stalls stage 2 past its deadline (the fallback path).
	polishDelay time.Duration
	// polishCalls records every stage-2 request body, in order.
	polishCalls []map[string]any
	// rejectMarkdown makes the fake platform answer 304036 (no markdown
	// template permission) for msg_type=2 requests.
	rejectMarkdown bool
	qqStatus       int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if os.Getenv("BOT_TEST_DEBUG") != "" {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	h := &harness{t: t, bus: bus.New(), aiReply: "这是测试回复。", allowProactive: true, logger: logger}

	qqServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		status := h.qqStatus
		h.mu.Unlock()

		switch {
		case r.URL.Path == "/app/getAppAccessToken":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":"7200"}`))
		case strings.HasSuffix(r.URL.Path, "/bot_state"):
			group := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/groups/"), "/bot_state")
			h.mu.Lock()
			openID, ok := h.botStates[group]
			h.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			h.mu.Lock()
			setting := h.botSetting[group]
			proactive := h.allowProactive
			h.mu.Unlock()
			if setting == "" {
				setting = "all"
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"member_openid":%q,"recv_msg_setting":%q,"allow_proactive_msg":%t,"member_role":"member"}`, openID, setting, proactive)
		case r.URL.Path == "/users/@me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"2356171532736147183","username":"Kaku"}`))
		case r.URL.Path == "/gateway/bot":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"url":"wss://example.invalid/websocket","shards":1}`))
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/messages/"):
			h.mu.Lock()
			h.recalled = append(h.recalled, r.URL.Path)
			h.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/files"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			h.mu.Lock()
			h.uploads = append(h.uploads, uploadedFile{Path: r.URL.Path, Body: body})
			fail := h.uploadFail
			h.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("{\"code\":40034001,\"message\":\"upload rejected\"}"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{\"file_info\":\"FILEINFO-E2E\",\"ttl\":3600}"))
		case strings.Contains(r.URL.Path, "/messages"):
			var body qq.SendMessageRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			h.mu.Lock()
			h.sent = append(h.sent, sentMessage{Path: r.URL.Path, Body: body})
			h.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"code":40034128,"message":"passive reply expired"}`))
				return
			}
			h.mu.Lock()
			noMD := h.rejectMarkdown
			h.mu.Unlock()
			if noMD && body.MsgType == qq.MsgTypeMarkdown {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":304036,"message":"no markdown permission"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"MSG-OUT-1","timestamp":"2026-07-21T10:00:01+08:00","ext_info":{"ref_idx":"REFIDX_SENT"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(qqServer.Close)

	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		// stage2（润色）不是主路径：它不能覆盖 lastAIBody，
		// 也不该被当成一次「对话调用」计数。join gate 同理：它是判定调用。
		stage2 := isPolishRequest(body)
		joinGate := hasSystemText(body, joinGateMarkerText)
		h.mu.Lock()
		if !stage2 && !joinGate {
			h.lastAIBody = body
		}
		fail := h.aiFail
		failStatus := h.aiFailStatus
		failMessage := h.aiFailMessage
		reply := h.aiReply
		delay := h.aiDelay
		reasoningOnly := h.aiReasoningOnly
		streaming, _ := body["stream"].(bool)
		h.mu.Unlock()

		if delay > 0 {
			time.Sleep(delay)
		}
		if isPolishRequest(body) {
			h.servePolish(w, body, streaming)
			return
		}
		// 自主加入判断：系统提示里带固定标记，返回 JSON 判定（不是一条回复）。
		if joinGate {
			h.mu.Lock()
			h.joinGateCalls++
			verdict := h.joinGateReply
			h.lastJoinGateBody = body
			h.mu.Unlock()
			if verdict == "" {
				verdict = defaultJoinGateVerdict
			}
			writeJSONCompletion(w, verdict)
			return
		}
		// 图片分析工具是独立的多模态请求：只有一条 user 消息、没有 tools。
		// 主对话即使带图也会带系统提示词与 tools，不能误判成一次视觉分析。
		// 脚本驱动的工具调用优先：测试要在带图消息上强制调用 view_image，
		// 这类请求必须先按脚本走一遍工具循环。
		if isVisionAnalysisRequest(body) && !h.hasScriptedTool() {
			h.mu.Lock()
			h.humanStatusVisionCalls++
			vision := h.humanStatusVision
			h.mu.Unlock()
			if vision == "" {
				vision = "画面是电脑上的游戏界面，右上角有计时。"
			}
			writeJSONCompletion(w, vision)
			return
		}
		if fail {
			status := failStatus
			if status == 0 {
				status = http.StatusInternalServerError
			}
			message := failMessage
			if message == "" {
				message = "boom"
			}
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"error":{"message":%q}}`, message)
			return
		}
		h.mu.Lock()
		rejectTools := h.aiRejectTools
		h.mu.Unlock()
		if _, hasTools := body["tools"]; hasTools && rejectTools {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"tools is not supported by this model"}}`))
			return
		}
		h.mu.Lock()
		var nextTool string
		if len(h.aiToolScript) > 0 {
			nextTool = h.aiToolScript[0]
			h.aiToolScript = h.aiToolScript[1:]
		}
		h.mu.Unlock()
		if nextTool != "" {
			w.Header().Set("Content-Type", "text/event-stream")
			args := "{}"
			if i := strings.Index(nextTool, "|"); i >= 0 {
				args = nextTool[i+1:]
				nextTool = nextTool[:i]
			} else if nextTool == "calc" {
				args = `{"expr":"(12+30)*2"}`
			}
			callID := "call_" + nextTool
			// 第一片带 id/name，第二片只带 arguments 片段：客户端必须按 index 拼接。
			head := fmt.Sprintf(
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":%q,"arguments":""}}]}}]}`,
				callID, nextTool)
			argsChunk, _ := json.Marshal(map[string]any{"choices": []map[string]any{{
				"delta": map[string]any{"tool_calls": []map[string]any{{
					"index": 0, "function": map[string]any{"arguments": args}}}}}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", head, argsChunk)
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return
		}
		// 承诺追问决策：系统提示里带固定标记，返回 JSON 决策。
		if hasSystemText(body, "承诺追问决策") {
			h.mu.Lock()
			h.commitmentCalls++
			verdict := h.commitmentReply
			h.mu.Unlock()
			if verdict == "" {
				verdict = `{"message":"该处理一下了，方便说一声吗？","next_check_seconds":7200,"reason":"测试默认"}`
			}
			writeJSONCompletion(w, verdict)
			return
		}
		// 人类状态推测：系统提示里带固定标记，返回 JSON 数组。
		if hasSystemText(body, "【人类状态推测】") {
			h.mu.Lock()
			h.humanStatusCalls++
			verdict := h.humanStatusReply
			withTool := h.humanStatusImageTool
			mediaID := h.humanStatusImageID
			h.mu.Unlock()
			if verdict == "" {
				verdict = "[]"
			}
			if withTool && !hasToolMessage(body) {
				call := map[string]any{
					"id": "call_hs_image_1", "type": "function",
					"function": map[string]any{
						"name":      "analyze_image",
						"arguments": fmt.Sprintf(`{"media_id":%q,"question":"画面里在做什么？"}`, mediaID),
					},
				}
				payload, _ := json.Marshal(map[string]any{
					"model": "test-model",
					"choices": []map[string]any{{
						"message":       map[string]any{"content": "", "tool_calls": []any{call}},
						"finish_reason": "tool_calls",
					}},
				})
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(payload)
				return
			}
			writeJSONCompletion(w, verdict)
			return
		}
		// 自发主动的反思请求：返回 JSON 决策
		if msgs, ok := body["messages"].([]any); ok {
			for _, raw := range msgs {
				m, _ := raw.(map[string]any)
				if text, _ := m["content"].(string); strings.Contains(text, "有机会主动说一句话") {
					h.mu.Lock()
					verdict := h.aiProactiveReply
					h.mu.Unlock()
					if verdict == "" {
						verdict = `{"speak":false,"text":"","reason":"没什么可说的"}`
					}
					w.Header().Set("Content-Type", "application/json")
					payload, _ := json.Marshal(map[string]any{
						"model": "test-model",
						"choices": []map[string]any{{
							"message":       map[string]string{"content": verdict},
							"finish_reason": "stop",
						}},
					})
					_, _ = w.Write(payload)
					return
				}
			}
		}
		// 记忆抽取请求：提示词里带「提取值得长期记住的事实」，返回 JSON 数组。
		if msgs, ok := body["messages"].([]any); ok {
			for _, raw := range msgs {
				m, _ := raw.(map[string]any)
				if text, _ := m["content"].(string); strings.Contains(text, "提取值得长期记住的事实") {
					h.mu.Lock()
					extracted := h.aiExtractReply
					h.mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					payload, _ := json.Marshal(map[string]any{
						"model": "test-model",
						"choices": []map[string]any{{
							"message":       map[string]string{"content": extracted},
							"finish_reason": "stop",
						}},
					})
					_, _ = w.Write(payload)
					return
				}
			}
		}
		if reasoningOnly {
			// 思维链是模型的草稿纸，任何情况下都不该出现在群消息里。
			const cot = "这是不该外发的思维链"
			if streaming {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\n",
					`{"choices":[{"delta":{"reasoning_content":"`+cot+`"}}]}`)
				_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w,
				`{"model":"test-model","choices":[{"message":{"content":"","reasoning_content":"%s"},"finish_reason":"stop"}]}`, cot)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(map[string]any{
			"model":   "test-model",
			"choices": []map[string]any{{"delta": map[string]string{"content": reply}, "finish_reason": "stop"}},
			"usage":   map[string]int{"total_tokens": 7},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	t.Cleanup(aiServer.Close)

	// 假图片服务器：模拟 QQ 的附件下载地址
	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG())
	}))
	t.Cleanup(imgServer.Close)
	h.imageURL = imgServer.URL + "/a.png"

	st, err := store.Open(t.TempDir() + "/bot.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	h.st = st
	// 配置都在数据库里了；cfg 只剩版本与数据库路径。
	h.cfg = &config.Config{Version: "test", DBPath: t.TempDir() + "/bot.db"}

	// QQ 凭证也是数据库配置；假服务端不校验，给个占位即可。
	tokens := qq.NewTokenManager("test-app-id", "test-secret", qqServer.URL, nil)
	// 和 main.go 一样：构造时给空值，真正生效的端点/密钥来自数据库设置。
	h.client = ai.NewClient("", "", &http.Client{Timeout: 10 * time.Second})
	h.qqClient = qq.NewClient(qqServer.URL, tokens, nil)
	h.sender = qq.NewSender(h.qqClient, h.logger)
	dl := media.NewDownloader(&http.Client{Timeout: 5 * time.Second}, 1<<20, 5*time.Second)
	dl.AllowedHostSuffixes = []string{"127.0.0.1"}
	h.media = media.NewBundle(media.NewStore(st.DB()), dl, h.logger, 2)

	sm, err := settings.New(context.Background(), st, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	h.settings = sm
	h.affect = affect.NewService(st, sm, logger, affect.NewMetrics())
	// 和 main.go 一样：端点与密钥每次请求重新解析，设置面板改了立刻生效。
	h.client.SetBaseURLResolver(func() string { return sm.Get().OpenAIBaseURL })
	h.client.SetAPIKeyResolver(func() string { return sm.Get().OpenAIAPIKey })
	// 端点/密钥现在只来自数据库，所以测试也得先把假上游写进设置里。
	h.aiBaseURL = aiServer.URL + "/v1"
	// polish_enabled 默认关：两阶段是**显式开启**的功能，老用例断言的都是
	// 「草稿原样送达」这条路径，润色用例自己 h.apply(t, {"polish_enabled":true})。
	h.apply(t, fmt.Sprintf(
		`{"openai_base_url":%q,"openai_api_key":"test-key","markdown_enabled":false,"humanize_level":"off","reply_footer_enabled":false,"polish_enabled":false}`,
		h.aiBaseURL))
	h.dis = h.build(sm)
	return h
}

// build wires a dispatcher around the given settings manager.
func (h *harness) build(sm *settings.Manager) *core.Dispatcher {
	d := core.New(core.Options{
		Settings: sm,
		Store:    h.st,
		AI:       h.client,
		Sender:   h.sender,
		Bus:      h.bus,
		Logger:   h.logger,
		Prompt:   "你是测试助手",
		QQ:       h.qqClient,
		Media:    h.media,
		Affect:   h.affect,
		Tools:    tools.Builtins(),
	})
	// 测试不需要随机的「不接话」，否则会出现偶发失败。
	d.SetSilenceRoll(func() float64 { return 1 })
	d.Start(context.Background())
	return d
}

// reloadSettings rebuilds the settings manager from the same store.
//
// Trigger/context mode are plain stored configuration now, so "having
// permission" is no longer a precondition for anything.
func (h *harness) reloadSettings() *settings.Manager {
	h.t.Helper()
	sm, err := settings.New(context.Background(), h.st, settings.Defaults())
	if err != nil {
		h.t.Fatalf("settings reload: %v", err)
	}
	return sm
}

func (h *harness) apply(t *testing.T, raw string) {
	t.Helper()
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &patch); err != nil {
		t.Fatalf("bad patch: %v", err)
	}
	if _, _, err := h.settings.Apply(context.Background(), patch); err != nil {
		t.Fatalf("apply %s: %v", raw, err)
	}
}

func (h *harness) recalls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.recalled))
	copy(out, h.recalled)
	return out
}

func (h *harness) sentUploads() []uploadedFile {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]uploadedFile, len(h.uploads))
	copy(out, h.uploads)
	return out
}

func (h *harness) failUploads(fail bool) {
	h.mu.Lock()
	h.uploadFail = fail
	h.mu.Unlock()
}

// hasScriptedTool reports whether the fake model still has a tool call queued.
func (h *harness) hasScriptedTool() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.aiToolScript) > 0
}

func (h *harness) sentMessages() []sentMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]sentMessage, len(h.sent))
	copy(out, h.sent)
	return out
}

type promptProbe struct{ text string }

func (p promptProbe) contains(want string) bool { return strings.Contains(p.text, want) }

// systemPrompt returns the system message of the most recent model request.
func (h *harness) systemPrompt(t *testing.T) promptProbe {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	msgs, _ := h.lastAIBody["messages"].([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] == "system" {
			text, _ := m["content"].(string)
			return promptProbe{text: text}
		}
	}
	return promptProbe{}
}

func (h *harness) waitForSent(t *testing.T, n int) []sentMessage {
	t.Helper()
	// 10s：人味档位会给回复加停顿（strong 上限 15s × scale），5s 在负载高时会误判。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.sentMessages(); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return h.sentMessages()
}

// polishMarker is how the fake provider recognises a stage-2 call: the polisher
// instruction is the only thing that makes a request "the polish one".
const polishMarker = "你是同一角色的润色器"

// joinGateMarkerText mirrors core's unexported 自主加入判断 marker. The fake
// provider routes on the system message, so no extra request state is needed.
const joinGateMarkerText = "【自主加入判断】"

// defaultJoinGateVerdict keeps the gate off unless a test asks it to join.
const defaultJoinGateVerdict = `{"topics":[{"id":"t1","title":"群聊","summary":"最近的群聊","message_ids":[]}],"current_topic_id":"t1","addressed_to":"unknown","action":"skip","confidence":0.1,"reason":"测试默认不加入"}`

func (h *harness) setJoinGateReply(reply string) {
	h.mu.Lock()
	h.joinGateReply = reply
	h.mu.Unlock()
}

func (h *harness) joinGateCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.joinGateCalls
}

// waitForJoinGateCalls waits for the asynchronous dispatcher to reach n calls.
func (h *harness) waitForJoinGateCalls(t *testing.T, n int) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.joinGateCallCount(); got >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return h.joinGateCallCount()
}

// joinDecisions lists the recorded decisions of one group (newest first).
func (h *harness) joinDecisions(t *testing.T, groupOpenID string) []store.JoinDecision {
	t.Helper()
	decisions, err := h.st.ListJoinDecisions(context.Background(), groupOpenID, 50)
	if err != nil {
		t.Fatalf("list join decisions: %v", err)
	}
	return decisions
}

// isPolishRequest reports whether the body is a stage-2 (polish) call.
// hasSystemText reports whether any system message contains marker.
func hasSystemText(body map[string]any, marker string) bool {
	msgs, _ := body["messages"].([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] != "system" {
			continue
		}
		text, _ := m["content"].(string)
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// writeJSONCompletion answers a non-streamed JSON-decision request.
func writeJSONCompletion(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	payload, _ := json.Marshal(map[string]any{
		"model": "test-model",
		"choices": []map[string]any{{
			"message":       map[string]string{"content": content},
			"finish_reason": "stop",
		}},
	})
	_, _ = w.Write(payload)
}

// hasImagePart reports whether a request carries multimodal image content.
func hasImagePart(body map[string]any) bool {
	msgs, _ := body["messages"].([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		parts, _ := m["content"].([]any)
		for _, p := range parts {
			part, _ := p.(map[string]any)
			if part["type"] == "image_url" {
				return true
			}
		}
	}
	return false
}

// isVisionAnalysisRequest recognises the standalone multimodal calls made by the
// view_image / analyze_image tools (one user message, no system prompt, no
// tools). The main turn carries a system prompt and tool defs, even when it
// also has image parts.
func isVisionAnalysisRequest(body map[string]any) bool {
	if _, hasTools := body["tools"]; hasTools {
		return false
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		return false
	}
	m, _ := msgs[0].(map[string]any)
	if m["role"] != "user" {
		return false
	}
	return hasImagePart(body)
}

// hasToolMessage reports whether this is the round after a tool execution.
func hasToolMessage(body map[string]any) bool {
	msgs, _ := body["messages"].([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] == "tool" {
			return true
		}
	}
	return false
}

func isPolishRequest(body map[string]any) bool {
	msgs, _ := body["messages"].([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] != "system" {
			continue
		}
		text, _ := m["content"].(string)
		return strings.Contains(text, polishMarker)
	}
	return false
}

// servePolish answers a stage-2 call according to the harness knobs.
func (h *harness) servePolish(w http.ResponseWriter, body map[string]any, streaming bool) {
	h.mu.Lock()
	h.polishCalls = append(h.polishCalls, body)
	reply := h.polishReply
	status := h.polishStatus
	empty := h.polishEmpty
	delay := h.polishDelay
	h.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if status != 0 {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"polish upstream is down"}}`))
		return
	}
	if reply == "" {
		reply = "润色后的回复。"
	}
	if empty {
		reply = ""
		// An empty streamed completion is exactly what a flaky gateway sends;
		// the client also retries non-streamed once, so keep both bodies blank.
		if streaming {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"polish-model","choices":[{"message":{"content":""},"finish_reason":"stop"}]}`))
		return
	}
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(map[string]any{
			"model":   "polish-model",
			"choices": []map[string]any{{"delta": map[string]string{"content": reply}, "finish_reason": "stop"}},
			"usage":   map[string]int{"total_tokens": 5},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	payload, _ := json.Marshal(map[string]any{
		"model": "polish-model",
		"choices": []map[string]any{{
			"message":       map[string]string{"content": reply},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"total_tokens": 5},
	})
	_, _ = w.Write(payload)
}

// polishCallCount returns how many stage-2 calls the fake upstream saw.
func (h *harness) polishCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.polishCalls)
}

// polishMessages returns the messages of the n-th stage-2 request (0-based).
func (h *harness) polishMessages(n int) []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n >= len(h.polishCalls) {
		return nil
	}
	out := []map[string]any{}
	msgs, _ := h.polishCalls[n]["messages"].([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		copied := map[string]any{}
		for k, v := range m {
			copied[k] = v
		}
		out = append(out, copied)
	}
	return out
}

// assistanthistory returns the stored assistant messages of a conversation.
func (h *harness) assistantHistory(t *testing.T, convID string) []string {
	t.Helper()
	msgs, err := h.st.RecentMessages(context.Background(), convID, 20, false)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	out := []string{}
	for _, m := range msgs {
		if m.Role == "assistant" {
			out = append(out, m.Content)
		}
	}
	return out
}

// waitForRecords waits until the audit table holds at least n outbound rows.
// The send and the audit insert are not atomic, so a test that asserts on the
// record right after the message would otherwise race the insert.
func (h *harness) waitForRecords(t *testing.T, n int) []store.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recs, _, err := h.st.ListRecords(context.Background(),
			store.RecordFilter{Direction: "out", Page: 1, PageSize: 20})
		if err != nil {
			t.Fatalf("records: %v", err)
		}
		if len(recs) >= n || time.Now().After(deadline) {
			return recs
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// groupAtMessage builds an E1 (explicit @) event payload.
func groupAtMessage(msgID, groupOpenID, memberOpenID, name, content string) *qq.Payload {
	d, _ := json.Marshal(map[string]any{
		"id":           msgID,
		"content":      content,
		"group_openid": groupOpenID,
		"timestamp":    "2026-07-21T10:00:00+08:00",
		"message_type": 0,
		"message_scene": map[string]any{
			"source": "default",
			// 真实事件里每条消息都带自己的引用索引；引用回复要用它。
			"ext": []string{"msg_idx=REFIDX_" + msgID},
		},
		"author": map[string]any{
			"id":            memberOpenID,
			"member_openid": memberOpenID,
			"username":      name,
			"bot":           false,
		},
	})
	return &qq.Payload{Op: qq.OpDispatch, T: qq.EventGroupAtMessageCreate, ID: "EV-" + msgID, D: d}
}

// groupAnyMessage builds an E2 (full-group) event payload.
func groupAnyMessage(msgID, groupOpenID, memberOpenID, name, content string) *qq.Payload {
	p := groupAtMessage(msgID, groupOpenID, memberOpenID, name, content)
	p.T = qq.EventGroupMessageCreate
	return p
}

// c2cMessage builds a private-chat payload. A C2C chat has no group and no
// member_openid: the user_openid is the whole identity.
func c2cMessage(msgID, userOpenID, name, content string) *qq.Payload {
	d, _ := json.Marshal(map[string]any{
		"id":        msgID,
		"content":   content,
		"timestamp": "2026-07-21T10:00:00+08:00",
		"author": map[string]any{
			"id":          userOpenID,
			"user_openid": userOpenID,
			"username":    name,
			"bot":         false,
		},
	})
	return &qq.Payload{Op: qq.OpDispatch, T: qq.EventC2CMessageCreate, ID: "EV-" + msgID, D: d}
}

func TestReplyIsSentAsPassiveMessage(t *testing.T) {
	h := newHarness(t)

	h.dis.HandlePayload(context.Background(), groupAtMessage("M1", "G1", "U1", "小明", "你好"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected exactly one outbound message, got %d", len(sent))
	}
	msg := sent[0]
	if msg.Path != "/v2/groups/G1/messages" {
		t.Fatalf("unexpected path %q", msg.Path)
	}
	if msg.Body.Content != "这是测试回复。" {
		t.Fatalf("unexpected content %q", msg.Body.Content)
	}
	if msg.Body.MsgID != "M1" {
		t.Fatalf("reply must carry msg_id, got %q", msg.Body.MsgID)
	}
	if msg.Body.MsgSeq != 1 {
		t.Fatalf("first reply must use msg_seq 1, got %d", msg.Body.MsgSeq)
	}
	if msg.Body.MsgType != qq.MsgTypeText {
		t.Fatalf("msg_type = %d", msg.Body.MsgType)
	}

	convID := core.EncodeConversationID("group:G1:U1")
	msgs, err := h.st.RecentMessages(context.Background(), convID, 10, false)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("unexpected stored history %#v", msgs)
	}
}

func TestAffectInjectsBlockWhenEnabled(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"affect_enabled":true,"affect_pack":"daily","affect_inject_mode":"full"}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("M1", "G1", "U1", "小明", "你也太厉害了吧"))
	h.waitForSent(t, 1)

	probe := h.systemPrompt(t)
	if !strings.Contains(probe.text, "此刻的状态") {
		t.Fatalf("affect block missing from the system prompt:\n%s", probe.text)
	}
	if !strings.Contains(probe.text, "仅作语气参考") {
		t.Fatalf("affect block lost its guard line:\n%s", probe.text)
	}
}

func TestAffectStaysOffByDefault(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), groupAtMessage("M1", "G1", "U1", "小明", "你也太厉害了吧"))
	h.waitForSent(t, 1)
	if probe := h.systemPrompt(t); strings.Contains(probe.text, "此刻的状态") {
		t.Fatalf("affect must not inject while disabled:\n%s", probe.text)
	}
}

func TestDuplicatePushIsIgnored(t *testing.T) {
	h := newHarness(t)
	p := groupAtMessage("M-DUP", "G1", "U1", "小明", "你好")

	h.dis.HandlePayload(context.Background(), p)
	h.dis.HandlePayload(context.Background(), p)
	h.dis.HandlePayload(context.Background(), p)

	h.waitForSent(t, 1)
	time.Sleep(300 * time.Millisecond)

	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("duplicate pushes must be de-duplicated, got %d sends", got)
	}
	if n := h.dis.Metrics().EventsDeduped.Load(); n != 2 {
		t.Fatalf("dedup counter = %d, want 2", n)
	}
}

func TestE2IgnoredWithoutFullGroupPermission(t *testing.T) {
	h := newHarness(t)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("M2", "G1", "U1", "小明", "随便聊聊"))
	time.Sleep(300 * time.Millisecond)

	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("E2 must be ignored while at_only is in force, got %d sends", got)
	}
	if n := h.dis.Metrics().EventsIgnored.Load(); n == 0 {
		t.Fatal("expected the ignore counter to move")
	}
}

func TestE2RepliesWhenEnabledWithPermission(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	if _, _, err := sm.Apply(context.Background(), map[string]json.RawMessage{
		"trigger_mode": json.RawMessage(`"all_messages"`),
	}); err != nil {
		t.Fatalf("apply E2: %v", err)
	}
	h.dis = h.build(sm)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("M3", "G1", "U1", "小明", "大家好"))
	if sent := h.waitForSent(t, 1); len(sent) != 1 {
		t.Fatalf("E2 should reply once permission is granted, got %d", len(sent))
	}
}

func TestSharedContextSeesOtherMembers(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	if _, _, err := sm.Apply(context.Background(), map[string]json.RawMessage{
		"context_mode": json.RawMessage(`"shared"`),
	}); err != nil {
		t.Fatalf("apply C2: %v", err)
	}
	h.dis = h.build(sm)

	h.dis.HandlePayload(context.Background(), groupAtMessage("S1", "G1", "U1", "小明", "第一个问题"))
	h.waitForSent(t, 1)
	h.dis.HandlePayload(context.Background(), groupAtMessage("S2", "G1", "U2", "小红", "第二个问题"))
	h.waitForSent(t, 2)

	shared := core.EncodeConversationID("group-shared:G1")
	msgs, err := h.st.RecentMessages(context.Background(), shared, 20, false)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	var userTurns []string
	for _, m := range msgs {
		if m.Role == "user" {
			userTurns = append(userTurns, m.Content)
		}
	}
	if len(userTurns) != 2 {
		t.Fatalf("expected two shared user turns, got %#v", userTurns)
	}
	if !strings.Contains(userTurns[0], "小明") || !strings.Contains(userTurns[1], "小红") {
		t.Fatalf("shared turns should be prefixed with the speaker: %#v", userTurns)
	}

	perMember := core.EncodeConversationID("group:G1:U1")
	if m, _ := h.st.RecentMessages(context.Background(), perMember, 10, false); len(m) != 0 {
		t.Fatalf("C2 must not write per-member conversations, got %d rows", len(m))
	}
}

func TestLongReplyIsSegmented(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiReply = strings.Repeat("这是一段很长的回答。", 400)
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("L1", "G1", "U1", "小明", "讲讲"))

	sent := h.waitForSent(t, 2)
	if len(sent) < 2 {
		t.Fatalf("expected the reply to be split, got %d segment(s)", len(sent))
	}
	if len(sent) > 3 {
		t.Fatalf("reply_max_segments is 3, got %d", len(sent))
	}
	for i, m := range sent {
		if m.Body.MsgSeq != i+1 {
			t.Fatalf("segment %d used msg_seq %d", i, m.Body.MsgSeq)
		}
		if n := len([]rune(m.Body.Content)); n > 1200 {
			t.Fatalf("segment %d is %d runes, over the limit", i, n)
		}
	}
}

func TestModelFailureSendsFallbackNotice(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiFail = true
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("F1", "G1", "U1", "小明", "在吗"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected one fallback notice, got %d", len(sent))
	}
	if !strings.Contains(sent[0].Body.Content, "模型服务异常（HTTP 500）") {
		t.Fatalf("fallback should carry the sanitized upstream status, got %q", sent[0].Body.Content)
	}
	if strings.Contains(sent[0].Body.Content, "boom") {
		t.Fatalf("provider message should be summarized without raw payload: %q", sent[0].Body.Content)
	}
	if n := h.dis.Metrics().AIErrors.Load(); n != 1 {
		t.Fatalf("ai error counter = %d", n)
	}
	if h.dis.LastAIError() == "" {
		t.Fatal("the last AI error should be recorded for the admin panel")
	}

	recs, _, err := h.st.ListRecords(context.Background(), store.RecordFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	var sawFailed bool
	for _, r := range recs {
		if r.Status == "failed" {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Fatalf("expected a failed record, got %#v", recs)
	}
}

func TestModelErrorPassthroughIsSanitized(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiFail = true
	h.aiFailStatus = http.StatusBadRequest
	h.aiFailMessage = "invalid model: demo-model; endpoint https://api.internal.example/v1"
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("F2", "G1", "U1", "小明", "在吗"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected one error notice, got %d", len(sent))
	}
	body := sent[0].Body.Content
	for _, want := range []string{"模型调用失败（HTTP 400）", "invalid model"} {
		if !strings.Contains(body, want) {
			t.Fatalf("notice %q missing %q", body, want)
		}
	}
	for _, deny := range []string{"api.internal.example", "https://"} {
		if strings.Contains(body, deny) {
			t.Fatalf("notice %q leaks %q", body, deny)
		}
	}
}

func TestExpiredPassiveWindowFallsBackToActiveMessage(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.qqStatus = http.StatusBadRequest
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("E1MSG", "G1", "U1", "小明", "你好"))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.sentMessages()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sent := h.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("expected a passive attempt plus an active fallback, got %d", len(sent))
	}
	if sent[1].Body.MsgID != "" {
		t.Fatalf("the fallback must be an active message, got msg_id %q", sent[1].Body.MsgID)
	}
}

func TestUserRateLimitStopsReply(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"rate_limit_user_qpm":1}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("R1", "G1", "U1", "小明", "第一句"))
	h.waitForSent(t, 1)
	h.dis.HandlePayload(context.Background(), groupAtMessage("R2", "G1", "U1", "小明", "第二句"))
	time.Sleep(400 * time.Millisecond)

	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("the second message should be rate limited, got %d sends", got)
	}
}

func TestAllowlistBlocksUnknownGroup(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"group_allowlist":["G-ALLOWED"]}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("A1", "G-OTHER", "U1", "小明", "你好"))
	time.Sleep(300 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("a group outside the allowlist must be ignored, got %d sends", got)
	}

	h.dis.HandlePayload(context.Background(), groupAtMessage("A2", "G-ALLOWED", "U1", "小明", "你好"))
	if got := len(h.waitForSent(t, 1)); got != 1 {
		t.Fatalf("the allowlisted group should be answered, got %d sends", got)
	}
}

func TestBlockedKeywordIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"blocked_keywords":["广告"]}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("K1", "G1", "U1", "小明", "这是广告内容"))
	time.Sleep(300 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("a blocked keyword must be ignored, got %d sends", got)
	}
}

// 平台把「其他机器人 @ 我们」的群消息按 AT 事件推送时，默认应当回复
// （reply_to_bots=true）；未 @ 我们的机器人发言另有用例覆盖，永不回复。
func TestBotAtEventIsAnsweredByDefault(t *testing.T) {
	h := newHarness(t)
	withBotState(t, h, "G1", ourBotOpenID)

	d, _ := json.Marshal(map[string]any{
		"id":           "BOT1",
		"content":      "我是别的机器人，@你一下",
		"group_openid": "G1",
		"author":       map[string]any{"id": otherBotOpenID, "member_openid": otherBotOpenID, "username": "另一个机器人", "bot": true},
	})
	h.dis.HandlePayload(context.Background(), &qq.Payload{Op: qq.OpDispatch, T: qq.EventGroupAtMessageCreate, D: d})

	if got := len(h.waitForSent(t, 1)); got != 1 {
		t.Fatalf("其他机器人 @ 我们时应回复，实际发送 %d 条", got)
	}
}

/* ------------------------------------------------------------------ *
 * 提前回复（thinking notice）与回复页脚
 * ------------------------------------------------------------------ */

func TestReplyFooterCarriesTimeAndTokens(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"reply_footer_enabled":true}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("FT1", "G1", "U1", "小明", "你好"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected one reply, got %d", len(sent))
	}
	content := sent[0].Body.Content
	if !strings.Contains(content, "用时") || !strings.Contains(content, "tokens") {
		t.Fatalf("footer missing from the delivered text: %q", content)
	}
	if !strings.HasPrefix(content, "这是测试回复。") {
		t.Fatalf("the model answer must come first: %q", content)
	}
	// A passive reply is still the default path.
	if sent[0].Body.MsgID != "FT1" {
		t.Fatalf("expected a passive reply, got msg_id %q", sent[0].Body.MsgID)
	}

	// The footer is presentation only: it must not leak into the context.
	convID := core.EncodeConversationID("group:G1:U1")
	msgs, err := h.st.RecentMessages(context.Background(), convID, 10, false)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 stored messages, got %d", len(msgs))
	}
	if strings.Contains(msgs[1].Content, "用时") {
		t.Fatalf("stored assistant text must stay free of the footer: %q", msgs[1].Content)
	}
}

func TestSlowModelTriggersEarlyReplyThenActiveAnswer(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiDelay = 1500 * time.Millisecond
	h.mu.Unlock()
	h.apply(t, `{"early_reply_enabled":true,"early_reply_delay_seconds":1,"reply_footer_enabled":false}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("SLOW1", "G1", "U1", "小明", "解释一下相对论"))

	sent := h.waitForSent(t, 2)
	if len(sent) != 2 {
		t.Fatalf("expected an early notice plus the answer, got %d sends", len(sent))
	}

	early := sent[0]
	if early.Body.MsgID != "SLOW1" || early.Body.MsgSeq != 1 {
		t.Fatalf("the early notice must be a passive reply: msg_id=%q msg_seq=%d", early.Body.MsgID, early.Body.MsgSeq)
	}
	if !strings.Contains(early.Body.Content, "多想") {
		t.Fatalf("unexpected early notice text %q", early.Body.Content)
	}
	if strings.Contains(early.Body.Content, "tokens") {
		t.Fatal("the early notice must not carry the reply footer")
	}

	answer := sent[1]
	if answer.Body.MsgID != "" {
		t.Fatalf("the real answer must be an active message, got msg_id %q", answer.Body.MsgID)
	}
	if !strings.HasPrefix(answer.Body.Content, "这是测试回复。") {
		t.Fatalf("unexpected answer text %q", answer.Body.Content)
	}
	if n := h.dis.Metrics().EarlyRepliesSent.Load(); n != 1 {
		t.Fatalf("early reply counter = %d, want 1", n)
	}
}

func TestFastModelSkipsTheEarlyNotice(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"early_reply_enabled":true,"early_reply_delay_seconds":5}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("FAST1", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)
	time.Sleep(400 * time.Millisecond)

	sent := h.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("a fast answer must not trigger the notice, got %d sends", len(sent))
	}
	if sent[0].Body.MsgID != "FAST1" {
		t.Fatalf("the answer should stay passive, got msg_id %q", sent[0].Body.MsgID)
	}
	if n := h.dis.Metrics().EarlyRepliesSent.Load(); n != 0 {
		t.Fatalf("early reply counter = %d, want 0", n)
	}
}

func TestEarlyNoticeDisabledByDefaultInHarness(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiDelay = 600 * time.Millisecond
	h.mu.Unlock()

	// No patch applied: the zero-value config keeps both features off.
	h.dis.HandlePayload(context.Background(), groupAtMessage("OFF1", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)
	time.Sleep(200 * time.Millisecond)

	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("features should be off by default, got %d sends", got)
	}
	if strings.Contains(h.sentMessages()[0].Body.Content, "用时") {
		t.Fatal("the footer should be off by default")
	}
}

/* ------------------------------------------------------------------ *
 * 全量模式下的 @ 识别（平台开启「接收所有消息」后不再推送 AT 事件）
 * ------------------------------------------------------------------ */

func TestFullGroupMessageWithBotMentionIsAnswered(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	// 必须知道「我们在这个群里的 openid」，否则无法区分是不是 @ 的自己。
	withBotState(t, h, "G1", ourBotOpenID)

	// trigger_mode 仍是 at_only（默认），事件类型是 GROUP_MESSAGE_CREATE。
	p := groupAnyMessage("FM1", "G1", "U1", "小明", "帮我算一下 12*8")
	withMentions(t, p, []map[string]any{
		{"id": ourBotOpenID, "member_openid": ourBotOpenID, "username": "Kaku", "bot": true},
	})

	h.dis.HandlePayload(context.Background(), p)

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("被 @ 的全量消息应当回复，实际发送 %d 条", len(sent))
	}
	if sent[0].Body.MsgID != "FM1" {
		t.Fatalf("应为被动回复，got msg_id=%q", sent[0].Body.MsgID)
	}
}

func TestFullGroupMessageWithoutMentionIsIgnoredButRecorded(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)

	// 普通群聊（没有 @），at_only 下不应回复，但必须留下可解释的记录。
	h.dis.HandlePayload(context.Background(), groupAnyMessage("FM2", "G1", "U2", "小红", "今天天气不错"))

	time.Sleep(400 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("未 @ 的群消息不应回复，实际发送 %d 条", got)
	}

	recs, _, err := h.st.ListRecords(context.Background(), store.RecordFilter{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	var reason string
	for _, r := range recs {
		if r.Direction == "in" && r.MsgID == "FM2" {
			if r.Status != "ignored" {
				t.Fatalf("状态应为 ignored，实际 %q", r.Status)
			}
			reason = r.Error
		}
	}
	if reason == "" {
		t.Fatal("被忽略的群消息必须在审计记录里留下原因")
	}
	if !strings.Contains(reason, "at_only") || !strings.Contains(reason, "bot_mentions=0") {
		t.Fatalf("忽略原因应说明触发策略与 @ 情况，实际 %q", reason)
	}
}

func TestFullGroupDetectionFlag(t *testing.T) {
	h := newHarness(t)
	if h.dis.FullGroupDetected() {
		t.Fatal("初始不应认为平台推送过全量消息")
	}
	h.dis.HandlePayload(context.Background(), groupAnyMessage("FM3", "G1", "U3", "小刚", "在吗"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !h.dis.FullGroupDetected() {
		time.Sleep(20 * time.Millisecond)
	}
	if !h.dis.FullGroupDetected() {
		t.Fatal("收到 GROUP_MESSAGE_CREATE 后应标记为已检测到全量权限")
	}
}

// withMentions rewrites the mentions array of a fabricated event.
func withMentions(t *testing.T, p *qq.Payload, mentions []map[string]any) {
	if t != nil {
		t.Helper()
	}
	var body map[string]any
	if err := json.Unmarshal(p.D, &body); err != nil {
		if t != nil {
			t.Fatalf("decode payload: %v", err)
		}
		return
	}
	body["mentions"] = mentions
	raw, err := json.Marshal(body)
	if err != nil {
		if t != nil {
			t.Fatalf("encode payload: %v", err)
		}
		return
	}
	p.D = raw
}

/* ------------------------------------------------------------------ *
 * 未 @ 机器人的群消息：进会话时间线，但不进模型上下文
 * ------------------------------------------------------------------ */

func TestUnaddressedGroupMessageEntersTimelineNotContext(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)

	// 1) 先来一条 @机器人的消息，形成一轮真实对话
	at := groupAtMessage("TL1", "G1", "U1", "小明", "你好")
	h.dis.HandlePayload(context.Background(), at)
	h.waitForSent(t, 1)

	// 2) 再来一条同一成员未 @ 的消息
	h.dis.HandlePayload(context.Background(), groupAnyMessage("TL2", "G1", "U1", "小明", "顺便说一句，今天降温了"))
	time.Sleep(300 * time.Millisecond)

	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("未 @ 的消息不应触发回复，实际发送 %d 条", got)
	}

	convID := core.EncodeConversationID("group:G1:U1")
	ctxBg := context.Background()

	timeline, err := h.st.Messages(ctxBg, convID, 50, 0)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	var observed *store.Message
	for i := range timeline {
		if timeline[i].ID > 0 && timeline[i].Kind == store.KindObserved {
			observed = &timeline[i]
		}
	}
	if observed == nil {
		t.Fatalf("未 @ 的消息应出现在会话时间线里，实际 %#v", timeline)
	}
	if observed.Speaker != "小明" {
		t.Fatalf("观察消息应带发送者昵称，实际 %q", observed.Speaker)
	}
	if observed.Content != "顺便说一句，今天降温了" {
		t.Fatalf("观察消息内容不对: %q", observed.Content)
	}

	// 模型上下文只看得到对话轮次
	history, err := h.st.RecentMessages(ctxBg, convID, 50, false)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	for _, m := range history {
		if m.Kind == store.KindObserved {
			t.Fatalf("观察消息绝不能进入模型上下文: %#v", m)
		}
	}
	if len(history) != 2 {
		t.Fatalf("模型上下文应只有 2 条轮次，实际 %d", len(history))
	}

	// 计数：消息数增加，但轮次不增加
	conv, err := h.st.GetConversation(ctxBg, convID)
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if conv.TurnCount != 2 {
		t.Fatalf("轮次数应保持为 2，实际 %d", conv.TurnCount)
	}
	if conv.MessageCount != 3 {
		t.Fatalf("消息数应为 3（含 1 条观察），实际 %d", conv.MessageCount)
	}
	if n := h.dis.Metrics().MessagesObserved.Load(); n != 1 {
		t.Fatalf("观察计数应为 1，实际 %d", n)
	}
}

func TestUnaddressedMessageIsScopedToItsSenderInC1(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)

	// C1：小红未 @ 的消息应落在小红自己的会话里，不会串进小明的视图
	h.dis.HandlePayload(context.Background(), groupAnyMessage("SC1", "G1", "U2", "小红", "大家好呀"))
	time.Sleep(300 * time.Millisecond)

	xiaoHong := core.EncodeConversationID("group:G1:U2")
	xiaoMing := core.EncodeConversationID("group:G1:U1")

	msgs, err := h.st.Messages(context.Background(), xiaoHong, 50, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Speaker != "小红" {
		t.Fatalf("小红的会话应有 1 条她自己的观察消息，实际 %#v", msgs)
	}
	if other, _ := h.st.Messages(context.Background(), xiaoMing, 50, 0); len(other) != 0 {
		t.Fatalf("小明的会话不应被别人的消息污染，实际 %#v", other)
	}
}

func TestSpeakerIsStoredForDialogueTurns(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), groupAtMessage("SP1", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)

	convID := core.EncodeConversationID("group:G1:U1")
	msgs, err := h.st.RecentMessages(context.Background(), convID, 10, false)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(msgs))
	}
	if msgs[0].Speaker != "小明" {
		t.Fatalf("用户轮次应记录昵称，实际 %q", msgs[0].Speaker)
	}
	if msgs[0].Kind != store.KindDialogue {
		t.Fatalf("轮次 kind 应为 dialogue，实际 %q", msgs[0].Kind)
	}
	if msgs[1].Speaker != "" {
		t.Fatalf("助手轮次不应带昵称，实际 %q", msgs[1].Speaker)
	}
}

// 后台时间线要能显示「到底是谁说的」：昵称会重复，openid 不会。
func TestSpeakerOpenIDIsStoredForEveryRow(t *testing.T) {
	h := newHarness(t)
	withBotState(t, h, "G1", ourBotOpenID)
	if _, ok := h.dis.Identity().GroupState(context.Background(), "G1"); !ok {
		t.Fatal("机器人自身群内身份未就绪，测试前置条件不成立")
	}

	h.dis.HandlePayload(context.Background(), groupAtMessage("SO1", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)

	convID := core.EncodeConversationID("group:G1:U1")
	msgs, err := h.st.RecentMessages(context.Background(), convID, 10, false)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(msgs))
	}
	if msgs[0].SpeakerOpenID != "U1" {
		t.Fatalf("用户轮次应记录发言人 openid，实际 %q", msgs[0].SpeakerOpenID)
	}
	if msgs[1].SpeakerOpenID != ourBotOpenID {
		t.Fatalf("助手轮次应记录机器人群内 openid，实际 %q", msgs[1].SpeakerOpenID)
	}
}

// C2 下会话本身没有 member_openid，审计记录必须改成写「发送者」，
// 否则后台看不出共享会话里的每条消息是谁发的。
func TestAuditRecordsKeepTheSenderOpenIDInSharedContext(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"context_mode":"shared","trigger_mode":"all_messages"}`)
	h.dis = h.build(h.reloadSettings())

	h.dis.HandlePayload(context.Background(), groupAtMessage("AU1", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)
	time.Sleep(200 * time.Millisecond)

	recs, _, err := h.st.ListRecords(context.Background(), store.RecordFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	var sawIn, sawOut bool
	for _, r := range recs {
		if r.Direction == "in" && r.Content == "你好" {
			sawIn = true
			if r.MemberOpenID != "U1" {
				t.Fatalf("入站审计记录应写明发送者 openid，实际 %q（会话 owner=%q）", r.MemberOpenID, r.ConversationID)
			}
		}
		if r.Direction == "out" {
			sawOut = true
			if r.MemberOpenID != "U1" {
				t.Fatalf("出站审计记录应写明应答对象 openid，实际 %q", r.MemberOpenID)
			}
		}
	}
	if !sawIn || !sawOut {
		t.Fatalf("审计记录缺失（in=%v out=%v）", sawIn, sawOut)
	}
}

// 同一个群里两个人都叫「小王」时，只有 openid 能把他们分开。
func TestSharedTimelineDisambiguatesMembersWithTheSameName(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"context_mode":"shared","trigger_mode":"at_only"}`)
	h.dis = h.build(h.reloadSettings())

	h.dis.HandlePayload(context.Background(), groupAnyMessage("SD1", "G1", "U1", "小王", "今天降温了"))
	h.dis.HandlePayload(context.Background(), groupAnyMessage("SD2", "G1", "U2", "小王", "我这儿还好"))
	time.Sleep(300 * time.Millisecond)

	convID := core.EncodeConversationID("group-shared:G1")
	msgs, err := h.st.Messages(context.Background(), convID, 50, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("共享会话应有 2 条观察消息，实际 %#v", msgs)
	}
	for i, want := range []string{"U1", "U2"} {
		if msgs[i].Speaker != "小王" {
			t.Fatalf("第 %d 条昵称应为小王，实际 %q", i, msgs[i].Speaker)
		}
		if msgs[i].SpeakerOpenID != want {
			t.Fatalf("第 %d 条发言人 openid 应为 %s，实际 %q", i, want, msgs[i].SpeakerOpenID)
		}
	}
}

// 模型只产出思维链时，群里绝不能看到思维链本身。
func TestReasoningIsNeverSentToTheGroup(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiReasoningOnly = true
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("RC1", "G1", "U1", "小明", "帮我分析一下"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected exactly one outbound message, got %d", len(sent))
	}
	if strings.Contains(sent[0].Body.Content, "思维链") {
		t.Fatalf("思维链被发出去了: %q", sent[0].Body.Content)
	}
	if !strings.Contains(sent[0].Body.Content, "没理清") {
		t.Fatalf("应回复一句得体的兜底话术，实际 %q", sent[0].Body.Content)
	}

	convID := core.EncodeConversationID("group:G1:U1")
	msgs, err := h.st.Messages(context.Background(), convID, 20, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "思维链") {
			t.Fatalf("思维链落进了会话上下文: %#v", m)
		}
	}
	if len(msgs) != 0 {
		t.Fatalf("没有产出答案的一轮不应写入上下文，实际 %#v", msgs)
	}
}

/* ------------------------------------------------------------------ *
 * 会话详情的实时刷新依赖：SSE 的 message 事件必须带 conversation_id
 * ------------------------------------------------------------------ */

func TestMessageEventsCarryConversationID(t *testing.T) {
	h := newHarness(t)
	events, cancel := h.bus.Subscribe(128)
	defer cancel()

	convID := core.EncodeConversationID("group:G1:U1")
	h.dis.HandlePayload(context.Background(), groupAtMessage("SSE1", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)

	deadline := time.Now().Add(3 * time.Second)
	var seen []map[string]any
	for time.Now().Before(deadline) {
		select {
		case ev := <-events:
			if ev.Name != "message" {
				continue
			}
			raw, err := json.Marshal(ev.Data)
			if err != nil {
				t.Fatalf("marshal event: %v", err)
			}
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}
			seen = append(seen, payload)
			if len(seen) >= 3 {
				goto done
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
done:
	if len(seen) == 0 {
		t.Fatal("分发消息后必须发布 message 事件，否则会话详情无法实时刷新")
	}

	var inSeen, outSeen bool
	for _, p := range seen {
		if p["conversation_id"] != convID {
			t.Fatalf("message 事件必须带本会话的 conversation_id，实际 %v", p["conversation_id"])
		}
		if p["direction"] == "in" {
			inSeen = true
		}
		if p["direction"] == "out" {
			outSeen = true
		}
	}
	if !inSeen || !outSeen {
		t.Fatalf("收发两个方向都应推送事件，实际 in=%v out=%v", inSeen, outSeen)
	}
}

func TestObservedMessageAlsoPublishesEvent(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)

	events, cancel := h.bus.Subscribe(64)
	defer cancel()

	convID := core.EncodeConversationID("group:G1:U2")
	h.dis.HandlePayload(context.Background(), groupAnyMessage("SSE2", "G1", "U2", "小红", "随便聊聊"))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-events:
			if ev.Name != "message" {
				continue
			}
			raw, _ := json.Marshal(ev.Data)
			var p map[string]any
			_ = json.Unmarshal(raw, &p)
			if p["conversation_id"] == convID {
				return
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("未 @ 的群消息也应推送 message 事件，否则它不会实时出现在会话详情里")
}

/* ------------------------------------------------------------------ *
 * 群内存在多个机器人时的兼容
 * ------------------------------------------------------------------ */

const (
	ourBotOpenID   = "AE5600BB1B22414E9774507729B2A288"
	otherBotOpenID = "AAAABBBBCCCCDDDDEEEEFFFF00001111"
)

// withBotState makes the fake QQ API answer /v2/groups/{id}/bot_state, i.e. it
// tells the bot which openid is its own inside that group.
func withBotState(t *testing.T, h *harness, groupOpenID, memberOpenID string) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.botStates == nil {
		h.botStates = map[string]string{}
	}
	h.botStates[groupOpenID] = memberOpenID
}

func botMessage(msgID, groupOpenID, memberOpenID, name, content string) *qq.Payload {
	d, _ := json.Marshal(map[string]any{
		"id":           msgID,
		"content":      content,
		"group_openid": groupOpenID,
		"author": map[string]any{
			"id":            memberOpenID,
			"member_openid": memberOpenID,
			"username":      name,
			"bot":           true,
		},
	})
	return &qq.Payload{Op: qq.OpDispatch, T: qq.EventGroupMessageCreate, ID: "EV-" + msgID, D: d}
}

func setBotSetting(t *testing.T, h *harness, groupOpenID, setting string) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.botSetting == nil {
		h.botSetting = map[string]string{}
	}
	h.botSetting[groupOpenID] = setting
}

// TestPlatformPermissionChangeIsPickedUp 覆盖「在 QQ 群里改了机器人权限，后台要能及时识别」。
func TestPlatformPermissionChangeIsPickedUp(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)
	setBotSetting(t, h, "G1", "only_mention")

	ctx := context.Background()
	if _, ok := h.dis.Identity().Refresh(ctx, "G1"); !ok {
		t.Fatal("应能取到群内机器人状态")
	}
	snap := h.dis.Identity().GroupSnapshot("G1")
	if snap == nil || snap.RecvMsgSetting != "only_mention" {
		t.Fatalf("首次读取应为 only_mention，实际 %#v", snap)
	}
	if h.dis.FullGroupDetected() {
		t.Fatal("平台侧只有 @机器人 时不应标记为已开启全量")
	}

	// 群管理员把权限改成「接收所有消息」
	setBotSetting(t, h, "G1", "all")
	if _, ok := h.dis.Identity().Refresh(ctx, "G1"); !ok {
		t.Fatal("重新读取失败")
	}
	snap = h.dis.Identity().GroupSnapshot("G1")
	if snap == nil || snap.RecvMsgSetting != "all" {
		t.Fatalf("刷新后应看到 all，实际 %#v", snap)
	}
	if !h.dis.FullGroupDetected() {
		t.Fatal("平台侧已开放全量后，应据此置位 full_group_detected")
	}
}

func TestMentioningAnotherBotDoesNotTriggerUs(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)

	p := groupAnyMessage("MB1", "G1", "U1", "小明", "帮我看看这个")
	withMentions(t, p, []map[string]any{
		{"id": otherBotOpenID, "member_openid": otherBotOpenID, "username": "另一个机器人", "bot": true},
	})
	h.dis.HandlePayload(context.Background(), p)
	time.Sleep(300 * time.Millisecond)

	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("@ 另一个机器人时我们不应回复，实际发送 %d 条", got)
	}
}

func TestMentioningOurselvesStillTriggers(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)

	p := groupAnyMessage("MB2", "G1", "U1", "小明", "你好")
	withMentions(t, p, []map[string]any{
		{"id": ourBotOpenID, "member_openid": ourBotOpenID, "username": "Kaku", "bot": true},
	})
	h.dis.HandlePayload(context.Background(), p)

	if got := len(h.waitForSent(t, 1)); got != 1 {
		t.Fatalf("@ 我们自己时应当回复，实际发送 %d 条", got)
	}
}

func TestMentioningBothBotsStillReplies(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)

	p := groupAnyMessage("MB3", "G1", "U1", "小明", "你们俩都看看")
	withMentions(t, p, []map[string]any{
		{"id": otherBotOpenID, "member_openid": otherBotOpenID, "username": "另一个机器人", "bot": true},
		{"id": ourBotOpenID, "member_openid": ourBotOpenID, "username": "Kaku", "bot": true},
	})
	h.dis.HandlePayload(context.Background(), p)

	if got := len(h.waitForSent(t, 1)); got != 1 {
		t.Fatalf("同时 @ 两个机器人时我们应当回复，实际发送 %d 条", got)
	}
}

func TestAnotherBotsMessageIsRecordedButNotAnswered(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)

	// 另一个机器人的发言：即使触发策略是 all_messages 也不该被回复。
	if _, _, err := sm.Apply(context.Background(), map[string]json.RawMessage{
		"trigger_mode": json.RawMessage(`"all_messages"`),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)

	// 小明先把机器人拉进对话（建立会话），随后另一个机器人才发言
	h.dis.HandlePayload(context.Background(), groupAnyMessage("BOTMSG0", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)

	h.dis.HandlePayload(context.Background(), botMessage("BOTMSG1", "G1", otherBotOpenID, "另一个机器人", "我是另一个机器人"))
	time.Sleep(300 * time.Millisecond)

	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("不应回复其他机器人，实际发送 %d 条", got)
	}

	convID := core.EncodeConversationID("group:G1:U1")
	all, err := h.st.Messages(context.Background(), convID, 20, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	var found *store.Message
	for i := range all {
		if all[i].Kind == store.KindObservedBot {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatalf("其他机器人的发言应出现在会话时间线里，实际 %#v", all)
	}
	if found.Speaker != "另一个机器人" {
		t.Fatalf("应记录发言者昵称，实际 %q", found.Speaker)
	}

	// 绝不能进入模型上下文
	history, err := h.st.RecentMessages(context.Background(), convID, 20, false)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	for _, m := range history {
		if m.Kind == store.KindObservedBot {
			t.Fatal("其他机器人的发言不能进入模型上下文")
		}
	}
}

func TestOurOwnEchoedMessageIsDropped(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)

	// 触发策略设为 all_messages：若不能识别「这条是我自己发的」，就会自问自答成环。
	if _, _, err := sm.Apply(context.Background(), map[string]json.RawMessage{
		"trigger_mode": json.RawMessage(`"all_messages"`),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)

	h.dis.HandlePayload(context.Background(), botMessage("SELF1", "G1", ourBotOpenID, "Kaku", "这是我自己的回复"))
	time.Sleep(300 * time.Millisecond)

	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("自己的消息被回显时不能回复，否则会自我循环，实际发送 %d 条", got)
	}
	convID := core.EncodeConversationID("group:G1:" + ourBotOpenID)
	if msgs, _ := h.st.Messages(context.Background(), convID, 20, 0); len(msgs) != 0 {
		t.Fatalf("自己的回显不应写入时间线，实际 %#v", msgs)
	}
	if n := h.dis.Metrics().MessagesObserved.Load(); n != 0 {
		t.Fatalf("自己的回显不应计入观察数，实际 %d", n)
	}
}

func TestBotStateUnavailableFallsBackToUsername(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	// 不配置 bot_state（接口不可用，例如 11253 白名单限制），
	// 退化为用 /users/@me 的 username 比对。
	p := groupAnyMessage("FB1", "G1", "U1", "小明", "你好")
	withMentions(t, p, []map[string]any{
		{"id": "SOME-OPENID", "member_openid": "SOME-OPENID", "username": "Kaku", "bot": true},
	})
	h.dis.HandlePayload(context.Background(), p)

	if got := len(h.waitForSent(t, 1)); got != 1 {
		t.Fatalf("bot_state 不可用时应按用户名匹配并回复，实际发送 %d 条", got)
	}
}

func TestUnknownIdentityStaysSilentOnBotMentions(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	// 既没有 bot_state，用户名也对不上：宁可沉默，也不要因为「群里有个 bot 被 @ 了」而乱插话。
	p := groupAnyMessage("UNK1", "G1", "U1", "小明", "叫一下别的机器人")
	withMentions(t, p, []map[string]any{
		{"id": otherBotOpenID, "member_openid": otherBotOpenID, "username": "另一个机器人", "bot": true},
	})
	h.dis.HandlePayload(context.Background(), p)
	time.Sleep(300 * time.Millisecond)

	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("无法确认是否 @ 自己时不应回复，实际发送 %d 条", got)
	}
}

func TestHumanMentionNeverCountsAsOurselves(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)

	// 就算某个人类的 openid 恰好等于配置值，bot=false 也不应被当成「在 @ 我们」
	p := groupAnyMessage("HUM1", "G1", "U1", "小明", "看看这个")
	withMentions(t, p, []map[string]any{
		{"id": ourBotOpenID, "member_openid": ourBotOpenID, "username": "某人", "bot": false},
	})
	h.dis.HandlePayload(context.Background(), p)
	time.Sleep(300 * time.Millisecond)

	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("@ 人类不应触发我们，实际发送 %d 条", got)
	}
}

/* ------------------------------------------------------------------ *
 * 即时回复的撤回
 * ------------------------------------------------------------------ */

func TestEarlyNoticeIsRecalledAfterTheAnswerLands(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiDelay = 1500 * time.Millisecond
	h.mu.Unlock()
	h.apply(t, `{"early_reply_enabled":true,"early_reply_delay_seconds":1,"early_reply_recall":true}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("RC1", "G1", "U1", "小明", "解释一下相对论"))

	sent := h.waitForSent(t, 2)
	if len(sent) != 2 {
		t.Fatalf("应先发即时回复再发正式答复，实际 %d 条", len(sent))
	}
	// 等撤回调用发生
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.recalls()) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	recalls := h.recalls()
	if len(recalls) != 1 {
		t.Fatalf("正式答复送达后应撤回即时回复，实际撤回 %d 次", len(recalls))
	}
	if recalls[0] != "/v2/groups/G1/messages/MSG-OUT-1" {
		t.Fatalf("撤回的应是被动回复那条即时消息，实际 %q", recalls[0])
	}
	if n := h.dis.Metrics().EarlyRepliesRecalled.Load(); n != 1 {
		t.Fatalf("撤回计数应为 1，实际 %d", n)
	}
}

func TestEarlyNoticeIsKeptWhenRecallDisabled(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiDelay = 1500 * time.Millisecond
	h.mu.Unlock()
	h.apply(t, `{"early_reply_enabled":true,"early_reply_delay_seconds":1,"early_reply_recall":false}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("RC2", "G1", "U1", "小明", "解释一下量子力学"))
	h.waitForSent(t, 2)
	time.Sleep(500 * time.Millisecond)

	if got := len(h.recalls()); got != 0 {
		t.Fatalf("关闭撤回后不应调用撤回接口，实际 %d 次", got)
	}
}

func TestEarlyNoticeIsKeptWhenTheAnswerFails(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiDelay = 1500 * time.Millisecond
	h.aiFail = true
	h.mu.Unlock()
	h.apply(t, `{"early_reply_enabled":true,"early_reply_delay_seconds":1,"early_reply_recall":true}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("RC3", "G1", "U1", "小明", "这个问题很难"))
	h.waitForSent(t, 2)
	time.Sleep(400 * time.Millisecond)

	if got := len(h.recalls()); got != 0 {
		t.Fatalf("正式答复失败时应保留即时回复，实际撤回 %d 次", got)
	}
}

/* ------------------------------------------------------------------ *
 * 是否回复其他机器人的消息（reply_to_bots）
 * ------------------------------------------------------------------ */

func botMentioningUs(msgID, groupOpenID, content string) *qq.Payload {
	p := botMessage(msgID, groupOpenID, otherBotOpenID, "另一个机器人", content)
	withMentions(nil, p, []map[string]any{
		{"id": ourBotOpenID, "member_openid": ourBotOpenID, "username": "Kaku", "bot": true},
	})
	return p
}

func TestOtherBotMentioningUsIsAnsweredByDefault(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)

	h.dis.HandlePayload(context.Background(), botMentioningUs("BB1", "G1", "@Kaku 帮个忙"))

	if got := len(h.waitForSent(t, 1)); got != 1 {
		t.Fatalf("默认应回复其他机器人的 @，实际发送 %d 条", got)
	}
}

func TestOtherBotMentioningUsRespectsTheSetting(t *testing.T) {
	h := newHarness(t)
	withBotState(t, h, "G1", ourBotOpenID)
	// 先让群里有一个会话，否则「其他机器人的消息」没有时间线可落
	h.dis.HandlePayload(context.Background(), groupAtMessage("BB0", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)
	h.apply(t, `{"reply_to_bots":false}`)

	h.dis.HandlePayload(context.Background(), botMentioningUs("BB2", "G1", "@Kaku 帮个忙"))
	time.Sleep(300 * time.Millisecond)

	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("关闭 reply_to_bots 后不应回复，实际发送 %d 条", got)
	}
	// 但仍要记录进时间线
	found := false
	groups, _ := h.st.DistinctGroupOpenIDs(context.Background())
	for _, gid := range groups {
		for _, cid := range conversationIDsOfGroup(t, h, gid) {
			if msgs, _ := h.st.Messages(context.Background(), cid, 20, 0); len(msgs) > 0 {
				for _, m := range msgs {
					if m.Kind == store.KindObservedBot {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("关闭回复后，其他机器人的消息仍应记录在时间线里")
	}
}

func TestUnaddressedBotChatterIsNeverAnswered(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)
	// 即使是 all_messages，也绝不能回复「没有 @ 我们」的 bot 发言，否则两个 bot 会互相刷
	if _, _, err := sm.Apply(context.Background(), map[string]json.RawMessage{
		"trigger_mode": json.RawMessage(`"all_messages"`),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)

	h.dis.HandlePayload(context.Background(), botMessage("BB3", "G1", otherBotOpenID, "另一个机器人", "我来播报一下天气"))
	time.Sleep(300 * time.Millisecond)

	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("未 @ 我们的 bot 发言不应被回复，实际发送 %d 条", got)
	}
}

func TestBotChainIsBrokenAfterTooManyReplies(t *testing.T) {
	h := newHarness(t)
	sm := h.reloadSettings()
	h.dis = h.build(sm)
	withBotState(t, h, "G1", ourBotOpenID)

	// 另一个 bot 连续 @ 我们：达到上限后必须停下来，避免两个 bot 无限对刷
	for i := 0; i < core.BotChainLimit+3; i++ {
		h.dis.HandlePayload(context.Background(), botMentioningUs(
			fmt.Sprintf("CHAIN%d", i), "G1", "@Kaku 继续说"))
		h.waitForSent(t, i+1)
	}
	time.Sleep(400 * time.Millisecond)

	got := len(h.sentMessages())
	if got != core.BotChainLimit {
		t.Fatalf("连续回复应在上限 %d 处停止，实际 %d 条", core.BotChainLimit, got)
	}

	// 人类插话后链条重置，可以继续回复
	h.dis.HandlePayload(context.Background(), groupAtMessage("CHAIN-RESET", "G1", "U1", "小明", "别刷了"))
	h.waitForSent(t, core.BotChainLimit+1)
	h.dis.HandlePayload(context.Background(), botMentioningUs("CHAIN-AFTER", "G1", "@Kaku 那继续"))
	if got := len(h.waitForSent(t, core.BotChainLimit+2)); got != core.BotChainLimit+2 {
		t.Fatalf("人类插话后应恢复回复，实际 %d 条", got)
	}
}

func conversationIDsOfGroup(t *testing.T, h *harness, groupOpenID string) []string {
	t.Helper()
	ids, err := h.st.ConversationsForGroup(context.Background(), groupOpenID, 20)
	if err != nil {
		t.Fatalf("conversations: %v", err)
	}
	return ids
}

/* ------------------------------------------------------------------ *
 * 图片：缓存到本地（时间线可见）+ 作为多模态输入交给模型
 * ------------------------------------------------------------------ */

func tinyPNG() []byte {
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

// imageMessage builds a group @-message carrying one image attachment.
func imageMessageWithURL(msgID, groupOpenID, memberOpenID, name, imageURL string) *qq.Payload {
	d, _ := json.Marshal(map[string]any{
		"id":           msgID,
		"content":      "这张图里是什么？",
		"group_openid": groupOpenID,
		"author": map[string]any{
			"id": memberOpenID, "member_openid": memberOpenID, "username": name, "bot": false,
		},
		"attachments": []map[string]any{{
			"url": imageURL, "content_type": "image/png", "filename": "a.png", "width": 1, "height": 1, "size": 70,
		}},
	})
	return &qq.Payload{Op: qq.OpDispatch, T: qq.EventGroupAtMessageCreate, ID: "EV-" + msgID, D: d}
}

func TestImageIsCachedAndVisibleInTheTimeline(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), imageMessageWithURL("IMG1", "G1", "U1", "小明", h.imageURL))
	h.waitForSent(t, 1)

	convID := core.EncodeConversationID("group:G1:U1")
	msgs, err := h.st.Messages(context.Background(), convID, 20, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	var withImage *store.Message
	for i := range msgs {
		if len(msgs[i].Attachments) > 0 {
			withImage = &msgs[i]
		}
	}
	if withImage == nil {
		t.Fatalf("带图消息应记录附件，实际 %#v", msgs)
	}
	att := withImage.Attachments[0]
	if att.Kind != "image" || att.MediaID == "" {
		t.Fatalf("图片应被缓存到本地，实际 %#v", att)
	}
	if att.Width != 1 || att.Height != 1 || att.Filename != "a.png" {
		t.Fatalf("附件元数据应保留，实际 %#v", att)
	}
	// 缓存内容必须真的能取回来（后台 /api/media/{id} 就是靠它）
	contentType, data, err := h.media.Load(context.Background(), att.MediaID)
	if err != nil || len(data) == 0 {
		t.Fatalf("缓存读取失败: %v (%d bytes)", err, len(data))
	}
	if contentType != "image/png" {
		t.Fatalf("content type 不对: %s", contentType)
	}
}

func TestImageIsSentToTheModelAsMultimodalInput(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), imageMessageWithURL("IMG2", "G1", "U1", "小明", h.imageURL))
	h.waitForSent(t, 1)

	h.mu.Lock()
	body := h.lastAIBody
	h.mu.Unlock()
	if body == nil {
		t.Fatal("没有捕获到模型请求")
	}

	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("请求里没有消息")
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	parts, ok := last["content"].([]any)
	if !ok {
		t.Fatalf("带图的那一轮应是多模态 content 数组，实际 %#v", last["content"])
	}

	var sawImage, sawText bool
	for _, raw := range parts {
		part, _ := raw.(map[string]any)
		switch part["type"] {
		case "text":
			sawText = true
		case "image_url":
			sawImage = true
			u, _ := part["image_url"].(map[string]any)
			url, _ := u["url"].(string)
			if !strings.HasPrefix(url, "data:image/png;base64,") {
				t.Fatalf("图片应以 data URI 内联发送，实际 %q", url[:min(40, len(url))])
			}
		}
	}
	if !sawText || !sawImage {
		t.Fatalf("应同时包含文本与图片部分，实际 text=%v image=%v", sawText, sawImage)
	}
}

func TestHistoryDoesNotResendImages(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), imageMessageWithURL("IMG3", "G1", "U1", "小明", h.imageURL))
	h.waitForSent(t, 1)
	// 第二轮：纯文字追问，不应再带图（否则每轮都在重复消耗图片 token）
	h.dis.HandlePayload(context.Background(), groupAtMessage("IMG4", "G1", "U1", "小明", "刚才那张图呢"))
	h.waitForSent(t, 2)

	h.mu.Lock()
	body := h.lastAIBody
	h.mu.Unlock()
	msgs, _ := body["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if _, isArray := last["content"].([]any); isArray {
		t.Fatal("历史里的图片不应在后续轮次重复发送")
	}
	// 但历史记录里仍应保留那条带图消息（供后台展示）
	convID := core.EncodeConversationID("group:G1:U1")
	all, _ := h.st.Messages(context.Background(), convID, 20, 0)
	found := false
	for _, m := range all {
		if len(m.Attachments) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("历史消息应保留附件记录")
	}
}

/* ------------------------------------------------------------------ *
 * 触发模式 与 上下文范围 解耦
 *   只回 @ 的消息，但模型仍能看到全群在聊什么
 * ------------------------------------------------------------------ */

func lastAIMessages(t *testing.T, h *harness) []map[string]any {
	t.Helper()
	h.mu.Lock()
	body := h.lastAIBody
	h.mu.Unlock()
	if body == nil {
		t.Fatal("没有捕获到模型请求")
	}
	raw, _ := body["messages"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, _ := item.(map[string]any)
		out = append(out, m)
	}
	return out
}

func contentText(m map[string]any) string {
	switch v := m["content"].(type) {
	case string:
		return v
	case []any:
		for _, part := range v {
			p, _ := part.(map[string]any)
			if p["type"] == "text" {
				s, _ := p["text"].(string)
				return s
			}
		}
	}
	return ""
}

// C1（每人独立）+ include_unaddressed：上下文只多出「该成员自己」未 @ 的发言。
func TestUnaddressedMessagesEnterContextInPerMemberScope(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"trigger_mode":"at_only","context_mode":"per_member","context_scope":"include_unaddressed"}`)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("CX1", "G1", "U1", "小明", "今天天气不错"))
	h.dis.HandlePayload(context.Background(), groupAnyMessage("CX2", "G1", "U2", "小红", "是呀，适合出去玩"))
	time.Sleep(300 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("at_only 下未 @ 的消息不应触发回复，实际 %d 条", got)
	}

	h.dis.HandlePayload(context.Background(), groupAtMessage("CX3", "G1", "U1", "小明", "刚才我说了什么？"))
	h.waitForSent(t, 1)

	var joined []string
	for _, m := range lastAIMessages(t, h) {
		joined = append(joined, contentText(m))
	}
	all := strings.Join(joined, "\n")
	if !strings.Contains(all, "小明：今天天气不错") {
		t.Fatalf("自己未 @ 的发言应带昵称进入上下文，实际：\n%s", all)
	}
	// C1 隔离：别人的闲聊属于他们各自的会话，不应出现在这里
	if strings.Contains(all, "适合出去玩") {
		t.Fatalf("C1 下不应看到其他成员的发言，实际：\n%s", all)
	}
}

// C2（整群共享）+ include_unaddressed：模型能看到整个群的聊天。
func TestUnaddressedMessagesEnterContextInSharedScope(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"trigger_mode":"at_only","context_mode":"shared","context_scope":"include_unaddressed"}`)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("CS1", "G1", "U1", "小明", "今天天气不错"))
	h.dis.HandlePayload(context.Background(), groupAnyMessage("CS2", "G1", "U2", "小红", "是呀，适合出去玩"))
	time.Sleep(300 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("at_only 下未 @ 的消息不应触发回复，实际 %d 条", got)
	}

	h.dis.HandlePayload(context.Background(), groupAtMessage("CS3", "G1", "U1", "小明", "刚才我们聊了什么？"))
	h.waitForSent(t, 1)

	var joined []string
	for _, m := range lastAIMessages(t, h) {
		joined = append(joined, contentText(m))
	}
	all := strings.Join(joined, "\n")
	for _, want := range []string{"小明：今天天气不错", "小红：是呀，适合出去玩"} {
		if !strings.Contains(all, want) {
			t.Fatalf("共享上下文应包含 %q，实际：\n%s", want, all)
		}
	}
}

func TestAddressedOnlyScopeKeepsTheGroupOut(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"trigger_mode":"at_only","context_scope":"addressed_only"}`)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("CY1", "G1", "U1", "小明", "这是不该进上下文的闲聊"))
	h.dis.HandlePayload(context.Background(), groupAtMessage("CY2", "G1", "U1", "小明", "只回答我的问题"))
	h.waitForSent(t, 1)

	var joined []string
	for _, m := range lastAIMessages(t, h) {
		joined = append(joined, contentText(m))
	}
	if strings.Contains(strings.Join(joined, "\n"), "这是不该进上下文的闲聊") {
		t.Fatal("addressed_only 下未 @ 的消息不应进入上下文")
	}
}

func TestContextScopeStillDoesNotTriggerReplies(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"trigger_mode":"at_only","context_scope":"include_unaddressed"}`)

	// 其他机器人连续发言：即使进了上下文，也绝不能因此触发回复（否则两 bot 对刷）
	withBotState(t, h, "G1", ourBotOpenID)
	h.dis.HandlePayload(context.Background(), groupAtMessage("CZ0", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)
	h.dis.HandlePayload(context.Background(), botMessage("CZ1", "G1", otherBotOpenID, "另一个机器人", "我来播报天气"))
	h.dis.HandlePayload(context.Background(), botMessage("CZ2", "G1", otherBotOpenID, "另一个机器人", "今天晴"))
	time.Sleep(300 * time.Millisecond)

	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("旁观内容进上下文不应触发回复，实际发送 %d 条", got)
	}
	// 但下一次被 @ 时，模型能看到它们
	h.dis.HandlePayload(context.Background(), groupAtMessage("CZ3", "G1", "U1", "小明", "刚才那个 bot 说了什么"))
	h.waitForSent(t, 2)
	var joined []string
	for _, m := range lastAIMessages(t, h) {
		joined = append(joined, contentText(m))
	}
	if !strings.Contains(strings.Join(joined, "\n"), "我来播报天气") {
		t.Fatal("其他机器人的发言应在 include_unaddressed 下进入上下文")
	}
}

/* ------------------------------------------------------------------ *
 * 按群覆盖：上下文范围 与 机器人共存
 * ------------------------------------------------------------------ */

func TestGroupOverrideContextScopeWins(t *testing.T) {
	h := newHarness(t)
	// 全局只含 @ 对话，但 G1 单独设为「含未 @ 消息」
	h.apply(t, `{"context_mode":"shared","context_scope":"addressed_only","group_overrides":[
		{"group_openid":"G1","trigger_mode":"at_only","context_mode":"shared","context_scope":"include_unaddressed"}
	]}`)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("OV1", "G1", "U1", "小明", "群里的闲聊"))
	h.dis.HandlePayload(context.Background(), groupAtMessage("OV2", "G1", "U1", "小明", "我刚说了啥"))
	h.waitForSent(t, 1)

	var joined []string
	for _, m := range lastAIMessages(t, h) {
		joined = append(joined, contentText(m))
	}
	if !strings.Contains(strings.Join(joined, "\n"), "群里的闲聊") {
		t.Fatal("按群覆盖的 include_unaddressed 应让该群纳入未 @ 的消息")
	}

	// 另一个群没有覆盖 → 仍跟随全局，旁观内容不进上下文
	h.dis.HandlePayload(context.Background(), groupAnyMessage("OV3", "G2", "U2", "小红", "另一个群的闲聊"))
	h.dis.HandlePayload(context.Background(), groupAtMessage("OV4", "G2", "U2", "小红", "问我"))
	h.waitForSent(t, 2)

	joined = nil
	for _, m := range lastAIMessages(t, h) {
		joined = append(joined, contentText(m))
	}
	if strings.Contains(strings.Join(joined, "\n"), "另一个群的闲聊") {
		t.Fatal("没有覆盖的群应继续跟随全局 addressed_only")
	}
}

func TestGroupOverrideReplyToBotsWins(t *testing.T) {
	h := newHarness(t)
	withBotState(t, h, "G1", ourBotOpenID)
	withBotState(t, h, "G2", ourBotOpenID)
	// 全局禁止回复其它机器人，但 G1 单独允许
	h.apply(t, `{"reply_to_bots":false,"group_overrides":[
		{"group_openid":"G1","enabled":true,"reply_to_bots":true}
	]}`)

	h.dis.HandlePayload(context.Background(), botMentioningUs("OB1", "G1", "@Kaku 帮个忙"))
	if got := len(h.waitForSent(t, 1)); got != 1 {
		t.Fatalf("G1 覆盖为允许时应当回复，实际 %d 条", got)
	}

	h.dis.HandlePayload(context.Background(), botMentioningUs("OB2", "G2", "@Kaku 帮个忙"))
	time.Sleep(400 * time.Millisecond)
	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("没有覆盖的 G2 应跟随全局禁止，实际 %d 条", got)
	}
}

/* ------------------------------------------------------------------ *
 * 后台手动发送（会话详情页的输入栏）
 * ------------------------------------------------------------------ */

// seedConversation 造出一条真实轮次，手动发送需要一个已存在的会话。
func seedConversation(t *testing.T, h *harness, msgID string) string {
	t.Helper()
	h.dis.HandlePayload(context.Background(), groupAtMessage(msgID, "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)
	return core.EncodeConversationID("group:G1:U1")
}

func TestManualSendPostsAnActiveMessage(t *testing.T) {
	h := newHarness(t)
	convID := seedConversation(t, h, "MS1")

	events, cancel := h.bus.Subscribe(32)
	defer cancel()

	res, err := h.dis.SendManual(context.Background(), convID, "大家好，我是管理员", 0)
	if err != nil {
		t.Fatalf("manual send: %v", err)
	}
	if !res.Active || res.Segments != 1 || res.MessageID == "" {
		t.Fatalf("unexpected result %#v", res)
	}

	sent := h.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("expected the bot reply plus the manual message, got %d", len(sent))
	}
	manual := sent[1]
	if manual.Path != "/v2/groups/G1/messages" {
		t.Fatalf("unexpected path %q", manual.Path)
	}
	if manual.Body.Content != "大家好，我是管理员" {
		t.Fatalf("content = %q", manual.Body.Content)
	}
	if manual.Body.MsgID != "" {
		t.Fatalf("手动发送必须走主动消息，不能带 msg_id，实际 %q", manual.Body.MsgID)
	}

	ctxBg := context.Background()

	// 时间线：kind=manual，且它是机器人的话，必须进模型上下文（D-028 的决定）。
	msgs, err := h.st.RecentMessages(ctxBg, convID, 20, false)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	var manualRow *store.Message
	for i := range msgs {
		if msgs[i].Kind == store.KindManual {
			manualRow = &msgs[i]
		}
	}
	if manualRow == nil {
		t.Fatalf("手动消息应写入会话时间线，实际 %#v", msgs)
	}
	if manualRow.Role != "assistant" || manualRow.Content != "大家好，我是管理员" {
		t.Fatalf("unexpected manual row %#v", manualRow)
	}

	// 审计：status=manual、delivery=active
	recs, _, err := h.st.ListRecords(ctxBg, store.RecordFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	var found bool
	for _, rec := range recs {
		if rec.Status != store.StatusManual {
			continue
		}
		found = true
		if rec.Delivery != store.DeliveryActive {
			t.Fatalf("手动发送应记为主动消息，实际 %q", rec.Delivery)
		}
		if rec.Direction != "out" || rec.ConversationID != convID {
			t.Fatalf("unexpected record %#v", rec)
		}
	}
	if !found {
		t.Fatalf("缺少 status=manual 的审计记录：%#v", recs)
	}

	// SSE：会话详情页靠这个事件自动刷新
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-events:
			if ev.Name != "message" {
				continue
			}
			raw, _ := json.Marshal(ev.Data)
			var payload map[string]any
			_ = json.Unmarshal(raw, &payload)
			if payload["conversation_id"] == convID && payload["status"] == store.StatusManual {
				return
			}
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatal("手动发送应发出带 conversation_id 的 message 事件")
}

func TestManualSendToAC2CChatUsesTheUserOpenID(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), c2cMessage("DM1", "U9", "小红", "在吗"))
	h.waitForSent(t, 1)

	convID := core.EncodeConversationID("c2c:U9")
	conv, err := h.st.GetConversation(context.Background(), convID)
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if conv.MemberOpenID != "U9" {
		t.Fatalf("私聊会话应记住 user_openid，实际 %q", conv.MemberOpenID)
	}

	h.mu.Lock()
	h.sent = nil
	h.mu.Unlock()

	if _, err := h.dis.SendManual(context.Background(), convID, "在的", 0); err != nil {
		t.Fatalf("manual send: %v", err)
	}
	sent := h.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(sent))
	}
	if !strings.Contains(sent[0].Path, "/v2/users/U9/messages") {
		t.Fatalf("私聊应发往 user_openid，实际路径 %q", sent[0].Path)
	}
}

// 老数据里私聊会话没有 member_openid，只能从会话 ID（c2c:<openid>）里解出来。
func TestManualSendToALegacyC2CConversationDecodesTheTarget(t *testing.T) {
	h := newHarness(t)
	ctxBg := context.Background()
	convID := core.EncodeConversationID("c2c:U7")
	if err := h.st.EnsureConversation(ctxBg, &store.Conversation{
		ID: convID, Scene: store.SceneC2C, GroupOpenID: "", MemberOpenID: "", MemberName: "老数据",
	}); err != nil {
		t.Fatalf("conversation: %v", err)
	}

	if _, err := h.dis.SendManual(ctxBg, convID, "补发的消息", 0); err != nil {
		t.Fatalf("manual send: %v", err)
	}
	sent := h.sentMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].Path, "/v2/users/U7/messages") {
		t.Fatalf("应从会话 ID 解出 user_openid，实际 %#v", sent)
	}
}

func TestManualSendRefusesOversizedContent(t *testing.T) {
	h := newHarness(t)
	convID := seedConversation(t, h, "MS2")
	before := len(h.sentMessages())

	// 默认 reply_max_chars=1200、manual_send_max_segments=5 → 超过 5 段即拒绝
	_, err := h.dis.SendManual(context.Background(), convID, strings.Repeat("字", 6001), 0)
	if !errors.Is(err, core.ErrManualSendTooLong) {
		t.Fatalf("超长内容应被拒绝，实际 %v", err)
	}
	if got := len(h.sentMessages()); got != before {
		t.Fatalf("被拒绝的内容不该调用平台，发送数从 %d 变成 %d", before, got)
	}
}

func TestManualSendCanSplitIntoMultipleSegments(t *testing.T) {
	h := newHarness(t)
	convID := seedConversation(t, h, "MS3")

	// 2400 字 → 2 段（每段 1200），应全部发出去，且不截断
	res, err := h.dis.SendManual(context.Background(), convID, strings.Repeat("好", 2400), 0)
	if err != nil {
		t.Fatalf("manual send: %v", err)
	}
	if res.Segments != 2 {
		t.Fatalf("应拆成 2 段，实际 %d", res.Segments)
	}
	sent := h.sentMessages()
	if len(sent) != 3 {
		t.Fatalf("应发出 1 条机器人回复 + 2 段手动消息，实际 %d", len(sent))
	}
	for _, s := range sent[1:] {
		if strings.Contains(s.Body.Content, "已截断") {
			t.Fatalf("人工消息不该被截断：%q", s.Body.Content)
		}
	}
}

func TestManualSendRespectsPerConversationRateLimit(t *testing.T) {
	h := newHarness(t)
	convID := seedConversation(t, h, "MS4")
	h.apply(t, `{"manual_send_per_conversation_qpm":1,"manual_send_per_minute":100}`)

	if _, err := h.dis.SendManual(context.Background(), convID, "第一条", 0); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, err := h.dis.SendManual(context.Background(), convID, "第二条", 0); !errors.Is(err, core.ErrManualSendLimited) {
		t.Fatalf("第二次应被限流，实际 %v", err)
	}
}

func TestManualSendRespectsTheGlobalSwitch(t *testing.T) {
	h := newHarness(t)
	convID := seedConversation(t, h, "MS5")
	h.apply(t, `{"manual_send_enabled":false}`)

	if _, err := h.dis.SendManual(context.Background(), convID, "试试", 0); !errors.Is(err, core.ErrManualSendDisabled) {
		t.Fatalf("关闭开关后应拒绝，实际 %v", err)
	}
}

func TestManualSendStopsWhenProactiveMessagesAreForbidden(t *testing.T) {
	h := newHarness(t)
	// 假 QQ API 只有在登记过 bot_state 的群里才答得出来，身份缓存也依赖它。
	withBotState(t, h, "G1", ourBotOpenID)
	convID := seedConversation(t, h, "MS6")
	before := len(h.sentMessages())

	h.mu.Lock()
	h.allowProactive = false
	h.mu.Unlock()
	if _, ok := h.dis.Identity().Refresh(context.Background(), "G1"); !ok {
		t.Fatal("刷新群状态失败，测试前置条件不成立")
	}

	if _, err := h.dis.SendManual(context.Background(), convID, "试试", 0); !errors.Is(err, core.ErrManualSendProactive) {
		t.Fatalf("没有主动消息权限时应拒绝，实际 %v", err)
	}
	if got := len(h.sentMessages()); got != before {
		t.Fatal("没有主动消息权限时不该调用平台")
	}
}

// 主动消息计数：手动发送与主动兜底共用一份平台配额，被动回复不占。
func TestActiveMessageCounterCountsProactiveSendsOnly(t *testing.T) {
	h := newHarness(t)
	convID := seedConversation(t, h, "MS7")
	ctxBg := context.Background()

	if n := h.dis.ActiveMessagesToday(ctxBg, "G1"); n != 0 {
		t.Fatalf("被动回复不该计入主动消息，实际 %d", n)
	}
	if _, err := h.dis.SendManual(ctxBg, convID, "第一条", 0); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := h.dis.SendManual(ctxBg, convID, "第二条", 0); err != nil {
		t.Fatalf("send: %v", err)
	}
	if n := h.dis.ActiveMessagesToday(ctxBg, "G1"); n != 2 {
		t.Fatalf("应有 2 条主动消息，实际 %d", n)
	}
	// 其它群不受影响
	if n := h.dis.ActiveMessagesToday(ctxBg, "G2"); n != 0 {
		t.Fatalf("G2 不该有主动消息，实际 %d", n)
	}
}

func TestManualSendRecordsFailures(t *testing.T) {
	h := newHarness(t)
	convID := seedConversation(t, h, "MS8")

	h.mu.Lock()
	h.qqStatus = http.StatusBadRequest
	h.mu.Unlock()

	if _, err := h.dis.SendManual(context.Background(), convID, "发不出去", 0); err == nil {
		t.Fatal("平台报错时应返回错误")
	}

	recs, _, err := h.st.ListRecords(context.Background(), store.RecordFilter{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	var failed *store.Record
	for i := range recs {
		if recs[i].Direction == "out" && recs[i].Status == "failed" {
			failed = &recs[i]
		}
	}
	if failed == nil {
		t.Fatalf("失败的发送也要留审计记录：%#v", recs)
	}
	if failed.Content != "发不出去" {
		t.Fatalf("审计记录应保留原文，实际 %q", failed.Content)
	}
	// 失败不该写进会话时间线（用户根本没看到）
	msgs, _ := h.st.Messages(context.Background(), convID, 20, 0)
	for _, m := range msgs {
		if m.Content == "发不出去" {
			t.Fatalf("发失败的消息不该进时间线：%#v", m)
		}
	}
}

/* ------------------------------------------------------------------ *
 * 引用回复
 * ------------------------------------------------------------------ */

// @ 触发时，回复要带上被引用消息的 msg_idx（平台的 message_reference）。
func TestQuoteReplyReferencesTheTriggeringMessage(t *testing.T) {
	h := newHarness(t)
	// 默认档位是 occasionally（约 30%）：pin 随机源，让断言检查引用管线而不是抽签。
	h.dis.SetQuoteRoll(func() float64 { return 0.1 })
	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ1", "G1", "U1", "小明", "帮我看看这个"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(sent))
	}
	ref := sent[0].Body.MessageRef
	if ref == nil || ref.MessageID != "REFIDX_QQ1" {
		t.Fatalf("回复应引用触发消息 REFIDX_QQ1，实际 %#v", ref)
	}
}

// 即时回复（「让我多想会儿」）不带引用：它是占位语，引用只会多出一个引用块。
func TestEarlyReplyIsNotQuoted(t *testing.T) {
	h := newHarness(t)
	h.dis.SetQuoteRoll(func() float64 { return 0.1 })
	h.apply(t, `{"early_reply_enabled":true,"early_reply_delay_seconds":1,"reply_footer_enabled":false}`)
	h.mu.Lock()
	h.aiDelay = 3 * time.Second
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ2", "G1", "U1", "小明", "慢一点回答"))

	sent := h.waitForSent(t, 2)
	if len(sent) < 2 {
		t.Fatalf("expected an early notice plus the answer, got %d", len(sent))
	}
	if sent[0].Body.MessageRef != nil {
		t.Fatalf("即时回复不该引用，实际 %#v", sent[0].Body.MessageRef)
	}
	if sent[1].Body.MessageRef == nil || sent[1].Body.MessageRef.MessageID != "REFIDX_QQ2" {
		t.Fatalf("正式答复仍应引用触发消息，实际 %#v", sent[1].Body.MessageRef)
	}
}

func TestQuoteReplyCanBeDisabledPerGroup(t *testing.T) {
	h := newHarness(t)
	h.dis.SetQuoteRoll(func() float64 { return 0.1 })
	h.apply(t, `{"group_overrides":[{"group_openid":"G1","enabled":true,"quote_reply":false}]}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ3", "G1", "U1", "小明", "关掉引用"))
	sent := h.waitForSent(t, 1)
	if sent[0].Body.MessageRef != nil {
		t.Fatalf("按群关闭后不该引用，实际 %#v", sent[0].Body.MessageRef)
	}

	// 另一个群跟随全局（默认开启）
	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ4", "G2", "U1", "小明", "这个群没关"))
	sent = h.waitForSent(t, 2)
	if sent[1].Body.MessageRef == nil {
		t.Fatal("没有覆盖的群应跟随全局（默认开启引用）")
	}
}

func TestQuoteReplyDisabledGlobally(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"quote_reply":false}`)
	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ5", "G1", "U1", "小明", "全局关闭"))
	sent := h.waitForSent(t, 1)
	if sent[0].Body.MessageRef != nil {
		t.Fatalf("全局关闭后不该引用，实际 %#v", sent[0].Body.MessageRef)
	}
}

// 机器人自己发的消息也要能被引用：把平台返回的 ref_idx 存进消息行。
func TestOutboundQuoteIndexIsStored(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ6", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)

	msgs, err := h.st.RecentMessages(context.Background(), core.EncodeConversationID("group:G1:U1"), 10, false)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(msgs))
	}
	if msgs[0].QuoteIndex != "REFIDX_QQ6" {
		t.Fatalf("入站消息应存下平台引用索引，实际 %q", msgs[0].QuoteIndex)
	}
	if msgs[1].QuoteIndex != "REFIDX_SENT" {
		t.Fatalf("出站消息应存下响应里的 ref_idx，实际 %q", msgs[1].QuoteIndex)
	}
}

// 后台手动发送时可以右键引用一条历史消息。
func TestManualSendQuotesAChosenMessage(t *testing.T) {
	h := newHarness(t)
	convID := seedConversation(t, h, "QQ7")

	msgs, err := h.st.Messages(context.Background(), convID, 20, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	var target int64
	for _, m := range msgs {
		if m.Role == "user" {
			target = m.ID
		}
	}
	if target == 0 {
		t.Fatal("没找到可引用的用户消息")
	}

	h.mu.Lock()
	h.sent = nil
	h.mu.Unlock()

	res, err := h.dis.SendManual(context.Background(), convID, "这条我回一下", target)
	if err != nil {
		t.Fatalf("manual send: %v", err)
	}
	if !res.Quoted {
		t.Fatal("结果应标记为引用了消息")
	}
	sent := h.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(sent))
	}
	if sent[0].Body.MessageRef == nil || sent[0].Body.MessageRef.MessageID != "REFIDX_QQ7" {
		t.Fatalf("手动发送应引用所选消息，实际 %#v", sent[0].Body.MessageRef)
	}
}

// 没有平台引用索引的消息（升级前写入的行）不能引用，也不能跨会话引用。
func TestManualSendRejectsUnquotableMessages(t *testing.T) {
	h := newHarness(t)
	ctxBg := context.Background()
	convID := core.EncodeConversationID("group:G1:U1")
	if err := h.st.EnsureConversation(ctxBg, &store.Conversation{
		ID: convID, Scene: store.SceneGroupMember, GroupOpenID: "G1", MemberOpenID: "U1",
	}); err != nil {
		t.Fatalf("conversation: %v", err)
	}
	oldID, err := h.st.AppendMessageID(ctxBg, store.MessageInput{
		ConversationID: convID, Role: "user", Content: "老数据没有引用索引", Kind: store.KindDialogue,
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	if _, err := h.dis.SendManual(ctxBg, convID, "引用一下", oldID); !errors.Is(err, core.ErrManualSendQuoteUnavailable) {
		t.Fatalf("没有引用索引的消息应被拒绝，实际 %v", err)
	}

	// 另一个会话里的消息不能拿来引用
	otherID, err := h.st.AppendMessageID(ctxBg, store.MessageInput{
		ConversationID: core.EncodeConversationID("group:G9:U9"), Role: "user",
		Content: "别的会话", Kind: store.KindDialogue, QuoteIndex: "REFIDX_OTHER",
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := h.dis.SendManual(ctxBg, convID, "跨会话引用", otherID); !errors.Is(err, core.ErrManualSendNoTarget) {
		t.Fatalf("跨会话引用应被拒绝，实际 %v", err)
	}
}

// 按群关掉引用回复后，手动引用也要被拒。
func TestManualSendQuoteHonoursTheGroupSwitch(t *testing.T) {
	h := newHarness(t)
	ctxBg := context.Background()
	convID := seedConversation(t, h, "QQ8")
	h.apply(t, `{"group_overrides":[{"group_openid":"G1","enabled":true,"quote_reply":false}]}`)

	msgs, _ := h.st.Messages(ctxBg, convID, 20, 0)
	var target int64
	for _, m := range msgs {
		if m.Role == "user" {
			target = m.ID
		}
	}
	if _, err := h.dis.SendManual(ctxBg, convID, "引用", target); !errors.Is(err, core.ErrManualSendQuoteDisabled) {
		t.Fatalf("按群关闭引用后应拒绝，实际 %v", err)
	}
}

/* ------------------------------------------------------------------ *
 * 模型端点可以在后台改（openai_base_url）
 * ------------------------------------------------------------------ */

func TestAIBaseURLIsAdjustableAtRuntime(t *testing.T) {
	h := newHarness(t)

	// 第二台「上游」：只有它认识这台机器
	var hits atomic.Int64
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(map[string]any{
			"model":   "test-model",
			"choices": []map[string]any{{"delta": map[string]string{"content": "来自第二个端点"}, "finish_reason": "stop"}},
			"usage":   map[string]int{"total_tokens": 3},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
	}))
	defer second.Close()

	if h.client.BaseURL() != h.aiBaseURL {
		t.Fatalf("初始端点应为 .env 值，实际 %q", h.client.BaseURL())
	}

	// 在「设置面板」里把端点换掉（末带斜杠，也要被规范化）
	h.apply(t, fmt.Sprintf(`{"openai_base_url":%q}`, second.URL+"/v1/"))
	if got := h.client.BaseURL(); got != second.URL+"/v1" {
		t.Fatalf("换端点后 BaseURL() = %q", got)
	}

	h.dis.HandlePayload(context.Background(), groupAtMessage("BU1", "G1", "U1", "小明", "你从哪来"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(sent))
	}
	if sent[0].Body.Content != "来自第二个端点" {
		t.Fatalf("回复应来自新端点，实际 %q", sent[0].Body.Content)
	}
	if hits.Load() == 0 {
		t.Fatal("新端点没有收到请求")
	}

	// 清空端点 = 未配置：已经没有 .env 兜底，必须明确报错而不是偷偷打到别处
	// （顺带把润色打开：stage2 同样不许打到别的上游。）
	h.apply(t, `{"openai_base_url":"","polish_enabled":true}`)
	if got := h.client.BaseURL(); got != "" {
		t.Fatalf("清空后不该存在端点，实际 %q", got)
	}

	h.dis.HandlePayload(context.Background(), groupAtMessage("BU2", "G1", "U1", "小明", "再来一次"))
	sent = h.waitForSent(t, 2)
	if len(sent) != 2 {
		t.Fatalf("未配置时应回一句兜底话术，实际发送 %d 条", len(sent))
	}
	if !strings.Contains(sent[1].Body.Content, "出错") {
		t.Fatalf("应回复兜底话术，实际 %q", sent[1].Body.Content)
	}
	if lastErr := h.dis.LastAIError(); !strings.Contains(lastErr, "未配置") {
		t.Fatalf("错误信息应指出端点没配，实际 %q", lastErr)
	}
	if hits.Load() != 1 {
		t.Fatalf("清空端点后不该再请求上游，实际累计 %d 次", hits.Load())
	}
	// 端点没配时 stage2 同样不该偷偷打上游（上面已把润色打开）。
	if got := h.polishCallCount(); got != 0 {
		t.Fatalf("端点清空后 stage2 不该发起请求，实际 %d 次", got)
	}
}

// 连密钥都没配时也不能静默降级：报错要能直接指路。
func TestUnconfiguredAIEndpointFailsClearly(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"openai_base_url":"","openai_api_key":""}`)
	if _, err := h.client.Chat(context.Background(), ai.ChatRequest{
		Model: "test-model", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	}); !errors.Is(err, ai.ErrNotConfigured) {
		t.Fatalf("未配置端点应返回 ErrNotConfigured，实际 %v", err)
	}
}

// 非法端点必须被设置接口挡下，而不是等到第一次模型调用才炸。
func TestInvalidAIBaseURLIsRejected(t *testing.T) {
	h := newHarness(t)
	for _, bad := range []string{
		`{"openai_base_url":"127.0.0.1:8080/v1"}`,
		`{"openai_base_url":"ftp://example.com"}`,
	} {
		var patch map[string]json.RawMessage
		if err := json.Unmarshal([]byte(bad), &patch); err != nil {
			t.Fatalf("bad fixture %s: %v", bad, err)
		}
		if _, _, err := h.settings.Apply(context.Background(), patch); err == nil {
			t.Fatalf("非法端点应被拒绝：%s", bad)
		}
	}
	if got := h.client.BaseURL(); got != h.aiBaseURL {
		t.Fatalf("被拒绝后端点不该变化，实际 %q", got)
	}
}

// 换端点必须能同时换密钥：新服务商不认旧 key，这正是线上会踩的坑。
func TestAIKeyIsAdjustableAtRuntime(t *testing.T) {
	h := newHarness(t)

	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer panel-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"API Key 无效"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(map[string]any{
			"model":   "test-model",
			"choices": []map[string]any{{"delta": map[string]string{"content": "新端点认证通过"}, "finish_reason": "stop"}},
			"usage":   map[string]int{"total_tokens": 3},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
	}))
	defer second.Close()

	// 只换端点、不换 key → 新端点拒绝（旧 key 是给别的服务商的）
	h.apply(t, fmt.Sprintf(`{"openai_base_url":%q}`, second.URL+"/v1"))
	h.dis.HandlePayload(context.Background(), groupAtMessage("BK1", "G1", "U1", "小明", "在吗"))
	sent := h.waitForSent(t, 1)
	if strings.Contains(sent[0].Body.Content, "新端点认证通过") {
		t.Fatal("旧密钥不该在新端点通过认证")
	}

	// 补上匹配的 key → 立刻可用
	h.apply(t, `{"openai_api_key":"panel-key"}`)
	h.dis.HandlePayload(context.Background(), groupAtMessage("BK2", "G1", "U1", "小明", "再试一次"))
	// 第一次失败会回一句兜底话术，所以只看最后一条。
	sent = h.waitForSent(t, 2)
	if last := sent[len(sent)-1]; last.Body.Content != "新端点认证通过" {
		t.Fatalf("补上密钥后应成功，实际 %q", last.Body.Content)
	}
}

/* ------------------------------------------------------------------ *
 * Markdown 发送
 * ------------------------------------------------------------------ */

// 开启 markdown 后正文走 msg_type=2、content 必须为空。
func TestMarkdownReplyUsesMsgType2(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"markdown_enabled":true}`)
	h.dis.HandlePayload(context.Background(), groupAtMessage("MD1", "G1", "U1", "小明", "说点什么"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(sent))
	}
	body := sent[0].Body
	if body.MsgType != qq.MsgTypeMarkdown {
		t.Fatalf("msg_type 应为 2（markdown），实际 %d", body.MsgType)
	}
	if body.Content != "" {
		t.Fatalf("markdown 消息的 content 必须为空，实际 %q", body.Content)
	}
	if body.Markdown == nil || body.Markdown.Content == "" {
		t.Fatalf("markdown.content 为空：%#v", body.Markdown)
	}
}

// 没有 markdown 权限时必须自动降级为纯文本，而且不能每条消息都先失败一次。
func TestMarkdownFallsBackToTextOnce(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"markdown_enabled":true}`)
	h.mu.Lock()
	h.rejectMarkdown = true
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("MD2", "G1", "U1", "小明", "第一条"))
	sent := h.waitForSent(t, 2) // 一次失败的 markdown + 一次成功的纯文本
	if len(sent) < 2 {
		t.Fatalf("应有一次失败 + 一次降级，实际 %d", len(sent))
	}
	if sent[0].Body.MsgType != qq.MsgTypeMarkdown {
		t.Fatalf("第一次应尝试 markdown，实际 msg_type=%d", sent[0].Body.MsgType)
	}
	last := sent[len(sent)-1].Body
	if last.MsgType != qq.MsgTypeText || last.Content == "" {
		t.Fatalf("降级后应发纯文本，实际 %#v", last)
	}

	// 冷却期内下一条消息直接发纯文本，不再浪费一次请求
	if h.sender.MarkdownUsable() {
		t.Fatal("markdown 被拒后应进入冷却")
	}
	before := len(h.sentMessages())
	h.dis.HandlePayload(context.Background(), groupAtMessage("MD3", "G1", "U1", "小明", "第二条"))
	sent = h.waitForSent(t, before+1)
	if got := sent[len(sent)-1].Body; got.MsgType != qq.MsgTypeText {
		t.Fatalf("冷却期内应直接发纯文本，实际 msg_type=%d", got.MsgType)
	}
}

/* ------------------------------------------------------------------ *
 * 工具调用（P0）
 * ------------------------------------------------------------------ */

// 模型先要时间、拿到结果再回答：请求里带 tools，回灌 role=tool，最后落审计。
func TestToolCallRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"tools_enabled":true}`)
	h.mu.Lock()
	h.aiToolScript = []string{"now"}
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("TC1", "G1", "U1", "小明", "现在几点了"))

	sent := h.waitForSent(t, 1)
	if len(sent) == 0 {
		t.Fatal("应发出最终答复")
	}

	// 第一次请求必须带工具定义，第二次必须带 role=tool 的结果。
	h.mu.Lock()
	body := h.lastAIBody
	h.mu.Unlock()
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("第二次请求没有消息：%#v", body)
	}
	var sawTool bool
	var toolContent string
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] == "tool" {
			sawTool = true
			toolContent, _ = m["content"].(string)
			if m["tool_call_id"] == "" {
				t.Fatalf("role=tool 必须带 tool_call_id：%#v", m)
			}
		}
	}
	if !sawTool {
		t.Fatalf("工具结果没有回灌给模型：%#v", msgs)
	}
	if !strings.Contains(toolContent, "星期") {
		t.Fatalf("now 的输出不对：%q", toolContent)
	}

	// 审计表里应有一行
	rows, err := h.st.ListToolCalls(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(rows) != 1 || rows[0].Tool != "now" || rows[0].Status != store.ToolCallOK {
		t.Fatalf("审计记录不对：%#v", rows)
	}
	if rows[0].ConversationID == "" || rows[0].MemberOpenID != "U1" {
		t.Fatalf("审计缺少上下文：%#v", rows[0])
	}
}

// 工具定义只该在开启时出现在请求里。
func TestToolsDisabledAdvertisesNothing(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"tools_enabled":false}`)
	h.dis.HandlePayload(context.Background(), groupAtMessage("TC2", "G1", "U1", "小明", "现在几点"))

	h.waitForSent(t, 1)
	h.mu.Lock()
	body := h.lastAIBody
	h.mu.Unlock()
	if _, ok := body["tools"]; ok {
		t.Fatalf("关闭后不该带工具定义：%#v", body["tools"])
	}
}

// 按群关闭时同样不该带工具。
func TestToolsCanBeDisabledPerGroup(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"tools_enabled":true}`)
	if _, _, err := h.settings.Apply(context.Background(), map[string]json.RawMessage{
		"group_overrides": json.RawMessage(`[{"group_openid":"G1","tools":false}]`),
	}); err != nil {
		t.Fatalf("设置按群覆盖: %v", err)
	}
	h.dis.HandlePayload(context.Background(), groupAtMessage("TC3", "G1", "U1", "小明", "现在几点"))

	h.waitForSent(t, 1)
	h.mu.Lock()
	body := h.lastAIBody
	h.mu.Unlock()
	if _, ok := body["tools"]; ok {
		t.Fatal("该群已关闭工具，不该带工具定义")
	}

	// 换个没关的群就该带上
	h.dis.HandlePayload(context.Background(), groupAtMessage("TC4", "G2", "U1", "小明", "现在几点"))
	h.waitForSent(t, 2)
	h.mu.Lock()
	body = h.lastAIBody
	h.mu.Unlock()
	if _, ok := body["tools"]; !ok {
		t.Fatal("未关闭的群应带工具定义")
	}
}

// 网关卡不支持 tools 时必须自动降级重试，而不是让每条回复都失败。
func TestProviderWithoutToolSupportStillReplies(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"tools_enabled":true}`)
	h.mu.Lock()
	h.aiRejectTools = true
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("TC5", "G1", "U1", "小明", "你好"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("应仍然回复一条，实际 %d", len(sent))
	}
	if sent[0].Body.Content == "" && (sent[0].Body.Markdown == nil || sent[0].Body.Markdown.Content == "") {
		t.Fatal("降级后应有正文")
	}
	h.mu.Lock()
	body := h.lastAIBody
	h.mu.Unlock()
	if _, ok := body["tools"]; ok {
		t.Fatal("降级重试不应再带 tools")
	}
}

/* ------------------------------------------------------------------ *
 * 记忆（P1）
 * ------------------------------------------------------------------ */

// 群记忆：只在写入的那个群可见，私聊和别的群都看不到。
func TestMemoryStaysInsideItsGroup(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiToolScript = []string{`remember|{"text":"小明养了一只叫豆豆的猫"}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("MEM1", "G1", "U1", "小明", "记住我养了一只叫豆豆的猫"))
	h.waitForSent(t, 1)

	// 第二条消息：同一群应能看到这条事实被贴进系统提示词
	h.dis.HandlePayload(context.Background(), groupAtMessage("MEM2", "G1", "U2", "小红", "我的猫叫什么"))
	h.waitForSent(t, 2)
	if !h.systemPrompt(t).contains("豆豆") {
		t.Fatalf("群 G1 的系统提示词里应带上记忆，实际：%s", h.systemPrompt(t).text)
	}

	// 别的群不该看到
	h.dis.HandlePayload(context.Background(), groupAtMessage("MEM3", "G2", "U1", "小明", "我的猫叫什么"))
	h.waitForSent(t, 3)
	if h.systemPrompt(t).contains("豆豆") {
		t.Fatal("记忆不该跨群泄漏")
	}
}

// 记忆也是可检索的：写入后，下一轮模型可以主动用 recall 把事实取回来
// （系统提示词注入是「顺手看到」，recall 是「专门去查」）。
func TestRememberedFactComesBackThroughRecall(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiToolScript = []string{`remember|{"text":"小明养了一只叫豆豆的猫"}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("MEMR1", "G1", "U1", "小明", "记住我养了一只叫豆豆的猫"))
	h.waitForSent(t, 1)

	h.mu.Lock()
	h.aiToolScript = []string{`recall|{"query":"豆豆"}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("MEMR2", "G1", "U1", "小明", "我的猫叫什么来着"))
	h.waitForSent(t, 2)

	h.mu.Lock()
	body := h.lastAIBody
	h.mu.Unlock()
	msgs, _ := body["messages"].([]any)
	var recalled string
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] == "tool" {
			recalled, _ = m["content"].(string)
		}
	}
	if !strings.Contains(recalled, "豆豆") {
		t.Fatalf("recall 应把写入的事实取回来，实际 %q", recalled)
	}
}

// 只有管理员能把记忆写成全局。
func TestOnlyAdminCanWriteGlobalMemory(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"admin_openids":["UADMIN"]}`)

	// 普通人尝试写全局：应被拒绝，且不落库
	h.mu.Lock()
	h.aiToolScript = []string{`remember|{"text":"全局事实","scope":"global"}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("MEM4", "G1", "U1", "小明", "记住这条全局的"))
	h.waitForSent(t, 1)
	rows, _ := h.st.ListMemories(context.Background(), store.MemoryGlobal, "", "", 10, 0)
	if len(rows) != 0 {
		t.Fatalf("非管理员不该写入全局记忆：%#v", rows)
	}

	// 管理员写全局：成功
	h.mu.Lock()
	h.aiToolScript = []string{`remember|{"text":"管理员说的全局事实","scope":"global"}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("MEM5", "G2", "UADMIN", "管理员", "记住这条全局的"))
	h.waitForSent(t, 2)
	rows, _ = h.st.ListMemories(context.Background(), store.MemoryGlobal, "", "", 10, 0)
	if len(rows) != 1 || rows[0].Text != "管理员说的全局事实" {
		t.Fatalf("管理员应能写全局记忆：%#v", rows)
	}

	// 全局记忆在任何群都可见
	h.dis.HandlePayload(context.Background(), groupAtMessage("MEM6", "G1", "U1", "小明", "随便聊聊"))
	h.waitForSent(t, 3)
	if !h.systemPrompt(t).contains("管理员说的全局事实") {
		t.Fatal("全局记忆应对所有群可见")
	}
}

// 关掉记忆后：不声明记忆工具，也不注入事实。
func TestMemoryCanBeDisabled(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"memory_enabled":true}`)
	h.mu.Lock()
	h.aiToolScript = []string{`remember|{"text":"会被关掉的事实"}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("MEM7", "G1", "U1", "小明", "记住这个"))
	h.waitForSent(t, 1)

	h.apply(t, `{"memory_enabled":false}`)
	h.dis.HandlePayload(context.Background(), groupAtMessage("MEM8", "G1", "U2", "小红", "还记得吗"))
	h.waitForSent(t, 2)
	if h.systemPrompt(t).contains("会被关掉的事实") {
		t.Fatal("关闭后不该注入记忆")
	}
	h.mu.Lock()
	body := h.lastAIBody
	h.mu.Unlock()
	if defs, ok := body["tools"].([]any); ok {
		for _, raw := range defs {
			d, _ := raw.(map[string]any)
			fn, _ := d["function"].(map[string]any)
			if name, _ := fn["name"].(string); name == "remember" || name == "recall" {
				t.Fatalf("关闭后不该声明记忆工具：%s", name)
			}
		}
	}
}

// 后台抽取：每 N 轮跑一次，把事实写进当前会话的作用域。
func TestMemoryExtractionWritesFacts(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"memory_enabled":true,"memory_extract_enabled":true,"memory_extract_every":1}`)
	h.mu.Lock()
	h.aiExtractReply = `好的：[{"text":"小明养了一只叫豆豆的猫","kind":"fact"}]`
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("MEM9", "G1", "U1", "小明", "我家猫最近不爱吃饭"))
	h.waitForSent(t, 1)

	// 抽取是后台 goroutine，等它落库。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := h.st.VisibleMemories(context.Background(), store.MemoryGroup, "G1", "", "豆豆", 10)
		if err != nil {
			t.Fatalf("visible: %v", err)
		}
		if len(rows) == 1 && strings.Contains(rows[0].Text, "豆豆") {
			if rows[0].Source != "extract" || rows[0].Scope != store.MemoryGroup {
				t.Fatalf("来源/作用域不对：%#v", rows[0])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("抽取的事实没有落库")
}

// 关掉抽取时不该额外调用模型。
func TestMemoryExtractionOffByDefault(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), groupAtMessage("MEM10", "G1", "U1", "小明", "随便说点什么"))
	h.waitForSent(t, 1)
	time.Sleep(150 * time.Millisecond)
	rows, _ := h.st.ListMemories(context.Background(), store.MemoryGroup, "G1", "", 10, 0)
	if len(rows) != 0 {
		t.Fatalf("默认不该抽取：%#v", rows)
	}
}

/* ------------------------------------------------------------------ *
 * 联网与容器内执行（P2）
 * ------------------------------------------------------------------ */

func toolNames(body map[string]any) map[string]bool {
	out := map[string]bool{}
	defs, _ := body["tools"].([]any)
	for _, raw := range defs {
		d, _ := raw.(map[string]any)
		fn, _ := d["function"].(map[string]any)
		if name, _ := fn["name"].(string); name != "" {
			out[name] = true
		}
	}
	return out
}

// 默认：web_fetch 可用、执行工具不声明（exec 默认关）、web_search 不声明
// （没配搜索后端，下发了也只会报错——工具定义是一种承诺）。
func TestDefaultCapabilities(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), groupAtMessage("CAP1", "G1", "U1", "小明", "现在几点"))
	h.waitForSent(t, 1)

	h.mu.Lock()
	names := toolNames(h.lastAIBody)
	h.mu.Unlock()
	if !names["web_fetch"] {
		t.Fatalf("web_fetch 应默认可用：%v", names)
	}
	if names["web_search"] {
		t.Fatalf("未配置搜索后端时不该声明 web_search：%v", names)
	}
	if names["run_shell"] {
		t.Fatalf("执行工具默认必须关闭：%v", names)
	}
}

// 配好 searxng 后端后，web_search 才随之下发。
func TestWebSearchIsAdvertisedOnceASearchBackendExists(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"web_search_provider":"searxng","web_search_base_url":"https://searxng.internal.example"}`)
	h.dis.HandlePayload(context.Background(), groupAtMessage("CAP2", "G1", "U1", "小明", "帮我搜一下"))

	h.waitForSent(t, 1)
	h.mu.Lock()
	names := toolNames(h.lastAIBody)
	h.mu.Unlock()
	if !names["web_search"] || !names["web_fetch"] {
		t.Fatalf("配置齐全后两个联网工具都应下发：%v", names)
	}
}

// 按群打开执行能力后，只有该群能看到 run_shell。
func TestExecCanBeEnabledPerGroup(t *testing.T) {
	h := newHarness(t)
	// 按群开关之外还有身份要求：U1 是管理员，才能在 G1 看到 run_shell。
	h.apply(t, `{"exec_enabled":true,"admin_openids":["U1"]}`)
	if _, _, err := h.settings.Apply(context.Background(), map[string]json.RawMessage{
		"group_overrides": json.RawMessage(`[{"group_openid":"G1","exec":true},{"group_openid":"G2","exec":false}]`),
	}); err != nil {
		t.Fatalf("设置覆盖: %v", err)
	}

	h.dis.HandlePayload(context.Background(), groupAtMessage("CAP2", "G1", "U1", "小明", "算一下"))
	h.waitForSent(t, 1)
	h.mu.Lock()
	inG1 := toolNames(h.lastAIBody)
	h.mu.Unlock()
	if !inG1["run_shell"] {
		t.Fatalf("G1 已开启却看不到 run_shell：%v", inG1)
	}

	h.dis.HandlePayload(context.Background(), groupAtMessage("CAP3", "G2", "U1", "小明", "算一下"))
	h.waitForSent(t, 2)
	h.mu.Lock()
	inG2 := toolNames(h.lastAIBody)
	h.mu.Unlock()
	if inG2["run_shell"] {
		t.Fatalf("G2 关闭了却仍声明 run_shell：%v", inG2)
	}
}

// 端到端跑一条命令：结果要回灌给模型，并落审计。
func TestShellToolRoundTrip(t *testing.T) {
	h := newHarness(t)
	// 执行能力自 P5 之后要求管理员身份，这里让发送者进 admin_openids。
	h.apply(t, `{"exec_enabled":true,"admin_openids":["U1"]}`)
	h.mu.Lock()
	h.aiToolScript = []string{`run_shell|{"command":"echo 容器里的输出"}`}
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("CAP4", "G1", "U1", "小明", "跑个命令"))
	h.waitForSent(t, 1)

	h.mu.Lock()
	msgs, _ := h.lastAIBody["messages"].([]any)
	h.mu.Unlock()
	var toolOutput string
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] == "tool" {
			toolOutput, _ = m["content"].(string)
		}
	}
	if !strings.Contains(toolOutput, "容器里的输出") {
		t.Fatalf("命令输出没有回灌给模型：%q", toolOutput)
	}

	rows, err := h.st.ListToolCalls(context.Background(), 5, 0)
	if err != nil || len(rows) != 1 || rows[0].Tool != "run_shell" || rows[0].Status != store.ToolCallOK {
		t.Fatalf("审计记录不对：%+v err=%v", rows, err)
	}
}

/* ------------------------------------------------------------------ *
 * 人味（P3）
 * ------------------------------------------------------------------ */

// 中等档要给回复加一段停顿（模拟读题+打字）；off 则立即发。
func TestHumanizePauseBeforeReply(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"humanize_level":"medium"}`)

	start := time.Now()
	h.dis.HandlePayload(context.Background(), groupAtMessage("HZ1", "G1", "U1", "小明", "在吗"))
	h.waitForSent(t, 1)
	// medium 的 BaseDelay=800ms，抖动 ±40%，下限就是 480ms；卡 500ms 会在抖动
	// 到低端偶发失败（实测 499ms）。450ms 既能证明「有停顿」，又不会误报：
	// off 档是 0ms，两者相差一个数量级。
	if elapsed := time.Since(start); elapsed < 450*time.Millisecond {
		t.Fatalf("中等档应该有可见停顿，实际只用了 %s", elapsed)
	}

	h.apply(t, `{"humanize_level":"off"}`)
	start = time.Now()
	h.dis.HandlePayload(context.Background(), groupAtMessage("HZ2", "G1", "U1", "小明", "在吗"))
	h.waitForSent(t, 2)
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("off 档不该停顿，实际用了 %s", elapsed)
	}
}

// 未 @ 机器人时，人味可以决定「不接话」；@ 了必须回。
func TestHumanizeSilenceOnlyForUnaddressed(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"humanize_level":"strong","humanize_delay_scale":0.5,"trigger_mode":"all_messages"}`)
	h.dis.SetSilenceRoll(func() float64 { return 0 }) // 一定沉默

	// 未 @（E2 全量事件）：应选择沉默
	h.dis.HandlePayload(context.Background(), groupAnyMessage("HZ3", "G1", "U1", "小明", "今天天气不错"))
	time.Sleep(200 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("选择沉默时不该发消息，实际发了 %d 条", got)
	}

	// @ 了：必须回
	h.dis.HandlePayload(context.Background(), groupAtMessage("HZ4", "G1", "U1", "小明", "小助手你说呢"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("@ 机器人时必须回复，实际 %d 条", len(sent))
	}
}

// 提示词要带上人味约束（禁表格、禁「作为 AI」、带人设）。
func TestHumanizePromptReachesSystemMessage(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"humanize_level":"medium","persona":"说话简短，偶尔用颜文字"}`)
	h.dis.HandlePayload(context.Background(), groupAtMessage("HZ5", "G1", "U1", "小明", "聊聊"))
	h.waitForSent(t, 1)

	prompt := h.systemPrompt(t)
	if !prompt.contains("不要像客服") || !prompt.contains("人设：说话简短") {
		t.Fatalf("系统提示词缺少人味约束：%s", prompt.text)
	}
}

/* ------------------------------------------------------------------ *
 * 委托式主动（P4）
 * ------------------------------------------------------------------ */

// 到点的提醒要主动发出来，并写进时间线。
func TestScheduledTaskIsDelivered(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiToolScript = []string{`schedule_task|{"text":"该喝水了","delay_seconds":60}`}
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("TK1", "G1", "U1", "小明", "一小时后提醒我喝水"))
	h.waitForSent(t, 1)

	tasks, err := h.st.ListTasks(context.Background(), store.TaskPending, 10, 0)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("应有一条待发提醒：%+v err=%v", tasks, err)
	}

	// 把时间推过到期点，跑一次调度
	h.dis.RunDueTasks(context.Background(), time.Now().Add(2*time.Minute))
	sent := h.waitForSent(t, 2)
	last := sent[len(sent)-1].Body
	if last.Content != "该喝水了" && (last.Markdown == nil || last.Markdown.Content != "该喝水了") {
		t.Fatalf("提醒内容不对：%#v", last)
	}

	done, _ := h.st.ListTasks(context.Background(), store.TaskSent, 10, 0)
	if len(done) != 1 || done[0].SentAt == "" {
		t.Fatalf("任务应标记为已发送：%+v", done)
	}

	// 时间线里应能看到机器人说过这句
	msgs, _ := h.st.RecentMessages(context.Background(), "group-shared:G1", 10, true)
	_ = msgs // 共享会话未启用时为空，这里不断言具体行，只保证不 panic
}

// 投递链路要闭环：发出 → 标记已发送 → 落进创建它的会话时间线，
// 并且下一轮模型能看到「自己确实说过这句话」（否则容易重复提醒）。
func TestScheduledTaskLandsInItsConversationTimeline(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiToolScript = []string{`schedule_task|{"text":"该喝水了","delay_seconds":60}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("TKT1", "G1", "U1", "小明", "一小时后提醒我喝水"))
	h.waitForSent(t, 1)

	h.dis.RunDueTasks(context.Background(), time.Now().Add(2*time.Minute))
	h.waitForSent(t, 2)

	done, err := h.st.ListTasks(context.Background(), store.TaskSent, 10, 0)
	if err != nil || len(done) != 1 {
		t.Fatalf("应有一条已发送的提醒：%+v err=%v", done, err)
	}
	task := done[0]
	if task.ConversationID == "" {
		t.Fatal("任务应记住创建它的会话，投递时才知道往哪条时间线记")
	}
	timeline, err := h.st.RecentMessages(context.Background(), task.ConversationID, 10, false)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	var landed bool
	for _, m := range timeline {
		if m.Content != task.Text {
			continue
		}
		landed = true
		if m.Role != "assistant" || m.Kind != store.KindManual {
			t.Fatalf("提醒应作为 assistant/manual 落进时间线：%+v", m)
		}
	}
	if !landed {
		t.Fatalf("时间线里没有这条提醒：%+v", timeline)
	}

	// 下一轮：模型应当看到自己刚说过这句话。
	h.dis.HandlePayload(context.Background(), groupAtMessage("TKT2", "G1", "U1", "小明", "刚才你提醒我什么了"))
	h.waitForSent(t, 3)
	h.mu.Lock()
	body := h.lastAIBody
	h.mu.Unlock()
	msgs, _ := body["messages"].([]any)
	var seen bool
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if text, _ := m["content"].(string); text == task.Text && m["role"] == "assistant" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("下一轮上下文里应带上已发出的提醒：%#v", msgs)
	}
}

// 停机会导致错过窗口：过期太久就不再发，标记失败而不是补一条突兀的提醒。
func TestMissedTaskIsNotSentLate(t *testing.T) {
	h := newHarness(t)
	_, err := h.st.AddTask(context.Background(), store.TaskInput{
		GroupOpenID: "G1", MemberOpenID: "U1", Text: "一小时前的提醒",
		DueAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	h.dis.RunDueTasks(context.Background(), time.Now())
	time.Sleep(150 * time.Millisecond)

	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("过期提醒不该再发：%d 条", got)
	}
	failed, _ := h.st.ListTasks(context.Background(), store.TaskFailed, 10, 0)
	if len(failed) != 1 || !strings.Contains(failed[0].Error, "错过发送窗口") {
		t.Fatalf("应记失败并说明原因：%+v", failed)
	}
}

// 取消只能作用于本会话的任务。
func TestCancelTaskTool(t *testing.T) {
	h := newHarness(t)
	id, err := h.st.AddTask(context.Background(), store.TaskInput{
		GroupOpenID: "G1", MemberOpenID: "U1", CreatorOpenID: "U1", Text: "待取消",
		DueAt: time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	h.mu.Lock()
	h.aiToolScript = []string{`cancel_task|{"id":` + strconv.FormatInt(id, 10) + `}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("TK2", "G1", "U1", "小明", "取消那个提醒"))
	h.waitForSent(t, 1)

	canceled, _ := h.st.ListTasks(context.Background(), store.TaskCanceled, 10, 0)
	if len(canceled) != 1 {
		t.Fatalf("应已取消：%+v", canceled)
	}

	// 别人的任务不受影响
	if pending, _ := h.st.PendingTasksOfCreator(context.Background(), "U2"); pending != 0 {
		t.Fatalf("U2 不该有任务：%d", pending)
	}
}

/* ------------------------------------------------------------------ *
 * 自发主动（P5）
 * ------------------------------------------------------------------ */

// seedHumanChat 造一段群里的真实对话，作为额度来源。
func (h *harness) seedHumanChat(t *testing.T, groupOpenID string, n int) {
	t.Helper()
	conv := "group-shared:" + groupOpenID
	if err := h.st.EnsureConversation(context.Background(), &store.Conversation{
		ID: conv, Scene: store.SceneGroupShared, GroupOpenID: groupOpenID,
	}); err != nil {
		t.Fatalf("ensure conversation: %v", err)
	}
	for i := 0; i < n; i++ {
		if err := h.st.AppendMessage(context.Background(), store.MessageInput{
			ConversationID: conv, Role: "user", Kind: store.KindDialogue,
			Content: "群友闲聊第 " + strconv.Itoa(i+1) + " 条", Speaker: "群友",
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

// 默认关闭：不发、也不调用模型。
func TestProactiveOffByDefault(t *testing.T) {
	h := newHarness(t)
	h.seedHumanChat(t, "G1", 20)
	h.mu.Lock()
	h.aiProactiveReply = `{"speak":true,"text":"我不该说话","reason":"测试"}`
	before := h.lastAIBody
	h.mu.Unlock()
	_ = before

	h.dis.RunProactiveCheck(context.Background(), time.Now())
	time.Sleep(100 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("默认关闭时不该主动发言，实际 %d 条", got)
	}
}

// 额度够了、条件都满足时，模型决定说话 → 主动发出并记录理由。
func TestProactiveSpeaksWithinBudget(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"proactive_level":"auto","proactive_ratio":3,"proactive_min_interval_minutes":1,"proactive_quiet_start":0,"proactive_quiet_end":0}`)
	h.seedHumanChat(t, "G1", 6) // 6 条 / 比例 3 = 2 点额度
	h.mu.Lock()
	h.aiProactiveReply = `{"speak":true,"text":"刚才那个链接我也可以帮忙看看","reason":"有人卡在链接上"}`
	h.mu.Unlock()

	h.dis.RunProactiveCheck(context.Background(), time.Now())

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("应主动发一条，实际 %d", len(sent))
	}
	body := sent[0].Body
	text := body.Content
	if text == "" && body.Markdown != nil {
		text = body.Markdown.Content
	}
	if !strings.Contains(text, "链接") {
		t.Fatalf("内容不对：%q", text)
	}

	states, err := h.st.ListProactiveStates(context.Background())
	if err != nil || len(states) != 1 {
		t.Fatalf("状态应落库：%+v err=%v", states, err)
	}
	if states[0].LastReason == "" || states[0].LastSentAt == "" {
		t.Fatalf("应记录发言时间与理由：%+v", states[0])
	}
}

// 静默时段绝不发。
func TestProactiveRespectsQuietHours(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"proactive_level":"auto","proactive_ratio":3,"proactive_min_interval_minutes":1,"proactive_quiet_start":23,"proactive_quiet_end":8}`)
	h.seedHumanChat(t, "G1", 6)
	h.mu.Lock()
	h.aiProactiveReply = `{"speak":true,"text":"深夜不该说话","reason":"测试"}`
	h.mu.Unlock()

	night := time.Date(2026, 10, 30, 3, 0, 0, 0, time.Local)
	h.dis.RunProactiveCheck(context.Background(), night)
	time.Sleep(100 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("静默时段不该发，实际 %d 条", got)
	}
}

// 额度不够（人类消息太少）时不该浪费时间调用模型。
func TestProactiveNeedsCredits(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"proactive_level":"auto","proactive_ratio":10,"proactive_min_interval_minutes":1,"proactive_quiet_start":0,"proactive_quiet_end":0}`)
	h.seedHumanChat(t, "G1", 3) // 3 < 10
	h.dis.RunProactiveCheck(context.Background(), time.Now())
	time.Sleep(100 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("额度不足不该发言，实际 %d 条", got)
	}
}

// 连续两次没人理之后自动降级：不再自发发言。
func TestProactiveStopsAfterSilentStreak(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"proactive_level":"auto","proactive_ratio":3,"proactive_min_interval_minutes":1,"proactive_quiet_start":0,"proactive_quiet_end":0}`)
	h.seedHumanChat(t, "G1", 30)
	if err := h.st.SaveProactiveState(context.Background(), store.ProactiveState{
		GroupOpenID: "G1", SilentStreak: 2,
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	h.mu.Lock()
	h.aiProactiveReply = `{"speak":true,"text":"我还在","reason":"测试"}`
	h.mu.Unlock()

	h.dis.RunProactiveCheck(context.Background(), time.Now())
	time.Sleep(100 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("连续自说自话后应停手，实际 %d 条", got)
	}
}

/* ------------------------------------------------------------------ *
 * 执行能力只对管理员开放
 * ------------------------------------------------------------------ */

// 非管理员：即使 exec 开着，模型也看不到 run_shell。
func TestShellHiddenFromNonAdmin(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"exec_enabled":true,"admin_openids":["UADMIN"]}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("AX1", "G1", "U1", "小明", "跑个命令"))
	h.waitForSent(t, 1)
	h.mu.Lock()
	names := toolNames(h.lastAIBody)
	h.mu.Unlock()
	if names["run_shell"] {
		t.Fatalf("非管理员不该看到 run_shell：%v", names)
	}
}

// 管理员：应当看到。
func TestShellVisibleToAdmin(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"exec_enabled":true,"admin_openids":["UADMIN"]}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("AX2", "G1", "UADMIN", "管理员", "跑个命令"))
	h.waitForSent(t, 1)
	h.mu.Lock()
	names := toolNames(h.lastAIBody)
	h.mu.Unlock()
	if !names["run_shell"] {
		t.Fatalf("管理员应看到 run_shell：%v", names)
	}
}

// 即使模型被诱导去调用，非管理员也执行不了，审计记 rejected。
func TestShellRejectsNonAdminEvenIfCalled(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"exec_enabled":true,"admin_openids":["UADMIN"]}`)
	h.mu.Lock()
	h.aiToolScript = []string{`run_shell|{"command":"echo 不该执行"}`}
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("AX3", "G1", "U1", "小明", "帮我 echo 一下"))
	h.waitForSent(t, 1)

	// 命令没有被执行：工具回给模型的是拒绝说明
	h.mu.Lock()
	msgs, _ := h.lastAIBody["messages"].([]any)
	h.mu.Unlock()
	var toolOutput string
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] == "tool" {
			toolOutput, _ = m["content"].(string)
		}
	}
	if !strings.Contains(toolOutput, "管理员") {
		t.Fatalf("应回拒绝说明，实际 %q", toolOutput)
	}
	if strings.Contains(toolOutput, "不该执行") {
		t.Fatalf("命令不该被执行：%q", toolOutput)
	}

	rows, err := h.st.ListToolCalls(context.Background(), 5, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("应有审计记录：%+v err=%v", rows, err)
	}
	if rows[0].Status != store.ToolCallRejected {
		t.Fatalf("状态应为 rejected，实际 %q（%s）", rows[0].Status, rows[0].Error)
	}
}

/* ------------------------------------------------------------------ *
 * 管理员通过对话改 harness 配置
 * ------------------------------------------------------------------ */

// 非管理员：看不到 settings_write（空管理员名单时谁都看不到）。
func TestSettingsToolsHiddenWithoutAdmin(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), groupAtMessage("ST1", "G1", "U1", "小明", "把温度调到 0.2"))
	h.waitForSent(t, 1)

	h.mu.Lock()
	names := toolNames(h.lastAIBody)
	h.mu.Unlock()
	if names["settings_write"] || names["settings_read"] {
		t.Fatalf("没有管理员名单时不该下发配置工具：%v", names)
	}
}

// 管理员：能读、能写，且立即生效。
func TestAdminCanChangeSettingsByChat(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"admin_openids":["UADMIN"]}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("ST2", "G1", "UADMIN", "管理员", "把温度调到 0.2"))
	h.waitForSent(t, 1)
	h.mu.Lock()
	names := toolNames(h.lastAIBody)
	h.mu.Unlock()
	if !names["settings_write"] || !names["settings_read"] {
		t.Fatalf("管理员应看到配置工具：%v", names)
	}

	// 真正改一次
	h.mu.Lock()
	h.aiToolScript = []string{`settings_write|{"settings":{"temperature":0.2,"humanize_level":"soft"}}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("ST3", "G1", "UADMIN", "管理员", "调低点温度，说话快一点"))
	h.waitForSent(t, 2)

	cur := h.settings.Effective()
	if cur.Temperature != 0.2 {
		t.Fatalf("温度没生效：%v", cur.Temperature)
	}
	if cur.HumanizeLevel != "soft" {
		t.Fatalf("人味档位没生效：%q", cur.HumanizeLevel)
	}

	rows, _ := h.st.ListToolCalls(context.Background(), 5, 0)
	if len(rows) != 1 || rows[0].Tool != "settings_write" || rows[0].Status != store.ToolCallOK {
		t.Fatalf("审计记录不对：%+v", rows)
	}
}

// 越界值要被拒绝，而且配置不变。
func TestSettingsWriteRejectsInvalidValue(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"admin_openids":["UADMIN"]}`)
	before := h.settings.Effective().Temperature

	h.mu.Lock()
	h.aiToolScript = []string{`settings_write|{"settings":{"temperature":9}}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("ST4", "G1", "UADMIN", "管理员", "温度调到 9"))
	h.waitForSent(t, 1)

	if got := h.settings.Effective().Temperature; got != before {
		t.Fatalf("越界值不该被接受：%v", got)
	}
}

// 密钥与管理员名单不能通过聊天改（一个泄密、一个提权）。
func TestSettingsWriteDeniesSecretAndAdminList(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"admin_openids":["UADMIN"]}`)

	for _, patch := range []string{
		`settings_write|{"settings":{"openai_api_key":"sk-xxx"}}`,
		`settings_write|{"settings":{"admin_openids":["U-attacker"]}}`,
	} {
		h.mu.Lock()
		h.aiToolScript = []string{patch}
		h.mu.Unlock()
		h.dis.HandlePayload(context.Background(), groupAtMessage("ST5", "G1", "UADMIN", "管理员", "改一下"))
		h.waitForSent(t, 1)
	}

	if got := h.settings.Effective().AdminOpenIDs; len(got) != 1 || got[0] != "UADMIN" {
		t.Fatalf("管理员名单被改了：%v", got)
	}
	// harness 里预置的是 test-key，被拒后必须原样保留。
	if got := h.settings.Effective().OpenAIAPIKey; got != "test-key" {
		t.Fatalf("密钥被改了：%q", got)
	}
}

// 非管理员强行调用也执行不了。
func TestSettingsWriteRejectsNonAdminEvenIfCalled(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"admin_openids":["UADMIN"]}`)
	before := h.settings.Effective().Temperature

	h.mu.Lock()
	h.aiToolScript = []string{`settings_write|{"settings":{"temperature":0.1}}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("ST6", "G1", "U1", "小明", "把温度调到 0.1"))
	h.waitForSent(t, 1)

	if got := h.settings.Effective().Temperature; got != before {
		t.Fatalf("非管理员不该改得动：%v", got)
	}
	rows, _ := h.st.ListToolCalls(context.Background(), 5, 0)
	if len(rows) != 1 || rows[0].Status != store.ToolCallRejected {
		t.Fatalf("应记为 rejected：%+v", rows)
	}
}

/* ------------------------------------------------------------------ *
 * 后台配置接口的返回契约（前端 bug 回归）
 * ------------------------------------------------------------------ */

// PUT /api/config 必须返回 {settings: {...}}，settings 里是扁平的可编辑项。
// 之前前端把整个 data 当扁平字段读，导致保存后所有控件变空。
func TestConfigUpdateContract(t *testing.T) {
	h := newHarness(t)
	// 后台口令现在存在数据库里，测试直接写进设置。
	if _, _, err := h.settings.Apply(context.Background(), map[string]json.RawMessage{
		"admin_token": json.RawMessage(`"test-admin"`),
	}); err != nil {
		t.Fatalf("set admin token: %v", err)
	}
	srv := admin.New(admin.Deps{
		Config:     h.cfg,
		Settings:   h.settings,
		Store:      h.st,
		Dispatcher: h.dis,
		AI:         h.client,
		Bus:        h.bus,
		Logger:     h.logger,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	put := func(body string) map[string]any {
		req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/config", strings.NewReader(body))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-admin")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var envelope struct {
			OK   bool           `json:"ok"`
			Data map[string]any `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !envelope.OK {
			t.Fatalf("PUT 失败：%+v", envelope)
		}
		return envelope.Data
	}

	data := put(`{"temperature":0.33}`)

	settings, ok := data["settings"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 settings 对象，实际键：%v", keysOf(data))
	}
	if settings["temperature"] != 0.33 {
		t.Fatalf("settings.temperature 不生效：%v", settings["temperature"])
	}
	// 这些是前端 assign() 依赖的扁平字段，缺任何一个都会让控件变空。
	for _, key := range []string{
		"system_prompt", "model", "max_tokens", "context_max_turns", "reply_max_chars",
		"humanize_level", "memory_enabled", "web_enabled", "exec_enabled",
		"markdown_enabled", "tasks_enabled", "proactive_level",
	} {
		if _, present := settings[key]; !present {
			t.Fatalf("settings 缺少扁平字段 %q（前端会把它读成 undefined → 控件变空）", key)
		}
	}
	// 密钥永远不回显
	if v, present := settings["openai_api_key"]; present && v != "" {
		t.Fatalf("密钥不该回显：%v", v)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

/* ------------------------------------------------------------------ *
 * 定时任务按人隔离（同群互不可见、互不可取消）
 * ------------------------------------------------------------------ */

func TestTasksArePrivateWithinAGroup(t *testing.T) {
	h := newHarness(t)
	due := time.Now().Add(time.Hour).Format(time.RFC3339)

	// A 安排一条
	idA, err := h.st.AddTask(context.Background(), store.TaskInput{
		ConversationID: "group-shared:G1", GroupOpenID: "G1", CreatorOpenID: "U-A",
		Text: "A 的提醒", DueAt: due,
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	// B 查列表：看不到 A 的
	h.mu.Lock()
	h.aiToolScript = []string{`list_tasks|{}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("TS1", "G1", "U-B", "小B", "我有哪些提醒"))
	h.waitForSent(t, 1)
	h.mu.Lock()
	msgs, _ := h.lastAIBody["messages"].([]any)
	h.mu.Unlock()
	var toolOut string
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] == "tool" {
			toolOut, _ = m["content"].(string)
		}
	}
	if strings.Contains(toolOut, "A 的提醒") {
		t.Fatalf("B 不该看到 A 的提醒：%q", toolOut)
	}

	// B 尝试取消 A 的：取消不掉
	h.mu.Lock()
	h.aiToolScript = []string{`cancel_task|{"id":` + strconv.FormatInt(idA, 10) + `}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("TS2", "G1", "U-B", "小B", "把那条取消"))
	h.waitForSent(t, 2)
	pending, _ := h.st.ListPendingTasksOfCreator(context.Background(), "U-A", 10)
	if len(pending) != 1 {
		t.Fatalf("A 的提醒被 B 取消了：%+v", pending)
	}

	// A 自己取消：成功
	h.mu.Lock()
	h.aiToolScript = []string{`cancel_task|{"id":` + strconv.FormatInt(idA, 10) + `}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("TS3", "G1", "U-A", "小A", "把那条取消"))
	h.waitForSent(t, 3)
	pending, _ = h.st.ListPendingTasksOfCreator(context.Background(), "U-A", 10)
	if len(pending) != 0 {
		t.Fatalf("A 自己取消后应没有了：%+v", pending)
	}
}

// 每人限额按人算，不是按群算。
func TestTaskLimitIsPerMember(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"tasks_max_per_member":1}`)
	due := time.Now().Add(time.Hour).Format(time.RFC3339)

	if _, err := h.st.AddTask(context.Background(), store.TaskInput{
		GroupOpenID: "G1", CreatorOpenID: "U-A", Text: "A 的第一条", DueAt: due,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	// A 再排一条：被自己的限额拦住
	h.mu.Lock()
	h.aiToolScript = []string{`schedule_task|{"text":"A 的第二条","delay_seconds":600}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("TS4", "G1", "U-A", "小A", "再提醒我一次"))
	h.waitForSent(t, 1)

	all, _ := h.st.ListTasks(context.Background(), store.TaskPending, 10, 0)
	if len(all) != 1 {
		t.Fatalf("A 超出上限仍被安排：%+v", all)
	}

	// B 不受影响
	h.mu.Lock()
	h.aiToolScript = []string{`schedule_task|{"text":"B 的提醒","delay_seconds":600}`}
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("TS5", "G1", "U-B", "小B", "提醒我一下"))
	h.waitForSent(t, 2)

	all, _ = h.st.ListTasks(context.Background(), store.TaskPending, 10, 0)
	if len(all) != 2 {
		t.Fatalf("B 应该能安排（限额按人算）：%+v", all)
	}
}

// ---------------------------------------------------------------------------
// 两阶段生成（stage 1 草稿 → stage 2 无上下文润色 → 发送润色稿）
// ---------------------------------------------------------------------------

// 开启时发送润色稿：断言请求体里 stage2 只有 system + user 草稿两条，没有历史。
func TestPolishSendsThePolishedDraft(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"polish_enabled":true}`)
	h.mu.Lock()
	h.aiReply = "这是主模型的草稿。"
	h.polishReply = "「润色稿」这是本角色说的话。"
	h.mu.Unlock()

	// 先播一段历史，确认 stage2 看不到它。
	h.seedHumanChat(t, "G1", 3)

	h.dis.HandlePayload(context.Background(), groupAtMessage("PL1", "G1", "U1", "小明", "你好"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected one reply, got %d", len(sent))
	}
	if got := sent[0].Body.Content; !strings.Contains(got, "润色稿") {
		t.Fatalf("delivered text should be the polished draft, got %q", got)
	}
	if strings.Contains(sent[0].Body.Content, "主模型的草稿") {
		t.Fatalf("the raw draft must not be delivered: %q", sent[0].Body.Content)
	}

	if n := h.polishCallCount(); n != 1 {
		t.Fatalf("expected exactly one polish call, got %d", n)
	}
	msgs := h.polishMessages(0)
	if len(msgs) != 2 {
		t.Fatalf("stage2 must carry exactly 2 messages (system + draft), got %d: %#v", len(msgs), msgs)
	}
	if msgs[0]["role"] != "system" {
		t.Fatalf("first stage2 message must be the system/persona block: %#v", msgs[0])
	}
	sys, _ := msgs[0]["content"].(string)
	if !strings.Contains(sys, "你是测试助手") {
		t.Fatalf("stage2 system prompt must carry the resolved prompt/persona: %q", sys)
	}
	if !strings.Contains(sys, polishMarker) {
		t.Fatalf("stage2 system prompt must carry the polisher instruction: %q", sys)
	}
	if msgs[1]["role"] != "user" {
		t.Fatalf("second stage2 message must be the draft: %#v", msgs[1])
	}
	if draft, _ := msgs[1]["content"].(string); !strings.Contains(draft, "主模型的草稿") {
		t.Fatalf("stage2 must receive the stage1 draft, got %q", draft)
	}

	// 落库、记忆抽取与审计都用润色稿。
	convID := core.EncodeConversationID("group:G1:U1")
	assistant := h.assistantHistory(t, convID)
	foundPolished := false
	for _, text := range assistant {
		if strings.Contains(text, "润色稿") {
			foundPolished = true
		}
		if strings.Contains(text, "主模型的草稿") {
			t.Fatalf("stored assistant text must be the polished version, got %q", text)
		}
	}
	if !foundPolished {
		t.Fatalf("polished text must be persisted as the assistant turn: %v", assistant)
	}

	recs := h.waitForRecords(t, 1)
	if len(recs) != 1 || !strings.Contains(recs[0].Content, "润色稿") {
		t.Fatalf("audit record must hold the delivered text: %#v", recs)
	}
	if n := h.dis.Metrics().PolishRequests.Load(); n != 1 {
		t.Fatalf("polish_requests = %d, want 1", n)
	}
	if n := h.dis.Metrics().PolishFallbacks.Load(); n != 0 {
		t.Fatalf("polish_fallbacks = %d, want 0", n)
	}
}

// 关闭时直接发送草稿，且根本不会发起 stage2 请求。
func TestPolishDisabledSendsTheDraft(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"polish_enabled":false}`)
	h.mu.Lock()
	h.aiReply = "没有润色的原始草稿。"
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("PL2", "G1", "U1", "小明", "你好"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected one reply, got %d", len(sent))
	}
	if got := sent[0].Body.Content; got != "没有润色的原始草稿。" {
		t.Fatalf("close stage should deliver the draft verbatim, got %q", got)
	}
	if n := h.polishCallCount(); n != 0 {
		t.Fatalf("no polish call may go out while the switch is off, got %d", n)
	}
	// 跳过（开关关）既不是一次 stage2 请求，也不是一次失败回退。
	if n := h.dis.Metrics().PolishRequests.Load(); n != 0 {
		t.Fatalf("polish_requests = %d, want 0", n)
	}
	if n := h.dis.Metrics().PolishFallbacks.Load(); n != 0 {
		t.Fatalf("polish_fallbacks = %d, want 0", n)
	}
}

// 润色失败（500 / 空结果 / 超时）都必须回退草稿，且审计仍是 replied。
func TestPolishFailuresFallBackToTheDraft(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness)
	}{
		{"http 500", func(h *harness) { h.polishStatus = http.StatusInternalServerError }},
		{"empty result", func(h *harness) { h.polishEmpty = true }},
		{"timeout", func(h *harness) {
			// polish_timeout_seconds 的下限是 5s，所以把超时压到下限、
			// 再让假上游睡得比它久一点，走到「stage2 超时 → 回退草稿」这条路径。
			h.apply(h.t, `{"polish_timeout_seconds":5,"request_timeout_seconds":90}`)
			h.polishDelay = 6 * time.Second
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.apply(t, `{"polish_enabled":true}`)
			h.mu.Lock()
			h.aiReply = "失败时也要发出去的草稿。"
			tc.setup(h)
			h.mu.Unlock()

			h.dis.HandlePayload(context.Background(), groupAtMessage("PLF1", "G1", "U1", "小明", "你好"))
			sent := h.waitForSent(t, 1)
			if len(sent) != 1 {
				t.Fatalf("a polish failure must still deliver one message, got %d", len(sent))
			}
			if got := sent[0].Body.Content; got != "失败时也要发出去的草稿。" {
				t.Fatalf("fallback must send the draft, got %q", got)
			}
			if n := h.dis.Metrics().PolishFallbacks.Load(); n != 1 {
				t.Fatalf("polish_fallbacks = %d, want 1", n)
			}

			recs := h.waitForRecords(t, 1)
			if len(recs) != 1 || recs[0].Status != "replied" {
				t.Fatalf("the turn must still be recorded as replied: %#v", recs)
			}
			// 回退的草稿同样要落库，否则下一轮上下文缺一条。
			convID := core.EncodeConversationID("group:G1:U1")
			if got := h.assistantHistory(t, convID); len(got) != 1 || got[0] != "失败时也要发出去的草稿。" {
				t.Fatalf("fallback draft must be persisted: %v", got)
			}
		})
	}
}

// 工具调用场景：stage1 跑完整个工具循环，stage2 只拿到最终草稿。
func TestPolishAfterToolCallsSeesOnlyTheFinalDraft(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"polish_enabled":true}`)
	h.mu.Lock()
	h.aiToolScript = []string{"calc"}
	h.aiReply = "工具算完之后的最终草稿。"
	h.polishReply = "工具场景的润色稿。"
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("PLT1", "G1", "U1", "小明", "帮我算一下"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected one reply, got %d", len(sent))
	}
	if got := sent[0].Body.Content; got != "工具场景的润色稿。" {
		t.Fatalf("tool turn must deliver the polished draft, got %q", got)
	}
	if n := h.polishCallCount(); n != 1 {
		t.Fatalf("expected exactly one polish call, got %d", n)
	}
	msgs := h.polishMessages(0)
	if len(msgs) != 2 {
		t.Fatalf("stage2 must not see tool messages, got %d messages: %#v", len(msgs), msgs)
	}
	for _, m := range msgs {
		if m["role"] == "tool" {
			t.Fatalf("tool results must never reach the polisher: %#v", msgs)
		}
		if _, has := m["tool_calls"]; has {
			t.Fatalf("tool call metadata must never reach the polisher: %#v", msgs)
		}
	}
	if draft, _ := msgs[1]["content"].(string); !strings.Contains(draft, "最终草稿") {
		t.Fatalf("stage2 must receive only the final draft, got %q", draft)
	}
}

// 按群关闭：G1 关、G2 跟着全局开。
func TestPolishPerGroupSwitch(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"polish_enabled":true,"group_overrides":[{"group_openid":"G1","polish":false}]}`)
	h.mu.Lock()
	h.aiReply = "按群测试的草稿。"
	h.polishReply = "按群测试的润色稿。"
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("PLG1", "G1", "U1", "小明", "你好"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 || sent[0].Body.Content != "按群测试的草稿。" {
		t.Fatalf("G1 has polish off, expected the draft, got %#v", sent)
	}
	if n := h.polishCallCount(); n != 0 {
		t.Fatalf("G1 must not issue a polish call, got %d", n)
	}

	h.dis.HandlePayload(context.Background(), groupAtMessage("PLG2", "G2", "U2", "小红", "你好"))
	sent = h.waitForSent(t, 2)
	if len(sent) != 2 || sent[1].Body.Content != "按群测试的润色稿。" {
		t.Fatalf("G2 follows the global switch, expected the polished draft, got %#v", sent)
	}
	if n := h.polishCallCount(); n != 1 {
		t.Fatalf("G2 must issue exactly one polish call, got %d", n)
	}
}

// polish_min_chars：草稿太短就跳过润色（省一次调用），且不算「失败」。
func TestPolishMinCharsSkipsShortDrafts(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"polish_enabled":true,"polish_min_chars":50}`)
	h.mu.Lock()
	h.aiReply = "短。"
	h.polishReply = "不该出现的润色稿。"
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("PLS1", "G1", "U1", "小明", "你好"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 || sent[0].Body.Content != "短。" {
		t.Fatalf("a draft below polish_min_chars must be sent as-is, got %#v", sent)
	}
	if n := h.polishCallCount(); n != 0 {
		t.Fatalf("short drafts must skip the polish call, got %d", n)
	}

	// 超过阈值后恢复正常润色。
	h.mu.Lock()
	h.aiReply = strings.Repeat("长", 60) + "的草稿。"
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("PLS2", "G2", "U2", "小红", "你好"))
	sent = h.waitForSent(t, 2)
	if len(sent) != 2 || sent[1].Body.Content != "不该出现的润色稿。" {
		t.Fatalf("a long draft must be polished, got %#v", sent)
	}
	if n := h.polishCallCount(); n != 1 {
		t.Fatalf("expected one polish call for the long draft, got %d", n)
	}
}

// 手动发送 / 回复失败兜底文案都不能经过润色（它们不是 AI 正文）。
func TestNonAIBodiesAreNeverPolished(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"polish_enabled":true}`)
	h.mu.Lock()
	h.polishReply = "不该出现的润色稿。"
	h.mu.Unlock()

	// 1) 模型失败 → 兜底文案，stage1 都没有成功，自然没有 stage2。
	h.mu.Lock()
	h.aiFail = true
	h.mu.Unlock()
	h.dis.HandlePayload(context.Background(), groupAtMessage("PLN1", "G1", "U1", "小明", "你好"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected the fallback notice, got %d", len(sent))
	}
	if strings.Contains(sent[0].Body.Content, "润色稿") {
		t.Fatalf("the failure notice must never be polished: %q", sent[0].Body.Content)
	}
	if n := h.polishCallCount(); n != 0 {
		t.Fatalf("a failed stage1 must not reach stage2, got %d calls", n)
	}
	h.mu.Lock()
	h.aiFail = false
	h.mu.Unlock()

	// 2) 手动发送：后台操作员的原文必须原样出去。
	convID := core.EncodeConversationID("group:G1:U1")
	if _, err := h.dis.SendManual(context.Background(), convID, "手动发送的原文", 0); err != nil {
		t.Fatalf("manual send: %v", err)
	}
	if n := h.polishCallCount(); n != 0 {
		t.Fatalf("manual send must not be polished, got %d calls", n)
	}
	sent = h.sentMessages()
	if got := sent[len(sent)-1].Body.Content; got != "手动发送的原文" {
		t.Fatalf("manual send must go out verbatim, got %q", got)
	}
}

// --- 自主加入（join gate）----------------------------------------------------

// joinGateJoinVerdict builds a valid verdict that joins, with one topic.
func joinGateJoinVerdict(confidence float64, reason, title string) string {
	return `{"topics":[{"id":"t1","title":"` + title + `","summary":"摘要","message_ids":[],"type":"help"}],"current_topic_id":"t1","addressed_to":"group","action":"join","confidence":` +
		strconv.FormatFloat(confidence, 'f', 2, 64) + `,"reason":"` + reason + `"}`
}

// joinGateVerdictWithAction builds a verdict for one specific action.
func joinGateVerdictWithAction(action, addressedTo string) string {
	return `{"topics":[{"id":"t1","title":"闲聊","summary":"只是闲聊","message_ids":[],"type":"chat"}],"current_topic_id":"t1","addressed_to":"` +
		addressedTo + `","action":"` + action + `","confidence":0.8,"reason":"测试判定"}`
}

const joinGateSilentVerdict = `{"topics":[{"id":"t1","title":"闲聊","summary":"只是闲聊","message_ids":[]}],"current_topic_id":"t1","addressed_to":"someone_else","action":"skip","confidence":0.2,"reason":"没有需要接住的东西"}`

func (h *harness) waitForJoinDecisions(t *testing.T, groupOpenID string, n int) []store.JoinDecision {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := h.joinDecisions(t, groupOpenID)
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return h.joinDecisions(t, groupOpenID)
}

// 开启 join gate 后，未 @ 的群消息被判定加入时也要回复（at_only 不再是硬门）。
func TestJoinGateRepliesToUnaddressedMessage(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)
	h.setJoinGateReply(joinGateJoinVerdict(0.9, "有人明确求助", "Go 求助"))

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JG1", "G1", "U1", "小明", "有人会 Go 吗"))

	if sent := h.waitForSent(t, 1); len(sent) != 1 {
		t.Fatalf("join gate should deliver one reply, got %d", len(sent))
	}
	if got := h.joinGateCallCount(); got != 1 {
		t.Fatalf("join gate calls = %d, want 1", got)
	}
	decisions := h.waitForJoinDecisions(t, "G1", 1)
	if len(decisions) != 1 || !decisions[0].Joined || decisions[0].Action != "join" {
		t.Fatalf("expected one joined decision, got %#v", decisions)
	}
	if decisions[0].Confidence != 0.9 || decisions[0].Preview != "有人会 Go 吗" {
		t.Fatalf("decision fields wrong: %#v", decisions[0])
	}
	metrics := h.dis.Metrics()
	if metrics.JoinGateEvals.Load() != 1 || metrics.JoinGateJoins.Load() != 1 {
		t.Fatalf("metrics evals/joins = %d/%d, want 1/1",
			metrics.JoinGateEvals.Load(), metrics.JoinGateJoins.Load())
	}
}

// join=false 时保持沉默，但每次评估都要留一条可解释的 decision。
func TestJoinGateSilentVerdictKeepsTheBotQuiet(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)
	h.setJoinGateReply(joinGateSilentVerdict)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JG2", "G1", "U1", "小明", "今天天气不错"))

	decisions := h.waitForJoinDecisions(t, "G1", 1)
	if len(decisions) != 1 || decisions[0].Joined || decisions[0].Action != "skip" {
		t.Fatalf("expected one skipped decision, got %#v", decisions)
	}
	if decisions[0].Reason != "没有需要接住的东西" {
		t.Fatalf("decision reason lost: %q", decisions[0].Reason)
	}
	time.Sleep(200 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("a false verdict must not send anything, got %d", got)
	}
	metrics := h.dis.Metrics()
	if metrics.JoinGateEvals.Load() != 1 || metrics.JoinGateSkips.Load() != 1 || metrics.JoinGateJoins.Load() != 0 {
		t.Fatalf("metrics evals/skips/joins = %d/%d/%d, want 1/1/0",
			metrics.JoinGateEvals.Load(), metrics.JoinGateSkips.Load(), metrics.JoinGateJoins.Load())
	}
	if got := metrics.JoinGateWaits.Load(); got != 0 {
		t.Fatalf("a skip verdict must not count as a wait, got %d", got)
	}
}

// 默认关闭：未 @ 消息既不评估也不回复，行为与 v0.2.0 一致。
func TestJoinGateOffByDefaultKeepsOldBehaviour(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), groupAnyMessage("JG3", "G1", "U1", "小明", "随便聊聊"))
	time.Sleep(300 * time.Millisecond)

	if got := h.joinGateCallCount(); got != 0 {
		t.Fatalf("join gate must stay dormant by default, got %d calls", got)
	}
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("at_only must still drop unaddressed messages, got %d sends", got)
	}
	metrics := h.dis.Metrics()
	if metrics.JoinGateEvals.Load() != 0 || metrics.JoinGateSkips.Load() != 0 {
		t.Fatalf("disabled gate must not move its counters: %+v", metrics.Snapshot())
	}
}

// @ 消息永远跳过 join gate。
func TestJoinGateSkipsAddressedMessages(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("JG4", "G1", "U1", "小明", "你好"))

	if sent := h.waitForSent(t, 1); len(sent) != 1 {
		t.Fatalf("@ message should reply normally, got %d", len(sent))
	}
	if got := h.joinGateCallCount(); got != 0 {
		t.Fatalf("@ messages must not be evaluated, got %d calls", got)
	}
	if decisions := h.joinDecisions(t, "G1"); len(decisions) != 0 {
		t.Fatalf("@ messages must not write a decision: %#v", decisions)
	}
}

// 全局关是总开关：按群 join_gate=true 也打不开。
func TestJoinGateGroupOverrideCannotEnableGlobalOff(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"group_overrides":[{"group_openid":"G1","join_gate":true}]}`)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JG5", "G1", "U1", "小明", "有人吗"))
	time.Sleep(300 * time.Millisecond)

	if got := h.joinGateCallCount(); got != 0 {
		t.Fatalf("global off must win, got %d gate calls", got)
	}
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("global off must keep at_only behaviour, got %d sends", got)
	}
}

// 群覆盖 false 可以关闭全局开启的功能。
func TestJoinGateGroupOverrideCanDisable(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"group_overrides":[{"group_openid":"G1","join_gate":false}]}`)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JG6", "G1", "U1", "小明", "有人吗"))
	time.Sleep(300 * time.Millisecond)

	if got := h.joinGateCallCount(); got != 0 {
		t.Fatalf("group override false must disable the gate, got %d calls", got)
	}
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("disabled gate must not reply, got %d sends", got)
	}
}

// 非法 JSON fail-closed：不回复，但写一条 gate error decision。
func TestJoinGateFailsClosedOnBadJSON(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)
	h.setJoinGateReply("我觉得可以加入，但没有 JSON")

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JG7", "G1", "U1", "小明", "有人会 Go 吗"))

	decisions := h.waitForJoinDecisions(t, "G1", 1)
	if len(decisions) != 1 || decisions[0].Joined {
		t.Fatalf("expected one fail-closed decision, got %#v", decisions)
	}
	if !strings.HasPrefix(decisions[0].Reason, "gate error:") {
		t.Fatalf("fail-closed reason should be explicit, got %q", decisions[0].Reason)
	}
	time.Sleep(150 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("a parse failure must not send anything, got %d", got)
	}
	if got := h.dis.Metrics().JoinGateErrors.Load(); got != 1 {
		t.Fatalf("join gate errors = %d, want 1", got)
	}
	if got := h.dis.Metrics().JoinGateJoins.Load(); got != 0 {
		t.Fatalf("a parse failure must not count as a join, got %d", got)
	}
}

// 评估冷却：第二次评估在冷却期内直接跳过，且不再调用模型。
func TestJoinGateCooldownSkipsWithoutCallingTheModel(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm","join_gate_eval_cooldown_seconds":600}`)
	h.setJoinGateReply(joinGateSilentVerdict)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JG8", "G1", "U1", "小明", "第一句话"))
	if got := h.waitForJoinGateCalls(t, 1); got != 1 {
		t.Fatalf("first message should be evaluated, got %d calls", got)
	}
	h.waitForJoinDecisions(t, "G1", 1)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JG9", "G1", "U1", "小明", "第二句话"))

	decisions := h.waitForJoinDecisions(t, "G1", 2)
	if len(decisions) != 2 {
		t.Fatalf("cooldown skip must still record a decision, got %d", len(decisions))
	}
	if !strings.HasPrefix(decisions[0].Reason, "cooldown:") {
		t.Fatalf("newest decision should explain the cooldown, got %q", decisions[0].Reason)
	}
	if got := h.joinGateCallCount(); got != 1 {
		t.Fatalf("a cooldown skip must not call the model again, got %d calls", got)
	}
	if got := h.dis.Metrics().JoinGateSkips.Load(); got != 2 {
		t.Fatalf("skips = %d, want 2", got)
	}
}

// 话题缓存：解析成功后落库，并在 15 分钟内的后续回复里作为背景注入。
func TestJoinGateTopicsBecomeBackground(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)
	h.setJoinGateReply(joinGateJoinVerdict(0.9, "话题延续", "部署讨论"))

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JG10", "G1", "U1", "小明", "容器怎么部署比较好"))
	if sent := h.waitForSent(t, 1); len(sent) != 1 {
		t.Fatalf("expected the gate reply, got %d", len(sent))
	}

	topics, err := h.st.GetJoinTopics(context.Background(), "G1")
	if err != nil {
		t.Fatalf("get join topics: %v", err)
	}
	if topics == nil || !strings.Contains(topics.TopicsJSON, "部署讨论") {
		t.Fatalf("topics were not persisted: %#v", topics)
	}
	if got := h.dis.Metrics().JoinGateTopics.Load(); got != 1 {
		t.Fatalf("join gate topics counter = %d, want 1", got)
	}

	// 后续 @ 消息的回复应带上话题背景。
	h.dis.HandlePayload(context.Background(), groupAtMessage("JG11", "G1", "U1", "小明", "那你觉得呢"))
	if sent := h.waitForSent(t, 2); len(sent) != 2 {
		t.Fatalf("expected the second reply, got %d", len(sent))
	}
	probe := h.systemPrompt(t)
	if !strings.Contains(probe.text, "群里最近的话题（仅背景，不要强行转回）") {
		t.Fatalf("topic background block missing:\n%s", probe.text)
	}
	if !strings.Contains(probe.text, "部署讨论") {
		t.Fatalf("topic title missing from the background block:\n%s", probe.text)
	}
}

// join_gate_model 只作用于判定调用；判定请求的参数也是契约的一部分。
func TestJoinGateUsesConfiguredModelAndParameters(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm","join_gate_model":"gate-model"}`)
	h.setJoinGateReply(joinGateSilentVerdict)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JG12", "G1", "U1", "小明", "试试看"))
	if got := h.waitForJoinGateCalls(t, 1); got != 1 {
		t.Fatalf("expected one gate call, got %d", got)
	}

	h.mu.Lock()
	body := h.lastJoinGateBody
	h.mu.Unlock()
	if body == nil {
		t.Fatal("the gate request was not captured")
	}
	if got, _ := body["model"].(string); got != "gate-model" {
		t.Fatalf("gate model = %q, want gate-model", got)
	}
	if got, _ := body["temperature"].(float64); got != 0.1 {
		t.Fatalf("gate temperature = %v, want 0.1", got)
	}
	if got, _ := body["max_tokens"].(float64); got != 600 {
		t.Fatalf("gate max_tokens = %v, want 600", got)
	}
}

// groupAnyMessageQuoting builds an E2 payload that quotes another message.
// refMsgIndex is the platform token of the message being replied to.
func groupAnyMessageQuoting(msgID, groupOpenID, memberOpenID, name, content, refMsgIndex string) *qq.Payload {
	d, _ := json.Marshal(map[string]any{
		"id":           msgID,
		"content":      content,
		"group_openid": groupOpenID,
		"timestamp":    "2026-07-21T10:00:00+08:00",
		"message_type": 0,
		"message_scene": map[string]any{
			"source": "default",
			"ext":    []string{"ref_msg_idx=" + refMsgIndex, "msg_idx=REFIDX_" + msgID},
		},
		"author": map[string]any{
			"id":            memberOpenID,
			"member_openid": memberOpenID,
			"username":      name,
			"bot":           false,
		},
	})
	return &qq.Payload{Op: qq.OpDispatch, T: qq.EventGroupMessageCreate, ID: "EV-" + msgID, D: d}
}

// seedBotMessage stores an assistant message of the bot in a group and returns
// its platform quote index, which is what a member quotes to reply to it.
func seedBotMessage(t *testing.T, h *harness, groupOpenID, quoteIndex string) string {
	t.Helper()
	ctxBg := context.Background()
	convID := core.EncodeConversationID("group:" + groupOpenID + ":U1")
	if err := h.st.EnsureConversation(ctxBg, &store.Conversation{
		ID: convID, Scene: store.SceneGroupMember, GroupOpenID: groupOpenID, MemberOpenID: "U1",
	}); err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if err := h.st.AppendMessage(ctxBg, store.MessageInput{
		ConversationID: convID, Role: "assistant", Content: "我之前说过的话",
		Kind: store.KindDialogue, QuoteIndex: quoteIndex,
	}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	return quoteIndex
}

// 引用机器人：命中 store.IsBotQuote 后与 @ 等价——直接回复，且不进入 join gate。
func TestJoinGateQuoteOfBotSkipsGateAndReplies(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)
	h.setJoinGateReply(joinGateSilentVerdict)
	seedBotMessage(t, h, "G1", "REFIDX_BOTQ")

	h.dis.HandlePayload(context.Background(), groupAnyMessageQuoting("JQ1", "G1", "U1", "小明", "这个怎么解释", "REFIDX_BOTQ"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("quoting the bot must produce exactly one reply, got %d", len(sent))
	}
	if got := h.joinGateCallCount(); got != 0 {
		t.Fatalf("a bot quote is addressed; the gate must not run, got %d calls", got)
	}
	if decisions := h.joinDecisions(t, "G1"); len(decisions) != 0 {
		t.Fatalf("a bot quote must not write a join decision: %#v", decisions)
	}
}

// 无匹配的引用：行为不变 —— 未 @ 群消息按原语义处理，开启闸门时照常评估。
func TestJoinGateUnmatchedQuoteKeepsBehaviour(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)
	h.setJoinGateReply(joinGateSilentVerdict)
	seedBotMessage(t, h, "G1", "REFIDX_BOTQ")

	h.dis.HandlePayload(context.Background(), groupAnyMessageQuoting("JQ2", "G1", "U1", "小明", "这个怎么解释", "REFIDX_SOMEONE_ELSE"))

	if got := h.waitForJoinGateCalls(t, 1); got != 1 {
		t.Fatalf("an unmatched quote is still an unaddressed message, got %d gate calls", got)
	}
	decisions := h.waitForJoinDecisions(t, "G1", 1)
	if len(decisions) != 1 || decisions[0].Action != "skip" {
		t.Fatalf("expected one skipped decision, got %#v", decisions)
	}
	time.Sleep(150 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("a silent verdict must stay silent, got %d sends", got)
	}
}

// 引用识别关闭闸门时也必须工作：命中即 @ 语义，不受 join_gate_enabled 影响。
func TestQuoteOfBotWorksWithGateDisabled(t *testing.T) {
	h := newHarness(t)
	seedBotMessage(t, h, "G1", "REFIDX_BOTQ")

	h.dis.HandlePayload(context.Background(), groupAnyMessageQuoting("JQ3", "G1", "U1", "小明", "还在吗", "REFIDX_BOTQ"))

	if sent := h.waitForSent(t, 1); len(sent) != 1 {
		t.Fatalf("quoting the bot must reply even with the gate off, got %d", len(sent))
	}
	if got := h.joinGateCallCount(); got != 0 {
		t.Fatalf("join gate is off; it must not run, got %d calls", got)
	}
}

// wait 只写 decision、不回复，并计进 join_gate_waits（同时仍算 skip）。
func TestJoinGateWaitVerdictIsSilentAndCounted(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)
	h.setJoinGateReply(joinGateVerdictWithAction("wait", "someone_else"))

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JW1", "G1", "U1", "小明", "两个人正在聊"))

	decisions := h.waitForJoinDecisions(t, "G1", 1)
	if len(decisions) != 1 || decisions[0].Action != "wait" || decisions[0].Joined {
		t.Fatalf("expected one wait decision, got %#v", decisions)
	}
	time.Sleep(150 * time.Millisecond)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("wait must not send anything, got %d", got)
	}
	metrics := h.dis.Metrics()
	if metrics.JoinGateWaits.Load() != 1 || metrics.JoinGateSkips.Load() != 1 {
		t.Fatalf("waits/skips = %d/%d, want 1/1",
			metrics.JoinGateWaits.Load(), metrics.JoinGateSkips.Load())
	}
	if metrics.JoinGateJoins.Load() != 0 {
		t.Fatalf("wait must not count as a join, got %d", metrics.JoinGateJoins.Load())
	}
}

// wait 重试：评估冷却仍生效（新消息只写 cooldown decision，不调模型）；
// 把上一条 wait 的评估时间回拨到重试窗口之外后，下一条新消息必须提前重评估。
func TestJoinGateWaitRetryReevaluatesAfterTheWindow(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm","join_gate_eval_cooldown_seconds":600,"join_gate_wait_retry_seconds":30}`)
	h.setJoinGateReply(joinGateVerdictWithAction("wait", "group"))

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JWR1", "G1", "U1", "小明", "这个话题还没聊完"))
	if got := h.waitForJoinGateCalls(t, 1); got != 1 {
		t.Fatalf("the first message should be evaluated, got %d calls", got)
	}
	h.waitForJoinDecisions(t, "G1", 1)

	// 冷却期内：只写 cooldown decision，不调模型。
	h.dis.HandlePayload(context.Background(), groupAnyMessage("JWR2", "G1", "U1", "小明", "我也觉得"))
	decisions := h.waitForJoinDecisions(t, "G1", 2)
	if len(decisions) != 2 {
		t.Fatalf("a cooldown skip must still record a decision, got %d", len(decisions))
	}
	if !strings.HasPrefix(decisions[0].Reason, "cooldown:") {
		t.Fatalf("newest decision should explain the cooldown, got %q", decisions[0].Reason)
	}
	if got := h.joinGateCallCount(); got != 1 {
		t.Fatalf("wait retry only applies after its window, got %d calls", got)
	}

	// 回拨上一条真实评估：模拟 join_gate_wait_retry_seconds 已经过去。
	if _, err := h.st.DB().ExecContext(context.Background(),
		`UPDATE join_decisions SET created_at = ? WHERE group_openid = ? AND model <> ''`,
		time.Now().Add(-90*time.Second).Format(time.RFC3339), "G1"); err != nil {
		t.Fatalf("backdate the wait decision: %v", err)
	}

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JWR3", "G1", "U1", "小明", "现在有新进展了"))
	if got := h.waitForJoinGateCalls(t, 2); got != 2 {
		t.Fatalf("a wait older than the retry window must re-evaluate, got %d calls", got)
	}
	if sent := h.sentMessages(); len(sent) != 0 {
		t.Fatalf("a wait verdict must stay silent, got %d sends", len(sent))
	}
}

// lastJoinGateUserText is the user message of the newest gate call, which is
// where the window and the previous-topic block are rendered.
func (h *harness) lastJoinGateUserText(t *testing.T) string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	msgs, _ := h.lastJoinGateBody["messages"].([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] != "user" {
			continue
		}
		text, _ := m["content"].(string)
		return text
	}
	return ""
}

// 上一次分析的话题（含状态）必须作为「上次话题」回灌给闸门模型：
// closed 的话题明确标注「已结束，不要提」，避免模型旧事重提。
func TestJoinGateFeedsPreviousTopicsWithStatus(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)
	h.setJoinGateReply(joinGateSilentVerdict)
	seed := `[
		{"id":"t1","title":"容器部署","summary":"在聊部署","status":"active","message_ids":[]},
		{"id":"t2","title":"旧账单","summary":"早就结了","status":"closed","message_ids":[]}
	]`
	if err := h.st.SaveJoinTopics(context.Background(), "G1", seed, time.Now().Add(-2*time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatalf("seed topics: %v", err)
	}

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JPT1", "G1", "U1", "小明", "再聊聊部署的事"))
	if got := h.waitForJoinGateCalls(t, 1); got != 1 {
		t.Fatalf("expected one gate call, got %d", got)
	}
	text := h.lastJoinGateUserText(t)
	if !strings.Contains(text, "上次话题") {
		t.Fatalf("the gate input lost the previous-topic block:\n%s", text)
	}
	if !strings.Contains(text, "容器部署 [进行中]") {
		t.Fatalf("active topic not fed with its label:\n%s", text)
	}
	if !strings.Contains(text, "旧账单 [已结束，不要提]") {
		t.Fatalf("closed topic not fed as closed:\n%s", text)
	}
}

// 保存前跑年龄兜底：模型没给 status 的话题落库时是 active，且带 resumable。
func TestJoinGateSavedTopicsCarryLifecycle(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)
	h.setJoinGateReply(joinGateSilentVerdict)

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JPT2", "G1", "U1", "小明", "今天我们聊点别的"))
	h.waitForJoinDecisions(t, "G1", 1)

	topics, err := h.st.GetJoinTopics(context.Background(), "G1")
	if err != nil || topics == nil {
		t.Fatalf("topics not saved: %v %#v", err, topics)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(topics.TopicsJSON), &parsed); err != nil || len(parsed) == 0 {
		t.Fatalf("saved topics are not JSON: %v %q", err, topics.TopicsJSON)
	}
	if parsed[0]["status"] != "active" || parsed[0]["resumable"] != true {
		t.Fatalf("a fresh topic must be saved as active/resumable: %#v", parsed[0])
	}
}

// 全部 closed 的话题缓存不注入回复侧背景：没有可说的旧事。
func TestJoinGateAllClosedTopicsAreNotInjected(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)
	closed := `[{"id":"t1","title":"旧账单","summary":"早就结了","status":"closed"}]`
	if err := h.st.SaveJoinTopics(context.Background(), "G1", closed, time.Now().Add(-time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatalf("seed topics: %v", err)
	}

	h.dis.HandlePayload(context.Background(), groupAtMessage("JPT3", "G1", "U1", "小明", "你好"))
	if sent := h.waitForSent(t, 1); len(sent) != 1 {
		t.Fatalf("expected one reply, got %d", len(sent))
	}
	if probe := h.systemPrompt(t); strings.Contains(probe.text, "群里最近的话题") {
		t.Fatalf("an all-closed cache must not be injected:\n%s", probe.text)
	}
}

// 年龄兜底也要在 e2e 里成立：缓存里的 active 话题若只有旧的分析时间、
// 窗口里也没有它的消息，闸门输入必须已经把它降级成 closed。
func TestJoinGatePreviousTopicAgedToClosed(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)
	h.setJoinGateReply(joinGateSilentVerdict)
	seed := `[{"id":"t1","title":"旧账单","summary":"早就结了","status":"active","message_ids":[]}]`
	old := time.Now().Add(-200 * time.Minute).Format(time.RFC3339)
	if err := h.st.SaveJoinTopics(context.Background(), "G1", seed, old); err != nil {
		t.Fatalf("seed topics: %v", err)
	}

	h.dis.HandlePayload(context.Background(), groupAnyMessage("JPT4", "G1", "U1", "小明", "说个新话题"))
	if got := h.waitForJoinGateCalls(t, 1); got != 1 {
		t.Fatalf("expected one gate call, got %d", got)
	}
	text := h.lastJoinGateUserText(t)
	if !strings.Contains(text, "旧账单 [已结束，不要提]") {
		t.Fatalf("an aged active topic must be fed as closed:\n%s", text)
	}
}

// 闸门输入里要能看出「这条在回复谁」：被引用的窗口消息与当前消息都要标注。
func TestJoinGateContextAnnotatesQuotedWindowMessage(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"join_gate_enabled":true,"join_gate_mode":"llm"}`)
	h.setJoinGateReply(joinGateSilentVerdict)
	ctxBg := context.Background()
	convID := core.EncodeConversationID("group:G1:U1")
	if err := h.st.EnsureConversation(ctxBg, &store.Conversation{
		ID: convID, Scene: store.SceneGroupMember, GroupOpenID: "G1", MemberOpenID: "U1",
	}); err != nil {
		t.Fatalf("conversation: %v", err)
	}
	quotedID, err := h.st.AppendMessageID(ctxBg, store.MessageInput{
		ConversationID: convID, Role: "user", Content: "有人会 Go 吗", Speaker: "小明",
		SpeakerOpenID: "U1", Kind: store.KindObserved, QuoteIndex: "REFIDX_Q1",
	})
	if err != nil {
		t.Fatalf("append quoted message: %v", err)
	}

	h.dis.HandlePayload(context.Background(), groupAnyMessageQuoting("JQ4", "G1", "U2", "小红", "我会一点", "REFIDX_Q1"))
	if got := h.waitForJoinGateCalls(t, 1); got != 1 {
		t.Fatalf("expected one gate call, got %d", got)
	}
	text := h.lastJoinGateUserText(t)
	if !strings.Contains(text, "（回复 [#"+strconv.FormatInt(quotedID, 10)+"]）") {
		t.Fatalf("the current message's quote target is not annotated:\n%s", text)
	}
	if !strings.Contains(text, "（当前消息引用了这条）") {
		t.Fatalf("the quoted window line is not marked:\n%s", text)
	}
}
