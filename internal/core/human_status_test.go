package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/media"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

func newHumanStatusTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "human-status.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.AppendMessage(context.Background(), store.MessageInput{
		ConversationID: "conv-1", Role: "user", Kind: store.KindDialogue,
		Content: "开黑中，先不聊", Speaker: "小明", SpeakerOpenID: "U1",
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	return st
}

func humanStatusTestDispatcher(t *testing.T, st *store.Store, client *ai.Client) *Dispatcher {
	t.Helper()
	return &Dispatcher{
		st:    st,
		ai:    client,
		aiSem: newDynamicSem(4),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestRefreshHumanStatusWithoutAI(t *testing.T) {
	d := &Dispatcher{aiSem: newDynamicSem(1)}
	err := d.RefreshHumanStatus(context.Background(), convTarget{ID: "conv-1"}, nil, settings.Settings{Model: "test"})
	if !errors.Is(err, errHumanStatusNoAI) {
		t.Fatalf("err = %v, want errHumanStatusNoAI", err)
	}
	if got := d.Metrics().Snapshot(); got.HumanStatusFailures != 1 || got.HumanStatusRuns != 0 {
		t.Fatalf("counters = %+v, want 1 failure and 0 runs", got)
	}
}

func TestRefreshHumanStatusCountsFailureOnce(t *testing.T) {
	st := newHumanStatusTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded: postgres://user:secret@db/bot", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	d := humanStatusTestDispatcher(t, st, ai.NewClient(srv.URL, "sk-test", srv.Client()))

	s := settings.Settings{Model: "test-model", HumanStatusTTLMinutes: 120}
	err := d.RefreshHumanStatus(context.Background(), convTarget{ID: "conv-1"}, nil, s)
	if err == nil {
		t.Fatal("expected a model failure")
	}
	got := d.Metrics().Snapshot()
	if got.HumanStatusRuns != 1 || got.HumanStatusFailures != 1 {
		t.Fatalf("counters = %+v, want runs=1 failures=1", got)
	}
	if d.aiSem.InUse() != 0 {
		t.Fatalf("ai semaphore leaked: in use = %d", d.aiSem.InUse())
	}
	if reason := humanStatusFailureReason(err); strings.Contains(reason, "secret") || reason != "模型服务不可用" {
		t.Fatalf("reason = %q, want a sanitized reason", reason)
	}
}

func TestRefreshHumanStatusSuccessCountsOnce(t *testing.T) {
	st := newHumanStatusTestStore(t)
	verdict := "[{\"member_openid\":\"U1\",\"display_name\":\"小明\",\"status\":\"打游戏\",\"confidence\":0.8,\"evidence\":\"刚说在开黑\"}]"
	payload, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"message":       map[string]string{"content": verdict},
			"finish_reason": "stop",
		}},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	d := humanStatusTestDispatcher(t, st, ai.NewClient(srv.URL, "sk-test", srv.Client()))

	s := settings.Settings{Model: "test-model", HumanStatusTTLMinutes: 120}
	if err := d.RefreshHumanStatus(context.Background(), convTarget{ID: "conv-1"}, nil, s); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got := d.Metrics().Snapshot()
	if got.HumanStatusRuns != 1 || got.HumanStatusFailures != 0 {
		t.Fatalf("counters = %+v, want runs=1 failures=0", got)
	}
	items, err := st.ListHumanStatuses(context.Background(), "conv-1")
	if err != nil || len(items) != 1 || items[0].Status != "打游戏" {
		t.Fatalf("statuses = %+v err=%v", items, err)
	}
}

func TestHumanStatusFailureReasonHidesDetails(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("ai: HTTP 500: postgres://user:secret@db/bot"), "模型服务不可用"},
		{fmt.Errorf("ai: HTTP 401: bad key sk-live-secret"), "模型鉴权失败"},
		{context.DeadlineExceeded, "超时"},
		{errHumanStatusNoAI, "模型未配置"},
		{fmt.Errorf("human status reply was not a JSON array: sk-live-secret"), "模型调用失败"},
	} {
		got := humanStatusFailureReason(tc.err)
		if got != tc.want {
			t.Fatalf("reason(%v) = %q, want %q", tc.err, got, tc.want)
		}
		if strings.Contains(got, "secret") || strings.Contains(got, "postgres") {
			t.Fatalf("reason leaked details: %q", got)
		}
	}
}

// 纯文本渲染必须是列表：QQ 群聊是比例字体，空格对齐的"表格"在客户端一定错位，
// 所以这里断言的是「- 」+ 全角「｜」，而不是列宽 padding。
func TestHumanStatusListRendering(t *testing.T) {
	rows := []humanStatusRow{
		{Object: "小明", Status: "打游戏", Confidence: "82%", Evidence: "刚说在开黑", Updated: "21:40"},
		{Object: "一个名字特别特别长所以不会被截断的聊天对象", Status: "工作", Confidence: "65%", Evidence: "在赶周报", Updated: "18:05"},
	}
	out := humanStatusList(rows)
	if !strings.HasPrefix(out, "最近聊天对象状态（2 人）：\n- ") {
		t.Fatalf("list header = %q", out)
	}
	for _, want := range []string{
		"- 小明｜打游戏｜置信度 82%｜依据 刚说在开黑｜更新 21:40",
		"- 一个名字特别特别长所以不会被截断的聊天对象｜工作｜置信度 65%｜依据 在赶周报｜更新 18:05",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("list missing %q: %q", want, out)
		}
	}
	for _, banned := range []string{"| ---", "  ", "推测状态", "更新于"} {
		if strings.Contains(out, banned) {
			t.Fatalf("plain list must not contain %q: %q", banned, out)
		}
	}
	if lines := strings.Split(out, "\n"); len(lines) != len(rows)+1 {
		t.Fatalf("want one line per object plus the header, got %d lines: %q", len(lines), out)
	}
	t.Logf("纯文本样例:\n%s", out)
}

// markdown 开关打开时输出代码块包裹的等宽表格（QQ 不文档支持表格语法，
// 但桌面/iOS/新版安卓会渲染 ``` 代码块）。列内容与纯文本一致。
func TestHumanStatusMarkdownRendering(t *testing.T) {
	rows := []humanStatusRow{
		{Object: "小明", Status: "打游戏", Confidence: "82%", Evidence: "刚说在开黑", Updated: "21:40"},
		{Object: "小红", Status: "工作", Confidence: "65%", Evidence: "在赶周报", Updated: "18:05"},
	}
	out := humanStatusMarkdown(rows)
	if !strings.HasPrefix(out, "最近聊天对象状态（2 人）：\n\n"+fenceOpen+"\n") {
		t.Fatalf("markdown header = %q", out)
	}
	if !strings.HasSuffix(out, "\n"+fenceClose) {
		t.Fatalf("markdown table must be closed by a fence: %q", out)
	}
	for _, want := range []string{"对象", "状态", "置信度", "依据", "更新", "小明", "打游戏", "82%", "刚说在开黑", "21:40", "---"} {
		if !strings.Contains(out, want) {
			t.Fatalf("markdown table missing %q: %q", want, out)
		}
	}
	// 表头与数据行必须按显示宽度对齐（CJK 算两列），否则等宽字体里仍然是歪的。
	lines := strings.Split(strings.TrimSuffix(out, "\n"+fenceClose), "\n")
	data := lines[len(lines)-2]
	if !strings.Contains(data, "  ") {
		t.Fatalf("data row is not column padded: %q", data)
	}
	if strings.Contains(out, "| ---") {
		t.Fatalf("must not emit a markdown pipe table: %q", out)
	}
	t.Logf("markdown 样例:\n%s", out)
}

// 时间线必须按成员分组、带时间与图片 id，并且受 timeline_messages 限制。
func TestHumanStatusTimelineGroupsMembersAndImages(t *testing.T) {
	msgs := []store.Message{
		{Role: "user", Speaker: "小明", SpeakerOpenID: "U1", Content: "早上好", CreatedAt: "2026-11-04T08:00:00+08:00"},
		{Role: "user", Speaker: "小红", SpeakerOpenID: "U2", Content: "在赶周报", CreatedAt: "2026-11-04T08:01:00+08:00"},
		{Role: "user", Speaker: "", SpeakerOpenID: "U3", Content: "冒个泡", CreatedAt: "2026-11-04T08:02:00+08:00"},
		{Role: "user", Speaker: "小明", SpeakerOpenID: "U1", Content: "发张图", CreatedAt: "2026-11-04T08:03:00+08:00",
			Attachments: []store.Attachment{{Kind: "image", MediaID: "img-1", ContentType: "image/png"}}},
		{Role: "assistant", Content: "收到", CreatedAt: "2026-11-04T08:04:00+08:00"},
	}
	out, images := buildHumanStatusTimeline(msgs, 120)
	for _, want := range []string{
		"## 成员：小明 (U1)",
		"## 成员：小红 (U2)",
		"## 成员：未知 (U3)",
		"[08:00] 早上好",
		"[08:03] 发张图 [图片 media_id=img-1]",
		"## 全局最近消息（按时间顺序）",
		"[08:01] 小红：在赶周报",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("timeline missing %q:\n%s", want, out)
		}
	}
	if images != 1 {
		t.Fatalf("analyzable images = %d, want 1", images)
	}
}

func TestHumanStatusTimelineRespectsMessageLimit(t *testing.T) {
	msgs := make([]store.Message, 0, 30)
	for i := 0; i < 30; i++ {
		msgs = append(msgs, store.Message{
			Role: "user", Speaker: "小明", SpeakerOpenID: "U1",
			Content: fmt.Sprintf("消息-%02d", i), CreatedAt: "2026-11-04T08:00:00+08:00",
		})
	}
	out, _ := buildHumanStatusTimeline(msgs, 20)
	if strings.Contains(out, "消息-09") {
		t.Fatalf("timeline included a message older than the 20-message window:\n%s", out)
	}
	if !strings.Contains(out, "消息-29") {
		t.Fatalf("timeline lost the newest message:\n%s", out)
	}
	memberPart := strings.Split(out, "## 全局最近消息")[0]
	entries := 0
	for _, line := range strings.Split(memberPart, "\n") {
		if strings.HasPrefix(line, "[") {
			entries++
		}
	}
	if entries != 20 {
		t.Fatalf("member timeline entries = %d, want 20:\n%s", entries, memberPart)
	}
}

// 图片提示不能爆炸：每人最多 4 张、全局最多 8 张。
func TestHumanStatusTimelineCapsImages(t *testing.T) {
	msgs := make([]store.Message, 0, 15)
	for member := 0; member < 3; member++ {
		for i := 0; i < 5; i++ {
			msgs = append(msgs, store.Message{
				Role: "user", Speaker: fmt.Sprintf("成员%d", member), SpeakerOpenID: fmt.Sprintf("U%d", member),
				Content: "看图", CreatedAt: "2026-11-04T08:00:00+08:00",
				Attachments: []store.Attachment{{
					Kind: "image", MediaID: fmt.Sprintf("img-%d-%d", member, i), ContentType: "image/png",
				}},
			})
		}
	}
	out, images := buildHumanStatusTimeline(msgs, 120)
	if images != 8 {
		t.Fatalf("analyzable images = %d, want 8", images)
	}
	if got := strings.Count(out, "[图片 media_id="); got != 8 {
		t.Fatalf("image markers = %d, want 8:\n%s", got, out)
	}
	for _, banned := range []string{"img-2-", "img-0-4"} {
		if strings.Contains(out, banned) {
			t.Fatalf("timeline exceeded an image cap (%s):\n%s", banned, out)
		}
	}
}

const coreHumanStatusVerdict = `[{"member_openid":"U1","display_name":"小明","status":"打游戏","confidence":0.82,"evidence":"画面是游戏界面"}]`

type coreFakeAI struct {
	mu          sync.Mutex
	requests    []map[string]any
	visionCalls int
	visionFail  bool
	mediaID     string
	withTool    bool
	visionText  string
}

func (f *coreFakeAI) handler(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.requests = append(f.requests, body)
	f.mu.Unlock()

	if coreHasImagePart(body) {
		f.mu.Lock()
		f.visionCalls++
		fail := f.visionFail
		f.mu.Unlock()
		if fail {
			http.Error(w, "vision exploded: postgres://user:secret@db/bot", http.StatusInternalServerError)
			return
		}
		text := f.visionText
		if text == "" {
			text = "画面是电脑上的游戏界面，屏幕右上角有计时。"
		}
		coreWriteCompletion(w, text)
		return
	}
	if coreHasToolMessage(body) {
		coreWriteCompletion(w, coreHumanStatusVerdict)
		return
	}
	if !f.withTool {
		coreWriteCompletion(w, coreHumanStatusVerdict)
		return
	}
	call := map[string]any{
		"id": "call_analyze_1", "type": "function",
		"function": map[string]any{
			"name":      "analyze_image",
			"arguments": fmt.Sprintf(`{"media_id":%q,"question":"画面里在做什么？"}`, f.mediaID),
		},
	}
	coreWriteJSON(w, map[string]any{
		"model": "test-model",
		"choices": []any{map[string]any{
			"message":       map[string]any{"content": "", "tool_calls": []any{call}},
			"finish_reason": "tool_calls",
		}},
	})
}

func coreWriteCompletion(w http.ResponseWriter, text string) {
	coreWriteJSON(w, map[string]any{
		"model": "test-model",
		"choices": []any{map[string]any{
			"message":       map[string]any{"content": text},
			"finish_reason": "stop",
		}},
	})
}

func coreWriteJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func coreHasImagePart(body map[string]any) bool {
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

func coreHasToolMessage(body map[string]any) bool {
	msgs, _ := body["messages"].([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] == "tool" {
			return true
		}
	}
	return false
}

func coreToolContents(body map[string]any) string {
	var b strings.Builder
	msgs, _ := body["messages"].([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] != "tool" {
			continue
		}
		text, _ := m["content"].(string)
		b.WriteString(text)
		b.WriteString("\n")
	}
	return b.String()
}

func newHumanStatusVisionTest(t *testing.T, fail bool) (*Dispatcher, *coreFakeAI, string) {
	t.Helper()
	st := newHumanStatusTestStore(t)
	mediaStore := media.NewStore(st.DB())
	data := []byte{
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
	mediaID, err := mediaStore.Put(context.Background(), "image/png", data)
	if err != nil {
		t.Fatalf("seed media: %v", err)
	}
	if err := st.AppendMessage(context.Background(), store.MessageInput{
		ConversationID: "conv-1", Role: "user", Kind: store.KindDialogue,
		Content: "看这个界面", Speaker: "小明", SpeakerOpenID: "U1",
		Attachments: []store.Attachment{{Kind: "image", MediaID: mediaID, ContentType: "image/png"}},
	}); err != nil {
		t.Fatalf("seed image message: %v", err)
	}

	fake := &coreFakeAI{withTool: true, mediaID: mediaID, visionFail: fail}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	d := humanStatusTestDispatcher(t, st, ai.NewClient(srv.URL, "sk-test", srv.Client()))
	d.media = &media.Bundle{Store: mediaStore}
	return d, fake, mediaID
}

// 模型第一轮请求 analyze_image，第二轮拿到视觉描述后返回 JSON；视觉模型
// 必须按配置单独指定，role=tool 消息必须把描述带回主模型。
func TestHumanStatusImageToolLoop(t *testing.T) {
	d, fake, mediaID := newHumanStatusVisionTest(t, false)
	s := settings.Settings{
		Model: "main-model", HumanStatusTTLMinutes: 120, HumanStatusTimelineMessages: 120,
		HumanStatusVisionEnabled: true, HumanStatusVisionModel: "vision-model",
	}
	if err := d.RefreshHumanStatus(context.Background(), convTarget{ID: "conv-1"}, nil, s); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	fake.mu.Lock()
	requests := append([]map[string]any(nil), fake.requests...)
	visionCalls := fake.visionCalls
	fake.mu.Unlock()
	if visionCalls != 1 {
		t.Fatalf("vision calls = %d, want 1", visionCalls)
	}
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3 (infer, vision, infer with tool)", len(requests))
	}
	if _, ok := requests[0]["tools"]; !ok {
		t.Fatalf("first inference request must carry the analyze_image tool")
	}
	if requests[1]["model"] != "vision-model" {
		t.Fatalf("vision model = %v, want vision-model", requests[1]["model"])
	}
	if !coreHasImagePart(requests[1]) {
		t.Fatalf("vision request has no image part")
	}
	toolText := coreToolContents(requests[2])
	if !strings.Contains(toolText, "游戏界面") {
		t.Fatalf("role=tool message missing the vision description: %q", toolText)
	}

	items, err := d.st.ListHumanStatuses(context.Background(), "conv-1")
	if err != nil || len(items) != 1 || items[0].Status != "打游戏" {
		t.Fatalf("statuses = %+v err=%v", items, err)
	}
	if got := d.Metrics().Snapshot().HumanStatusImageCalls; got != 1 {
		t.Fatalf("human_status_image_calls = %d, want 1", got)
	}
	_ = mediaID
}

// 视觉调用失败只把“图片不可用”作为工具结果回灌，推测仍然产出 JSON。
func TestHumanStatusImageToolFailureDoesNotAbortInference(t *testing.T) {
	d, fake, _ := newHumanStatusVisionTest(t, true)
	s := settings.Settings{
		Model: "main-model", HumanStatusTTLMinutes: 120, HumanStatusTimelineMessages: 120,
		HumanStatusVisionEnabled: true,
	}
	if err := d.RefreshHumanStatus(context.Background(), convTarget{ID: "conv-1"}, nil, s); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	fake.mu.Lock()
	requests := append([]map[string]any(nil), fake.requests...)
	fake.mu.Unlock()
	if len(requests) < 3 {
		t.Fatalf("requests = %d, want at least 3", len(requests))
	}
	toolText := coreToolContents(requests[len(requests)-1])
	if !strings.Contains(toolText, "图片不可用") {
		t.Fatalf("tool failure must be reported as 图片不可用: %q", toolText)
	}
	if strings.Contains(toolText, "secret") || strings.Contains(toolText, "postgres") {
		t.Fatalf("tool failure leaked upstream details: %q", toolText)
	}
	items, err := d.st.ListHumanStatuses(context.Background(), "conv-1")
	if err != nil || len(items) != 1 || items[0].Status != "打游戏" {
		t.Fatalf("statuses = %+v err=%v", items, err)
	}
	if got := d.Metrics().Snapshot().HumanStatusImageCalls; got != 1 {
		t.Fatalf("human_status_image_calls = %d, want 1", got)
	}
}

// coreReplyScript serves a fixed sequence of model replies and records the
// request bodies, so tests can distinguish "first attempt" from "retry".
type coreReplyScript struct {
	mu       sync.Mutex
	replies  []string
	requests []map[string]any
}

func (s *coreReplyScript) handler(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	idx := len(s.requests)
	s.requests = append(s.requests, body)
	text := s.replies[len(s.replies)-1]
	if idx < len(s.replies) {
		text = s.replies[idx]
	}
	s.mu.Unlock()
	coreWriteCompletion(w, text)
}

func (s *coreReplyScript) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *coreReplyScript) request(i int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[i]
}

func newHumanStatusScripted(t *testing.T, replies ...string) (*Dispatcher, *store.Store, *coreReplyScript, *bytes.Buffer) {
	t.Helper()
	st := newHumanStatusTestStore(t)
	script := &coreReplyScript{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(script.handler))
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	d := &Dispatcher{
		st:    st,
		ai:    ai.NewClient(srv.URL, "sk-test", srv.Client()),
		aiSem: newDynamicSem(4),
		log:   slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	return d, st, script, &logs
}

func humanStatusTestSettings() settings.Settings {
	return settings.Settings{Model: "test-model", HumanStatusTTLMinutes: 120, HumanStatusTimelineMessages: 120}
}

// markdown 围栏不能成为解析失败的理由：去掉 ```json / ``` 后照常切对象。
func TestHumanStatusJSONFence(t *testing.T) {
	reply := "```json\n" + coreHumanStatusVerdict + "\n```"
	d, st, script, _ := newHumanStatusScripted(t, reply)
	if err := d.RefreshHumanStatus(context.Background(), convTarget{ID: "conv-1"}, nil, humanStatusTestSettings()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if script.calls() != 1 {
		t.Fatalf("calls = %d, want 1", script.calls())
	}
	items, err := st.ListHumanStatuses(context.Background(), "conv-1")
	if err != nil || len(items) != 1 || items[0].Status != "打游戏" {
		t.Fatalf("statuses = %+v err=%v", items, err)
	}
}

// 截断的回复（有效对象 + 半截对象）必须保留已解析对象并成功，日志标记 partial。
func TestHumanStatusJSONTruncatedKeepsParsedObjects(t *testing.T) {
	truncated := `[{"member_openid":"U1","display_name":"小明","status":"打游戏","confidence":0.82,"evidence":"画面是游戏界面"},` +
		`{"member_openid":"U2","display_name":"小红","status":"工作","confidence":0.4,"evidence":"在赶周报`
	d, st, script, logs := newHumanStatusScripted(t, truncated)
	if err := d.RefreshHumanStatus(context.Background(), convTarget{ID: "conv-1"}, nil, humanStatusTestSettings()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if script.calls() != 1 {
		t.Fatalf("calls = %d, want 1 (a partial reply must not retry)", script.calls())
	}
	items, err := st.ListHumanStatuses(context.Background(), "conv-1")
	if err != nil || len(items) != 1 || items[0].MemberOpenID != "U1" {
		t.Fatalf("statuses = %+v err=%v, want only the fully decoded U1 object", items, err)
	}
	out := logs.String()
	if !strings.Contains(out, "truncated") || !strings.Contains(out, "partial=true") {
		t.Fatalf("partial parse must be logged with partial=true:\n%s", out)
	}
	t.Logf("截断恢复样例（输入）:\n%s", truncated)
	t.Logf("截断恢复样例（保留 %d 个对象，日志）:\n%s", len(items), out)
}

// 第一次纯文本、第二次合法 JSON：追加合约消息重试一次后成功。
func TestHumanStatusJSONRetryOnce(t *testing.T) {
	d, st, script, logs := newHumanStatusScripted(t,
		"看起来大家状态都不错。", coreHumanStatusVerdict)
	if err := d.RefreshHumanStatus(context.Background(), convTarget{ID: "conv-1"}, nil, humanStatusTestSettings()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if script.calls() != 2 {
		t.Fatalf("calls = %d, want 2 (one retry)", script.calls())
	}
	second := script.request(1)
	msgs, _ := second["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("retry request has no messages")
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("retry must append a user message, got %v", last["role"])
	}
	content, _ := last["content"].(string)
	if !strings.Contains(content, "只输出 JSON 数组") || !strings.Contains(content, "evidence ≤ 40 字") {
		t.Fatalf("retry instruction = %q", content)
	}
	if got, _ := second["max_tokens"].(float64); int(got) != humanStatusOutputMinTokens {
		t.Fatalf("retry max_tokens = %v, want %d", second["max_tokens"], humanStatusOutputMinTokens)
	}
	items, err := st.ListHumanStatuses(context.Background(), "conv-1")
	if err != nil || len(items) != 1 {
		t.Fatalf("statuses = %+v err=%v", items, err)
	}
	if !strings.Contains(logs.String(), "human status JSON retry") || !strings.Contains(logs.String(), "tokens=") {
		t.Fatalf("retry cost must be logged:\n%s", logs.String())
	}
}

// 两次都不给 JSON 数组 → 仍然失败，走原失败路径，不写状态。
func TestHumanStatusJSONRetryBothFail(t *testing.T) {
	d, st, script, _ := newHumanStatusScripted(t, "这不是 JSON", "仍然不是 JSON")
	err := d.RefreshHumanStatus(context.Background(), convTarget{ID: "conv-1"}, nil, humanStatusTestSettings())
	if err == nil {
		t.Fatal("expected a parse failure after the retry")
	}
	if script.calls() != 2 {
		t.Fatalf("calls = %d, want 2", script.calls())
	}
	if got := humanStatusFailureReason(err); got != "模型调用失败" {
		t.Fatalf("reason = %q, want 模型调用失败", got)
	}
	items, listErr := st.ListHumanStatuses(context.Background(), "conv-1")
	if listErr != nil || len(items) != 0 {
		t.Fatalf("failed inference must not store anything: %+v err=%v", items, listErr)
	}
}

// 关闭图片工具时请求里不应出现 tools，也不应产生额外视觉调用。
func TestHumanStatusImageToolDisabled(t *testing.T) {
	d, fake, _ := newHumanStatusVisionTest(t, false)
	fake.mu.Lock()
	fake.withTool = false
	fake.mu.Unlock()
	s := settings.Settings{
		Model: "main-model", HumanStatusTTLMinutes: 120, HumanStatusTimelineMessages: 120,
		HumanStatusVisionEnabled: false,
	}
	if err := d.RefreshHumanStatus(context.Background(), convTarget{ID: "conv-1"}, nil, s); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	fake.mu.Lock()
	requests := append([]map[string]any(nil), fake.requests...)
	visionCalls := fake.visionCalls
	fake.mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if _, ok := requests[0]["tools"]; ok {
		t.Fatalf("vision disabled must not advertise tools")
	}
	if visionCalls != 0 {
		t.Fatalf("vision calls = %d, want 0", visionCalls)
	}
	if got := d.Metrics().Snapshot().HumanStatusImageCalls; got != 0 {
		t.Fatalf("human_status_image_calls = %d, want 0", got)
	}
}
