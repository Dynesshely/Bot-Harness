package admin

// 引用频率分档（quote_reply_mode）的后台契约：策略文档、会话详情与按群开关都
// 必须暴露新档位，同时保住旧的 quote_reply 布尔（老前端与老配置还在读它）。

import (
	"net/http"
	"testing"
)

func TestAdminPoliciesExposeQuoteReplyMode(t *testing.T) {
	h := newAdminTestHarness(t)

	policies := h.wantOK(t, h.get(t, "/api/policies"))
	if policies["quote_reply_mode"] != "occasionally" {
		t.Fatalf("默认档位应为 occasionally，实际 %#v", policies["quote_reply_mode"])
	}
	if policies["quote_reply"] != true {
		t.Fatalf("兼容布尔应与 occasionally 一致，实际 %#v", policies["quote_reply"])
	}

	// 写新键：响应里的两个视图一起变。
	updated := h.wantOK(t, h.put(t, "/api/policies", map[string]any{"quote_reply_mode": "never"}))
	if updated["quote_reply_mode"] != "never" || updated["quote_reply"] != false {
		t.Fatalf("mode=never 后两个视图应一致为关：%#v", updated)
	}

	// 写旧布尔仍然可用，并映射回新键。
	updated = h.wantOK(t, h.put(t, "/api/policies", map[string]any{"quote_reply": true}))
	if updated["quote_reply_mode"] != "occasionally" || updated["quote_reply"] != true {
		t.Fatalf("旧布尔写入应映射为 occasionally：%#v", updated)
	}

	// 非法档位当场拒绝。
	h.wantError(t, h.put(t, "/api/policies", map[string]any{"quote_reply_mode": "sometimes"}),
		http.StatusBadRequest, "invalid_request")
}

func TestAdminConversationDetailExposesQuoteReplyMode(t *testing.T) {
	h := newAdminTestHarness(t)
	f := seedAdminFixture(t, h)

	data := h.wantOK(t, h.get(t, "/api/conversations/"+f.sharedID))
	if data["quote_reply_mode"] != "occasionally" {
		t.Fatalf("detail.quote_reply_mode = %#v", data["quote_reply_mode"])
	}
	if data["quote_reply_global_mode"] != "occasionally" {
		t.Fatalf("detail.quote_reply_global_mode = %#v", data["quote_reply_global_mode"])
	}
	// 旧字段保持原样（契约不回退）。
	if data["quote_reply"] != true || data["quote_reply_global"] != true {
		t.Fatalf("detail 兼容布尔被破坏：%#v", data)
	}

	// 全局改成 often 之后，群里没有覆盖时跟随全局档位。
	h.wantOK(t, h.put(t, "/api/config", map[string]any{"quote_reply_mode": "often"}))
	data = h.wantOK(t, h.get(t, "/api/conversations/"+f.sharedID))
	if data["quote_reply_mode"] != "often" || data["quote_reply_global_mode"] != "often" {
		t.Fatalf("全局 often 未生效：%#v", data)
	}

	// 按群关掉：档位变成 never，覆盖值仍是旧布尔。
	off := h.wantOK(t, h.post(t, "/api/conversations/"+f.sharedID+"/quote-reply",
		map[string]any{"enabled": false}))
	if off["quote_reply_mode"] != "never" || off["quote_reply"] != false {
		t.Fatalf("按群关闭后档位应为 never：%#v", off)
	}
	data = h.wantOK(t, h.get(t, "/api/conversations/"+f.sharedID))
	if data["quote_reply_mode"] != "never" {
		t.Fatalf("按群覆盖未反映到详情：%#v", data)
	}
	// 清掉覆盖 → 回到全局 often。
	cleared := h.wantOK(t, h.rawRequest(t, http.MethodPost,
		"/api/conversations/"+f.sharedID+"/quote-reply", `{"enabled":null}`))
	if cleared["quote_reply_mode"] != "often" {
		t.Fatalf("清掉覆盖后应回到全局 often：%#v", cleared)
	}
}

// 会话详情里的按群开关也接受新写法 mode：
// never = 关掉本群引用，occasionally / often = 打开本群引用（按群覆盖只有布尔粒度）。
func TestAdminSetQuoteReplyAcceptsMode(t *testing.T) {
	h := newAdminTestHarness(t)
	f := seedAdminFixture(t, h)

	data := h.wantOK(t, h.post(t, "/api/conversations/"+f.sharedID+"/quote-reply",
		map[string]any{"mode": "never"}))
	if data["quote_reply"] != false || data["override"] != false || data["quote_reply_mode"] != "never" {
		t.Fatalf("mode=never 应答 %#v", data)
	}

	data = h.wantOK(t, h.post(t, "/api/conversations/"+f.sharedID+"/quote-reply",
		map[string]any{"mode": "occasionally"}))
	if data["quote_reply"] != true || data["override"] != true || data["quote_reply_mode"] != "occasionally" {
		t.Fatalf("mode=occasionally 应答 %#v", data)
	}

	h.wantError(t, h.post(t, "/api/conversations/"+f.sharedID+"/quote-reply",
		map[string]any{"mode": "sometimes"}), http.StatusBadRequest, "invalid_request")
}

// 口吻轮换的两组键必须能通过 PUT /api/config 往返，非法值被拒。
func TestAdminReplyStyleVariantContract(t *testing.T) {
	h := newAdminTestHarness(t)

	data := h.wantOK(t, h.put(t, "/api/config", map[string]any{
		"reply_style_variants":            []string{"用 1–2 个字回复", "带一点翻译腔"},
		"reply_style_variant_probability": 0.5,
	}))
	updated, ok := data["settings"].(map[string]any)
	if !ok {
		t.Fatalf("PUT /api/config 应返回 settings 块：%#v", data)
	}
	variants, _ := updated["reply_style_variants"].([]any)
	if len(variants) != 2 || variants[0] != "用 1–2 个字回复" {
		t.Fatalf("reply_style_variants 往返失败：%#v", updated["reply_style_variants"])
	}
	if updated["reply_style_variant_probability"] != 0.5 {
		t.Fatalf("概率往返失败：%#v", updated["reply_style_variant_probability"])
	}

	got := h.wantOK(t, h.get(t, "/api/config"))
	editable := got["editable"].(map[string]any)
	if editable["reply_style_variant_probability"] != 0.5 {
		t.Fatalf("GET /api/config 未反映新值：%#v", editable["reply_style_variant_probability"])
	}

	h.wantError(t, h.put(t, "/api/config", map[string]any{
		"reply_style_variant_probability": 1.5,
	}), http.StatusBadRequest, "invalid_request")
}
