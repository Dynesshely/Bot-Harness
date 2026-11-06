package qq

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// newPanelTestClient wires a real Client/TokenManager against an httptest
// platform. The token endpoint is answered here so each test only has to deal
// with panel routes.
func newPanelTestClient(t *testing.T, panels http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"test-token","expires_in":"7200"}`)
			return
		}
		panels(w, r)
	}))
	t.Cleanup(srv.Close)
	tokens := NewTokenManager("app-id", "app-secret", srv.URL, srv.Client())
	return NewClient(srv.URL, tokens, srv.Client())
}

type recordedPanelRequest struct {
	Method string
	Path   string
	Query  url.Values
	Body   map[string]any
}

func TestListPanelsFollowsCursor(t *testing.T) {
	var mu sync.Mutex
	var requests []recordedPanelRequest

	client := newPanelTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/panels" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		requests = append(requests, recordedPanelRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query()})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = io.WriteString(w, `{
				"records":[{"panel_id":"P1","scope":"group","target_type":"all",
					"panel":{"items":[{"type":"command","name":"/own","desc":"用户面板"}],"remark":"user:manual","version":2},
					"version":2,"created_at":"2026-01-01T00:00:00+08:00","updated_at":"2026-01-01T00:00:00+08:00"}],
				"next_cursor":"CURSOR-1","is_end":false}`)
		case "CURSOR-1":
			_, _ = io.WriteString(w, `{
				"records":[{"panel_id":"P2","scope":"group","target_type":"all",
					"panel":{"items":[{"type":"command","name":"/help","desc":"查看可用命令"}],"remark":"bot-harness:commands","version":5},
					"version":5}],
				"next_cursor":"","is_end":true}`)
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("cursor"))
			http.Error(w, "bad cursor", http.StatusBadRequest)
		}
	})

	ctx := context.Background()
	first, err := client.ListPanels(ctx, PanelScopeGroup, "", 20)
	if err != nil {
		t.Fatalf("ListPanels page 1: %v", err)
	}
	if len(first.Records) != 1 || first.Records[0].PanelID != "P1" {
		t.Fatalf("page 1 records = %#v", first.Records)
	}
	if first.Records[0].Panel.Remark != "user:manual" || first.Records[0].Version != 2 {
		t.Fatalf("page 1 record = %#v", first.Records[0])
	}
	if first.NextCursor != "CURSOR-1" || first.IsEnd {
		t.Fatalf("page 1 paging = next=%q end=%v", first.NextCursor, first.IsEnd)
	}

	second, err := client.ListPanels(ctx, PanelScopeGroup, first.NextCursor, 20)
	if err != nil {
		t.Fatalf("ListPanels page 2: %v", err)
	}
	if len(second.Records) != 1 || second.Records[0].PanelID != "P2" {
		t.Fatalf("page 2 records = %#v", second.Records)
	}
	if second.Records[0].Panel.Items[0].Name != "/help" || !second.IsEnd {
		t.Fatalf("page 2 = %#v end=%v", second.Records[0].Panel.Items, second.IsEnd)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected 2 list calls, got %d", len(requests))
	}
	if got := requests[0].Query.Get("scope"); got != "group" {
		t.Fatalf("scope query = %q", got)
	}
	if got := requests[0].Query.Get("limit"); got != "20" {
		t.Fatalf("limit query = %q", got)
	}
	if got := requests[1].Query.Get("cursor"); got != "CURSOR-1" {
		t.Fatalf("cursor query = %q", got)
	}
}

func TestCreatePanelWireShape(t *testing.T) {
	var got recordedPanelRequest
	client := newPanelTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/panels" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode create body: %v (%s)", err, body)
		}
		got = recordedPanelRequest{Method: r.Method, Path: r.URL.Path, Body: decoded}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"panel_id":"P-NEW"}`)
	})

	id, err := client.CreatePanel(context.Background(), CreatePanelRequest{
		Scope:      PanelScopeGroup,
		TargetType: PanelTargetAll,
		Panel: Panel{
			// Version must not reach the wire: the platform owns it.
			Version: 9,
			Remark:  "bot-harness:commands",
			Items: []PanelItem{
				{Type: PanelItemCommand, Name: "/help", Desc: "查看可用命令"},
				{Type: PanelItemCommand, Name: "/tokenstats", Desc: "开关 token 用量统计", OnlyAdmin: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreatePanel: %v", err)
	}
	if id != "P-NEW" {
		t.Fatalf("panel id = %q", id)
	}
	if got.Body["scope"] != "group" || got.Body["target_type"] != "all" {
		t.Fatalf("create body = %#v", got.Body)
	}
	panel, _ := got.Body["panel"].(map[string]any)
	if panel["remark"] != "bot-harness:commands" {
		t.Fatalf("panel remark = %#v", panel)
	}
	if _, present := panel["version"]; present {
		t.Fatalf("create must not send panel.version: %#v", panel)
	}
	items, _ := panel["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("panel items = %#v", panel["items"])
	}
	second, _ := items[1].(map[string]any)
	if second["name"] != "/tokenstats" || second["only_admin"] != true {
		t.Fatalf("second item = %#v", second)
	}
}

func TestUpdatePanelSendsOnlyPanelAndReturnsVersion(t *testing.T) {
	var got recordedPanelRequest
	client := newPanelTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v2/panels/P-1" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode update body: %v (%s)", err, body)
		}
		got = recordedPanelRequest{Method: r.Method, Path: r.URL.Path, Body: decoded}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":8}`)
	})

	version, err := client.UpdatePanel(context.Background(), "P-1", Panel{
		Version: 3, // stale on purpose: it must not be part of the request
		Remark:  "bot-harness:commands",
		Items:   []PanelItem{{Type: PanelItemCommand, Name: "/help", Desc: "查看可用命令"}},
	})
	if err != nil {
		t.Fatalf("UpdatePanel: %v", err)
	}
	if version != 8 {
		t.Fatalf("version = %d", version)
	}
	if len(got.Body) != 1 {
		t.Fatalf("update body must only contain panel: %#v", got.Body)
	}
	panel, ok := got.Body["panel"].(map[string]any)
	if !ok {
		t.Fatalf("missing panel object: %#v", got.Body)
	}
	if _, present := panel["version"]; present {
		t.Fatalf("update must not send panel.version: %#v", panel)
	}
	if panel["remark"] != "bot-harness:commands" {
		t.Fatalf("panel remark = %#v", panel)
	}
}

func TestDeletePanel(t *testing.T) {
	var seen string
	client := newPanelTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v2/panels/P-9" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		seen = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	})
	if err := client.DeletePanel(context.Background(), "P-9"); err != nil {
		t.Fatalf("DeletePanel: %v", err)
	}
	if seen != "/v2/panels/P-9" {
		t.Fatalf("delete path = %q", seen)
	}
}

func TestPanelAPIErrorPassthrough(t *testing.T) {
	client := newPanelTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":40034006,"message":"消息内容违规"}`)
	})

	_, err := client.CreatePanel(context.Background(), CreatePanelRequest{
		Scope:      PanelScopeGroup,
		TargetType: PanelTargetAll,
		Panel:      Panel{Items: []PanelItem{{Type: PanelItemCommand, Name: "/help"}}},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !CodeIs(err, ErrContentBlocked) {
		t.Fatalf("expected code %d, got %v", ErrContentBlocked, err)
	}
	text := err.Error()
	for _, want := range []string{"40034006", "消息内容违规", "/v2/panels"} {
		if !strings.Contains(text, want) {
			t.Fatalf("error text %q is missing %q", text, want)
		}
	}

	// Listing errors must be the same typed error, not a wrapped string.
	_, err = client.ListPanels(context.Background(), PanelScopeGroup, "", 20)
	if err == nil || !CodeIs(err, ErrContentBlocked) {
		t.Fatalf("list error = %v", err)
	}
}
