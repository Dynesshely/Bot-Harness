package e2e

import (
	"context"
	"encoding/json"
	"testing"
)

// 按群关闭联网（group_overrides[].web=false）时，该群连 web_fetch 都不下发，
// 其它群不受影响 —— 这是「每期功能独立开关 + 可按群覆盖」的联网版。
func TestWebToolsAreHiddenInAGroupThatDisabledThem(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"tools_enabled":true,"web_search_provider":"searxng",`+
		`"web_search_base_url":"https://searxng.internal.example"}`)
	if _, _, err := h.settings.Apply(context.Background(), map[string]json.RawMessage{
		"group_overrides": json.RawMessage(`[{"group_openid":"G1","web":false}]`),
	}); err != nil {
		t.Fatalf("apply group override: %v", err)
	}

	// G1 关闭：两个联网工具都不该出现在请求里。
	h.dis.HandlePayload(context.Background(), groupAtMessage("WEB1", "G1", "U1", "小明", "帮我搜一下"))
	h.waitForSent(t, 1)
	h.mu.Lock()
	disabled := toolNames(h.lastAIBody)
	h.mu.Unlock()
	if disabled["web_fetch"] || disabled["web_search"] {
		t.Fatalf("该群已关闭联网，不该下发联网工具：%v", disabled)
	}

	// G2 没关：配置齐全时 web_fetch + web_search 都该在。
	h.dis.HandlePayload(context.Background(), groupAtMessage("WEB2", "G2", "U1", "小明", "帮我搜一下"))
	h.waitForSent(t, 2)
	h.mu.Lock()
	enabled := toolNames(h.lastAIBody)
	h.mu.Unlock()
	if !enabled["web_fetch"] || !enabled["web_search"] {
		t.Fatalf("未关闭的群应下发两个联网工具：%v", enabled)
	}
}
