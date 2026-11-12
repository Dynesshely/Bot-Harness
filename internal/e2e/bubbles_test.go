package e2e

import (
	"context"
	"strings"
	"testing"
	"time"
)

/* ------------------------------------------------------------------ *
 * 分段气泡 + 逐段打字
 * ------------------------------------------------------------------ */

const bubbleLongReply = "第一句话讲背景。第二句话讲原因。第三句话给结论。第四句话补充说明。第五句话收尾。"

// 长回复拆成多条气泡，且引用只挂在第一条上。
func TestHumanizeBubblesQuoteOnlyTheFirstMessage(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"humanize_level":"medium","humanize_segment_split":"strong",
		"humanize_delay_scale":0.5,"humanize_typing_seconds_per_char":0.05,
		"humanize_typing_max_seconds":1,"quote_reply_mode":"occasionally"}`)
	h.dis.SetSplitRoll(func() float64 { return 0 })
	h.dis.SetQuoteRoll(func() float64 { return 0.1 })
	h.mu.Lock()
	h.aiReply = bubbleLongReply
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("BBQ1", "G1", "U1", "小明", "说说看"))
	sent := h.waitForSent(t, 2)
	if len(sent) < 2 {
		t.Fatalf("长回复应拆成多条，实际 %d 条", len(sent))
	}
	if ref := sent[0].Body.MessageRef; ref == nil || ref.MessageID != "REFIDX_BBQ1" {
		t.Fatalf("引用应挂在第一条：%#v", sent[0].Body.MessageRef)
	}
	for i := 1; i < len(sent); i++ {
		if sent[i].Body.MessageRef != nil {
			t.Fatalf("第 %d 条不该带引用：%#v", i+1, sent[i].Body.MessageRef)
		}
	}
	// msg_seq 递增且连续：平台的被动回复就是靠它去重。
	for i, m := range sent {
		if m.Body.MsgSeq != i+1 {
			t.Fatalf("第 %d 条的 msg_seq = %d", i+1, m.Body.MsgSeq)
		}
	}
	// 内容拼回去不该丢字。
	var joined string
	for _, m := range sent {
		joined += sentText(m)
	}
	if joined != bubbleLongReply {
		t.Fatalf("拆分丢了内容：%q", joined)
	}
}

// 逐段等待受 humanize_typing_max_seconds 约束：按字数算要几十秒，实际必须在
// 上限附近收住。
func TestHumanizeTypingWaitsAreCapped(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"humanize_level":"soft","humanize_segment_split":"strong",
		"humanize_delay_scale":0.5,"humanize_typing_seconds_per_char":1.0,
		"humanize_typing_max_seconds":1}`)
	h.dis.SetSplitRoll(func() float64 { return 0 })
	h.mu.Lock()
	h.aiReply = strings.Repeat(bubbleLongReply, 3)
	h.mu.Unlock()

	start := time.Now()
	h.dis.HandlePayload(context.Background(), groupAtMessage("BBT1", "G1", "U1", "小明", "讲讲"))
	sent := h.waitForSent(t, 2)
	elapsed := time.Since(start)
	if len(sent) < 2 {
		t.Fatalf("长回复应拆成多条，实际 %d 条", len(sent))
	}
	// 按字数 1s/字、上百字正文，不设上限会是几十秒；上限 1s + 软档读题停顿
	// （≤3s×0.5）+ 调度开销，6s 是很宽松的上界。
	if elapsed > 6*time.Second {
		t.Fatalf("逐段等待没有被上限收住：%v", elapsed)
	}
	if elapsed < 300*time.Millisecond {
		t.Fatalf("逐段等待似乎完全没生效：%v", elapsed)
	}
}

// humanize_level=off：不分段、不等待，完全保持旧行为。
func TestHumanizeOffNeverSplitsOrWaits(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"humanize_level":"off","humanize_segment_split":"strong",
		"humanize_typing_seconds_per_char":1.0,"humanize_typing_max_seconds":30}`)
	h.dis.SetSplitRoll(func() float64 { return 0 })
	h.mu.Lock()
	h.aiReply = strings.Repeat(bubbleLongReply, 3)
	h.mu.Unlock()

	start := time.Now()
	h.dis.HandlePayload(context.Background(), groupAtMessage("BBO1", "G1", "U1", "小明", "讲讲"))
	sent := h.waitForSent(t, 1)
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("off 档不该有任何等待，实际 %v", elapsed)
	}
	if len(sent) != 1 {
		t.Fatalf("off 档不该分段，实际 %d 条", len(sent))
	}
}

// humanize_segment_split=off：人味仍开（读题停顿照旧），但不拆气泡。
func TestHumanizeSegmentSplitOffKeepsOneMessage(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"humanize_level":"medium","humanize_segment_split":"off",
		"humanize_delay_scale":0.5,"humanize_typing_seconds_per_char":0.05}`)
	h.dis.SetSplitRoll(func() float64 { return 0 })
	h.mu.Lock()
	h.aiReply = bubbleLongReply
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("BBS1", "G1", "U1", "小明", "说说看"))
	sent := h.waitForSent(t, 1)
	// 给后台一个机会多发出第二条（如果有 bug）。
	time.Sleep(300 * time.Millisecond)
	if got := len(h.sentMessages()); got != 1 {
		t.Fatalf("segment_split=off 应只发 1 条，实际 %d", got)
	}
	if sentText(sent[0]) != bubbleLongReply {
		t.Fatalf("内容应保持完整：%q", sentText(sent[0]))
	}
}
