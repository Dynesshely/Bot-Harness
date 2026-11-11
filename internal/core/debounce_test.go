package core

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/settings"
)

// 群聊消息合并的单元测试：档位映射、活跃判定、批次折叠、20s 硬上限与上下文取消。
// 端到端行为（一次模型调用 / 两条消息都留痕）在 internal/e2e/pipeline_test.go。

func newDebounceTestDispatcher(t *testing.T, cap time.Duration) *Dispatcher {
	t.Helper()
	return &Dispatcher{
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		debounce:    newDebouncer(),
		debounceCap: cap,
		baseCtx:     context.Background(),
	}
}

func inboundForDebounce(msgID string) *Inbound {
	return &Inbound{MsgID: msgID, Scene: "group", GroupOpenID: "G1", Content: "一句想法"}
}

func TestDebounceWindowsFollowTheMode(t *testing.T) {
	cases := []struct {
		mode          string
		quiet, active int
		wantQuiet     time.Duration
		wantActive    time.Duration
	}{
		{"off", 2, 5, 0, 0},
		{"fast", 2, 5, time.Second, 3 * time.Second},
		{"natural", 2, 5, 2 * time.Second, 5 * time.Second},
		{"steady", 2, 5, 4 * time.Second, 7 * time.Second},
		// 空值 / 未知值按 natural 处理（normalize 也会把它兜成 natural）。
		{"", 3, 6, 3 * time.Second, 6 * time.Second},
		// 数字为 0 时两个窗口都是 0，调用方据此走原来的逐条路径。
		{"natural", 0, 0, 0, 0},
	}
	for _, c := range cases {
		quiet, active := debounceWindows(c.mode, c.quiet, c.active)
		if quiet != c.wantQuiet || active != c.wantActive {
			t.Fatalf("debounceWindows(%q,%d,%d) = %v/%v, want %v/%v",
				c.mode, c.quiet, c.active, quiet, active, c.wantQuiet, c.wantActive)
		}
	}
}

func TestDebounceGroupBecomesActiveAfterThreeMessages(t *testing.T) {
	d := newDebounceTestDispatcher(t, 0)
	now := time.Now()

	if d.debounce.groupActive("G1", now) {
		t.Fatal("没有任何消息时不应判定活跃")
	}
	if d.debounce.noteHuman("G1", now.Add(-2*time.Minute)) {
		t.Fatal("一条两分钟前的消息不应让群变活跃")
	}
	// 窗口外的旧消息不计数：60 秒内只有这一条。
	if d.debounce.noteHuman("G1", now) {
		t.Fatal("窗口内只有一条消息时不应判定活跃")
	}
	if d.debounce.noteHuman("G1", now.Add(time.Second)) {
		t.Fatal("两条消息不应判定活跃")
	}
	if !d.debounce.noteHuman("G1", now.Add(2*time.Second)) {
		t.Fatal("60 秒内第三条人类消息应判定为活跃")
	}
	// 只读判定不该新增记录：同一时刻连续查询结果稳定。
	for i := 0; i < 3; i++ {
		if !d.debounce.groupActive("G1", now.Add(2*time.Second)) {
			t.Fatal("活跃状态在窗口内应保持")
		}
	}
	// 窗口过去之后重新变回安静。
	if d.debounce.groupActive("G1", now.Add(3*time.Minute)) {
		t.Fatal("60 秒窗口过去后应回到非活跃")
	}
	// 另一个群互不影响。
	if d.debounce.noteHuman("G2", now) {
		t.Fatal("活跃判定必须按群独立")
	}
}

// 合并窗口按活跃度选 quiet / active；active 只由「本群最近 60 秒 >= 3 条」决定，
// 与全局人味窗口（5 分钟）无关。
func TestDebounceWaitUsesTheActiveWindowForBusyGroups(t *testing.T) {
	m, err := settings.New(context.Background(), nil, settings.Defaults())
	if err != nil {
		t.Fatalf("new settings: %v", err)
	}
	d := newDebounceTestDispatcher(t, 0)
	d.settings = m
	s := settings.Defaults()
	s.ReplyDebounceMode = "natural"
	s.ReplyDebounceQuietSeconds = 2
	s.ReplyDebounceActiveSeconds = 5

	for i := 0; i < 2; i++ {
		d.noteGroupHuman(&Inbound{Scene: "group", GroupOpenID: "G1"})
	}
	if got := d.debounceWaitForGroup(s, "G1"); got != 2*time.Second {
		t.Fatalf("两条消息的群应使用 quiet 窗口，实际 %v", got)
	}
	d.noteGroupHuman(&Inbound{Scene: "group", GroupOpenID: "G1"})
	if got := d.debounceWaitForGroup(s, "G1"); got != 5*time.Second {
		t.Fatalf("三条消息的群应使用 active 窗口，实际 %v", got)
	}
	// 另一个群仍然安静。
	if got := d.debounceWaitForGroup(s, "G2"); got != 2*time.Second {
		t.Fatalf("安静群应使用 quiet 窗口，实际 %v", got)
	}
	// off / 秒数 0：不等待。
	s.ReplyDebounceMode = "off"
	if got := d.debounceWaitForGroup(s, "G1"); got != 0 {
		t.Fatalf("off 模式不应等待，实际 %v", got)
	}
	s.ReplyDebounceMode = "natural"
	s.ReplyDebounceQuietSeconds = 0
	s.ReplyDebounceActiveSeconds = 0
	if got := d.debounceWaitForGroup(s, "G1"); got != 0 {
		t.Fatalf("两个窗口都是 0 时不应等待，实际 %v", got)
	}
}

func TestDebounceEnqueueFoldsIntoOneBatch(t *testing.T) {
	b := newDebouncer()
	first := inboundForDebounce("M1")
	batch, leader := b.enqueue("G1", first, time.Second)
	if batch == nil || !leader {
		t.Fatalf("第一条消息应创建批次，batch=%v leader=%v", batch, leader)
	}
	if batch.leaderMsgID() != "M1" {
		t.Fatalf("批次 leader = %q, want M1", batch.leaderMsgID())
	}

	second := inboundForDebounce("M2")
	again, leader := b.enqueue("G1", second, 3*time.Second)
	if again != batch {
		t.Fatal("同一群的第二条消息必须进同一个批次")
	}
	if leader {
		t.Fatal("只有批次的第一条消息是 leader")
	}
	if got := batch.currentWait(); got != 3*time.Second {
		t.Fatalf("窗口应被新消息重置为 3s，实际 %v", got)
	}
	// 另一个群是独立批次。
	other, leader := b.enqueue("G2", inboundForDebounce("M3"), time.Second)
	if other == batch || !leader {
		t.Fatal("不同群必须各自成批")
	}
	if n := len(b.batches); n != 2 {
		t.Fatalf("应有两个 pending 批次，实际 %d", n)
	}
	if got := b.pop("G1"); got != batch {
		t.Fatal("pop 应返回该群的批次")
	}
	if got := b.pop("G1"); got != nil {
		t.Fatal("批次取出后不能再被取到")
	}
}

func TestDebounceWaitResetsButNeverPassesTheCap(t *testing.T) {
	// 硬上限 400ms、每次窗口 250ms：如果没有上限，连续 reset 会让等待无限延长。
	const cap = 400 * time.Millisecond
	d := newDebounceTestDispatcher(t, cap)
	batch, _ := d.debounce.enqueue("G1", inboundForDebounce("M1"), 250*time.Millisecond)

	go func() {
		// 每 80ms 来一条新消息，一直把窗口重置到 250ms 之后。
		for i := 0; i < 6; i++ {
			time.Sleep(80 * time.Millisecond)
			d.debounce.enqueue("G1", inboundForDebounce("MX"), 250*time.Millisecond)
		}
	}()

	started := time.Now()
	if !d.waitForBatch(d.base(), batch) {
		t.Fatal("窗口到点应返回 true")
	}
	elapsed := time.Since(started)
	if elapsed < cap {
		t.Fatalf("硬上限之前就结束了：%v < %v", elapsed, cap)
	}
	if elapsed > cap+250*time.Millisecond {
		t.Fatalf("硬上限失效，等待了 %v（上限 %v）", elapsed, cap)
	}
}

func TestDebounceWaitIsCancelledByBaseContext(t *testing.T) {
	d := newDebounceTestDispatcher(t, 0)
	batch, _ := d.debounce.enqueue("G1", inboundForDebounce("M1"), 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- d.waitForBatch(ctx, batch) }()
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case ok := <-done:
		if ok {
			t.Fatal("上下文取消后不应再处理这条消息")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("上下文取消后 waitForBatch 应立即返回，不能泄漏 goroutine")
	}
}

func TestDebounceCloseDropsPendingBatches(t *testing.T) {
	b := newDebouncer()
	b.enqueue("G1", inboundForDebounce("M1"), time.Second)
	b.enqueue("G2", inboundForDebounce("M2"), time.Second)

	// 关闭必须立刻唤醒等待中的 leader，而不是让它继续睡到窗口到点
	// （那会在进程已经收尾之后仍然跑一轮模型调用）。
	leaderCtx := context.Background()
	d := &Dispatcher{debounce: b, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	woke := make(chan bool, 1)
	batch, _ := b.enqueue("G3", inboundForDebounce("M3"), 10*time.Second)
	go func() { woke <- d.waitForBatch(leaderCtx, batch) }()
	time.Sleep(20 * time.Millisecond)

	if n := b.close(); n != 3 {
		t.Fatalf("关闭时应报告 3 个 pending 批次，实际 %d", n)
	}
	select {
	case ok := <-woke:
		if ok {
			t.Fatal("关闭后 leader 不应再处理消息")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("关闭应立即唤醒等待中的 leader")
	}
	// 关闭后不再排队：调用方看到 nil 批次就回到原来的逐条路径。
	if batch, leader := b.enqueue("G3", inboundForDebounce("M3"), time.Second); batch != nil || !leader {
		t.Fatalf("关闭后不应再创建批次，batch=%v leader=%v", batch, leader)
	}
	if n := b.close(); n != 0 {
		t.Fatalf("重复关闭应报告 0 个批次，实际 %d", n)
	}
}

// 设置键的校验：非法模式与越界秒数必须报错，合法值必须能写进去。
func TestReplyDebounceSettingsApplyAndValidate(t *testing.T) {
	m, err := settings.New(context.Background(), nil, settings.Defaults())
	if err != nil {
		t.Fatalf("new settings: %v", err)
	}
	ctx := context.Background()

	good := map[string]json.RawMessage{
		"reply_debounce_mode":           json.RawMessage(`"STEADY"`),
		"reply_debounce_quiet_seconds":  json.RawMessage("15"),
		"reply_debounce_active_seconds": json.RawMessage("0"),
	}
	got, _, err := m.Apply(ctx, good)
	if err != nil {
		t.Fatalf("合法防抖设置被拒绝：%v", err)
	}
	if got.ReplyDebounceMode != "steady" || got.ReplyDebounceQuietSeconds != 15 || got.ReplyDebounceActiveSeconds != 0 {
		t.Fatalf("防抖设置未生效：%+v", got)
	}

	bad := []map[string]json.RawMessage{
		{"reply_debounce_mode": json.RawMessage(`"sometimes"`)},
		{"reply_debounce_mode": json.RawMessage(`""`)},
		{"reply_debounce_quiet_seconds": json.RawMessage("-1")},
		{"reply_debounce_quiet_seconds": json.RawMessage("16")},
		{"reply_debounce_active_seconds": json.RawMessage("-1")},
		{"reply_debounce_active_seconds": json.RawMessage("16")},
	}
	for _, patch := range bad {
		if _, _, err := m.Apply(ctx, patch); err == nil {
			t.Fatalf("非法防抖设置被接受：%v", patch)
		}
	}
}

// 默认值与文档一致：natural + quiet 2 / active 5。
func TestReplyDebounceDefaults(t *testing.T) {
	s := settings.Defaults()
	if s.ReplyDebounceMode != "natural" {
		t.Fatalf("reply_debounce_mode default = %q, want natural", s.ReplyDebounceMode)
	}
	if s.ReplyDebounceQuietSeconds != 2 || s.ReplyDebounceActiveSeconds != 5 {
		t.Fatalf("默认窗口 = %d/%d, want 2/5",
			s.ReplyDebounceQuietSeconds, s.ReplyDebounceActiveSeconds)
	}
	keys := strings.Join(settings.KnownKeys(), ",")
	for _, key := range []string{
		"reply_debounce_mode", "reply_debounce_quiet_seconds", "reply_debounce_active_seconds",
	} {
		if !strings.Contains(keys, key) {
			t.Fatalf("KnownKeys 缺少 %q", key)
		}
	}
}
