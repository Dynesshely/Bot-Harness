package admin

// Conversation endpoints: listing/paging/filtering, detail with platform facts,
// enable/disable, clearing and the bulk-clear confirmation gate.

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/dynesshely/bot-harness/internal/store"
)

func TestAdminListConversations(t *testing.T) {
	h := newAdminTestHarness(t)
	seedAdminFixture(t, h)

	data := h.wantOK(t, h.get(t, "/api/conversations"))
	if adminTestNumber(t, data, "total") != 3 {
		t.Fatalf("total = %v", data["total"])
	}
	if adminTestNumber(t, data, "page") != 1 || adminTestNumber(t, data, "page_size") != 20 {
		t.Fatalf("paging defaults = %#v", data)
	}
	items := adminTestItems(t, data)
	if len(items) != 3 {
		t.Fatalf("items = %d", len(items))
	}
	first := items[0].(map[string]any)
	for _, key := range []string{
		"id", "scene", "group_openid", "member_openid", "enabled",
		"message_count", "turn_count", "token_estimate", "last_active_at", "created_at",
	} {
		if _, present := first[key]; !present {
			t.Fatalf("conversation item is missing %q: %#v", key, first)
		}
	}

	// Filters.
	if data = h.wantOK(t, h.get(t, "/api/conversations?q=小明")); adminTestNumber(t, data, "total") != 1 {
		t.Fatalf("q filter total = %v", data["total"])
	}
	if data = h.wantOK(t, h.get(t, "/api/conversations?scene=c2c")); adminTestNumber(t, data, "total") != 1 {
		t.Fatalf("scene filter total = %v", data["total"])
	}
	// A non-boolean enabled value is ignored rather than treated as false.
	if data = h.wantOK(t, h.get(t, "/api/conversations?enabled=maybe")); adminTestNumber(t, data, "total") != 3 {
		t.Fatalf("garbage enabled should not filter: %v", data["total"])
	}
	if data = h.wantOK(t, h.get(t, "/api/conversations?enabled=false")); adminTestNumber(t, data, "total") != 0 {
		t.Fatalf("enabled=false total = %v", data["total"])
	}

	// Paging and sort.
	data = h.wantOK(t, h.get(t, "/api/conversations?page=2&page_size=2"))
	if adminTestNumber(t, data, "page") != 2 || len(adminTestItems(t, data)) != 1 {
		t.Fatalf("page 2 = %#v", data)
	}
	data = h.wantOK(t, h.get(t, "/api/conversations?page=0&page_size=1&sort=messages_desc"))
	if adminTestNumber(t, data, "page") != 1 || len(adminTestItems(t, data)) != 1 {
		t.Fatalf("page 0 should be clamped: %#v", data)
	}
	data = h.wantOK(t, h.get(t, "/api/conversations?page=99"))
	if len(adminTestItems(t, data)) != 0 {
		t.Fatalf("page 99 should be empty: %#v", data)
	}
}

func TestAdminGetConversationDetail(t *testing.T) {
	h := newAdminTestHarness(t)
	f := seedAdminFixture(t, h)

	data := h.wantOK(t, h.get(t, "/api/conversations/"+f.sharedID))
	if data["id"] != f.sharedID || data["scene"] != store.SceneGroupShared {
		t.Fatalf("detail = %#v", data)
	}
	if adminTestString(t, data, "group_name") != "测试群" {
		t.Fatalf("group_name = %v", data["group_name"])
	}
	if data["quote_reply"] != true || data["quote_reply_global"] != true {
		t.Fatalf("quote reply defaults = %#v", data)
	}
	if data["quote_reply_override"] != nil {
		t.Fatalf("quote_reply_override should be null without an override: %v", data["quote_reply_override"])
	}
	if adminTestNumber(t, data, "active_today") != 0 {
		t.Fatalf("active_today = %v", data["active_today"])
	}
	// The platform facts are probed on demand, not assumed.
	platform, ok := data["platform"].(map[string]any)
	if !ok || platform["recv_msg_setting"] != "all" || platform["allow_proactive_msg"] != true {
		t.Fatalf("platform = %#v", data["platform"])
	}
	messages, _ := data["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %d", len(messages))
	}
	msg := messages[0].(map[string]any)
	for _, key := range []string{"id", "role", "content", "tokens", "kind", "created_at"} {
		if _, present := msg[key]; !present {
			t.Fatalf("message is missing %q: %#v", key, msg)
		}
	}

	// Paging the timeline backwards.
	page1 := h.wantOK(t, h.get(t, "/api/conversations/"+f.sharedID+"?limit=2"))
	firstPage, _ := page1["messages"].([]any)
	if len(firstPage) != 2 {
		t.Fatalf("limit=2 returned %d messages", len(firstPage))
	}
	oldest := firstPage[0].(map[string]any)["id"].(float64)
	page2 := h.wantOK(t, h.get(t, fmt.Sprintf("/api/conversations/%s?limit=2&before_id=%.0f", f.sharedID, oldest)))
	secondPage, _ := page2["messages"].([]any)
	if len(secondPage) != 1 {
		t.Fatalf("before_id page = %#v", page2["messages"])
	}

	// A C2C conversation has no platform group state and no active quota.
	c2c := h.wantOK(t, h.get(t, "/api/conversations/"+f.c2cID))
	if c2c["platform"] != nil {
		t.Fatalf("C2C platform = %#v", c2c["platform"])
	}
	if adminTestNumber(t, c2c, "active_today") != 0 {
		t.Fatalf("C2C active_today = %v", c2c["active_today"])
	}

	h.wantError(t, h.get(t, "/api/conversations/nope"), http.StatusNotFound, "not_found")
}

func TestAdminSetConversationEnabled(t *testing.T) {
	h := newAdminTestHarness(t)
	f := seedAdminFixture(t, h)

	h.wantError(t, h.post(t, "/api/conversations/"+f.memberID+"/enabled", map[string]any{}),
		http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.rawRequest(t, http.MethodPost, "/api/conversations/"+f.memberID+"/enabled", `{"enabled":"yes"}`),
		http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.post(t, "/api/conversations/nope/enabled", map[string]any{"enabled": true}),
		http.StatusNotFound, "not_found")

	data := h.wantOK(t, h.post(t, "/api/conversations/"+f.memberID+"/enabled", map[string]any{"enabled": false}))
	if data["enabled"] != false || data["id"] != f.memberID {
		t.Fatalf("enable response = %#v", data)
	}
	detail := h.wantOK(t, h.get(t, "/api/conversations/"+f.memberID))
	if detail["enabled"] != false {
		t.Fatalf("detail enabled = %v", detail["enabled"])
	}
	list := h.wantOK(t, h.get(t, "/api/conversations?enabled=false"))
	if adminTestNumber(t, list, "total") != 1 {
		t.Fatalf("disabled filter total = %v", list["total"])
	}
}

func TestAdminDeleteAndClearConversations(t *testing.T) {
	h := newAdminTestHarness(t)
	f := seedAdminFixture(t, h)

	h.wantError(t, h.del(t, "/api/conversations/nope"), http.StatusNotFound, "not_found")

	cleared := h.wantOK(t, h.del(t, "/api/conversations/"+f.memberID))
	if cleared["cleared"] != true {
		t.Fatalf("delete response = %#v", cleared)
	}
	detail := h.wantOK(t, h.get(t, "/api/conversations/"+f.memberID))
	if msgs, _ := detail["messages"].([]any); len(msgs) != 0 {
		t.Fatalf("messages survived the delete: %#v", msgs)
	}
	if adminTestNumber(t, detail, "message_count") != 0 {
		t.Fatalf("message_count after delete = %v", detail["message_count"])
	}

	// Bulk clear is guarded by an explicit confirmation, compared verbatim.
	h.wantError(t, h.del(t, "/api/conversations"), http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.del(t, "/api/conversations?confirm=clear_all"), http.StatusBadRequest, "invalid_request")
	data := h.wantOK(t, h.del(t, "/api/conversations?confirm=CLEAR_ALL"))
	if data["cleared"] != true {
		t.Fatalf("clear response = %#v", data)
	}
	list := h.wantOK(t, h.get(t, "/api/conversations"))
	if adminTestNumber(t, list, "total") != 0 {
		t.Fatalf("conversations remain after clear: %v", list["total"])
	}
}
