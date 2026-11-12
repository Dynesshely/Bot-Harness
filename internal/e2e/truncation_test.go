package e2e

import (
	"context"
	"strings"
	"testing"
)

// 第一次回复以 finish_reason=length 在句中结束：必须用更大预算重试，
// 最终发出完整文本，而不是把半句话发到群里。
func TestTruncatedReplyRetriedToCompletion(t *testing.T) {
	h := newHarness(t)
	full := "这是一段完整的回答，前面讲原因，后面给结论。如果只发前半句，用户就会看到说一半不说了。"
	h.mu.Lock()
	h.aiReply = full
	h.aiTruncateOnce = true
	h.mu.Unlock()
	h.apply(t, `{"polish_enabled":false,"humanize_level":"off"}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("TRUNC1", "G1", "U1", "小明", "说说看"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected one complete reply, got %d", len(sent))
	}
	if got := sent[0].Body.Content; got != full {
		t.Fatalf("reply should be complete: got %q want %q", got, full)
	}
	if strings.HasSuffix(sent[0].Body.Content, "这是一段") {
		t.Fatalf("still truncated: %q", sent[0].Body.Content)
	}
}
