package e2e

import (
	"context"
	"strings"
	"testing"
)

func TestBotNameReachesSystemPrompt(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), groupAtMessage("NAME1", "G1", "U1", "小明", "你叫什么名字"))
	h.waitForSent(t, 1)

	h.mu.Lock()
	body := h.lastAIBody
	h.mu.Unlock()
	msgs, _ := body["messages"].([]any)
	var system string
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] == "system" {
			system, _ = m["content"].(string)
			break
		}
	}
	if !strings.Contains(system, "Kaku") || !strings.Contains(system, "你的名字") {
		t.Fatalf("system prompt missing bot name: %q", system)
	}
}
