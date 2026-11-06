package admin

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/store"
)

func TestAdminListCommitments(t *testing.T) {
	h := newAdminTestHarness(t)
	ctx := context.Background()
	now := time.Now()
	due := now.Add(time.Hour).Format(time.RFC3339)
	first, err := h.store.AddCommitment(ctx, store.CommitmentInput{
		ConversationID: "conv-1", GroupOpenID: "G1", MemberOpenID: "U1",
		CreatorOpenID: "U1", Text: "吃药", Priority: 2, NextCheckAt: due,
	})
	if err != nil {
		t.Fatalf("add first: %v", err)
	}
	if _, err := h.store.AddCommitment(ctx, store.CommitmentInput{
		ConversationID: "conv-2", MemberOpenID: "U2", CreatorOpenID: "U2",
		Text: "交报告", NextCheckAt: due,
	}); err != nil {
		t.Fatalf("add second: %v", err)
	}

	// Protected like every other admin read.
	h.wantError(t, h.request(t, http.MethodGet, "/api/commitments", nil, adminTestWithoutAuth()),
		http.StatusUnauthorized, "unauthorized")

	data := h.wantOK(t, h.get(t, "/api/commitments"))
	items := adminTestItems(t, data)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	var got map[string]any
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if int64(adminTestNumber(t, item, "id")) == first {
			got = item
		}
	}
	if got == nil {
		t.Fatalf("first commitment missing from %#v", items)
	}
	for _, key := range []string{
		"id", "conversation_id", "group_openid", "member_openid", "creator_openid",
		"text", "status", "priority", "deadline_at", "next_check_at", "last_nudge_at",
		"nudge_count", "ack_at", "ack_note", "meta", "created_at",
	} {
		if _, ok := got[key]; !ok {
			t.Fatalf("commitment item missing field %q: %#v", key, got)
		}
	}
	if got["status"] != store.CommitmentWaiting || got["text"] != "吃药" {
		t.Fatalf("commitment item = %#v", got)
	}

	// Filters narrow by conversation and creator; limit is honoured.
	byConv := h.wantOK(t, h.get(t, "/api/commitments?conversation_id=conv-1"))
	if items := adminTestItems(t, byConv); len(items) != 1 {
		t.Fatalf("conversation filter = %d items", len(items))
	}
	byCreator := h.wantOK(t, h.get(t, "/api/commitments?creator=U2"))
	if items := adminTestItems(t, byCreator); len(items) != 1 {
		t.Fatalf("creator filter = %d items", len(items))
	}
	limited := h.wantOK(t, h.get(t, "/api/commitments?limit=1"))
	if items := adminTestItems(t, limited); len(items) != 1 {
		t.Fatalf("limit filter = %d items", len(items))
	}
}

func TestAdminCancelCommitment(t *testing.T) {
	h := newAdminTestHarness(t)
	ctx := context.Background()
	due := time.Now().Add(time.Hour).Format(time.RFC3339)
	open, err := h.store.AddCommitment(ctx, store.CommitmentInput{
		ConversationID: "conv-1", MemberOpenID: "U1", CreatorOpenID: "U1",
		Text: "交周报", Priority: 1, NextCheckAt: due,
	})
	if err != nil {
		t.Fatalf("add open: %v", err)
	}
	done, err := h.store.AddCommitment(ctx, store.CommitmentInput{
		ConversationID: "conv-1", MemberOpenID: "U1", CreatorOpenID: "U1",
		Text: "吃药", NextCheckAt: due,
	})
	if err != nil {
		t.Fatalf("add done: %v", err)
	}
	if err := h.store.FinishCommitment(ctx, done, store.CommitmentDone, "已经吃过"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// Protected like every other admin write.
	h.wantError(t, h.request(t, http.MethodDelete, "/api/commitments/"+strconv.FormatInt(open, 10), nil, adminTestWithoutAuth()),
		http.StatusUnauthorized, "unauthorized")

	h.wantError(t, h.del(t, "/api/commitments/abc"), http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.del(t, "/api/commitments/0"), http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.del(t, "/api/commitments/999999"), http.StatusNotFound, "not_found")

	// done is terminal: 409 (not a silent rewrite of the user's ack note).
	h.wantError(t, h.del(t, "/api/commitments/"+strconv.FormatInt(done, 10)),
		http.StatusConflict, "conflict")

	data := h.wantOK(t, h.del(t, "/api/commitments/"+strconv.FormatInt(open, 10)))
	if data["canceled"] != true {
		t.Fatalf("cancel response = %#v", data)
	}

	list := h.wantOK(t, h.get(t, "/api/commitments?conversation_id=conv-1"))
	var row map[string]any
	for _, raw := range adminTestItems(t, list) {
		item, _ := raw.(map[string]any)
		if int64(adminTestNumber(t, item, "id")) == open {
			row = item
		}
	}
	if row == nil {
		t.Fatalf("cancelled commitment missing from %#v", list)
	}
	if row["status"] != store.CommitmentCancelled || row["ack_note"] != "后台取消" || row["ack_at"] == "" {
		t.Fatalf("cancelled row = %#v", row)
	}

	// Second cancel: the row is closed, so it is a conflict, not a success.
	h.wantError(t, h.del(t, "/api/commitments/"+strconv.FormatInt(open, 10)),
		http.StatusConflict, "conflict")
}
