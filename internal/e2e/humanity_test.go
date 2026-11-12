package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/core"
)

// 群消息合并：同一窗口内的未 @ 消息只触发一次模型调用，其余消息仍进时间线。
func TestDebounceMergesUnaddressedMessages(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"trigger_mode":"all_messages","reply_debounce_mode":"natural","reply_debounce_quiet_seconds":1,"reply_debounce_active_seconds":1}`)

	for i := 0; i < 3; i++ {
		h.dis.HandlePayload(context.Background(),
			groupAnyMessage("DB"+string(rune('1'+i)), "G1", "U1", "小明", "一句想法"))
		time.Sleep(50 * time.Millisecond)
	}
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("3 条同窗口消息应合并为 1 条回复，实际 %d", len(sent))
	}
	// 稍等片刻确认没有第二条回复。
	time.Sleep(300 * time.Millisecond)
	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("不应有额外回复，实际 %d", got)
	}
	convID := core.EncodeConversationID("group:G1:U1")
	msgs, err := h.st.RecentMessages(context.Background(), convID, 10, false)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("被合并的消息也应写时间线，实际 %d 条", len(msgs))
	}
}

// @ 消息不受合并窗口影响：立即处理。
func TestDebounceAtMessageIsImmediate(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"reply_debounce_mode":"steady","reply_debounce_active_seconds":5}`)
	start := time.Now()
	h.dis.HandlePayload(context.Background(), groupAtMessage("DBAT", "G1", "U1", "小明", "在吗"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("@ 消息应立即回复，实际 %d", len(sent))
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("@ 消息被合并窗口拖慢了：%v", elapsed)
	}
	if !strings.Contains(sentText(sent[0]), "测试回复") {
		t.Fatalf("unexpected reply: %q", sentText(sent[0]))
	}
}

// off 模式逐条处理，与旧行为一致。
func TestDebounceOffProcessesEachMessage(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"trigger_mode":"all_messages","reply_debounce_mode":"off","group_cooldown_seconds":0}`)
	for i := 0; i < 3; i++ {
		h.dis.HandlePayload(context.Background(),
			groupAnyMessage("DBO"+string(rune('1'+i)), "G1", "U1", "小明", "逐条"))
		time.Sleep(50 * time.Millisecond)
	}
	sent := h.waitForSent(t, 3)
	if len(sent) != 3 {
		t.Fatalf("off 模式应逐条回复 3 条，实际 %d", len(sent))
	}
}

// 分段气泡：长回复在 humanize 开启时拆成多条；代码块不拆。
func TestHumanizeBubblesSplitLongReply(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"humanize_level":"medium","humanize_segment_split":"strong",`+
		`"humanize_typing_seconds_per_char":0.05,"humanize_typing_max_seconds":0,`+
		`"humanize_delay_scale":0.5}`)
	h.mu.Lock()
	h.aiReply = "第一句话讲背景。第二句话讲原因。第三句话给结论。第四句话补充说明。"
	h.mu.Unlock()
	// 拆分本身是概率的：测试要的是「拆开后发送正确」，所以把随机源钉成必拆。
	h.dis.SetSplitRoll(func() float64 { return 0 })

	h.dis.HandlePayload(context.Background(), groupAtMessage("BB1", "G1", "U1", "小明", "说说看"))
	sent := h.waitForSent(t, 2)
	if len(sent) < 2 {
		t.Fatalf("长回复应拆成多条，实际 %d 条", len(sent))
	}
}

func TestHumanizeBubblesOffKeepsOneMessage(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"humanize_level":"medium","humanize_segment_split":"off"}`)
	h.mu.Lock()
	h.aiReply = "第一句话讲背景。第二句话讲原因。第三句话给结论。第四句话补充说明。"
	h.mu.Unlock()
	th := time.Now()
	h.dis.HandlePayload(context.Background(), groupAtMessage("BB2", "G1", "U1", "小明", "说说看"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("off 应只发 1 条，实际 %d", len(sent))
	}
	_ = th
}
