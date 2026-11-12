// Package e2e end-to-end proof for the two capabilities production has never
// switched on: spontaneous proactive messages and background memory extraction.
//
// Helpers here are local to this file on purpose: T12 also works in this
// package, and pipeline_test.go must stay untouched.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/store"
)

// modelProbe is a counting OpenAI-compatible endpoint.
//
// The shared harness server only keeps the last request body, but these two
// capabilities are about *how often* the model is called (extraction every N
// turns, and gates that must fuse before spending a call). Tests point the
// resolved base URL at this probe instead, so every call is observable.
type modelProbe struct {
	mu sync.Mutex

	counts map[string]int

	chatReply    string
	extractReply string
	reflectReply string
	failExtract  bool

	// events wakes waiters without polling clocks: one send per recorded call.
	events chan struct{}
}

func newModelProbe(t *testing.T, h *harness) *modelProbe {
	t.Helper()
	p := &modelProbe{
		counts:       map[string]int{},
		chatReply:    "这是测试回复。",
		extractReply: `[]`,
		reflectReply: `{"speak":false,"text":"","reason":"没什么可说的"}`,
		events:       make(chan struct{}, 256),
	}
	srv := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(srv.Close)
	// Same as the harness: the endpoint is resolved per request from settings.
	h.apply(t, fmt.Sprintf(`{"openai_base_url":%q}`, srv.URL))
	return p
}

func (p *modelProbe) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)

	var all strings.Builder
	if msgs, ok := body["messages"].([]any); ok {
		for _, item := range msgs {
			m, _ := item.(map[string]any)
			if text, _ := m["content"].(string); text != "" {
				all.WriteString(text)
				all.WriteByte('\n')
			}
		}
	}
	kind := "chat"
	switch {
	case strings.Contains(all.String(), "你是同一角色的润色器"):
		// stage 2 of the two-stage pipeline. It is counted separately from
		// "chat" so the call-budget assertions stay about the conversation
		// itself, but it still answers like a chat call.
		kind = "polish"
	case strings.Contains(all.String(), "提取值得长期记住的事实"):
		kind = "extract"
	case strings.Contains(all.String(), "有机会主动说一句话"):
		kind = "reflect"
	}

	p.mu.Lock()
	p.counts[kind]++
	if kind == "chat" || kind == "polish" {
		reply := p.chatReply
		p.mu.Unlock()
		writeChatReply(w, body, reply)
	} else {
		var reply string
		switch kind {
		case "extract":
			reply = p.extractReply
		default:
			reply = p.reflectReply
		}
		fail := kind == "extract" && p.failExtract
		p.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"extract boom"}}`))
		} else {
			writeJSONReply(w, reply)
		}
	}
	select {
	case p.events <- struct{}{}:
	default:
	}
}

func writeJSONReply(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	payload, _ := json.Marshal(map[string]any{
		"model": "test-model",
		"choices": []map[string]any{{
			"message":       map[string]string{"content": text},
			"finish_reason": "stop",
		}},
	})
	_, _ = w.Write(payload)
}

func writeChatReply(w http.ResponseWriter, body map[string]any, text string) {
	if stream, _ := body["stream"].(bool); !stream {
		writeJSONReply(w, text)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	payload, _ := json.Marshal(map[string]any{
		"model":   "test-model",
		"choices": []map[string]any{{"delta": map[string]string{"content": text}, "finish_reason": "stop"}},
		"usage":   map[string]int{"total_tokens": 7},
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// count reports how many calls of a kind have been observed.
func (p *modelProbe) count(kind string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.counts[kind]
}

// total reports the number of model calls of every kind.
func (p *modelProbe) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	sum := 0
	for _, n := range p.counts {
		sum += n
	}
	return sum
}

func (p *modelProbe) countsSnapshot() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]int, len(p.counts))
	for k, v := range p.counts {
		out[k] = v
	}
	return out
}

// setReflectReply lets a test decide what the reflection call answers.
func (p *modelProbe) setReflectReply(reply string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reflectReply = reply
}

// waitFor blocks until at least n calls of a kind have been recorded. It waits on
// the probe's own event channel, so it never guesses with a fixed sleep.
func (p *modelProbe) waitFor(t *testing.T, kind string, n int) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		if p.count(kind) >= n {
			return
		}
		select {
		case <-p.events:
		case <-timeout:
			t.Fatalf("5s 内没有看到第 %d 次 %s 调用（实际 %d 次，全部：%v）",
				n, kind, p.count(kind), p.countsSnapshot())
		}
	}
}

// waitForMemory waits for a memory row whose text contains want. Extraction runs
// in a goroutine after the reply is delivered, so this waits for the stored fact
// instead of sleeping a fixed amount.
func waitForMemory(t *testing.T, st *store.Store, scope, groupOpenID, memberOpenID, want string) store.Memory {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, err := st.ListMemories(context.Background(), scope, groupOpenID, memberOpenID, 20, 0)
		if err != nil {
			t.Fatalf("list memories: %v", err)
		}
		for _, m := range rows {
			if strings.Contains(m.Text, want) {
				return m
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("5s 内没有写入记忆 %q（scope=%s group=%s member=%s，现有 %d 条）",
				want, scope, groupOpenID, memberOpenID, len(rows))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForSalience waits until the stored fact has been seen often enough to be
// reinforced (AddMemory raises salience on a repeat instead of adding a row).
func waitForSalience(t *testing.T, st *store.Store, scope, groupOpenID, memberOpenID, want string, min float64) store.Memory {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, err := st.ListMemories(context.Background(), scope, groupOpenID, memberOpenID, 20, 0)
		if err != nil {
			t.Fatalf("list memories: %v", err)
		}
		for _, m := range rows {
			if strings.Contains(m.Text, want) && m.Salience >= min {
				return m
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("5s 内记忆 %q 的 salience 未升到 %.2f（现有 %+v）", want, min, rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// sentText returns the visible text of one outbound message.
func sentText(m sentMessage) string {
	if m.Body.Content != "" {
		return m.Body.Content
	}
	if m.Body.Markdown != nil {
		return m.Body.Markdown.Content
	}
	return ""
}

// proactiveDefaults disables everything that would make a proactive pass noisy
// (quiet hours, markdown) while keeping the credit model explicit.
const proactiveDefaults = `"proactive_level":"auto","proactive_ratio":3,"proactive_max_credits":3,` +
	`"proactive_min_interval_minutes":10,"proactive_quiet_start":0,"proactive_quiet_end":0,` +
	`"markdown_enabled":false`

// sameDayHour returns today's wall clock at the given hour, so injected times
// stay within the 24h credit window of the messages seedHumanChat writes now.
func sameDayHour(t *testing.T, hour int) time.Time {
	t.Helper()
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.Local)
}

/* ------------------------------------------------------------------ *
 * 记忆抽取（MemoryExtractEnabled）
 * ------------------------------------------------------------------ */

// 每 N 轮触发一次：第 1 轮不抽，第 2 轮抽一次并落进群作用域，第 3 轮也不抽。
func TestMemoryExtractionRunsEveryNthTurnInGroupScope(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.extractReply = `好：[{"text":"小明养了一只叫豆豆的猫","kind":"fact"}]`
	h.apply(t, `{"memory_enabled":true,"memory_extract_enabled":true,"memory_extract_every":2}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("MX1", "G1", "U1", "小明", "我家猫今天不太吃饭"))
	h.waitForSent(t, 1)
	if got := p.count("extract"); got != 0 {
		t.Fatalf("第 1 轮不该触发抽取（every=2），实际调用 %d 次", got)
	}
	if got := p.count("chat"); got != 1 {
		t.Fatalf("第 1 轮应只有 1 次对话调用，实际 %d", got)
	}

	h.dis.HandlePayload(context.Background(), groupAtMessage("MX2", "G1", "U1", "小明", "它叫豆豆"))
	h.waitForSent(t, 2)
	p.waitFor(t, "extract", 1)

	got := waitForMemory(t, h.st, store.MemoryGroup, "G1", "", "豆豆")
	if got.Scope != store.MemoryGroup || got.GroupOpenID != "G1" || got.MemberOpenID != "" {
		t.Fatalf("抽取的事实应写进群作用域：%+v", got)
	}
	if got.Source != "extract" || got.Kind != "fact" {
		t.Fatalf("来源/类型不对：%+v", got)
	}

	// 第 3 轮（计数 3）不整除 2，仍不该抽取。
	h.dis.HandlePayload(context.Background(), groupAtMessage("MX3", "G1", "U1", "小明", "嗯"))
	h.waitForSent(t, 3)
	if got := p.count("extract"); got != 1 {
		t.Fatalf("第 3 轮不该再触发抽取，累计 %d 次", got)
	}
	if got := p.count("chat"); got != 3 {
		t.Fatalf("3 轮对话应有 3 次对话调用，实际 %d", got)
	}
}

// 同一句话被抽到两次：唯一键去重成一行，salience 提升（不是重复插入）。
func TestMemoryExtractionDeduplicatesRepeatedFacts(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.extractReply = `[{"text":"小明养了一只叫豆豆的猫","kind":"fact"}]`
	h.apply(t, `{"memory_enabled":true,"memory_extract_enabled":true,"memory_extract_every":1}`)

	for i := 1; i <= 2; i++ {
		h.dis.HandlePayload(context.Background(),
			groupAtMessage(fmt.Sprintf("MD%d", i), "G1", "U1", "小明", "我家猫叫豆豆"))
		h.waitForSent(t, i)
		p.waitFor(t, "extract", i)
	}

	reinforced := waitForSalience(t, h.st, store.MemoryGroup, "G1", "", "豆豆", 1.2)
	rows, err := h.st.ListMemories(context.Background(), store.MemoryGroup, "G1", "", 20, 0)
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("重复事实应被去重成 1 行，实际 %d 行：%+v", len(rows), rows)
	}
	if reinforced.Salience <= 0.8 {
		t.Fatalf("重复抽取应提升 salience，实际 %.2f", reinforced.Salience)
	}
	if got := p.count("extract"); got != 2 {
		t.Fatalf("每轮都该抽一次（every=1），实际 %d 次", got)
	}
}

// 私聊写入个人作用域，不落到群作用域。
func TestMemoryExtractionWritesMemberScopeForPrivateChat(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.extractReply = `[{"text":"小明住在杭州","kind":"fact"}]`
	h.apply(t, `{"memory_enabled":true,"memory_extract_enabled":true,"memory_extract_every":1}`)

	h.dis.HandlePayload(context.Background(), c2cMessage("MC1", "U9", "小明", "我住在杭州"))
	h.waitForSent(t, 1)
	p.waitFor(t, "extract", 1)

	got := waitForMemory(t, h.st, store.MemoryMember, "", "U9", "杭州")
	if got.Scope != store.MemoryMember || got.GroupOpenID != "" || got.MemberOpenID != "U9" {
		t.Fatalf("私聊事实应写进个人作用域：%+v", got)
	}
	if rows, err := h.st.ListMemories(context.Background(), store.MemoryGroup, "", "", 20, 0); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(rows) != 0 {
		t.Fatalf("私聊事实不该出现在群作用域：%+v", rows)
	}
}

// 关闭抽取（默认就是关的）：三轮对话只有三次对话调用，没有任何额外模型调用。
func TestMemoryExtractionDisabledMakesNoExtraModelCall(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	h.apply(t, `{"memory_enabled":true,"memory_extract_enabled":false,"memory_extract_every":1}`)

	for i := 1; i <= 3; i++ {
		h.dis.HandlePayload(context.Background(),
			groupAtMessage(fmt.Sprintf("MO%d", i), "G1", "U1", "小明", "随便聊聊"))
		h.waitForSent(t, i)
	}

	if got := p.count("extract"); got != 0 {
		t.Fatalf("抽取关闭时不该有抽取调用，实际 %d 次（%v）", got, p.countsSnapshot())
	}
	// 只数主路径：stage2（润色）是另一条独立调用，harness 里默认关闭。
	if got := p.count("chat"); got != 3 {
		t.Fatalf("3 轮对话应只有 3 次对话调用，实际 %d 次（%v）", got, p.countsSnapshot())
	}
	if rows, err := h.st.ListMemories(context.Background(), "", "", "", 50, 0); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(rows) != 0 {
		t.Fatalf("抽取关闭时不该写入记忆：%+v", rows)
	}
}

// 按群关掉记忆时，即使全局开着抽取也不该发生（每个功能都能单独回滚）。
func TestMemoryExtractionHonoursThePerGroupMemorySwitch(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	h.apply(t, `{"memory_enabled":true,"memory_extract_enabled":true,"memory_extract_every":1}`)
	if _, _, err := h.settings.Apply(context.Background(), map[string]json.RawMessage{
		"group_overrides": json.RawMessage(`[{"group_openid":"G1","memory":false}]`),
	}); err != nil {
		t.Fatalf("apply group override: %v", err)
	}

	h.dis.HandlePayload(context.Background(), groupAtMessage("MG1", "G1", "U1", "小明", "我家猫叫豆豆"))
	h.waitForSent(t, 1)

	if got := p.count("extract"); got != 0 {
		t.Fatalf("该群记忆已关闭，不该抽取，实际调用 %d 次", got)
	}
	if got := p.count("chat"); got != 1 {
		t.Fatalf("应只有 1 次对话调用，实际 %d 次（%v）", got, p.countsSnapshot())
	}
}

// 抽取失败或返回垃圾：只影响记忆，不影响已经发出的回复，也不能 panic。
func TestMemoryExtractionFailureIsInvisibleToTheUser(t *testing.T) {
	cases := []struct {
		name  string
		setup func(p *modelProbe)
	}{
		{"HTTP 500", func(p *modelProbe) {
			p.mu.Lock()
			p.failExtract = true
			p.mu.Unlock()
		}},
		{"不是 JSON", func(p *modelProbe) {
			p.mu.Lock()
			p.extractReply = "好的，我觉得这段对话没什么值得记的。"
			p.mu.Unlock()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			p := newModelProbe(t, h)
			tc.setup(p)
			h.apply(t, `{"memory_enabled":true,"memory_extract_enabled":true,"memory_extract_every":1}`)

			h.dis.HandlePayload(context.Background(),
				groupAtMessage("MF1", "G1", "U1", "小明", "我家猫叫豆豆"))
			sent := h.waitForSent(t, 1)
			if len(sent) != 1 || sentText(sent[0]) == "" {
				t.Fatalf("抽取失败不能影响回复：%+v", sent)
			}
			p.waitFor(t, "extract", 1)
			if rows, err := h.st.ListMemories(context.Background(), "", "", "", 50, 0); err != nil {
				t.Fatalf("list: %v", err)
			} else if len(rows) != 0 {
				t.Fatalf("抽取失败不该写入任何记忆：%+v", rows)
			}
		})
	}
}

/* ------------------------------------------------------------------ *
 * 自发主动（ProactiveLevel=auto）
 * ------------------------------------------------------------------ */

// 成功路径：模型给出要说的话 + 理由 → 主动消息落到平台 → proactive_state 落库。
// 然后同一笔额度只能花一次：下一分钟没有新的人类消息就不再请求模型。
func TestProactiveSendsAndRecordsWhy(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.setReflectReply(`{"speak":true,"text":"刚才那个链接我也可以帮忙看看","reason":"有人卡在链接上"}`)
	h.apply(t, `{`+proactiveDefaults+`}`)
	h.seedHumanChat(t, "G1", 6) // 6 / 3 = 2 点额度

	now := time.Now()
	h.dis.RunProactiveCheck(context.Background(), now)

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("应主动发一条，实际 %d 条", len(sent))
	}
	if text := sentText(sent[0]); !strings.Contains(text, "链接") {
		t.Fatalf("内容不对：%q", text)
	}
	if !strings.Contains(sent[0].Path, "/v2/groups/G1/messages") {
		t.Fatalf("应发到群里，实际 %q", sent[0].Path)
	}

	state, err := h.st.GetProactiveState(context.Background(), "G1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Sends != 1 {
		t.Fatalf("应记一次发送，实际 %+v", state)
	}
	if state.LastSentAt != now.Format(time.RFC3339) {
		t.Fatalf("last_sent_at 应为注入的时间，实际 %q（想要 %q）",
			state.LastSentAt, now.Format(time.RFC3339))
	}
	if state.LastReason != "有人卡在链接上" {
		t.Fatalf("应记录「为什么现在说」，实际 %q", state.LastReason)
	}
	if got := p.count("reflect"); got != 1 {
		t.Fatalf("应只反思一次，实际 %d 次", got)
	}

	h.dis.RunProactiveCheck(context.Background(), now.Add(time.Minute))
	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("额度已花掉，不该再发，实际累计 %d 条", got)
	}
	if got := p.count("reflect"); got != 1 {
		t.Fatalf("额度为 0 时不该再花反思调用，实际 %d 次", got)
	}
}

// 最小间隔：额度够也得等够时间；过了间隔后同一批人类消息仍能换到发言。
func TestProactiveMinimumIntervalBlocksEarlySecondMessage(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.setReflectReply(`{"speak":true,"text":"我补充一句","reason":"测试"}`)
	h.apply(t, `{`+proactiveDefaults+`}`) // 最小间隔 10 分钟
	h.seedHumanChat(t, "G1", 6)

	now := time.Now()
	if err := h.st.SaveProactiveState(context.Background(), store.ProactiveState{
		GroupOpenID: "G1",
		LastSentAt:  now.Add(-2 * time.Minute).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	h.dis.RunProactiveCheck(context.Background(), now)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("距上次发言不到最小间隔，不该再发（实际 %d 条）", got)
	}
	if got := p.count("reflect"); got != 0 {
		t.Fatalf("被间隔挡下时不该花一次反思调用，实际 %d 次", got)
	}

	h.dis.RunProactiveCheck(context.Background(), now.Add(11*time.Minute))
	if sent := h.waitForSent(t, 1); len(sent) != 1 {
		t.Fatalf("过了最小间隔应能发言，实际 %d 条", len(sent))
	}
}

// 静默时段：23:00-08:00 一律不发（注入可控 now），且不浪费反思调用。
func TestProactiveQuietHoursUseTheInjectedClock(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.setReflectReply(`{"speak":true,"text":"深夜不该说话","reason":"测试"}`)
	h.apply(t, `{"proactive_level":"auto","proactive_ratio":3,"proactive_min_interval_minutes":1,`+
		`"proactive_quiet_start":23,"proactive_quiet_end":8,"markdown_enabled":false}`)
	h.seedHumanChat(t, "G1", 6)

	h.dis.RunProactiveCheck(context.Background(), sameDayHour(t, 3))
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("静默时段不该发，实际 %d 条", got)
	}
	if got := p.count("reflect"); got != 0 {
		t.Fatalf("静默时段不该花反思调用，实际 %d 次", got)
	}

	// 同一个小时窗口之外（窗口取反，保证任何运行时刻都成立）应该能发。
	now := time.Now()
	start := (now.Hour() + 2) % 24
	if _, _, err := h.settings.Apply(context.Background(), map[string]json.RawMessage{
		"proactive_quiet_start": json.RawMessage(fmt.Sprintf("%d", start)),
		"proactive_quiet_end":   json.RawMessage(fmt.Sprintf("%d", (start+1)%24)),
	}); err != nil {
		t.Fatalf("apply quiet window: %v", err)
	}
	h.dis.RunProactiveCheck(context.Background(), now.Add(time.Minute))
	if sent := h.waitForSent(t, 1); len(sent) != 1 {
		t.Fatalf("不在静默时段时应能发言，实际 %d 条", len(sent))
	}
}

// 额度门在调用模型之前就合上：人类消息不够时一次模型调用都不该花。
func TestProactiveCreditBudgetGatesBeforeTheModelCall(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.setReflectReply(`{"speak":true,"text":"憋不住了","reason":"测试"}`)
	h.apply(t, `{"proactive_level":"auto","proactive_ratio":10,"proactive_max_credits":3,`+
		`"proactive_min_interval_minutes":1,"proactive_quiet_start":0,"proactive_quiet_end":0,`+
		`"markdown_enabled":false}`)
	h.seedHumanChat(t, "G1", 3) // 3 < 10 → 0 额度

	now := time.Now()
	h.dis.RunProactiveCheck(context.Background(), now)
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("额度不足不该发，实际 %d 条", got)
	}
	if got := p.count("reflect"); got != 0 {
		t.Fatalf("额度不足时不该花反思调用，实际 %d 次", got)
	}

	h.seedHumanChat(t, "G1", 7) // 累计 10 条 → 1 点额度
	h.dis.RunProactiveCheck(context.Background(), now.Add(time.Minute))
	if sent := h.waitForSent(t, 1); len(sent) != 1 {
		t.Fatalf("攒够额度后应能发言，实际 %d 条", len(sent))
	}
}

// kill switch：开启并验证会说话之后，切回 off 就彻底停手（连模型都不问）。
func TestProactiveKillSwitchStopsAfterBeingEnabled(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.setReflectReply(`{"speak":true,"text":"我来了","reason":"测试"}`)
	h.apply(t, `{`+proactiveDefaults+`}`)
	h.seedHumanChat(t, "G1", 6)

	now := time.Now()
	h.dis.RunProactiveCheck(context.Background(), now)
	if sent := h.waitForSent(t, 1); len(sent) != 1 {
		t.Fatalf("开启状态下应能发言，实际 %d 条", len(sent))
	}

	h.apply(t, `{"proactive_level":"off"}`)
	h.seedHumanChat(t, "G1", 6)
	h.dis.RunProactiveCheck(context.Background(), now.Add(11*time.Minute))
	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("kill switch 之后不该再发，实际累计 %d 条", got)
	}
	if got := p.count("reflect"); got != 1 {
		t.Fatalf("kill switch 之后不该再花模型调用，实际 %d 次", got)
	}
}

// 按群覆盖：全局 auto，但 G1 关掉 → 只有 G2 会发言。
func TestProactivePerGroupOverrideDisablesOneGroup(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.setReflectReply(`{"speak":true,"text":"我看到群里在聊","reason":"测试"}`)
	h.apply(t, `{`+proactiveDefaults+`}`)
	if _, _, err := h.settings.Apply(context.Background(), map[string]json.RawMessage{
		"group_overrides": json.RawMessage(`[{"group_openid":"G1","proactive":"off"}]`),
	}); err != nil {
		t.Fatalf("apply group override: %v", err)
	}
	h.seedHumanChat(t, "G1", 6)
	h.seedHumanChat(t, "G2", 6)

	h.dis.RunProactiveCheck(context.Background(), time.Now())
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("只该有一个群发言，实际 %d 条", len(sent))
	}
	if !strings.Contains(sent[0].Path, "/v2/groups/G2/messages") {
		t.Fatalf("发言的应是 G2（G1 被按群关闭），实际 %q", sent[0].Path)
	}
	if got := p.count("reflect"); got != 1 {
		t.Fatalf("被按群关闭的 G1 不该花模型调用，实际累计 %d 次", got)
	}
	g1, _ := h.st.GetProactiveState(context.Background(), "G1")
	if g1.Sends != 0 {
		t.Fatalf("G1 不该有发言记录：%+v", g1)
	}
	g2, _ := h.st.GetProactiveState(context.Background(), "G2")
	if g2.Sends != 1 {
		t.Fatalf("G2 应记一次发言：%+v", g2)
	}
}

// 连续无人回应：streak 会累计到 2，且期间不再花模型调用；有人说话后归零。
//
// 注意：在正常路径上「streak >= 2」与「有额度」互斥（没有人类消息就没有额度，
// 一有人类消息 streak 立刻清零），所以 D-034 的「连续两次没人理就不再自发」
// 目前只是一个计数器，真正的熔断由额度门承担。这里钉住可观测行为。
func TestProactiveSilentStreakCountsUnansweredMessages(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.setReflectReply(`{"speak":true,"text":"我再说一句","reason":"测试"}`)
	h.apply(t, `{`+proactiveDefaults+`}`)
	h.seedHumanChat(t, "G1", 6)

	now := time.Now()
	h.dis.RunProactiveCheck(context.Background(), now)
	if sent := h.waitForSent(t, 1); len(sent) != 1 {
		t.Fatalf("第一轮应发言，实际 %d 条", len(sent))
	}

	// 之后没人说话：额度为 0，两次检查只累计 streak。
	h.dis.RunProactiveCheck(context.Background(), now.Add(2*time.Minute))
	h.dis.RunProactiveCheck(context.Background(), now.Add(4*time.Minute))
	state, err := h.st.GetProactiveState(context.Background(), "G1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.SilentStreak != 2 {
		t.Fatalf("连续两次没人回应后 streak 应为 2，实际 %+v", state)
	}
	if state.Sends != 1 {
		t.Fatalf("期间不该再发，实际 %+v", state)
	}
	if got := p.count("reflect"); got != 1 {
		t.Fatalf("没有额度时不该花反思调用，实际 %d 次", got)
	}

	// 群里重新有人说话（把这批消息放进额度窗口）→ streak 归零、可以再发。
	h.seedHumanChat(t, "G1", 6)
	state.LastSentAt = now.Add(-20 * time.Minute).Format(time.RFC3339)
	state.SilentStreak = 2
	if err := h.st.SaveProactiveState(context.Background(), state); err != nil {
		t.Fatalf("save state: %v", err)
	}
	h.dis.RunProactiveCheck(context.Background(), now.Add(11*time.Minute))
	if sent := h.waitForSent(t, 2); len(sent) != 2 {
		t.Fatalf("有人说话后应能再发，实际累计 %d 条", len(sent))
	}
	after, _ := h.st.GetProactiveState(context.Background(), "G1")
	if after.SilentStreak != 0 || after.Sends != 2 {
		t.Fatalf("有人说话后 streak 应归零并记第二次发送，实际 %+v", after)
	}
}

// at_only 群恒为 0 额度：它收到的每一条消息都是冲着机器人来的（addressed=1），
// 而额度只由「没对机器人说话」的旁观发言驱动，所以既不发也不花反思调用。
// 这是 D-034 的语义，靠 store.CountHumanMessagesSince 的口径保证。
func TestProactiveAtOnlyGroupsEarnNoCreditsFromMentions(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.setReflectReply(`{"speak":true,"text":"我也可以帮上忙","reason":"测试"}`)
	// 放开限流/冷却：这条测试要的是额度口径，不是限流。
	h.apply(t, `{"proactive_level":"auto","proactive_ratio":3,"proactive_min_interval_minutes":1,`+
		`"proactive_quiet_start":0,"proactive_quiet_end":0,"trigger_mode":"at_only","markdown_enabled":false,`+
		`"rate_limit_user_qpm":600,"rate_limit_group_qpm":600,"group_cooldown_seconds":0,`+
		`"reply_debounce_mode":"off"}`)

	for i := 0; i < 9; i++ {
		h.dis.HandlePayload(context.Background(),
			groupAtMessage(fmt.Sprintf("AO%d", i), "G1", "U1", "小明", "在吗"))
	}
	h.waitForSent(t, 9)

	human, err := h.st.CountHumanMessagesSince(context.Background(), "G1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("count human messages: %v", err)
	}
	if human != 0 {
		t.Fatalf("@ 消息不该兑换额度（addressed=1），实际统计到 %d 条", human)
	}

	// 9 条 @ 也已超过 3 倍比例，如果口径错了这里必然多发一条。
	h.dis.RunProactiveCheck(context.Background(), time.Now())
	if got := len(h.sentMessages()); got != 9 {
		t.Fatalf("at_only 群不该自发发言，实际累计 %d 条", got)
	}
	if got := p.count("reflect"); got != 0 {
		t.Fatalf("额度恒为 0，不该花反思调用，实际 %d 次", got)
	}
}

// all_messages 群：自然交谈（未 @ 机器人、但 bot 会回它）仍要能攒额度。
// 这类消息是 kind=dialogue/addressed=0，与 at_only 的 @ 消息（addressed=1）区分开。
func TestProactiveAllMessagesChatterEarnsCredits(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.setReflectReply(`{"speak":true,"text":"刚才聊的那个我也感兴趣","reason":"群里聊得起劲"}`)
	// 放开限流/冷却：E2 默认有每群冷却，6 条连发会被丢掉，测不到额度。
	h.apply(t, `{"proactive_level":"auto","proactive_ratio":3,"proactive_min_interval_minutes":1,`+
		`"proactive_quiet_start":0,"proactive_quiet_end":0,"trigger_mode":"all_messages","markdown_enabled":false,`+
		`"rate_limit_user_qpm":600,"rate_limit_group_qpm":600,"group_cooldown_seconds":0,`+
		`"reply_debounce_mode":"off"}`)

	// E2：未 @ 机器人的群消息也会触发回复（所以它们是 dialogue），但属于自然交谈。
	for i := 0; i < 6; i++ {
		h.dis.HandlePayload(context.Background(),
			groupAnyMessage(fmt.Sprintf("E2%d", i), "G1", "U1", "小明", fmt.Sprintf("群友闲聊 %d", i+1)))
	}
	// 关掉防抖后逐条处理，等六条回复都发完再统计额度。
	h.waitForSent(t, 6)

	human, err := h.st.CountHumanMessagesSince(context.Background(), "G1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("count human messages: %v", err)
	}
	if human != 6 {
		t.Fatalf("all_messages 的自然交谈应全部计入额度，实际 %d 条", human)
	}

	h.dis.RunProactiveCheck(context.Background(), time.Now())
	sent := h.waitForSent(t, 7)
	if len(sent) != 7 {
		t.Fatalf("攒够额度应自发发言，期望 7 条，实际 %d", len(sent))
	}
	if text := sentText(sent[6]); !strings.Contains(text, "感兴趣") {
		t.Fatalf("最后一条应是主动消息，实际 %q", text)
	}
	if got := p.count("reflect"); got != 1 {
		t.Fatalf("应发生一次反思调用，实际 %d 次", got)
	}
}

// 手动发送（后台主动消息）不进额度：它是 assistant/manual，不是人类发言。
func TestProactiveManualSendsDoNotEarnCredits(t *testing.T) {
	h := newHarness(t)
	p := newModelProbe(t, h)
	p.setReflectReply(`{"speak":true,"text":"我来说一句","reason":"测试"}`)
	h.apply(t, `{"proactive_level":"auto","proactive_ratio":3,"proactive_min_interval_minutes":1,`+
		`"proactive_quiet_start":0,"proactive_quiet_end":0,"markdown_enabled":false}`)

	// 先有一轮 @ 对话把会话建出来；这条 addressed=1，本身也不进额度。
	h.dis.HandlePayload(context.Background(), groupAtMessage("MS0", "G1", "U1", "小明", "在吗"))
	h.waitForSent(t, 1)

	convID := core.EncodeConversationID("group:G1:U1")
	for i := 0; i < 3; i++ {
		if _, err := h.dis.SendManual(context.Background(), convID, fmt.Sprintf("手动消息 %d", i+1), 0); err != nil {
			t.Fatalf("manual send: %v", err)
		}
	}
	if got := len(h.sentMessages()); got != 4 {
		t.Fatalf("应有 1 条回复 + 3 条手动消息，实际 %d 条", got)
	}

	human, err := h.st.CountHumanMessagesSince(context.Background(), "G1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("count human messages: %v", err)
	}
	if human != 0 {
		t.Fatalf("手动发送与 @ 消息都不该计入额度，实际 %d 条", human)
	}

	h.dis.RunProactiveCheck(context.Background(), time.Now())
	if got := len(h.sentMessages()); got != 4 {
		t.Fatalf("没有额度就不该自发发言，实际累计 %d 条", got)
	}
	if got := p.count("reflect"); got != 0 {
		t.Fatalf("额度为 0 不该花反思调用，实际 %d 次", got)
	}
}
