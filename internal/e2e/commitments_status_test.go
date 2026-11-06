package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/store"
)

// 承诺的完整生命周期：到点追问 → 模型给出下一次间隔 → 再追问 → 用户回执收口。
func TestCommitmentNudgeUntilAckDone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"admin_openids":["U1"],"commitment_enabled":true}`)

	convID := core.EncodeConversationID("group:G1:U1")
	// 先通过模型工具建立承诺：验证 create_commitment 的完整链路。
	h.mu.Lock()
	h.aiToolScript = []string{`create_commitment|{"text":"晚上八点吃药","first_delay_seconds":60,"priority":2}`}
	h.mu.Unlock()
	h.dis.HandlePayload(ctx, groupAtMessage("M-CREATE", "G1", "U1", "小明", "提醒我晚上八点吃药"))
	if sent := h.waitForSent(t, 1); len(sent) < 1 {
		t.Fatal("create_commitment reply not delivered")
	}
	items, err := h.st.ListCommitments(ctx, convID, "U1", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("commitment not created by the tool: %v %+v", err, items)
	}
	id := items[0].ID
	if err := h.st.SetCommitmentNextCheck(ctx, id, time.Now().Add(-time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatalf("make it due: %v", err)
	}

	h.mu.Lock()
	h.commitmentReply = `{"message":"八点啦，记得吃药。","next_check_seconds":120,"reason":"刚提醒完先等两分钟"}`
	h.mu.Unlock()
	h.dis.RunDueCommitments(ctx, time.Now())
	if sent := h.sentMessages(); len(sent) != 2 {
		t.Fatalf("expected one nudge after the tool reply, got %d", len(sent))
	}
	items, err = h.st.ListCommitments(ctx, convID, "U1", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("list commitments: %v %+v", err, items)
	}
	first := items[0]
	if first.Status != store.CommitmentWaiting || first.NudgeCount != 1 || first.LastNudgeAt == "" {
		t.Fatalf("after first nudge: %+v", first)
	}
	next, err := time.Parse(time.RFC3339, first.NextCheckAt)
	if err != nil {
		t.Fatalf("next_check_at: %v", err)
	}
	if delta := time.Until(next); delta < time.Minute || delta > 3*time.Minute {
		t.Fatalf("next check should be ~120s, got %s", delta)
	}

	// 第二追问：模型这次给一个更长的间隔。
	if err := h.st.SetCommitmentNextCheck(ctx, id, time.Now().Add(-time.Second).Format(time.RFC3339)); err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	h.mu.Lock()
	h.commitmentReply = `{"message":"再问一下，药吃了吗？","next_check_seconds":3600,"reason":"隔一小时再看"}`
	h.mu.Unlock()
	h.dis.RunDueCommitments(ctx, time.Now())
	if sent := h.sentMessages(); len(sent) != 3 {
		t.Fatalf("expected a second nudge, got %d", len(sent))
	}
	items, _ = h.st.ListCommitments(ctx, convID, "U1", 10)
	if items[0].NudgeCount != 2 {
		t.Fatalf("nudge_count = %d, want 2", items[0].NudgeCount)
	}

	// 用户回执：模型调用 ack_commitment(done)，承诺收口。
	h.mu.Lock()
	h.aiToolScript = []string{`ack_commitment|{"id":` + itoa(id) + `,"outcome":"done","note":"已经吃过了"}`}
	h.mu.Unlock()
	h.dis.HandlePayload(ctx, groupAtMessage("M-ACK", "G1", "U1", "小明", "我吃过药了"))
	if sent := h.waitForSent(t, 4); len(sent) < 4 {
		t.Fatalf("ack reply not delivered: %d messages", len(sent))
	}
	items, _ = h.st.ListCommitments(ctx, convID, "U1", 10)
	if items[0].Status != store.CommitmentDone || items[0].AckNote != "已经吃过了" || items[0].AckAt == "" {
		t.Fatalf("commitment not closed: %+v", items[0])
	}
	if got := h.dis.Metrics().Snapshot().CommitmentAcked; got < 1 {
		t.Fatalf("commitment_acked = %d", got)
	}
}

func TestCommitmentPostponeExtendsNextCheck(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"admin_openids":["U1"],"commitment_enabled":true}`)
	convID := core.EncodeConversationID("c2c:U2")
	id, err := h.st.AddCommitment(ctx, store.CommitmentInput{
		ConversationID: convID, MemberOpenID: "U2", CreatorOpenID: "U2",
		Text: "交周报", NextCheckAt: time.Now().Add(-time.Minute).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	h.mu.Lock()
	h.aiToolScript = []string{`ack_commitment|{"id":` + itoa(id) + `,"outcome":"postpone","remind_after_seconds":3600,"note":"晚点再弄"}`}
	h.mu.Unlock()
	h.dis.HandlePayload(ctx, c2cMessage("M-POST", "U2", "小红", "晚点再提醒我吧"))
	if sent := h.waitForSent(t, 1); len(sent) < 1 {
		t.Fatal("postpone reply not delivered")
	}
	items, _ := h.st.ListCommitments(ctx, convID, "U2", 10)
	if items[0].Status != store.CommitmentWaiting {
		t.Fatalf("postponed commitment status = %q", items[0].Status)
	}
	next, _ := time.Parse(time.RFC3339, items[0].NextCheckAt)
	if delta := time.Until(next); delta < 50*time.Minute || delta > 70*time.Minute {
		t.Fatalf("postpone should be ~1h, got %s", delta)
	}
	if got := h.dis.Metrics().Snapshot().CommitmentPostponed; got < 1 {
		t.Fatalf("commitment_postponed = %d", got)
	}
}

// TestCommitmentClosedDuringNudgeStaysClosed is the end-to-end half of the
// in-flight race: the scheduler leases a due row, the decision model takes a
// moment, and the user closes the promise before the send/reschedule finishes.
// The nudge may already be on the wire, but the row must stay done: no timeline
// entry, no nudge counter, no next_check rewrite.
func TestCommitmentClosedDuringNudgeStaysClosed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"admin_openids":["U1"],"commitment_enabled":true}`)

	convID := core.EncodeConversationID("group:G1:U1")
	id, err := h.st.AddCommitment(ctx, store.CommitmentInput{
		ConversationID: convID, GroupOpenID: "G1", MemberOpenID: "U1", CreatorOpenID: "U1",
		Text: "交周报", NextCheckAt: time.Now().Add(-time.Minute).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	h.mu.Lock()
	h.commitmentReply = `{"message":"周报交了吗？","next_check_seconds":3600,"reason":"测试"}`
	h.aiDelay = 300 * time.Millisecond
	h.mu.Unlock()

	done := make(chan struct{})
	go func() {
		h.dis.RunDueCommitments(ctx, time.Now())
		close(done)
	}()

	// Wait until the scheduler has claimed the row (status=leased), then close
	// it exactly as ack_commitment(done) would while the model call is running.
	waitFor(t, 5*time.Second, func() bool {
		items, err := h.st.ListCommitments(ctx, convID, "U1", 10)
		return err == nil && len(items) == 1 && items[0].Status == store.CommitmentLeased
	})
	before, err := h.st.ListCommitments(ctx, convID, "U1", 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("list before close: %v %+v", err, before)
	}
	leasedNext := before[0].NextCheckAt
	if err := h.st.FinishCommitment(ctx, id, store.CommitmentDone, "已经交了"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunDueCommitments did not finish")
	}

	if sent := h.sentMessages(); len(sent) != 1 {
		t.Fatalf("the nudge was already in flight and should have been sent once, got %d", len(sent))
	}
	items, err := h.st.ListCommitments(ctx, convID, "U1", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("list after nudge: %v %+v", err, items)
	}
	if items[0].Status != store.CommitmentDone || items[0].AckNote != "已经交了" {
		t.Fatalf("the in-flight nudge resurrected a closed promise: %+v", items[0])
	}
	if items[0].NextCheckAt != leasedNext || items[0].NudgeCount != 0 {
		t.Fatalf("closed row was rewritten: next_check=%q nudge_count=%d (want %q / 0)",
			items[0].NextCheckAt, items[0].NudgeCount, leasedNext)
	}
	msgs, err := h.st.Messages(ctx, convID, 50, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("a nudge that lost the reschedule race must not enter the timeline: %+v", msgs)
	}
	if got := h.dis.Metrics().Snapshot().CommitmentNudges; got != 0 {
		t.Fatalf("commitment_nudges = %d, want 0 for a dropped in-flight nudge", got)
	}
}

// 模型决策失败时不能把承诺卡死：用模板文案 + 3 小时保守间隔继续。
func TestCommitmentModelFailureFallsBack(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	convID := core.EncodeConversationID("group:G1:U1")
	_, err := h.st.AddCommitment(ctx, store.CommitmentInput{
		ConversationID: convID, GroupOpenID: "G1", MemberOpenID: "U1", CreatorOpenID: "U1",
		Text: "给猫铲屎", NextCheckAt: time.Now().Add(-time.Minute).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	h.mu.Lock()
	h.aiFail = true
	h.mu.Unlock()
	h.dis.RunDueCommitments(ctx, time.Now())

	sent := h.sentMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].Body.Content, "给猫铲屎") {
		t.Fatalf("fallback nudge = %+v", sent)
	}
	items, _ := h.st.ListCommitments(ctx, convID, "U1", 10)
	if items[0].NudgeCount != 1 || items[0].LastNudgeAt == "" {
		t.Fatalf("fallback must still advance the loop: %+v", items[0])
	}
	next, _ := time.Parse(time.RFC3339, items[0].NextCheckAt)
	if delta := time.Until(next); delta < 2*time.Hour+50*time.Minute || delta > 3*time.Hour+10*time.Minute {
		t.Fatalf("fallback interval should be ~3h, got %s", delta)
	}
}

// 人类状态：累计到阈值触发一次异步推测，同一会话并发只跑一次。
func TestHumanStatusTriggerAndSingleFlight(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"human_status_enabled":true,"human_status_trigger_messages":5,"human_status_ttl_minutes":120}`)
	convID := core.EncodeConversationID("group:G1:U1")
	h.mu.Lock()
	h.aiDelay = 150 * time.Millisecond
	h.humanStatusReply = `[{"member_openid":"U1","display_name":"小明","status":"打游戏","confidence":0.8,"evidence":"刚说在开黑"}]`
	h.mu.Unlock()

	for i := 0; i < 5; i++ {
		h.dis.HandlePayload(ctx, groupAnyMessage("HS1-"+itoa(int64(i)), "G1", "U1", "小明", "开黑中，先不聊"))
	}
	// 第一次推测还在跑的时候再凑一批：不能被启动第二次。
	for i := 0; i < 5; i++ {
		h.dis.HandlePayload(ctx, groupAnyMessage("HS2-"+itoa(int64(i)), "G1", "U1", "小明", "还在打"))
	}
	waitFor(t, 3*time.Second, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.humanStatusCalls >= 1
	})
	time.Sleep(300 * time.Millisecond)
	h.mu.Lock()
	calls := h.humanStatusCalls
	h.mu.Unlock()
	if calls != 1 {
		t.Fatalf("human status calls = %d, want 1 (single-flight)", calls)
	}
	statuses, err := h.st.ListHumanStatuses(ctx, convID)
	if err != nil || len(statuses) != 1 {
		t.Fatalf("statuses = %+v err=%v", statuses, err)
	}
	if statuses[0].Status != "打游戏" || statuses[0].MemberOpenID != "U1" {
		t.Fatalf("stored status = %+v", statuses[0])
	}
	if got := h.dis.Metrics().Snapshot().HumanStatusRuns; got < 1 {
		t.Fatalf("human_status_runs = %d", got)
	}
}

// 推测失败只记计数 + WARN，不写状态、不影响聊天。
func TestHumanStatusInferenceFailureIsSoft(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"human_status_enabled":true,"human_status_trigger_messages":5}`)
	h.mu.Lock()
	h.humanStatusReply = "这不是 JSON"
	h.mu.Unlock()
	convID := core.EncodeConversationID("group:G1:U1")
	for i := 0; i < 5; i++ {
		h.dis.HandlePayload(ctx, groupAnyMessage("HSF-"+itoa(int64(i)), "G1", "U1", "小明", "随便聊聊"))
	}
	waitFor(t, 2*time.Second, func() bool {
		return h.dis.Metrics().Snapshot().HumanStatusFailures >= 1
	})
	statuses, err := h.st.ListHumanStatuses(ctx, convID)
	if err != nil || len(statuses) != 0 {
		t.Fatalf("failed inference must not store anything: %+v err=%v", statuses, err)
	}
}

// 提示注入：未收口承诺与状态推测都进入系统提示，且不进入用户可见消息。
func TestCommitmentAndHumanStatusPromptInjection(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	convID := core.EncodeConversationID("group:G1:U1")
	_, err := h.st.AddCommitment(ctx, store.CommitmentInput{
		ConversationID: convID, GroupOpenID: "G1", MemberOpenID: "U1", CreatorOpenID: "U1",
		Text: "周五前交报告", NextCheckAt: time.Now().Add(2 * time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("add commitment: %v", err)
	}
	if err := h.st.UpsertHumanStatus(ctx, store.HumanStatusInput{
		Scope: store.HumanStatusGroup, ConversationID: convID, GroupOpenID: "G1",
		MemberOpenID: "U1", DisplayName: "小明", Status: "工作",
		Confidence: 0.9, Evidence: "说在赶稿",
		UpdatedAt: time.Now().Format(time.RFC3339),
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert status: %v", err)
	}

	h.dis.HandlePayload(ctx, groupAtMessage("M-INJ", "G1", "U1", "小明", "在吗"))
	if sent := h.waitForSent(t, 1); len(sent) < 1 {
		t.Fatal("reply not delivered")
	}
	prompt := h.systemPrompt(t)
	for _, want := range []string{"未收口的承诺", "周五前交报告", "当前聊天对象状态推测", "工作"} {
		if !prompt.contains(want) {
			t.Fatalf("system prompt missing %q: %s", want, prompt.text)
		}
	}
	sent := h.sentMessages()
	if strings.Contains(sent[0].Body.Content, "周五前交报告") {
		t.Fatalf("prompt-only context leaked into the visible reply: %q", sent[0].Body.Content)
	}
}

func TestHumanStatusCommandRendersListAndFencedTable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"admin_openids":["U1"],"markdown_enabled":false}`)
	// 这条用例钉的是**文本回退**的排版：让上传失败，命令就会把完整的文本列表
	// 发回来（成功路径的图片由 human_status_command_test.go 覆盖）。
	h.failUploads(true)
	convID := core.EncodeConversationID("group:G1:U1")
	// 24h 发言者名单是 /human-status 的硬限制：没有发言就没有可分析对象，所以
	// 这条用例必须同时拥有一条 24h 内的消息（缓存只负责失败回退的渲染）。
	if err := h.st.AppendMessage(ctx, store.MessageInput{
		ConversationID: convID, Role: "user", Kind: store.KindDialogue,
		Content: "今晚继续开黑", Speaker: "小明", SpeakerOpenID: "U1",
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	// 50 个字符的依据用来钉「截 40 rune」：纯文本列表不截对象名，但要截依据。
	longEvidence := strings.Repeat("开黑", 25)
	if err := h.st.UpsertHumanStatus(ctx, store.HumanStatusInput{
		Scope: store.HumanStatusGroup, ConversationID: convID, GroupOpenID: "G1",
		MemberOpenID: "U1", DisplayName: "小明", Status: "打游戏",
		Confidence: 0.82, Evidence: longEvidence,
		UpdatedAt: time.Now().Format(time.RFC3339),
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	h.dis.HandlePayload(ctx, groupAtMessage("M-HS", "G1", "U1", "小明", "/human-status"))
	sent := h.waitForSent(t, 1)
	if len(sent) < 1 {
		t.Fatal("command reply not delivered")
	}
	reply := sent[0].Body.Content
	for _, want := range []string{"最近聊天对象状态（1 人）：", "- ", "｜", "小明", "打游戏", "82%", "…"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("plain status list missing %q: %q", want, reply)
		}
	}
	// QQ 群聊是比例字体：纯文本不能有 markdown 表格、也不能靠空格 padding 对齐。
	for _, banned := range []string{"| ---", "  "} {
		if strings.Contains(reply, banned) {
			t.Fatalf("plain status list must not contain %q: %q", banned, reply)
		}
	}
	if strings.Contains(reply, longEvidence) {
		t.Fatalf("evidence must be truncated to 40 runes: %q", reply)
	}
	t.Logf("e2e 纯文本回复:\n%s", reply)

	h.apply(t, `{"markdown_enabled":true}`)
	h.dis.HandlePayload(ctx, groupAtMessage("M-HS-MD", "G1", "U1", "小明", "/human-status"))
	sent = h.waitForSent(t, 2)
	last := sent[len(sent)-1]
	md := last.Body.Content
	if last.Body.Markdown != nil {
		md = last.Body.Markdown.Content
	}
	for _, want := range []string{"```text", "对象", "状态", "置信度", "依据", "更新", "小明", "打游戏", "82%", "---"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown status table missing %q: %q", want, md)
		}
	}
	if strings.Contains(md, "| ---") {
		t.Fatalf("QQ markdown does not document pipe tables; use a fenced table: %q", md)
	}
	t.Logf("e2e markdown 回复:\n%s", md)
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

// itoa avoids importing strconv in every test literal.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
