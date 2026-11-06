package admin

// Data-plane endpoints: messages, logs, tool calls, proactive state, tasks,
// memories, stats, media, model catalogue and the platform group facts.

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/core"
)

func TestAdminListMessagesFilters(t *testing.T) {
	h := newAdminTestHarness(t)
	seedAdminFixture(t, h)

	data := h.wantOK(t, h.get(t, "/api/messages"))
	if adminTestNumber(t, data, "total") != 3 {
		t.Fatalf("total = %v", data["total"])
	}
	items := adminTestItems(t, data)
	if len(items) != 3 {
		t.Fatalf("items = %d", len(items))
	}
	row := items[0].(map[string]any)
	for _, key := range []string{
		"id", "direction", "scene", "group_openid", "member_openid", "member_name",
		"content", "msg_id", "status", "latency_ms", "created_at",
	} {
		if _, present := row[key]; !present {
			t.Fatalf("record is missing %q: %#v", key, row)
		}
	}

	cases := []struct {
		query string
		total float64
	}{
		{"?direction=out", 2},
		{"?status=failed", 1},
		{"?q=你好", 2},
		{"?since=2999-01-01T00:00:00Z", 0},
		{"?until=2999-01-01T00:00:00Z", 3},
		{"?page=2&page_size=1", 3},
	}
	for _, tc := range cases {
		data = h.wantOK(t, h.get(t, "/api/messages"+tc.query))
		if adminTestNumber(t, data, "total") != tc.total {
			t.Fatalf("messages%s total = %v, want %v", tc.query, data["total"], tc.total)
		}
	}
	paged := h.wantOK(t, h.get(t, "/api/messages?page=2&page_size=1"))
	if len(adminTestItems(t, paged)) != 1 {
		t.Fatalf("page 2 = %#v", paged)
	}
}

func TestAdminListLogs(t *testing.T) {
	h := newAdminTestHarness(t)
	h.logger.Info("hello-ring info", "component", "test-admin")
	h.logger.Warn("hello-ring warn", "component", "test-admin")
	h.logger.Error("hello-ring error", "component", "test-admin")
	h.logger.Debug("hello-ring debug", "component", "test-admin")

	data := h.wantOK(t, h.get(t, "/api/logs?q=hello-ring"))
	items := adminTestItems(t, data)
	// Default level is info, so the debug line must be filtered out.
	if len(items) != 3 {
		t.Fatalf("default level returned %d entries: %#v", len(items), items)
	}
	entry := items[0].(map[string]any)
	for _, key := range []string{"id", "ts", "level", "component", "message"} {
		if _, present := entry[key]; !present {
			t.Fatalf("log entry is missing %q: %#v", key, entry)
		}
	}

	if got := len(adminTestItems(t, h.wantOK(t, h.get(t, "/api/logs?q=hello-ring&level=error")))); got != 1 {
		t.Fatalf("level=error returned %d entries", got)
	}
	if got := len(adminTestItems(t, h.wantOK(t, h.get(t, "/api/logs?q=hello-ring&level=debug")))); got != 4 {
		t.Fatalf("level=debug returned %d entries", got)
	}
	first := adminTestItems(t, h.wantOK(t, h.get(t, "/api/logs?q=hello-ring&limit=1&level=debug")))[0].(map[string]any)
	before := int64(first["id"].(float64))
	paged := h.wantOK(t, h.get(t, "/api/logs?q=hello-ring&level=debug&before_id="+itoa(before)))
	if got := len(adminTestItems(t, paged)); got != 3 {
		t.Fatalf("before_id page returned %d entries", got)
	}
	// Unknown level falls back to info rather than erroring.
	if got := len(adminTestItems(t, h.wantOK(t, h.get(t, "/api/logs?q=hello-ring&level=verbose")))); got != 3 {
		t.Fatalf("unknown level returned %d entries", got)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

func TestAdminListToolCalls(t *testing.T) {
	h := newAdminTestHarness(t)
	seedAdminFixture(t, h)

	data := h.wantOK(t, h.get(t, "/api/tool-calls"))
	if len(adminTestItems(t, data)) != 2 {
		t.Fatalf("items = %#v", data["items"])
	}
	row := adminTestItems(t, data)[0].(map[string]any)
	for _, key := range []string{
		"id", "conversation_id", "group_openid", "member_openid",
		"tool", "args", "status", "error", "latency_ms", "result_chars", "created_at",
	} {
		if _, present := row[key]; !present {
			t.Fatalf("tool call is missing %q: %#v", key, row)
		}
	}
	if data["next_before_id"] != float64(0) {
		t.Fatalf("next_before_id = %v", data["next_before_id"])
	}

	page1 := h.wantOK(t, h.get(t, "/api/tool-calls?limit=1"))
	page1Items := adminTestItems(t, page1)
	if len(page1Items) != 1 {
		t.Fatalf("limit=1 items = %d", len(page1Items))
	}
	next := int64(page1["next_before_id"].(float64))
	if next == 0 {
		t.Fatalf("next_before_id must point at the next page: %#v", page1)
	}
	page2 := h.wantOK(t, h.get(t, "/api/tool-calls?limit=1&before_id="+itoa(next)))
	if got := len(adminTestItems(t, page2)); got != 1 {
		t.Fatalf("before_id page items = %d", got)
	}

	// A garbage limit falls back to the default instead of failing.
	if got := len(adminTestItems(t, h.wantOK(t, h.get(t, "/api/tool-calls?limit=abc")))); got != 2 {
		t.Fatalf("default limit items = %d", got)
	}
}

func TestAdminProactiveState(t *testing.T) {
	h := newAdminTestHarness(t)
	seedAdminFixture(t, h)

	data := h.wantOK(t, h.get(t, "/api/proactive"))
	if data["count"] != float64(1) || data["level"] != "off" {
		t.Fatalf("proactive = %#v", data)
	}
	if data["ratio"] != float64(10) || data["max_credits"] != float64(3) || data["min_interval_minutes"] != float64(10) {
		t.Fatalf("proactive defaults = %#v", data)
	}
	quiet, _ := data["quiet_hours"].([]any)
	if len(quiet) != 2 || quiet[0] != float64(23) || quiet[1] != float64(8) {
		t.Fatalf("quiet_hours = %#v", data["quiet_hours"])
	}
	items := adminTestItems(t, data)
	item := items[0].(map[string]any)
	if item["group_openid"] != adminTestGroup || item["last_reason"] != "有人卡在链接上" {
		t.Fatalf("proactive item = %#v", item)
	}
}

func TestAdminTasksListAndCancel(t *testing.T) {
	h := newAdminTestHarness(t)
	seedAdminFixture(t, h)

	data := h.wantOK(t, h.get(t, "/api/tasks?status=pending"))
	if adminTestNumber(t, data, "count") != 1 {
		t.Fatalf("pending tasks = %#v", data)
	}
	items := adminTestItems(t, data)
	task := items[0].(map[string]any)
	for _, key := range []string{
		"id", "conversation_id", "group_openid", "creator_openid", "text", "due_at", "status",
	} {
		if _, present := task[key]; !present {
			t.Fatalf("task is missing %q: %#v", key, task)
		}
	}
	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/tasks?status=sent")), "count"); got != 0 {
		t.Fatalf("sent tasks = %v", got)
	}
	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/tasks?limit=10&offset=0")), "count"); got != 1 {
		t.Fatalf("all tasks = %v", got)
	}

	h.wantError(t, h.del(t, "/api/tasks/not-a-number"), http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.del(t, "/api/tasks/0"), http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.del(t, "/api/tasks/999999"), http.StatusNotFound, "not_found")

	id := int64(task["id"].(float64))
	done := h.wantOK(t, h.del(t, "/api/tasks/"+itoa(id)))
	if done["canceled"] != true {
		t.Fatalf("cancel response = %#v", done)
	}
	h.wantError(t, h.del(t, "/api/tasks/"+itoa(id)), http.StatusNotFound, "not_found")
}

func TestAdminMemoriesCRUD(t *testing.T) {
	h := newAdminTestHarness(t)
	seedAdminFixture(t, h)

	data := h.wantOK(t, h.get(t, "/api/memories"))
	if adminTestNumber(t, data, "count") != 3 {
		t.Fatalf("memories = %#v", data)
	}
	row := adminTestItems(t, data)[0].(map[string]any)
	for _, key := range []string{
		"id", "scope", "group_openid", "member_openid", "kind", "text", "source", "salience",
	} {
		if _, present := row[key]; !present {
			t.Fatalf("memory is missing %q: %#v", key, row)
		}
	}

	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/memories?scope=group")), "count"); got != 1 {
		t.Fatalf("scope=group count = %v", got)
	}
	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/memories?scope=global")), "count"); got != 1 {
		t.Fatalf("scope=global count = %v", got)
	}
	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/memories?scope=member&member_openid=U2")), "count"); got != 1 {
		t.Fatalf("member filter count = %v", got)
	}
	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/memories?group_openid="+adminTestGroup)), "count"); got != 1 {
		t.Fatalf("group filter count = %v", got)
	}
	if got := adminTestNumber(t, h.wantOK(t, h.get(t, "/api/memories?limit=1&offset=0")), "count"); got != 1 {
		t.Fatalf("limit count = %v", got)
	}

	// Validation of hand-written facts.
	bad := []map[string]any{
		{"scope": "group", "text": ""},
		{"scope": "world", "text": "x"},
		{"scope": "group", "text": "x"},
		{"scope": "member", "text": "x"},
	}
	for _, body := range bad {
		h.wantError(t, h.post(t, "/api/memories", body), http.StatusBadRequest, "invalid_request")
	}
	added := h.wantOK(t, h.post(t, "/api/memories", map[string]any{
		"scope": "group", "group_openid": adminTestGroup, "text": "后台补一条事实",
	}))
	memoryID := int64(added["id"].(float64))
	if memoryID <= 0 {
		t.Fatalf("add memory id = %v", added["id"])
	}
	h.wantOK(t, h.post(t, "/api/memories", map[string]any{"scope": "global", "text": "全局事实"}))

	h.wantError(t, h.del(t, "/api/memories/abc"), http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.del(t, "/api/memories/999999"), http.StatusNotFound, "not_found")
	if got := h.wantOK(t, h.del(t, "/api/memories/"+itoa(memoryID))); got["deleted"] != float64(1) {
		t.Fatalf("delete memory = %#v", got)
	}
	h.wantError(t, h.del(t, "/api/memories"), http.StatusBadRequest, "invalid_request")
	if got := h.wantOK(t, h.del(t, "/api/memories?group_openid="+adminTestGroup)); got["deleted"] == float64(0) {
		t.Fatalf("forgetting a group should delete its facts: %#v", got)
	}
}

func TestAdminStatsRanges(t *testing.T) {
	h := newAdminTestHarness(t)
	seedAdminFixture(t, h)

	cases := map[string]string{"1h": "5m", "6h": "30m", "24h": "1h", "7d": "6h"}
	for rng, bucket := range cases {
		data := h.wantOK(t, h.get(t, "/api/stats?range="+rng))
		if data["range"] != rng || data["bucket"] != bucket {
			t.Fatalf("stats range %s = %#v", rng, data)
		}
		if _, ok := data["series"].([]any); !ok {
			t.Fatalf("series missing for %s: %#v", rng, data)
		}
		if _, ok := data["totals"].(map[string]any); !ok {
			t.Fatalf("totals missing for %s: %#v", rng, data)
		}
		top, ok := data["top_groups"].([]any)
		if !ok {
			t.Fatalf("top_groups missing for %s: %#v", rng, data)
		}
		if len(top) != 1 {
			t.Fatalf("top_groups for %s = %#v", rng, top)
		}
		entry := top[0].(map[string]any)
		if entry["group_openid"] != adminTestGroup || entry["group_name"] != "测试群" {
			t.Fatalf("top group = %#v", entry)
		}
	}
	// An unknown range falls back to 24h.
	data := h.wantOK(t, h.get(t, "/api/stats?range=forever"))
	if data["range"] != "24h" {
		t.Fatalf("unknown range = %v", data["range"])
	}
}

func TestAdminBucketLabelFallback(t *testing.T) {
	// Ranges exposed by the API all map to a known bucket; the fallback keeps the
	// helper honest if one is ever added.
	if got := bucketLabel(core.RangeSpec{Bucket: 2 * time.Minute}); got != "2m0s" {
		t.Fatalf("bucketLabel fallback = %q", got)
	}
}

func TestAdminMediaEndpoint(t *testing.T) {
	h := newAdminTestHarness(t)
	payload := []byte("fake-png-bytes")
	id, err := h.media.Store.Put(context.Background(), "image/png", payload)
	if err != nil {
		t.Fatalf("put media: %v", err)
	}

	resp := h.get(t, "/api/media/"+id)
	h.wantStatus(t, resp, http.StatusOK)
	if ct := resp.header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content type = %q", ct)
	}
	if cc := resp.header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("cache-control = %q", cc)
	}
	if !bytes.Equal(resp.body, payload) {
		t.Fatalf("body = %q", string(resp.body))
	}
	if resp.header.Get("Content-Length") != itoa(int64(len(payload))) {
		t.Fatalf("content-length = %q", resp.header.Get("Content-Length"))
	}

	h.wantError(t, h.get(t, "/api/media/short-id"), http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.get(t, "/api/media/"+strings.Repeat("a", 64)), http.StatusNotFound, "not_found")

	// Media can be disabled entirely.
	noMedia := newAdminTestHarness(t, func(o *adminTestOpts) { o.noMedia = true })
	noMedia.wantError(t, noMedia.get(t, "/api/media/"+strings.Repeat("b", 64)), http.StatusNotFound, "not_found")
}

func TestAdminModelsEndpoint(t *testing.T) {
	h := newAdminTestHarness(t)

	data := h.wantOK(t, h.get(t, "/api/models"))
	if !strings.HasSuffix(adminTestString(t, data, "base_url"), "/v1") {
		t.Fatalf("base_url = %v", data["base_url"])
	}
	if data["count"] != float64(2) || data["current"] != "test-model" || data["current_in_list"] != true {
		t.Fatalf("models = %#v", data)
	}
	if data["cached"] != false {
		t.Fatalf("first call should not be cached: %#v", data)
	}
	ids, _ := data["ids"].([]any)
	if len(ids) != 2 || ids[0] != "another-model" {
		t.Fatalf("ids = %#v", data["ids"])
	}

	// The second call is served from the 5-minute cache.
	if data = h.wantOK(t, h.get(t, "/api/models")); data["cached"] != true {
		t.Fatalf("second call should be cached: %#v", data)
	}
	// refresh=1 bypasses it.
	if data = h.wantOK(t, h.get(t, "/api/models?refresh=1")); data["cached"] != false {
		t.Fatalf("refresh should not be cached: %#v", data)
	}
	// Upstream failure surfaces as a 502.
	h.ai.setModelsStatus(http.StatusInternalServerError)
	h.wantError(t, h.get(t, "/api/models?refresh=true"), http.StatusBadGateway, "upstream_error")

	noAI := newAdminTestHarness(t, func(o *adminTestOpts) { o.noAI = true })
	noAI.wantError(t, noAI.get(t, "/api/models"), http.StatusBadRequest, "not_configured")
}

func TestAdminGroupStateEndpoint(t *testing.T) {
	h := newAdminTestHarness(t)

	data := h.wantOK(t, h.get(t, "/api/groups/"+adminTestGroup+"/state"))
	if data["member_openid"] != "BOT-MEMBER-1" || data["recv_msg_setting"] != "all" {
		t.Fatalf("group state = %#v", data)
	}
	if data["allow_proactive_msg"] != true || data["member_role"] != "member" {
		t.Fatalf("group state = %#v", data)
	}
	if adminTestString(t, data, "fetched_at") == "" {
		t.Fatalf("fetched_at missing: %#v", data)
	}
	// refresh=1 forces a re-read (the panel's refresh button).
	h.wantOK(t, h.get(t, "/api/groups/"+adminTestGroupNoPr+"/state?refresh=1"))

	// An unknown group returns the cached failure as data (the handler only
	// 502s when no snapshot exists at all).
	unknown := h.wantOK(t, h.get(t, "/api/groups/G-UNKNOWN/state"))
	if adminTestString(t, unknown, "error") == "" {
		t.Fatalf("unknown group should report its error: %#v", unknown)
	}

	noDispatcher := newAdminTestHarness(t, func(o *adminTestOpts) { o.noDispatcher = true })
	noDispatcher.wantError(t, noDispatcher.get(t, "/api/groups/"+adminTestGroup+"/state"),
		http.StatusBadRequest, "not_configured")
}
