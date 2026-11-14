package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestQuotedTextReachesModel(t *testing.T) {
	h := newHarness(t)
	p := groupAtMessage("QT1", "G1", "U1", "小明", "这是什么")
	var d map[string]any
	if err := json.Unmarshal(p.D, &d); err != nil {
		t.Fatal(err)
	}
	d["msg_elements"] = []any{map[string]any{
		"author":  map[string]any{"username": "小红", "member_openid": "U2"},
		"content": "被引用的原始内容",
	}}
	p.D, _ = json.Marshal(d)

	h.dis.HandlePayload(context.Background(), p)
	h.waitForSent(t, 1)
	h.mu.Lock()
	body := h.lastAIBody
	h.mu.Unlock()
	msgs, _ := body["messages"].([]any)
	var user string
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] == "user" {
			user, _ = m["content"].(string)
		}
	}
	if !strings.Contains(user, "引用消息") || !strings.Contains(user, "被引用的原始内容") {
		t.Fatalf("quoted text missing from model input: %q", user)
	}
}
