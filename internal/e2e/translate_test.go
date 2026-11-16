package e2e

import (
	"context"
	"strings"
	"testing"
)

func TestHumanizeTranslationRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.apply(t, "{\"humanize_level\":\"medium\",\"humanize_translate_enabled\":true,\"reply_footer_enabled\":false}")
	h.dis.HandlePayload(context.Background(), groupAtMessage("TR1", "G1", "U1", "小明", "你好"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected one reply, got %d", len(sent))
	}
	if !strings.Contains(sent[0].Body.Content, "你好，人类") {
		t.Fatalf("translated final text missing: %q", sent[0].Body.Content)
	}
	h.mu.Lock()
	calls := h.translateCalls
	h.mu.Unlock()
	if calls != 2 {
		t.Fatalf("translation calls = %d, want 2", calls)
	}
}

func TestHumanizeTranslationCanBeDisabled(t *testing.T) {
	h := newHarness(t)
	h.apply(t, "{\"humanize_level\":\"medium\",\"humanize_translate_enabled\":false,\"reply_footer_enabled\":false}")
	h.dis.HandlePayload(context.Background(), groupAtMessage("TR2", "G1", "U1", "小明", "你好"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 || !strings.Contains(sent[0].Body.Content, "这是测试回复") {
		t.Fatalf("disabled translation changed the reply: %#v", sent)
	}
	h.mu.Lock()
	calls := h.translateCalls
	h.mu.Unlock()
	if calls != 0 {
		t.Fatalf("translation calls = %d, want 0", calls)
	}
}
