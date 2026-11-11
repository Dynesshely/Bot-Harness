package admin

import (
	"net/http"
	"strings"
	"testing"
)

// 表达方式库的后台契约：列表分页 / 筛选、人工新增（重复返回已有行）、
// 启停、删除、强化，以及 404 与非法参数。
//
// 这里的断言只依赖 HTTP 契约，不关心 store 的实现细节：同一组用例在
// SQLite 与 PostgreSQL 上都应该通过（PG 用例由 PG_TEST_DSN 驱动）。
func TestAdminExpressionsContract(t *testing.T) {
	h := newAdminTestHarness(t)

	// 空库：列表可用、结构完整。
	data := h.wantOK(t, h.get(t, "/api/expressions"))
	if adminTestNumber(t, data, "total") != 0 {
		t.Fatalf("empty expression list total = %#v", data)
	}
	if adminTestNumber(t, data, "page") != 1 {
		t.Fatalf("page echo = %#v", data)
	}
	if got := adminTestNumber(t, data, "page_size"); got != 20 {
		t.Fatalf("default page_size = %v, want 20", got)
	}
	if items, ok := data["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("empty items must be an empty array, got %#v", data["items"])
	}

	// 新增：返回新行，字段齐全。
	created := h.wantOK(t, h.post(t, "/api/expressions", map[string]any{
		"group_openid": adminTestGroup,
		"situation":    "被夸的时候",
		"style":        "先谦虚两句，再把话题带回正事",
	}))
	id := int64(adminTestNumber(t, created, "id"))
	if id <= 0 {
		t.Fatalf("created expression id = %v", created["id"])
	}
	if created["group_openid"] != adminTestGroup || created["situation"] != "被夸的时候" {
		t.Fatalf("created row lost its fields: %#v", created)
	}
	if enabled, ok := created["enabled"].(bool); !ok || !enabled {
		t.Fatalf("a hand-written expression must start enabled: %#v", created)
	}
	if created["source"] != "admin" {
		t.Fatalf("hand-written expression source = %v, want admin", created["source"])
	}

	// 重复新增（同群 + 同情境 + 同说法）返回已有行，不产生第二条。
	again := h.wantOK(t, h.post(t, "/api/expressions", map[string]any{
		"group_openid": adminTestGroup,
		"situation":    "被夸的时候",
		"style":        "先谦虚两句，再把话题带回正事",
	}))
	if int64(adminTestNumber(t, again, "id")) != id {
		t.Fatalf("duplicate add must return the existing row: %#v", again)
	}
	// 全局行（空群号）与群行是两个不同的键。
	global := h.wantOK(t, h.post(t, "/api/expressions", map[string]any{
		"group_openid": "",
		"situation":    "有人讲冷笑话",
		"style":        "配合吐槽一句",
	}))
	globalID := int64(adminTestNumber(t, global, "id"))
	if globalID == id {
		t.Fatalf("global row must not reuse the group row id")
	}

	// 列表：两条，分页与筛选都按契约回显。
	data = h.wantOK(t, h.get(t, "/api/expressions"))
	if adminTestNumber(t, data, "total") != 2 {
		t.Fatalf("total after two adds = %#v", data)
	}
	row := adminTestItems(t, data)[0].(map[string]any)
	for _, key := range []string{
		"id", "group_openid", "situation", "style", "source", "enabled",
		"use_count", "created_at",
	} {
		if _, present := row[key]; !present {
			t.Fatalf("expression row is missing %q: %#v", key, row)
		}
	}
	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/expressions?group_openid="+adminTestGroup)), "total"); got != 1 {
		t.Fatalf("group filter total = %v", got)
	}
	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/expressions?enabled=false")), "total"); got != 0 {
		t.Fatalf("enabled=false total = %v, want 0", got)
	}
	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/expressions?q=冷笑话")), "total"); got != 1 {
		t.Fatalf("q filter total = %v", got)
	}
	paged := h.wantOK(t, h.get(t, "/api/expressions?page=2&page_size=1"))
	if adminTestNumber(t, paged, "page") != 2 || adminTestNumber(t, paged, "page_size") != 1 {
		t.Fatalf("paging echo = %#v", paged)
	}
	if len(adminTestItems(t, paged)) != 1 {
		t.Fatalf("page 2 with page_size 1 should return one row: %#v", paged)
	}
	// 越界 page_size 回落到默认值，并如实回显。
	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/expressions?page_size=999")), "page_size"); got != 20 {
		t.Fatalf("out-of-range page_size = %v, want the default 20", got)
	}

	// 启停：先停再开，列表反映最新状态。
	off := h.wantOK(t, h.post(t, "/api/expressions/"+itoa(id)+"/enabled", map[string]any{"enabled": false}))
	if off["enabled"] != false {
		t.Fatalf("set enabled=false echo = %#v", off)
	}
	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/expressions?enabled=false")), "total"); got != 1 {
		t.Fatalf("disabled filter total = %v", got)
	}
	h.wantOK(t, h.post(t, "/api/expressions/"+itoa(id)+"/enabled", map[string]any{"enabled": true}))

	// 强化：可人工给一条加权（不改变启停）。
	reinforced := h.wantOK(t, h.post(t, "/api/expressions/"+itoa(id)+"/reinforce", nil))
	if reinforced["reinforced"] != true {
		t.Fatalf("reinforce echo = %#v", reinforced)
	}

	// 删除：返回 deleted=1，之后再取列表就没有它了。
	if got := h.wantOK(t, h.del(t, "/api/expressions/"+itoa(globalID))); got["deleted"] != float64(1) {
		t.Fatalf("delete echo = %#v", got)
	}
	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/expressions")), "total"); got != 1 {
		t.Fatalf("total after delete = %v", got)
	}

	// 404：不存在的 id 在四个写接口上都返回 not_found。
	h.wantError(t, h.post(t, "/api/expressions/999999/enabled", map[string]any{"enabled": true}), http.StatusNotFound, "not_found")
	h.wantError(t, h.post(t, "/api/expressions/999999/reinforce", nil), http.StatusNotFound, "not_found")
	h.wantError(t, h.del(t, "/api/expressions/999999"), http.StatusNotFound, "not_found")

	// 非法参数：id 不是数字、缺字段、超长、类型不对。
	h.wantError(t, h.post(t, "/api/expressions/abc/enabled", map[string]any{"enabled": true}), http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.post(t, "/api/expressions/0/enabled", map[string]any{"enabled": true}), http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.post(t, "/api/expressions/"+itoa(id)+"/enabled", map[string]any{}), http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.del(t, "/api/expressions/abc"), http.StatusBadRequest, "invalid_request")

	bad := []map[string]any{
		{"situation": "只有情境"},
		{"style": "只有说法"},
		{"group_openid": adminTestGroup, "situation": "   ", "style": "  "},
		{"group_openid": adminTestGroup, "situation": strings.Repeat("情", 201), "style": "说法"},
		{"group_openid": adminTestGroup, "situation": "情境", "style": strings.Repeat("法", 201)},
	}
	for _, body := range bad {
		h.wantError(t, h.post(t, "/api/expressions", body), http.StatusBadRequest, "invalid_request")
	}
	// 恰好 200 字是合法上界。
	h.wantOK(t, h.post(t, "/api/expressions", map[string]any{
		"group_openid": adminTestGroup,
		"situation":    strings.Repeat("情", 200),
		"style":        strings.Repeat("法", 200),
	}))
}

// 表达方式的读接口同样受登录保护：没有凭据一律 401。
func TestAdminExpressionsRequireAuth(t *testing.T) {
	h := newAdminTestHarness(t)
	resp := h.request(t, http.MethodGet, "/api/expressions", nil, func(r *http.Request) {
		r.Header.Del("Authorization")
	})
	h.wantError(t, resp, http.StatusUnauthorized, "unauthorized")
	resp = h.request(t, http.MethodPost, "/api/expressions", map[string]any{
		"group_openid": adminTestGroup, "situation": "s", "style": "x",
	}, func(r *http.Request) {
		r.Header.Del("Authorization")
	})
	h.wantError(t, resp, http.StatusUnauthorized, "unauthorized")
}
