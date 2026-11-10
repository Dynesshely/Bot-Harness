package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/store"
)

// TestJoinEndpointsRequireAuth pins the protected status of both join routes.
func TestJoinEndpointsRequireAuth(t *testing.T) {
	h := newAdminTestHarness(t)
	noAuth := func(req *http.Request) { req.Header.Del("Authorization") }

	for _, path := range []string{"/api/join/decisions", "/api/join/topics?group_openid=G1"} {
		resp := h.request(t, http.MethodGet, path, nil, noAuth)
		h.wantError(t, resp, http.StatusUnauthorized, "unauthorized")
	}
}

// TestJoinDecisionsParams covers the query contract: empty group = all groups,
// limit bounded to 1..200, and invalid values rejected instead of clamped.
func TestJoinDecisionsParams(t *testing.T) {
	h := newAdminTestHarness(t)

	// An empty result is still a successful list, not a 404.
	body := h.wantOK(t, h.get(t, "/api/join/decisions"))
	decisions, ok := body["decisions"].([]any)
	if !ok || len(decisions) != 0 {
		t.Fatalf("empty decisions = %#v, want an empty array", body["decisions"])
	}

	for _, path := range []string{
		"/api/join/decisions?limit=0",
		"/api/join/decisions?limit=201",
		"/api/join/decisions?limit=abc",
		"/api/join/decisions?limit=-3",
		"/api/join/decisions?group_openid=" + strings.Repeat("g", 257),
	} {
		resp := h.get(t, path)
		h.wantError(t, resp, http.StatusBadRequest, "invalid_request")
	}

	// The boundaries themselves are accepted.
	h.wantOK(t, h.get(t, "/api/join/decisions?limit=1"))
	h.wantOK(t, h.get(t, "/api/join/decisions?limit=200"))
}

func TestJoinTopicsParams(t *testing.T) {
	h := newAdminTestHarness(t)

	h.wantError(t, h.get(t, "/api/join/topics"), http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.get(t, "/api/join/topics?group_openid="), http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.get(t,
		"/api/join/topics?group_openid="+strings.Repeat("g", 257)), http.StatusBadRequest, "invalid_request")

	body := h.wantOK(t, h.get(t, "/api/join/topics?group_openid=G-NOBODY"))
	if body["topics"] != nil {
		t.Fatalf("topics for an unanalysed group = %#v, want null", body["topics"])
	}
}

// TestJoinEndpointsReturnRows is the happy path: decisions are newest first,
// the group filter is a subset, and the topic payload round-trips.
func TestJoinEndpointsReturnRows(t *testing.T) {
	h := newAdminTestHarness(t)
	ctx := context.Background()

	seed := []struct {
		group, conv, msg, preview, action string
		joined                            bool
		confidence                        float64
		at                                string
	}{
		{"G-JOIN-ADMIN-1", "conv-a", "m1", "有人懂这个吗", "join", true, 0.81, "2026-01-01T09:00:00+08:00"},
		{"G-JOIN-ADMIN-2", "conv-b", "m2", "随便聊聊", "wait", false, 0.12, "2026-01-01T09:05:00+08:00"},
		{"G-JOIN-ADMIN-1", "conv-a", "m3", "继续刚才的话题", "join", true, 0.66, "2026-01-01T09:10:00+08:00"},
	}
	for _, d := range seed {
		if err := h.store.AppendJoinDecision(ctx, store.JoinDecision{
			GroupOpenID: d.group, ConversationID: d.conv, MessageID: d.msg,
			Preview: d.preview, Joined: d.joined, Action: d.action, Confidence: d.confidence,
			Reason: "test", CreatedAt: d.at,
		}); err != nil {
			t.Fatalf("seed decision: %v", err)
		}
	}
	if err := h.store.SaveJoinTopics(ctx, "G-JOIN-ADMIN-1",
		`{"topics":[{"id":"t1","title":"Go 求助","summary":"有人在问并发","message_ids":[12,13]}]}`,
		"2026-01-01T09:10:00+08:00"); err != nil {
		t.Fatalf("seed topics: %v", err)
	}

	// All groups, newest first.
	all := h.wantOK(t, h.get(t, "/api/join/decisions"))
	list, ok := all["decisions"].([]any)
	if !ok || len(list) != 3 {
		t.Fatalf("all decisions = %#v, want 3 rows", all["decisions"])
	}
	first, _ := list[0].(map[string]any)
	if first["message_id"] != "m3" || first["joined"] != true || first["action"] != "join" {
		t.Fatalf("newest decision = %#v, want m3 joined/join", first)
	}
	second, _ := list[1].(map[string]any)
	if second["message_id"] != "m2" || second["action"] != "wait" {
		t.Fatalf("second decision = %#v, want m2 wait", second)
	}

	// One group is a subset.
	one := h.wantOK(t, h.get(t, "/api/join/decisions?group_openid=G-JOIN-ADMIN-1"))
	subset, _ := one["decisions"].([]any)
	if len(subset) != 2 {
		t.Fatalf("group decisions = %#v, want 2 rows", one["decisions"])
	}
	for _, raw := range subset {
		row, _ := raw.(map[string]any)
		if row["group_openid"] != "G-JOIN-ADMIN-1" {
			t.Fatalf("group filter leaked %#v", row)
		}
	}

	// limit is applied to the newest window.
	limited := h.wantOK(t, h.get(t, "/api/join/decisions?limit=2"))
	if rows, _ := limited["decisions"].([]any); len(rows) != 2 {
		t.Fatalf("limited decisions = %#v, want 2 rows", limited["decisions"])
	}

	// Topics payload.
	body := h.wantOK(t, h.get(t, "/api/join/topics?group_openid=G-JOIN-ADMIN-1"))
	topics, ok := body["topics"].(map[string]any)
	if !ok {
		t.Fatalf("topics = %#v, want an object", body["topics"])
	}
	if topics["group_openid"] != "G-JOIN-ADMIN-1" ||
		topics["analyzed_at"] != "2026-01-01T09:10:00+08:00" {
		t.Fatalf("topics = %#v", topics)
	}
	if raw, _ := topics["topics"].(string); !strings.Contains(raw, "Go 求助") {
		t.Fatalf("topics JSON = %q", raw)
	}
}

// TestGroupNamesRequireAuth pins the new group-name lookup to the protected
// surface: it exposes group metadata, so it is not a public endpoint.
func TestGroupNamesRequireAuth(t *testing.T) {
	h := newAdminTestHarness(t)
	resp := h.request(t, http.MethodGet, "/api/groups/names?openids=G1", nil,
		func(req *http.Request) { req.Header.Del("Authorization") })
	h.wantError(t, resp, http.StatusUnauthorized, "unauthorized")
}

// TestGroupNamesParams covers the batch contract: at least one openid, at most
// 100 after de-duplication, and whitespace-only entries rejected.
func TestGroupNamesParams(t *testing.T) {
	h := newAdminTestHarness(t)

	for _, path := range []string{
		"/api/groups/names",
		"/api/groups/names?openids=",
		"/api/groups/names?openids=%20,%20",
	} {
		h.wantError(t, h.get(t, path), http.StatusBadRequest, "invalid_request")
	}

	// 101 unique ids exceed the batch; duplicate entries do not count twice.
	ids := make([]string, 0, 102)
	for i := 0; i < 101; i++ {
		ids = append(ids, "G-BATCH-"+strconv.Itoa(i))
	}
	over := strings.Join(ids, ",")
	h.wantError(t, h.get(t, "/api/groups/names?openids="+over),
		http.StatusBadRequest, "invalid_request")
	// Exactly 100 is accepted.
	if body := h.wantOK(t, h.get(t,
		"/api/groups/names?openids="+strings.Join(ids[:100], ","))); body["names"] == nil {
		t.Fatalf("names missing from boundary response: %#v", body)
	}
	if body := h.wantOK(t, h.get(t,
		"/api/groups/names?openids="+strings.Join(ids[:100], ",")+",G-BATCH-0")); body["names"] == nil {
		t.Fatalf("duplicate openid should not count against the limit: %#v", body)
	}
}

// TestGroupNamesResolve is the happy path: known groups get their cached name,
// unknown groups are still present with an empty string so the panel can fall
// back to the openid without special-casing a missing key.
func TestGroupNamesResolve(t *testing.T) {
	h := newAdminTestHarness(t)
	h.seedConversation(t, store.Conversation{
		ID: "names-conv-1", Scene: store.SceneGroupMember, GroupOpenID: "G-NAMES-1", GroupName: "测试群一",
	})
	h.seedConversation(t, store.Conversation{
		ID: "names-conv-2", Scene: store.SceneGroupMember, GroupOpenID: "G-NAMES-2", GroupName: "测试群二",
	})

	body := h.wantOK(t, h.get(t,
		"/api/groups/names?openids=%20G-NAMES-1%20,G-NAMES-2,G-NAMES-UNKNOWN,G-NAMES-1"))
	names, ok := body["names"].(map[string]any)
	if !ok {
		t.Fatalf("names = %#v, want an object", body["names"])
	}
	if len(names) != 3 {
		t.Fatalf("names = %#v, want 3 unique keys", names)
	}
	if names["G-NAMES-1"] != "测试群一" || names["G-NAMES-2"] != "测试群二" {
		t.Fatalf("names = %#v", names)
	}
	if name, present := names["G-NAMES-UNKNOWN"]; !present || name != "" {
		t.Fatalf("unknown group name = %#v (present=%v), want empty string", name, present)
	}
}

// TestJoinEndpointsWithoutStore exercises the 503 guard when the store was
// never wired (feature built but not enabled).
func TestJoinEndpointsWithoutStore(t *testing.T) {
	h := newAdminTestHarness(t)
	h.server.deps.Store = nil

	h.wantError(t, h.get(t, "/api/join/decisions"), http.StatusServiceUnavailable, "unavailable")
	h.wantError(t, h.get(t, "/api/join/topics?group_openid=G1"),
		http.StatusServiceUnavailable, "unavailable")
}
