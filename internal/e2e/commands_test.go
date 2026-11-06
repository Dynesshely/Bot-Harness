package e2e

// 聊天内命令的端到端用例：管理员用 /tokenstats 开关回复页脚，非管理员静默，
// 未 @ 的消息不触发，c2c 可用，且命令永远不调用模型。

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/store"
)

// waitForInboundRecord waits for a message's audit row to reach a terminal
// status (anything but "received"). Command rows are written synchronously, but
// a row that took the model path finalises from the worker goroutine.
func waitForInboundRecord(t *testing.T, h *harness, msgID string) store.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last store.Record
	for {
		recs, _, err := h.st.ListRecords(context.Background(),
			store.RecordFilter{Direction: "in", Page: 1, PageSize: 50})
		if err != nil {
			t.Fatalf("records: %v", err)
		}
		for _, rec := range recs {
			if rec.MsgID != msgID {
				continue
			}
			last = rec
			if rec.Status != "received" {
				return rec
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("入站记录 %s 未进入终态：%#v", msgID, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForOutboundRecord waits for the outbound audit row of a msg_id.
func waitForOutboundRecord(t *testing.T, h *harness, msgID string) store.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		recs, _, err := h.st.ListRecords(context.Background(),
			store.RecordFilter{Direction: "out", Page: 1, PageSize: 50})
		if err != nil {
			t.Fatalf("records: %v", err)
		}
		for _, rec := range recs {
			if rec.MsgID == msgID {
				return rec
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("没有 msg_id=%s 的出站记录", msgID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// messageFrame is one published SSE "message" frame, decoded back from the bus.
type messageFrame struct {
	Direction string
	Status    string
	MsgID     string
	Content   string
}

// recorder subscribes to the bus before the command runs and collects every
// "message" frame in arrival order. The bus is non-blocking, so the ordering
// observed here is exactly the ordering the admin SSE stream observes.
type recorder struct {
	mu     sync.Mutex
	frames []messageFrame
}

func startRecording(t *testing.T, h *harness) *recorder {
	t.Helper()
	rec := &recorder{}
	events, cancel := h.bus.Subscribe(256)
	t.Cleanup(cancel)
	go func() {
		for ev := range events {
			if ev.Name != "message" {
				continue
			}
			raw, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				continue
			}
			str := func(k string) string {
				s, _ := payload[k].(string)
				return s
			}
			rec.mu.Lock()
			rec.frames = append(rec.frames, messageFrame{
				Direction: str("direction"),
				Status:    str("status"),
				MsgID:     str("msg_id"),
				Content:   str("content"),
			})
			rec.mu.Unlock()
		}
	}()
	return rec
}

// forMsg returns the frames belonging to one msg_id, in stream order.
func (r *recorder) forMsg(msgID string) []messageFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []messageFrame
	for _, f := range r.frames {
		if f.MsgID == msgID {
			out = append(out, f)
		}
	}
	return out
}

// waitForMsgFrames waits until at least n frames for msg_id were published.
func (r *recorder) waitForMsgFrames(t *testing.T, msgID string, n int) []messageFrame {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		frames := r.forMsg(msgID)
		if len(frames) >= n {
			return frames
		}
		if time.Now().After(deadline) {
			t.Fatalf("msg_id=%s 只收到 %d 帧，期望 %d：%#v", msgID, len(frames), n, frames)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestCommandSendsInboundAuditBeforeOutbound peers the SSE contract: a command
// publishes in/command first and out/replied second, so the live stream never
// shows a reply before the message that triggered it. The audit table must
// agree — the inbound row is written before the outbound one.
func TestCommandSendsInboundAuditBeforeOutbound(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"admin_openids":["U-ADMIN"],"reply_footer_enabled":false}`)

	rec := startRecording(t, h)
	h.dis.HandlePayload(context.Background(), groupAtMessage("CMD-ORDER-1", "G1", "U-ADMIN", "管理员", "/tokenstats on"))

	frames := rec.waitForMsgFrames(t, "CMD-ORDER-1", 2)
	if frames[0].Direction != "in" || frames[0].Status != "command" {
		t.Fatalf("第一帧必须是 in/command，实际 %s/%s：%#v", frames[0].Direction, frames[0].Status, frames)
	}
	if frames[1].Direction != "out" || frames[1].Status != "replied" {
		t.Fatalf("第二帧必须是 out/replied，实际 %s/%s：%#v", frames[1].Direction, frames[1].Status, frames)
	}
	if !strings.Contains(frames[1].Content, "已开启 token 用量统计") {
		t.Fatalf("出站帧应带回复原文：%#v", frames[1])
	}

	inRec := waitForInboundRecord(t, h, "CMD-ORDER-1")
	outRec := waitForOutboundRecord(t, h, "CMD-ORDER-1")
	if inRec.ID >= outRec.ID {
		t.Fatalf("入站审计行必须先于出站写入（先 in 后 out），实际 in.id=%d out.id=%d", inRec.ID, outRec.ID)
	}
}

// TestTokenStatsCommandTogglesReplyFooter is the main contract: /tokenstats
// off/on changes later AI replies, the command itself never calls the model and
// never becomes conversation context.
func TestTokenStatsCommandTogglesReplyFooter(t *testing.T) {
	h := newHarness(t)
	// 夹具默认关页脚；这里显式写死，避免以后改默认值时用例跟着漂。
	h.apply(t, `{"admin_openids":["U-ADMIN"],"reply_footer_enabled":false}`)

	ctx := context.Background()
	convID := core.EncodeConversationID("group:G1:U-ADMIN")

	// (a) 管理员 @ 发送 /tokenstats off：回确认、不调模型、不写上下文。
	h.dis.HandlePayload(ctx, groupAtMessage("CMD-OFF-1", "G1", "U-ADMIN", "管理员", "/tokenstats off"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("命令应只发一条回复，实际 %d 条", len(sent))
	}
	if sent[0].Body.MsgID != "CMD-OFF-1" {
		t.Fatalf("命令回复必须是被动回复，got msg_id=%q", sent[0].Body.MsgID)
	}
	if !strings.Contains(sent[0].Body.Content, "已关闭 token 用量统计") {
		t.Fatalf("off 的确认文案不对：%q", sent[0].Body.Content)
	}
	if got := h.dis.Metrics().AIRequests.Load(); got != 0 {
		t.Fatalf("命令不应调用模型，ai_requests=%d", got)
	}
	if h.settings.Get().ReplyFooterEnabled {
		t.Fatal("off 之后 reply_footer_enabled 应为 false")
	}
	msgs, err := h.st.RecentMessages(ctx, convID, 20, false)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("命令不应写入会话上下文，实际 %d 条：%#v", len(msgs), msgs)
	}

	inRec := waitForInboundRecord(t, h, "CMD-OFF-1")
	if inRec.Status != "command" {
		t.Fatalf("入站记录状态应为 command，实际 %q", inRec.Status)
	}
	if inRec.Error != "" {
		t.Fatalf("成功命令不应带错误：%q", inRec.Error)
	}
	outRec := waitForOutboundRecord(t, h, "CMD-OFF-1")
	if outRec.Direction != "out" || outRec.Status != "replied" {
		t.Fatalf("出站记录应为 out/replied，实际 %s/%s", outRec.Direction, outRec.Status)
	}
	if !strings.Contains(outRec.Content, "已关闭 token 用量统计") {
		t.Fatalf("出站记录应保存回复原文：%q", outRec.Content)
	}

	// (b) 随后一条 AI 回复没有页脚。
	h.dis.HandlePayload(ctx, groupAtMessage("CMD-AI-1", "G1", "U-ADMIN", "管理员", "你好"))
	sent = h.waitForSent(t, 2)
	answer := sent[1].Body.Content
	if !strings.HasPrefix(answer, "这是测试回复。") {
		t.Fatalf("unexpected AI answer %q", answer)
	}
	if strings.Contains(answer, "用时") || strings.Contains(answer, "tokens") {
		t.Fatalf("关闭后不应有页脚：%q", answer)
	}
	if got := h.dis.Metrics().AIRequests.Load(); got != 1 {
		t.Fatalf("普通消息应调用一次模型，ai_requests=%d", got)
	}
	// 命令没有混进上下文：会话里只有这一轮的一问一答。
	history := h.assistantHistory(t, convID)
	if len(history) != 1 {
		t.Fatalf("assistant 上下文应为 1 条，实际 %d 条：%#v", len(history), history)
	}
	if strings.Contains(history[0], "token") || strings.Contains(history[0], "页脚") {
		t.Fatalf("命令回复不应变成 assistant 上下文：%q", history[0])
	}

	// (c) /tokenstats on 恢复后，下一条 AI 回复带页脚。
	h.dis.HandlePayload(ctx, groupAtMessage("CMD-ON-1", "G1", "U-ADMIN", "管理员", "/tokenstats on"))
	sent = h.waitForSent(t, 3)
	if !strings.Contains(sent[2].Body.Content, "已开启 token 用量统计") {
		t.Fatalf("on 的确认文案不对：%q", sent[2].Body.Content)
	}
	if !h.settings.Get().ReplyFooterEnabled {
		t.Fatal("on 之后 reply_footer_enabled 应为 true")
	}

	h.dis.HandlePayload(ctx, groupAtMessage("CMD-AI-2", "G1", "U-ADMIN", "管理员", "再问一句"))
	sent = h.waitForSent(t, 4)
	answer = sent[3].Body.Content
	if !strings.Contains(answer, "用时") || !strings.Contains(answer, "tokens") {
		t.Fatalf("开启后 AI 回复应带页脚：%q", answer)
	}
	if got := h.dis.Metrics().AIRequests.Load(); got != 2 {
		t.Fatalf("两条普通消息共两次模型调用，ai_requests=%d", got)
	}
}

// TestTokenStatsCommandRejectsNonAdmin pins the silent-ignore rule: a non-admin
// cannot toggle the setting, gets no reply at all, and the audit explains why.
func TestTokenStatsCommandRejectsNonAdmin(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"admin_openids":["U-ADMIN"],"reply_footer_enabled":false}`)

	ctx := context.Background()
	h.dis.HandlePayload(ctx, groupAtMessage("CMD-NOADMIN-1", "G1", "U-RANDOM", "路人", "/tokenstats on"))

	// 静默：不发任何消息，也不暴露命令存在。
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("非管理员执行管理命令必须静默，实际发送 %d 条", got)
	}
	if h.settings.Get().ReplyFooterEnabled {
		t.Fatal("非管理员不应能修改设置")
	}
	if got := h.dis.Metrics().AIRequests.Load(); got != 0 {
		t.Fatalf("被拒绝的命令不应调用模型，ai_requests=%d", got)
	}
	if got := h.dis.Metrics().EventsIgnored.Load(); got == 0 {
		t.Fatal("被拒绝的命令应计入 events_ignored")
	}
	rec := waitForInboundRecord(t, h, "CMD-NOADMIN-1")
	if rec.Status != "ignored" {
		t.Fatalf("入站记录应为 ignored，实际 %q", rec.Status)
	}
	if !strings.Contains(rec.Error, "非管理员") {
		t.Fatalf("忽略原因应包含「非管理员」，实际 %q", rec.Error)
	}

	// 同一条消息重复推送也不能执行第二次（MarkSeen 幂等）。
	h.dis.HandlePayload(ctx, groupAtMessage("CMD-NOADMIN-1", "G1", "U-RANDOM", "路人", "/tokenstats on"))
	if got := len(h.sentMessages()); got != 0 {
		t.Fatalf("重复推送仍然不应回复，实际发送 %d 条", got)
	}
	if h.settings.Get().ReplyFooterEnabled {
		t.Fatal("重复推送不应修改设置")
	}
}

// TestTokenStatsCommandWorksInC2C: a private chat has no @, so c2c is always
// command-eligible.
func TestTokenStatsCommandWorksInC2C(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"admin_openids":["U-ADMIN"],"reply_footer_enabled":false}`)

	ctx := context.Background()
	h.dis.HandlePayload(ctx, c2cMessage("CMD-C2C-1", "U-ADMIN", "管理员", "/tokenstats on"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("c2c 命令应回复一条，实际 %d 条", len(sent))
	}
	if sent[0].Body.MsgID != "CMD-C2C-1" {
		t.Fatalf("c2c 命令应为被动回复，got msg_id=%q", sent[0].Body.MsgID)
	}
	if !strings.Contains(sent[0].Path, "/users/") {
		t.Fatalf("c2c 回复应走用户会话接口，实际 %q", sent[0].Path)
	}
	if !strings.Contains(sent[0].Body.Content, "已开启 token 用量统计") {
		t.Fatalf("c2c 确认文案不对：%q", sent[0].Body.Content)
	}
	if !h.settings.Get().ReplyFooterEnabled {
		t.Fatal("c2c 管理员命令应生效")
	}
	rec := waitForInboundRecord(t, h, "CMD-C2C-1")
	if rec.Status != "command" {
		t.Fatalf("c2c 入站记录应为 command，实际 %q", rec.Status)
	}
}

// TestCommandPanelTapRunsBareCommand is the acceptance test for the real
// usability bug: the QQ command panel strips the leading "/" from item.name
// (platform fact), and the client inserts that name verbatim into the input box.
// A tap therefore sends "tokenstats" — no slash, no arguments. Before the fix
// that message was ordinary chat (the panel looked alive, the model got called,
// nothing happened). Now it must run the command default action.
func TestCommandPanelTapRunsBareCommand(t *testing.T) {
	h := newHarness(t)
	h.apply(t, "{\"admin_openids\":[\"U-ADMIN\"],\"reply_footer_enabled\":true}")

	ctx := context.Background()
	// 面板点击 = 管理员 @ 机器人 + 平台填入的「不带斜杠」命令名。
	h.dis.HandlePayload(ctx, groupAtMessage("CMD-BARE-1", "G1", "U-ADMIN", "管理员", "tokenstats"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("面板点击应只发一条回复，实际 %d 条", len(sent))
	}
	if sent[0].Body.MsgID != "CMD-BARE-1" {
		t.Fatalf("命令回复必须是被动回复，got msg_id=%q", sent[0].Body.MsgID)
	}
	// bare 命中 → args 为空 → 默认动作 status，回复里带当前状态与用法。
	content := sent[0].Body.Content
	if !strings.Contains(content, "token 用量统计：已开启") {
		t.Fatalf("面板点击应执行默认 status，实际回复 %q", content)
	}
	if !strings.Contains(content, "/tokenstats [on|off|status]") {
		t.Fatalf("status 回复应带用法：%q", content)
	}

	// 与所有命令一样：不调模型、不写会话上下文。
	if got := h.dis.Metrics().AIRequests.Load(); got != 0 {
		t.Fatalf("面板点击的命令不应调用模型，ai_requests=%d", got)
	}
	convID := core.EncodeConversationID("group:G1:U-ADMIN")
	msgs, err := h.st.RecentMessages(ctx, convID, 20, false)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("命令不应写入会话上下文，实际 %d 条：%#v", len(msgs), msgs)
	}

	rec := waitForInboundRecord(t, h, "CMD-BARE-1")
	if rec.Status != "command" {
		t.Fatalf("面板点击的入站记录应为 command，实际 %q（error=%q）", rec.Status, rec.Error)
	}
	outRec := waitForOutboundRecord(t, h, "CMD-BARE-1")
	if outRec.Direction != "out" || outRec.Status != "replied" {
		t.Fatalf("面板点击的出站记录应为 out/replied，实际 %s/%s", outRec.Direction, outRec.Status)
	}
}

// TestBareNameWithArgumentsIsNotACommand guards the boundary of the panel-tap
// rule: only the whole message being exactly one registered token counts.
// "@机器人 tokenstats off" without a slash has extra words and must stay
// ordinary chat, otherwise "help me with X" would become a command.
func TestBareNameWithArgumentsIsNotACommand(t *testing.T) {
	h := newHarness(t)
	h.apply(t, "{\"admin_openids\":[\"U-ADMIN\"],\"reply_footer_enabled\":true}")

	ctx := context.Background()
	h.dis.HandlePayload(ctx, groupAtMessage("CMD-BARE-ARGS-1", "G1", "U-ADMIN", "管理员", "tokenstats off"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("应走普通消息路径（一条 AI 回复），实际 %d 条", len(sent))
	}
	if strings.Contains(sent[0].Body.Content, "已关闭 token 用量统计") {
		t.Fatalf("无斜杠带参数不应被当成命令：%q", sent[0].Body.Content)
	}
	if !h.settings.Get().ReplyFooterEnabled {
		t.Fatal("无斜杠带参数不应修改设置")
	}
	if got := h.dis.Metrics().AIRequests.Load(); got != 1 {
		t.Fatalf("未命中的消息应走普通 AI 路径，ai_requests=%d", got)
	}
	rec := waitForInboundRecord(t, h, "CMD-BARE-ARGS-1")
	if rec.Status == "command" {
		t.Fatalf("无斜杠带参数的消息不应被记为 command：%#v", rec)
	}
}

// TestUnaddressedSlashIsNotACommand: in an all_messages group the bot sees
// every message, but a /tokenstats without @ is ordinary chatter and must not
// change settings.
func TestUnaddressedSlashIsNotACommand(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"admin_openids":["U-ADMIN"],"reply_footer_enabled":false,"trigger_mode":"all_messages"}`)
	sm := h.reloadSettings()
	h.dis = h.build(sm)

	ctx := context.Background()
	h.dis.HandlePayload(ctx, groupAnyMessage("CMD-PLAIN-1", "G1", "U-ADMIN", "管理员", "/tokenstats on"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("未 @ 的消息应按普通消息处理（一条 AI 回复），实际 %d 条", len(sent))
	}
	if strings.Contains(sent[0].Body.Content, "已开启 token 用量统计") {
		t.Fatalf("未 @ 的 /tokenstats 不应被当成命令：%q", sent[0].Body.Content)
	}
	if sm.Get().ReplyFooterEnabled {
		t.Fatal("未 @ 的 /tokenstats 不应修改设置")
	}
	if got := h.dis.Metrics().AIRequests.Load(); got != 1 {
		t.Fatalf("未 @ 的消息应走普通 AI 路径，ai_requests=%d", got)
	}
	rec := waitForInboundRecord(t, h, "CMD-PLAIN-1")
	if rec.Status == "command" {
		t.Fatalf("未 @ 的消息不应被记为 command：%#v", rec)
	}
}

// TestDuplicateCommandRunsOnce pins msg_id idempotency for the command path.
func TestDuplicateCommandRunsOnce(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"admin_openids":["U-ADMIN"],"reply_footer_enabled":true}`)

	ctx := context.Background()
	p := groupAtMessage("CMD-DUP-1", "G1", "U-ADMIN", "管理员", "/tokenstats off")
	h.dis.HandlePayload(ctx, p)
	h.dis.HandlePayload(ctx, p)

	sent := h.waitForSent(t, 1)
	time.Sleep(100 * time.Millisecond)
	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("重复推送应只执行一次，实际发送 %d 条", got)
	}
	if len(sent) != 1 {
		t.Fatalf("重复推送应只回复一次，实际 %d 条", len(sent))
	}

	recs, _, err := h.st.ListRecords(ctx, store.RecordFilter{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	in, out := 0, 0
	for _, r := range recs {
		if r.MsgID != "CMD-DUP-1" {
			continue
		}
		if r.Direction == "in" {
			in++
		} else {
			out++
		}
	}
	if in != 1 || out != 1 {
		t.Fatalf("重复推送应只留一对审计记录，实际 in=%d out=%d", in, out)
	}
}

// TestHelpCommandIsPublicAndDoesNotCallModel: /help 是普通命令（不要求管理员），
// 列出所有命令且别名不重复，并且和所有命令一样不进入对话管线。
func TestHelpCommandIsPublicAndDoesNotCallModel(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.dis.HandlePayload(ctx, groupAtMessage("CMD-HELP-1", "G1", "U-RANDOM", "路人", "/help"))

	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("help 应只回复一条，实际 %d 条", len(sent))
	}
	if sent[0].Body.MsgID != "CMD-HELP-1" {
		t.Fatalf("help 回复应为被动回复，got msg_id=%q", sent[0].Body.MsgID)
	}
	content := sent[0].Body.Content
	for _, want := range []string{
		"可用命令：",
		"/help — 查看可用命令",
		"/tokenstats [on|off|status] — 开关 token 用量统计（仅管理员）",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("help 输出缺少 %q：%q", want, content)
		}
	}
	if got := strings.Count(content, "开关 token 用量统计"); got != 1 {
		t.Fatalf("别名不应重复出现（出现 %d 次）：%q", got, content)
	}
	if got := h.dis.Metrics().AIRequests.Load(); got != 0 {
		t.Fatalf("help 不应调用模型，ai_requests=%d", got)
	}
	convID := core.EncodeConversationID("group:G1:U-RANDOM")
	msgs, err := h.st.RecentMessages(ctx, convID, 20, false)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("help 不应写入会话上下文，实际 %d 条", len(msgs))
	}
	rec := waitForInboundRecord(t, h, "CMD-HELP-1")
	if rec.Status != "command" {
		t.Fatalf("help 入站记录应为 command，实际 %q", rec.Status)
	}
	if rec.Error != "" {
		t.Fatalf("help 不应带错误：%q", rec.Error)
	}
}
