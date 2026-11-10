package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/affect"
	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// affectAdminHarness is a deliberately small harness: the affect endpoints only
// need the settings manager, the store and the service, so this avoids dragging
// in the fake QQ/OpenAI upstreams used by the full panel tests.
type affectAdminHarness struct {
	t        *testing.T
	store    *store.Store
	settings *settings.Manager
	service  *affect.Service
	server   *Server
	ts       *httptest.Server
}

const affectTestToken = "affect-admin-token"

func newAffectAdminHarness(t *testing.T) *affectAdminHarness {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/affect-admin.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	sm, err := settings.New(context.Background(), st, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	patch := map[string]any{
		"admin_token":        affectTestToken,
		"affect_enabled":     true,
		"affect_pack":        "daily",
		"affect_inject_mode": "full",
	}
	raw := make(map[string]json.RawMessage, len(patch))
	for k, v := range patch {
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal setting %q: %v", k, err)
		}
		raw[k] = encoded
	}
	if _, _, err := sm.Apply(context.Background(), raw); err != nil {
		t.Fatalf("apply settings: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := affect.NewService(st, sm, logger, affect.NewMetrics())
	// The dispatcher is only needed by the counters bridge (/api/status).
	dis := core.New(core.Options{Settings: sm, Store: st, Logger: logger})

	h := &affectAdminHarness{t: t, store: st, settings: sm, service: svc}
	h.server = New(Deps{
		Settings:   sm,
		Store:      st,
		Dispatcher: dis,
		Affect:     svc,
		Logger:     logger,
		StartedAt:  time.Now(),
	})
	h.ts = httptest.NewServer(h.server.Handler())
	t.Cleanup(h.ts.Close)
	return h
}

func (h *affectAdminHarness) request(method, path string, body any, withAuth bool) *adminTestResponse {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, h.ts.URL+path, reader)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+affectTestToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("read body: %v", err)
	}
	out := &adminTestResponse{status: resp.StatusCode, header: resp.Header, body: raw}
	out.decodeEnvelope()
	return out
}

func (h *affectAdminHarness) get(path string) *adminTestResponse {
	return h.request(http.MethodGet, path, nil, true)
}

func (h *affectAdminHarness) post(path string, body any) *adminTestResponse {
	return h.request(http.MethodPost, path, body, true)
}

func (h *affectAdminHarness) seedConversation(id, group string) {
	h.t.Helper()
	if err := h.store.EnsureConversation(context.Background(), &store.Conversation{
		ID: id, Scene: store.SceneGroupMember, GroupOpenID: group, GroupName: "测试群",
	}); err != nil {
		h.t.Fatalf("seed conversation: %v", err)
	}
}

// TestAffectAdminRequiresAuth pins all affect routes behind the admin session.
func TestAffectAdminRequiresAuth(t *testing.T) {
	h := newAffectAdminHarness(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/affect/packs"},
		{http.MethodGet, "/api/affect/conversations"},
		{http.MethodGet, "/api/affect/state?conversation=x"},
		{http.MethodGet, "/api/affect/history?conversation=x"},
		{http.MethodGet, "/api/affect/events?conversation=x"},
		{http.MethodPost, "/api/affect/pause"},
		{http.MethodPost, "/api/affect/resume"},
		{http.MethodPost, "/api/affect/preset"},
		{http.MethodPost, "/api/affect/reset"},
	} {
		resp := h.request(tc.method, tc.path, nil, false)
		if resp.status != http.StatusUnauthorized {
			t.Fatalf("%s %s without auth = %d, want 401", tc.method, tc.path, resp.status)
		}
		if resp.code != "unauthorized" {
			t.Fatalf("%s %s error code = %q", tc.method, tc.path, resp.code)
		}
	}
}

func TestAffectAdminPacks(t *testing.T) {
	h := newAffectAdminHarness(t)
	resp := h.get("/api/affect/packs")
	if resp.status != http.StatusOK || !resp.ok {
		t.Fatalf("packs status = %d body %s", resp.status, string(resp.body))
	}
	data := resp.data(t)
	packs, ok := data["packs"].([]any)
	if !ok || len(packs) == 0 {
		t.Fatalf("packs = %#v", data["packs"])
	}
	found := false
	for _, item := range packs {
		p, _ := item.(map[string]any)
		if p["id"] == "daily" {
			found = true
			if p["dimensions"] == nil || p["name"] == "" {
				t.Fatalf("daily pack entry = %#v", p)
			}
		}
	}
	if !found {
		t.Fatalf("daily pack missing from catalogue: %#v", packs)
	}
}

func TestAffectAdminValidationAndNotFound(t *testing.T) {
	h := newAffectAdminHarness(t)
	h.seedConversation("conv-known", "G-AFFECT")

	for _, path := range []string{
		"/api/affect/state",
		"/api/affect/state?conversation=",
		"/api/affect/history",
		"/api/affect/events",
	} {
		resp := h.get(path)
		if resp.status != http.StatusBadRequest || resp.code != "invalid_request" {
			t.Fatalf("GET %s = %d/%q, want 400 invalid_request (body %s)",
				path, resp.status, resp.code, string(resp.body))
		}
	}
	for _, path := range []string{
		"/api/affect/state?conversation=missing",
		"/api/affect/history?conversation=missing",
		"/api/affect/events?conversation=missing",
	} {
		resp := h.get(path)
		if resp.status != http.StatusNotFound || resp.code != "not_found" {
			t.Fatalf("GET %s = %d/%q, want 404 not_found (body %s)",
				path, resp.status, resp.code, string(resp.body))
		}
	}
	// Body validation on the mutations.
	for _, tc := range []struct {
		path string
		body any
	}{
		{"/api/affect/pause", map[string]any{"conversation": "conv-known", "minutes": 0}},
		{"/api/affect/pause", map[string]any{"conversation": "conv-known", "minutes": 10081}},
		{"/api/affect/pause", map[string]any{"conversation": "", "minutes": 5}},
		{"/api/affect/resume", map[string]any{"conversation": ""}},
		{"/api/affect/preset", map[string]any{"conversation": "conv-known", "preset": ""}},
		{"/api/affect/reset", map[string]any{"conversation": ""}},
	} {
		resp := h.post(tc.path, tc.body)
		if resp.status != http.StatusBadRequest || resp.code != "invalid_request" {
			t.Fatalf("POST %s %v = %d/%q, want 400 invalid_request (body %s)",
				tc.path, tc.body, resp.status, resp.code, string(resp.body))
		}
	}
	// Unknown preset is a 404 (docs/ADMIN-API.md), not a malformed request.
	unknownPreset := h.post("/api/affect/preset", map[string]any{"conversation": "conv-known", "preset": "nope"})
	if unknownPreset.status != http.StatusNotFound || unknownPreset.code != "not_found" {
		t.Fatalf("unknown preset = %d/%q, want 404 not_found (body %s)",
			unknownPreset.status, unknownPreset.code, string(unknownPreset.body))
	}
	for _, tc := range []struct {
		path string
		body any
	}{
		{"/api/affect/pause", map[string]any{"conversation": "missing", "minutes": 5}},
		{"/api/affect/resume", map[string]any{"conversation": "missing"}},
		{"/api/affect/preset", map[string]any{"conversation": "missing", "preset": "calm"}},
		{"/api/affect/reset", map[string]any{"conversation": "missing"}},
	} {
		resp := h.post(tc.path, tc.body)
		if resp.status != http.StatusNotFound || resp.code != "not_found" {
			t.Fatalf("POST %s missing = %d/%q, want 404 not_found (body %s)",
				tc.path, resp.status, resp.code, string(resp.body))
		}
	}
}

func TestAffectAdminStateMutationsAndHistory(t *testing.T) {
	h := newAffectAdminHarness(t)
	h.seedConversation("conv-1", "G-AFFECT")
	ctx := context.Background()

	// A state with a real pause and one event mark.
	at := time.Now().Add(-time.Minute).Format(time.RFC3339)
	if err := h.store.SaveAffectState(ctx, &store.AffectState{
		ConversationID: "conv-1",
		PackID:         "daily",
		Dims:           map[string]float64{"valence": 40, "energy": 30, "stress": 55, "closeness": 45, "patience": 50},
		Events:         []store.AffectEventMark{{ID: "praise", At: at}},
		TickedAt:       at,
		UpdatedAt:      at,
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	resp := h.get("/api/affect/state?conversation=conv-1")
	if resp.status != http.StatusOK || !resp.ok {
		t.Fatalf("state = %d body %s", resp.status, string(resp.body))
	}
	data := resp.data(t)
	state, _ := data["state"].(map[string]any)
	if state["conversation_id"] != "conv-1" || state["pack_id"] != "daily" {
		t.Fatalf("state = %#v", state)
	}
	dims, _ := state["dims"].(map[string]any)
	if dims["valence"] == nil {
		t.Fatalf("state dims = %#v", state["dims"])
	}
	if _, ok := data["behavior"].(map[string]any); !ok {
		t.Fatalf("behavior = %#v", data["behavior"])
	}
	blocks, _ := data["blocks"].(map[string]any)
	if blocks == nil || strings.TrimSpace(blocks["line"].(string)) == "" || strings.TrimSpace(blocks["full"].(string)) == "" {
		t.Fatalf("preview blocks = %#v", data["blocks"])
	}

	// Pause → state reflects the deadline; resume clears it.
	paused := h.post("/api/affect/pause", map[string]any{"conversation": "conv-1", "minutes": 30})
	if paused.status != http.StatusOK {
		t.Fatalf("pause = %d body %s", paused.status, string(paused.body))
	}
	pauseState, _ := paused.data(t)["state"].(map[string]any)
	if pauseState == nil || pauseState["paused_until"] == "" {
		t.Fatalf("pause data = %#v", paused.data(t))
	}
	afterPause := h.get("/api/affect/state?conversation=conv-1").data(t)["state"].(map[string]any)
	if afterPause["paused_until"] == "" {
		t.Fatalf("state not paused: %#v", afterPause)
	}
	// A paused conversation injects nothing, even in preview.
	pausedBlocks := h.get("/api/affect/state?conversation=conv-1").data(t)["blocks"].(map[string]any)
	if pausedBlocks["line"] != "" || pausedBlocks["full"] != "" {
		t.Fatalf("paused preview blocks = %#v", pausedBlocks)
	}

	resumed := h.post("/api/affect/resume", map[string]any{"conversation": "conv-1"})
	if resumed.status != http.StatusOK {
		t.Fatalf("resume = %d body %s", resumed.status, string(resumed.body))
	}
	resumeState, _ := resumed.data(t)["state"].(map[string]any)
	if resumeState == nil {
		t.Fatalf("resume data = %#v", resumed.data(t))
	}
	if until, _ := resumeState["paused_until"].(string); until != "" {
		t.Fatalf("resume left paused_until = %q", until)
	}

	// Preset writes the named dimensions; reset returns to the pack baseline.
	preset := h.post("/api/affect/preset", map[string]any{"conversation": "conv-1", "preset": "calm"})
	if preset.status != http.StatusOK {
		t.Fatalf("preset = %d body %s", preset.status, string(preset.body))
	}
	presetState, _ := preset.data(t)["state"].(map[string]any)
	if presetState == nil {
		t.Fatalf("preset response = %#v", preset.data(t))
	}
	presetDims, _ := presetState["dims"].(map[string]any)
	if presetDims["valence"] != float64(60) || presetDims["patience"] != float64(65) {
		t.Fatalf("preset dims = %#v", presetDims)
	}
	reset := h.post("/api/affect/reset", map[string]any{"conversation": "conv-1"})
	if reset.status != http.StatusOK {
		t.Fatalf("reset = %d body %s", reset.status, string(reset.body))
	}
	resetState, _ := reset.data(t)["state"].(map[string]any)
	if resetState == nil {
		t.Fatalf("reset response = %#v", reset.data(t))
	}
	resetDims, _ := resetState["dims"].(map[string]any)
	if resetDims["valence"] != float64(55) {
		t.Fatalf("reset dims = %#v", resetDims)
	}

	// History and events.
	for _, point := range []struct {
		at  time.Time
		val float64
	}{
		{time.Now().Add(-2 * time.Hour), 30},
		{time.Now().Add(-time.Hour), 50},
	} {
		if err := h.store.AppendAffectHistory(ctx, "conv-1", point.at.Format(time.RFC3339),
			map[string]float64{"valence": point.val}); err != nil {
			t.Fatalf("append history: %v", err)
		}
	}
	history := h.get("/api/affect/history?conversation=conv-1&hours=24")
	if history.status != http.StatusOK {
		t.Fatalf("history = %d body %s", history.status, string(history.body))
	}
	points, _ := history.data(t)["points"].([]any)
	if len(points) != 2 {
		t.Fatalf("history points = %#v", points)
	}
	if err := h.store.AppendAffectEvent(ctx, "conv-1", "praise", "positive", "rules", at); err != nil {
		t.Fatalf("append event: %v", err)
	}
	events := h.get("/api/affect/events?conversation=conv-1&limit=10")
	if events.status != http.StatusOK {
		t.Fatalf("events = %d body %s", events.status, string(events.body))
	}
	eventItems, _ := events.data(t)["events"].([]any)
	if len(eventItems) != 1 {
		t.Fatalf("events = %#v", eventItems)
	}

	// Conversations lists the seeded row with its pack and dims.
	conversations := h.get("/api/affect/conversations?limit=999")
	if conversations.status != http.StatusOK {
		t.Fatalf("conversations = %d body %s", conversations.status, string(conversations.body))
	}
	list, _ := conversations.data(t)["conversations"].([]any)
	if len(list) != 1 {
		t.Fatalf("conversations = %#v", list)
	}
	entry, _ := list[0].(map[string]any)
	if entry["id"] != "conv-1" || entry["pack"] != "daily" || entry["dims"] == nil {
		t.Fatalf("conversation entry = %#v", entry)
	}
}

// TestAffectAdminCountersMerged proves the affect totals reach the counters
// block under the agreed names.
func TestAffectAdminCountersMerged(t *testing.T) {
	h := newAffectAdminHarness(t)
	m := h.service.Metrics()
	m.Ticks.Add(3)
	m.Events.Add(5)
	m.AddInjection(42)
	m.Errors.Add(1)

	snapshot := h.server.statusCounters()
	if snapshot.AffectTicks != 3 || snapshot.AffectEvents != 5 ||
		snapshot.AffectInjections != 1 || snapshot.AffectInjectChars != 42 ||
		snapshot.AffectErrors != 1 {
		t.Fatalf("counters = %+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal counters: %v", err)
	}
	for _, key := range []string{
		"affect_ticks", "affect_events", "affect_injections",
		"affect_inject_chars", "affect_errors",
	} {
		if !bytes.Contains(encoded, []byte(key)) {
			t.Fatalf("counters JSON missing %q: %s", key, encoded)
		}
	}
}
