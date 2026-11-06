package admin

// Manual sending, quote replies and the same-group conversation merge: the
// write paths, where the failure modes matter most.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/store"
)

func TestAdminSendConversationMessage(t *testing.T) {
	h := newAdminTestHarness(t)
	f := seedAdminFixture(t, h)

	data := h.wantOK(t, h.post(t, "/api/conversations/"+f.sharedID+"/messages", map[string]any{
		"content": "后台公告：今晚维护",
	}))
	ids, _ := data["message_ids"].([]any)
	if len(ids) != 1 || ids[0] != "MSG-1" {
		t.Fatalf("message_ids = %#v", data["message_ids"])
	}
	if data["message_id"] != "MSG-1" || data["segments"] != float64(1) || data["active"] != true {
		t.Fatalf("send result = %#v", data)
	}
	if data["quoted"] != false {
		t.Fatalf("quoted = %#v", data["quoted"])
	}
	if adminTestNumber(t, data, "message_row_id") <= 0 || adminTestNumber(t, data, "record_id") <= 0 {
		t.Fatalf("persisted ids missing: %#v", data)
	}
	sent := h.qq.sent()
	last := sent[len(sent)-1]
	if last.Content != "后台公告：今晚维护" || last.MsgID != "" || last.MsgSeq != 0 {
		t.Fatalf("manual sends must be active messages: %#v", last)
	}

	// It becomes an assistant turn the model will see.
	detail := h.wantOK(t, h.get(t, "/api/conversations/"+f.sharedID))
	messages := detail["messages"].([]any)
	tail := messages[len(messages)-1].(map[string]any)
	if tail["kind"] != store.KindManual || tail["role"] != "assistant" {
		t.Fatalf("timeline tail = %#v", tail)
	}
	// And it is audited separately.
	rows, total, err := h.store.ListRecords(context.Background(), store.RecordFilter{Status: store.StatusManual})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("manual audit row = %d/%d (%v)", total, len(rows), err)
	}
	if rows[0].Delivery != store.DeliveryActive || rows[0].Content != "后台公告：今晚维护" {
		t.Fatalf("manual audit row = %#v", rows[0])
	}

	// Empty / whitespace-only content is refused.
	h.wantError(t, h.post(t, "/api/conversations/"+f.sharedID+"/messages", map[string]any{"content": "   "}),
		http.StatusBadRequest, "empty_content")
	// Unknown conversation.
	h.wantError(t, h.post(t, "/api/conversations/nope/messages", map[string]any{"content": "x"}),
		http.StatusNotFound, "not_found")
	// Unknown field in the body.
	h.wantError(t, h.post(t, "/api/conversations/"+f.sharedID+"/messages", map[string]any{
		"content": "x", "extra": 1,
	}), http.StatusBadRequest, "invalid_request")
}

func TestAdminSendConversationMessageRefusals(t *testing.T) {
	h := newAdminTestHarness(t)
	f := seedAdminFixture(t, h)

	// Master switch off.
	h.wantOK(t, h.put(t, "/api/config", map[string]any{"manual_send_enabled": false}))
	h.wantError(t, h.post(t, "/api/conversations/"+f.sharedID+"/messages", map[string]any{"content": "x"}),
		http.StatusForbidden, "manual_send_disabled")
	h.wantOK(t, h.put(t, "/api/config", map[string]any{"manual_send_enabled": true}))

	// Nothing to send to: a group scene without a group openid.
	h.seedConversation(t, store.Conversation{ID: "orphan-group-conv", Scene: store.SceneGroupMember})
	h.wantError(t, h.post(t, "/api/conversations/orphan-group-conv/messages", map[string]any{"content": "x"}),
		http.StatusConflict, "no_target")
	// A C2C conversation with neither a member openid nor a decodable id.
	h.seedConversation(t, store.Conversation{ID: "opaque-c2c", Scene: store.SceneC2C})
	h.wantError(t, h.post(t, "/api/conversations/opaque-c2c/messages", map[string]any{"content": "x"}),
		http.StatusConflict, "no_target")

	// The group has not granted proactive messages.
	h.seedConversation(t, store.Conversation{
		ID: "group-shared:" + adminTestGroupNoPr, Scene: store.SceneGroupShared,
		GroupOpenID: adminTestGroupNoPr,
	})
	h.wantError(t, h.post(t, "/api/conversations/group-shared:"+adminTestGroupNoPr+"/messages",
		map[string]any{"content": "x"}), http.StatusConflict, "no_proactive_permission")

	// Over-long content is refused, never truncated.
	h.wantOK(t, h.put(t, "/api/config", map[string]any{
		"reply_max_chars": 50, "manual_send_max_segments": 1,
	}))
	h.wantError(t, h.post(t, "/api/conversations/"+f.sharedID+"/messages",
		map[string]any{"content": strings.Repeat("长", 60)}), http.StatusBadRequest, "content_too_long")

	// Rate limit: one per minute globally means the second send is refused.
	limited := newAdminTestHarness(t)
	lf := seedAdminFixture(t, limited)
	limited.wantOK(t, limited.put(t, "/api/config", map[string]any{
		"manual_send_per_minute": 1, "manual_send_per_conversation_qpm": 10,
	}))
	limited.wantOK(t, limited.post(t, "/api/conversations/"+lf.sharedID+"/messages", map[string]any{"content": "第一条"}))
	limited.wantError(t, limited.post(t, "/api/conversations/"+lf.sharedID+"/messages", map[string]any{"content": "第二条"}),
		http.StatusTooManyRequests, "rate_limited")

	// A platform refusal is an upstream error and leaves a failed audit row.
	failing := newAdminTestHarness(t)
	ff := seedAdminFixture(t, failing)
	failing.qq.setSendStatus(http.StatusBadGateway)
	failing.wantError(t, failing.post(t, "/api/conversations/"+ff.sharedID+"/messages", map[string]any{"content": "会失败"}),
		http.StatusBadGateway, "upstream_error")
	rows, total, err := failing.store.ListRecords(context.Background(), store.RecordFilter{Status: "failed"})
	if err != nil {
		t.Fatalf("list failed records: %v", err)
	}
	if total != 2 {
		t.Fatalf("failed audit rows = %d, want 2 (one seeded fixture + one failed send)", total)
	}
	found := false
	for _, row := range rows {
		if row.Content == "会失败" {
			found = true
			if row.Error == "" {
				t.Fatalf("failed audit row must keep the platform error: %#v", row)
			}
		}
	}
	if !found {
		t.Fatalf("the failed manual send was not audited: %#v", rows)
	}
	// Nothing reached the timeline, because nothing reached the group.
	detail := failing.wantOK(t, failing.get(t, "/api/conversations/"+ff.sharedID))
	for _, raw := range detail["messages"].([]any) {
		if raw.(map[string]any)["kind"] == store.KindManual {
			t.Fatalf("a failed send must not appear in the timeline: %#v", raw)
		}
	}
}

func TestAdminSendConversationMessageQuoting(t *testing.T) {
	h := newAdminTestHarness(t)
	f := seedAdminFixture(t, h)

	// Quoting a message that carries a platform reference index.
	data := h.wantOK(t, h.post(t, "/api/conversations/"+f.sharedID+"/messages", map[string]any{
		"content": "引用回复", "quote_message_id": f.quoteMessageID,
	}))
	if data["quoted"] != true {
		t.Fatalf("quoted = %#v", data["quoted"])
	}
	sent := h.qq.sent()
	last := sent[len(sent)-1]
	if last.MessageRef == nil || last.MessageRef.MessageID != "REFIDX-SEED" {
		t.Fatalf("platform quote reference = %#v", last.MessageRef)
	}

	// Quoting a row without a reference index is refused.
	h.wantError(t, h.post(t, "/api/conversations/"+f.sharedID+"/messages", map[string]any{
		"content": "引用回复", "quote_message_id": f.plainMessageID,
	}), http.StatusConflict, "quote_unavailable")

	// Quoting a row from another conversation is refused.
	c2cMessages, err := h.store.Messages(context.Background(), f.c2cID, 10, 0)
	if err != nil || len(c2cMessages) == 0 {
		t.Fatalf("read C2C messages: %v", err)
	}
	var crossID int64
	for _, m := range c2cMessages {
		if m.QuoteIndex != "" {
			crossID = m.ID
		}
	}
	if crossID == 0 {
		t.Fatalf("fixture has no C2C message with a reference index: %#v", c2cMessages)
	}
	h.wantError(t, h.post(t, "/api/conversations/"+f.sharedID+"/messages", map[string]any{
		"content": "跨会话引用", "quote_message_id": crossID,
	}), http.StatusConflict, "no_target")

	// The group can switch quoting off.
	h.wantOK(t, h.put(t, "/api/policies", map[string]any{
		"group_overrides": []map[string]any{{
			"group_openid": f.groupOpenID, "quote_reply": false,
		}},
	}))
	h.wantError(t, h.post(t, "/api/conversations/"+f.sharedID+"/messages", map[string]any{
		"content": "引用回复", "quote_message_id": f.quoteMessageID,
	}), http.StatusConflict, "quote_disabled")

	// C2C sends target the user endpoint, not the group endpoint.
	h.wantOK(t, h.put(t, "/api/policies", map[string]any{"group_overrides": []map[string]any{}}))
	c2c := h.wantOK(t, h.post(t, "/api/conversations/"+f.c2cID+"/messages", map[string]any{"content": "私聊公告"}))
	if c2c["active"] != true {
		t.Fatalf("C2C send = %#v", c2c)
	}
	paths := h.qq.paths()
	if !strings.HasSuffix(paths[len(paths)-1], "/v2/users/U2/messages") {
		t.Fatalf("C2C send path = %#v", paths)
	}
}

func TestAdminSetQuoteReplyEndpoint(t *testing.T) {
	h := newAdminTestHarness(t)
	f := seedAdminFixture(t, h)

	h.wantError(t, h.post(t, "/api/conversations/nope/quote-reply", map[string]any{"enabled": false}),
		http.StatusNotFound, "not_found")
	h.wantError(t, h.rawRequest(t, http.MethodPost, "/api/conversations/"+f.sharedID+"/quote-reply", "not-json"),
		http.StatusBadRequest, "invalid_request")
	// C2C conversations follow the global default.
	h.wantError(t, h.post(t, "/api/conversations/"+f.c2cID+"/quote-reply", map[string]any{"enabled": false}),
		http.StatusConflict, "no_group")

	data := h.wantOK(t, h.post(t, "/api/conversations/"+f.sharedID+"/quote-reply", map[string]any{"enabled": false}))
	if data["quote_reply"] != false || data["override"] != false || data["global"] != true {
		t.Fatalf("quote reply response = %#v", data)
	}
	// The override is visible in the policies document too.
	policies := h.wantOK(t, h.get(t, "/api/policies"))
	overrides := policies["group_overrides"].([]any)
	if len(overrides) != 1 || overrides[0].(map[string]any)["quote_reply"] != false {
		t.Fatalf("policies group_overrides = %#v", overrides)
	}
	// null clears the override and follows the global default again.
	cleared := h.wantOK(t, h.rawRequest(t, http.MethodPost, "/api/conversations/"+f.sharedID+"/quote-reply",
		`{"enabled":null}`))
	if cleared["override"] != nil || cleared["quote_reply"] != true {
		t.Fatalf("cleared override = %#v", cleared)
	}
}

func TestAdminConversationMerge(t *testing.T) {
	h := newAdminTestHarness(t)
	ctx := context.Background()

	const groupA, groupB, groupC = "G-MERGE-A", "G-MERGE-B", "G-MERGE-C"
	sharedA := "group-shared:" + groupA
	memberA1 := "group:" + groupA + ":U1"
	memberA2 := "group:" + groupA + ":U2"
	for _, c := range []store.Conversation{
		{ID: sharedA, Scene: store.SceneGroupShared, GroupOpenID: groupA, GroupName: "合并群A"},
		{ID: memberA1, Scene: store.SceneGroupMember, GroupOpenID: groupA, MemberOpenID: "U1", MemberName: "小明"},
		{ID: memberA2, Scene: store.SceneGroupMember, GroupOpenID: groupA, MemberOpenID: "U2", MemberName: "小红"},
		{ID: "group-shared:" + groupB, Scene: store.SceneGroupShared, GroupOpenID: groupB},
	} {
		h.seedConversation(t, c)
	}
	h.seedMessage(t, store.MessageInput{ConversationID: memberA1, Role: "user", Content: "A 的第一条"})
	h.seedMessage(t, store.MessageInput{ConversationID: memberA1, Role: "assistant", Content: "A 的回复"})
	h.seedMessage(t, store.MessageInput{ConversationID: memberA2, Role: "user", Content: "A 的第二条"})

	// Not shared context: mergeable is reported but refused.
	data := h.wantOK(t, h.get(t, "/api/groups/mergeable"))
	if adminTestNumber(t, data, "total_groups") < 1 {
		t.Fatalf("mergeable = %#v", data)
	}
	var item map[string]any
	for _, raw := range adminTestItems(t, data) {
		entry := raw.(map[string]any)
		if entry["group_openid"] == groupA {
			item = entry
		}
	}
	if item == nil {
		t.Fatalf("group %s not listed as mergeable: %#v", groupA, data)
	}
	if item["mergeable"] != false || item["context_mode"] != "per_member" {
		t.Fatalf("mergeable item = %#v", item)
	}
	h.wantError(t, h.post(t, "/api/groups/"+groupA+"/conversations/merge", map[string]any{"dry_run": true}),
		http.StatusBadRequest, "context_mode_not_shared")

	// Switch to shared context, then merge.
	h.wantOK(t, h.put(t, "/api/config", map[string]any{"context_mode": "shared"}))
	data = h.wantOK(t, h.get(t, "/api/groups/mergeable"))
	if data["global_mode"] != "shared" {
		t.Fatalf("global_mode = %v", data["global_mode"])
	}

	dry := h.wantOK(t, h.post(t, "/api/groups/"+groupA+"/conversations/merge", map[string]any{
		"target_id": sharedA, "dry_run": true,
	}))
	if dry["dry_run"] != true || dry["moved_messages"] != float64(3) {
		t.Fatalf("dry run = %#v", dry)
	}
	if _, err := h.store.GetConversation(ctx, memberA1); err != nil {
		t.Fatalf("dry run must not delete sources: %v", err)
	}

	merged := h.wantOK(t, h.post(t, "/api/groups/"+groupA+"/conversations/merge", map[string]any{}))
	if merged["dry_run"] != false || merged["deleted_conversations"] != float64(2) {
		t.Fatalf("merge = %#v", merged)
	}
	if merged["target_message_count"] != float64(3) {
		t.Fatalf("target_message_count = %v", merged["target_message_count"])
	}
	for _, gone := range []string{memberA1, memberA2} {
		if _, err := h.store.GetConversation(ctx, gone); err == nil {
			t.Fatalf("source conversation %s survived the merge", gone)
		}
	}
	if _, err := h.store.GetConversation(ctx, sharedA); err != nil {
		t.Fatalf("target conversation missing after merge: %v", err)
	}

	// A target from another group is refused outright.
	h.wantError(t, h.post(t, "/api/groups/"+groupA+"/conversations/merge", map[string]any{
		"target_id": "group-shared:" + groupB,
	}), http.StatusBadRequest, "group_mismatch")

	// A group with no conversations at all has nothing to merge.
	h.wantError(t, h.post(t, "/api/groups/"+groupC+"/conversations/merge", map[string]any{
		"target_id": "whatever",
	}), http.StatusNotFound, "not_found")

	// Malformed body.
	h.wantError(t, h.rawRequest(t, http.MethodPost, "/api/groups/"+groupA+"/conversations/merge", "not-json"),
		http.StatusBadRequest, "invalid_request")

	// A group with a single conversation gets its shared target created on demand.
	solo := "group:" + groupC + ":U9"
	h.seedConversation(t, store.Conversation{
		ID: solo, Scene: store.SceneGroupMember, GroupOpenID: groupC, MemberOpenID: "U9",
	})
	h.seedMessage(t, store.MessageInput{ConversationID: solo, Role: "user", Content: "唯一一条"})
	created := h.wantOK(t, h.post(t, "/api/groups/"+groupC+"/conversations/merge", map[string]any{}))
	wantTarget := core.EncodeConversationID("group-shared:" + groupC)
	if created["target_id"] != wantTarget {
		t.Fatalf("auto target = %v, want %v", created["target_id"], wantTarget)
	}
	if created["deleted_conversations"] != float64(1) {
		t.Fatalf("auto-target merge = %#v", created)
	}
}
