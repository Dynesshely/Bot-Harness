package e2e

import (
	"context"
	"testing"
)

// 润色稿以 finish_reason=length 结束时必须回退到完整草稿，
// 不能让半句润色稿覆盖草稿发到群里。
func TestTruncatedPolishFallsBackToDraft(t *testing.T) {
	h := newHarness(t)
	draft := "这是完整草稿，润色器不应该把它缩短成半句话。"
	h.mu.Lock()
	h.aiReply = draft
	h.polishReply = "半句润色"
	h.polishFinishReason = "length"
	h.mu.Unlock()
	h.apply(t, `{"polish_enabled":true,"humanize_level":"off"}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("PTRUNC1", "G1", "U1", "小明", "说说看"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 || sent[0].Body.Content != draft {
		t.Fatalf("expected complete draft, got %#v", sent)
	}
}
