package e2e

import (
	"context"
	"strings"
	"testing"
)

// 群聊与私聊都要把对应的 OpenID 注入 system prompt，管理员说「本群」时模型才
// 知道该用哪个 id。
func TestSessionIdentityInjectedIntoSystemPrompt(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), groupAtMessage("SID-G", "G1", "U1", "小明", "在吗"))
	h.waitForSent(t, 1)
	if got := h.systemPrompt(t).text; !strings.Contains(got, "群 OpenID：G1") {
		t.Fatalf("group prompt missing group openid: %q", got)
	}

	h.dis.HandlePayload(context.Background(), c2cMessage("SID-C", "U9", "小红", "在吗"))
	h.waitForSent(t, 2)
	if got := h.systemPrompt(t).text; !strings.Contains(got, "用户 OpenID：U9") {
		t.Fatalf("c2c prompt missing user openid: %q", got)
	}
}
