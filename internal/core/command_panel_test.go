package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
)

// fakePanelPlatform is an httptest-backed QQ panel API. It is only a few dozen
// lines, and using the real qq.Client exercises do(), token handling and the
// APIError path instead of a hand-written interface fake.
type fakePanelPlatform struct {
	mu      sync.Mutex
	byScope map[string][]qq.PanelRecord
	nextID  int

	listCalls  []panelListCall
	created    []qq.CreatePanelRequest
	updated    []panelUpdateCall
	deleted    []string
	onList     func(scope, cursor string) (qq.PanelList, bool)
	errStatus  int
	errCode    int
	errMessage string
	// failScopes makes only the named scopes fail with errStatus/errCode, so a
	// test can pin the "one scope fails, the other still lands" contract.
	failScopes map[string]bool
}

type panelListCall struct {
	Scope  string
	Cursor string
	Limit  int
}

type panelUpdateCall struct {
	PanelID string
	Panel   qq.Panel
}

func newFakePanelPlatform() *fakePanelPlatform {
	return &fakePanelPlatform{byScope: map[string][]qq.PanelRecord{}}
}

// normalizeItems mirrors the platform, which strips a leading "/" from item
// names on write: sending "/help" reads back as "help". Without this the fake
// would be more forgiving than production and hide the re-write loop.
func normalizeItems(items []qq.PanelItem) []qq.PanelItem {
	out := append([]qq.PanelItem(nil), items...)
	for i := range out {
		out[i].Name = strings.TrimPrefix(out[i].Name, "/")
	}
	return out
}

func clonePanelRecords(in []qq.PanelRecord) []qq.PanelRecord {
	out := make([]qq.PanelRecord, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Panel.Items = append([]qq.PanelItem(nil), in[i].Panel.Items...)
	}
	return out
}

func (f *fakePanelPlatform) serveHTTP(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	f.mu.Lock()
	status, code, message := f.errStatus, f.errCode, f.errMessage
	failScopes := map[string]bool{}
	for scope, fail := range f.failScopes {
		failScopes[scope] = fail
	}
	f.mu.Unlock()
	if status != 0 && failScopes[r.URL.Query().Get("scope")] {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"code":%d,"message":%q}`, code, message)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v2/panels":
		scope := r.URL.Query().Get("scope")
		cursor := r.URL.Query().Get("cursor")
		limit := 0
		_, _ = fmt.Sscanf(r.URL.Query().Get("limit"), "%d", &limit)

		f.mu.Lock()
		f.listCalls = append(f.listCalls, panelListCall{Scope: scope, Cursor: cursor, Limit: limit})
		onList := f.onList
		records := clonePanelRecords(f.byScope[scope])
		f.mu.Unlock()

		if onList != nil {
			if page, handled := onList(scope, cursor); handled {
				_ = json.NewEncoder(w).Encode(page)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(qq.PanelList{Records: records, IsEnd: true})

	case r.Method == http.MethodPost && r.URL.Path == "/v2/panels":
		var req qq.CreatePanelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode create request: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.nextID++
		id := fmt.Sprintf("P-%d", f.nextID)
		rec := qq.PanelRecord{
			PanelID:    id,
			Scope:      req.Scope,
			TargetType: req.TargetType,
			Panel: qq.Panel{
				Items:   normalizeItems(req.Panel.Items),
				Remark:  req.Panel.Remark,
				Version: 1,
			},
			Version: 1,
		}
		f.byScope[req.Scope] = append(f.byScope[req.Scope], rec)
		f.created = append(f.created, req)
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"panel_id":%q}`, id)

	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v2/panels/"):
		id := strings.TrimPrefix(r.URL.Path, "/v2/panels/")
		var body struct {
			Panel qq.Panel `json:"panel"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode update request: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.updated = append(f.updated, panelUpdateCall{PanelID: id, Panel: body.Panel})
		found := false
		version := 0
		for scope := range f.byScope {
			for i := range f.byScope[scope] {
				rec := &f.byScope[scope][i]
				if rec.PanelID != id {
					continue
				}
				rec.Panel.Items = normalizeItems(body.Panel.Items)
				rec.Panel.Remark = body.Panel.Remark
				rec.Version++
				rec.Panel.Version = rec.Version
				version = rec.Version
				found = true
			}
		}
		f.mu.Unlock()
		if !found {
			http.Error(w, "panel not found", http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprintf(w, `{"version":%d}`, version)

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v2/panels/"):
		id := strings.TrimPrefix(r.URL.Path, "/v2/panels/")
		f.mu.Lock()
		f.deleted = append(f.deleted, id)
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{}`)

	default:
		http.NotFound(w, r)
	}
}

// newCorePanelDispatcher wires the real qq.Client (and its token manager) to
// the fake platform.
func newCorePanelDispatcher(t *testing.T, platform *fakePanelPlatform) *Dispatcher {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"test-token","expires_in":"7200"}`)
			return
		}
		platform.serveHTTP(t, w, r)
	}))
	t.Cleanup(srv.Close)

	client := qq.NewClient(srv.URL, qq.NewTokenManager("app-id", "app-secret", srv.URL, srv.Client()), srv.Client())
	d := &Dispatcher{
		commands: map[string]*command{},
		qq:       client,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	d.registerBuiltinCommands()
	return d
}

func panelItemNames(items []qq.PanelItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return out
}

func TestCommandPanelItemsProjection(t *testing.T) {
	d := &Dispatcher{commands: map[string]*command{}}
	d.registerBuiltinCommands()
	d.registerCommand(&command{
		name:      "longdesc",
		aliases:   []string{"long", "ld"},
		adminOnly: true,
		usage:     "/longdesc [x]",
		desc:      strings.Repeat("长", 35),
	})
	d.registerCommand(&command{name: "bare", usage: ""})

	items := d.commandPanelItems()
	names := panelItemNames(items)
	// The platform strips the leading "/" on write, so the projection drops it too:
	// what we send must equal what the platform reads back, byte for byte.
	want := []string{"bare", "help", "human-status", "longdesc", "mood", "tokenstats"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("panel names = %v, want %v", names, want)
	}
	if len(items) != len(want) {
		t.Fatalf("aliases must not become separate items: %#v", items)
	}

	byName := map[string]qq.PanelItem{}
	for _, item := range items {
		byName[item.Name] = item
		if item.Type != qq.PanelItemCommand {
			t.Fatalf("%s type = %q", item.Name, item.Type)
		}
	}
	if byName["tokenstats"].Desc != tokenStatsDesc {
		t.Fatalf("tokenstats desc = %q", byName["tokenstats"].Desc)
	}
	if !byName["tokenstats"].OnlyAdmin {
		t.Fatal("tokenstats must be admin-only")
	}
	if byName["help"].OnlyAdmin {
		t.Fatal("help must be available to everyone")
	}
	if got := len([]rune(byName["longdesc"].Desc)); got != commandPanelDescMax {
		t.Fatalf("long desc was not truncated to %d runes: %d (%q)",
			commandPanelDescMax, got, byName["longdesc"].Desc)
	}
}

func TestSyncCommandPanelCreatesBothScopes(t *testing.T) {
	platform := newFakePanelPlatform()
	d := newCorePanelDispatcher(t, platform)

	results, err := d.SyncCommandPanel(context.Background())
	if err != nil {
		t.Fatalf("SyncCommandPanel: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	for i, scope := range []string{qq.PanelScopeGroup, qq.PanelScopeC2C} {
		if results[i].Scope != scope || results[i].Action != PanelActionCreated {
			t.Fatalf("result[%d] = %#v", i, results[i])
		}
		if results[i].PanelID == "" {
			t.Fatalf("result[%d] has no panel id: %#v", i, results[i])
		}
	}

	platform.mu.Lock()
	created := append([]qq.CreatePanelRequest(nil), platform.created...)
	platform.mu.Unlock()
	if len(created) != 2 {
		t.Fatalf("create calls = %d, want 2", len(created))
	}
	for _, req := range created {
		if req.TargetType != qq.PanelTargetAll {
			t.Fatalf("target_type = %q", req.TargetType)
		}
		if req.Panel.Remark != panelRemark {
			t.Fatalf("remark = %q, want marker %q", req.Panel.Remark, panelRemark)
		}
		if len(req.Panel.Items) < 2 {
			t.Fatalf("items = %#v", req.Panel.Items)
		}
	}
}

func TestSyncCommandPanelUpdatesOwnPanelAndLeavesUsersAlone(t *testing.T) {
	platform := newFakePanelPlatform()
	d := newCorePanelDispatcher(t, platform)
	wantItems := d.commandPanelItems()

	platform.byScope[qq.PanelScopeGroup] = []qq.PanelRecord{
		{
			PanelID: "P-USER", Scope: qq.PanelScopeGroup, TargetType: qq.PanelTargetAll,
			Panel:   qq.Panel{Remark: "user:manual", Items: []qq.PanelItem{{Type: "command", Name: "/mine"}}},
			Version: 4,
		},
		{
			PanelID: "P-OURS", Scope: qq.PanelScopeGroup, TargetType: qq.PanelTargetAll,
			Panel: qq.Panel{Remark: panelRemark, Version: 7,
				Items: []qq.PanelItem{{Type: "command", Name: "/stale", Desc: "旧内容"}}},
			Version: 7,
		},
	}
	platform.byScope[qq.PanelScopeC2C] = []qq.PanelRecord{
		{
			PanelID: "P-C2C", Scope: qq.PanelScopeC2C, TargetType: qq.PanelTargetAll,
			Panel:   qq.Panel{Remark: panelRemark, Version: 3, Items: append([]qq.PanelItem(nil), wantItems...)},
			Version: 3,
		},
	}

	results, err := d.SyncCommandPanel(context.Background())
	if err != nil {
		t.Fatalf("SyncCommandPanel: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	group, c2c := results[0], results[1]
	if group.Scope != qq.PanelScopeGroup || group.PanelID != "P-OURS" ||
		group.Action != PanelActionUpdated || group.Version != 8 {
		t.Fatalf("group result = %#v", group)
	}
	if c2c.Scope != qq.PanelScopeC2C || c2c.PanelID != "P-C2C" ||
		c2c.Action != PanelActionUnchanged || c2c.Version != 3 {
		t.Fatalf("c2c result = %#v", c2c)
	}

	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.created) != 0 {
		t.Fatalf("must not create a second panel: %#v", platform.created)
	}
	if len(platform.updated) != 1 || platform.updated[0].PanelID != "P-OURS" {
		t.Fatalf("updates = %#v", platform.updated)
	}
	if platform.updated[0].Panel.Remark != panelRemark {
		t.Fatalf("update remark = %q", platform.updated[0].Panel.Remark)
	}

	// 用户手工面板必须原样保留。
	var user qq.PanelRecord
	for _, rec := range platform.byScope[qq.PanelScopeGroup] {
		if rec.PanelID == "P-USER" {
			user = rec
		}
	}
	if user.Version != 4 || user.Panel.Remark != "user:manual" ||
		len(user.Panel.Items) != 1 || user.Panel.Items[0].Name != "/mine" {
		t.Fatalf("user panel was touched: %#v", user)
	}
}

func TestSyncCommandPanelFindsPanelOnLaterPage(t *testing.T) {
	platform := newFakePanelPlatform()
	d := newCorePanelDispatcher(t, platform)
	wantItems := d.commandPanelItems()

	platform.onList = func(scope, cursor string) (qq.PanelList, bool) {
		if scope != qq.PanelScopeGroup {
			return qq.PanelList{}, false
		}
		if cursor == "" {
			return qq.PanelList{
				Records: []qq.PanelRecord{{
					PanelID: "P-FIRST", Scope: qq.PanelScopeGroup,
					Panel:   qq.Panel{Remark: "user:manual", Items: []qq.PanelItem{{Type: "command", Name: "/mine"}}},
					Version: 1,
				}},
				NextCursor: "PAGE-2",
				IsEnd:      false,
			}, true
		}
		if cursor == "PAGE-2" {
			return qq.PanelList{
				Records: []qq.PanelRecord{{
					PanelID: "P-OURS", Scope: qq.PanelScopeGroup,
					Panel:   qq.Panel{Remark: panelRemark, Version: 11, Items: append([]qq.PanelItem(nil), wantItems...)},
					Version: 11,
				}},
				IsEnd: true,
			}, true
		}
		t.Errorf("unexpected cursor %q", cursor)
		return qq.PanelList{}, true
	}

	results, err := d.SyncCommandPanel(context.Background())
	if err != nil {
		t.Fatalf("SyncCommandPanel: %v", err)
	}
	group := results[0]
	if group.PanelID != "P-OURS" || group.Action != PanelActionUnchanged || group.Version != 11 {
		t.Fatalf("group result = %#v", group)
	}

	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.listCalls) != 3 {
		// group page 1 + group page 2 + c2c page 1
		t.Fatalf("list calls = %#v", platform.listCalls)
	}
	if platform.listCalls[0].Cursor != "" || platform.listCalls[1].Cursor != "PAGE-2" {
		t.Fatalf("cursors = %#v", platform.listCalls)
	}
}

func TestSyncCommandPanelSurfacesPlatformError(t *testing.T) {
	platform := newFakePanelPlatform()
	platform.errStatus = http.StatusBadRequest
	platform.errCode = qq.ErrContentBlocked
	platform.errMessage = "面板内容违规"
	platform.failScopes = map[string]bool{
		qq.PanelScopeGroup: true,
		qq.PanelScopeC2C:   true,
	}
	d := newCorePanelDispatcher(t, platform)

	results, err := d.SyncCommandPanel(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(results) != 0 {
		t.Fatalf("results on failure = %#v", results)
	}
	if !qq.CodeIs(err, qq.ErrContentBlocked) {
		t.Fatalf("platform code was lost: %v", err)
	}
	for _, want := range []string{"面板内容违规", "group", "c2c"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q is missing %q", err.Error(), want)
		}
	}
}

func TestRunCommandPanelSyncSkipsWhenDisabled(t *testing.T) {
	mgr, err := settings.New(context.Background(), nil, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if _, _, err := mgr.Apply(context.Background(), map[string]json.RawMessage{
		"command_panel_enabled": json.RawMessage("false"),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	platform := newFakePanelPlatform()
	d := newCorePanelDispatcher(t, platform)
	d.settings = mgr

	old := commandPanelInitialDelay
	commandPanelInitialDelay = 0
	t.Cleanup(func() { commandPanelInitialDelay = old })

	done := make(chan struct{})
	go func() {
		d.RunCommandPanelSync(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disabled auto sync did not return promptly")
	}

	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.listCalls) != 0 || len(platform.created) != 0 {
		t.Fatalf("disabled sync must not call the platform: %#v %#v", platform.listCalls, platform.created)
	}
}

// TestSyncCommandPanelIsIdempotentAgainstPlatformNormalisation pins the real
// platform behaviour that a first deployment exposed: item names are accepted
// with a leading "/" but read back without it. If the projection kept the slash,
// every sync would see a difference and rewrite the panel forever.
func TestSyncCommandPanelIsIdempotentAgainstPlatformNormalisation(t *testing.T) {
	platform := newFakePanelPlatform()
	d := newCorePanelDispatcher(t, platform)

	if _, err := d.SyncCommandPanel(context.Background()); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	results, err := d.SyncCommandPanel(context.Background())
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	for _, res := range results {
		if res.Action != PanelActionUnchanged {
			t.Fatalf("second sync must be unchanged, got %#v", res)
		}
	}

	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.updated) != 0 {
		t.Fatalf("idempotent sync must not write: %#v", platform.updated)
	}
	if len(platform.created) != 2 {
		t.Fatalf("create calls = %d, want 2", len(platform.created))
	}
}

// TestSyncCommandPanelTreatsSlashNamesAsUnchanged covers panels written before
// the normalisation rule (their items carry a leading "/"): comparing the raw
// strings would rewrite them once for no reason.
func TestSyncCommandPanelTreatsSlashNamesAsUnchanged(t *testing.T) {
	platform := newFakePanelPlatform()
	d := newCorePanelDispatcher(t, platform)

	legacy := d.commandPanelItems()
	for i := range legacy {
		legacy[i].Name = "/" + legacy[i].Name
	}
	for _, scope := range []string{qq.PanelScopeGroup, qq.PanelScopeC2C} {
		platform.byScope[scope] = []qq.PanelRecord{{
			PanelID: "P-" + scope, Scope: scope, TargetType: qq.PanelTargetAll,
			Panel:   qq.Panel{Remark: panelRemark, Version: 5, Items: append([]qq.PanelItem(nil), legacy...)},
			Version: 5,
		}}
	}

	results, err := d.SyncCommandPanel(context.Background())
	if err != nil {
		t.Fatalf("SyncCommandPanel: %v", err)
	}
	for _, res := range results {
		if res.Action != PanelActionUnchanged || res.Version != 5 {
			t.Fatalf("slash-prefixed panel result = %#v, want unchanged v5", res)
		}
	}

	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.updated) != 0 {
		t.Fatalf("slash-prefixed names must compare equal: %#v", platform.updated)
	}
}

// TestSyncCommandPanelOneScopeFailuresStillReturnsTheOther pins the acceptance
// rule "one scope fails, the other succeeds": the failing scope must not swallow
// the successful result, otherwise the admin endpoint and the logs would report
// a total failure while one panel actually landed on the platform.
func TestSyncCommandPanelOneScopeFailuresStillReturnsTheOther(t *testing.T) {
	cases := []struct {
		name      string
		failScope string
		wantScope string
	}{
		{"group 失败，c2c 仍然返回", qq.PanelScopeGroup, qq.PanelScopeC2C},
		{"c2c 失败，group 仍然返回", qq.PanelScopeC2C, qq.PanelScopeGroup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			platform := newFakePanelPlatform()
			platform.failScopes = map[string]bool{tc.failScope: true}
			platform.errStatus = http.StatusBadRequest
			platform.errCode = qq.ErrContentBlocked
			platform.errMessage = "面板内容违规"
			d := newCorePanelDispatcher(t, platform)

			results, err := d.SyncCommandPanel(context.Background())
			if err == nil {
				t.Fatal("一个 scope 失败时仍必须返回错误，便于 HTTP 层报 upstream_error")
			}
			if !strings.Contains(err.Error(), tc.failScope) {
				t.Fatalf("错误 %q 应点名失败的 scope %q", err.Error(), tc.failScope)
			}
			if !qq.CodeIs(err, qq.ErrContentBlocked) {
				t.Fatalf("平台 code 不应在部分失败中丢失：%v", err)
			}

			// 正向隔离：成功的那半边必须原样返回，且只返回成功的那个。
			if len(results) != 1 {
				t.Fatalf("部分失败时 results = %#v, want 仅 %q", results, tc.wantScope)
			}
			res := results[0]
			if res.Scope != tc.wantScope {
				t.Fatalf("result.scope = %q, want %q", res.Scope, tc.wantScope)
			}
			if res.Action != PanelActionCreated || res.PanelID == "" {
				t.Fatalf("成功 scope 的结果被破坏：%#v", res)
			}

			// 平台侧的事实：成功的那个 panel 真的被创建了，失败的没有。
			platform.mu.Lock()
			defer platform.mu.Unlock()
			if got := len(platform.byScope[tc.wantScope]); got != 1 {
				t.Fatalf("%s 面板应被创建一次，实际 %d", tc.wantScope, got)
			}
			if got := len(platform.byScope[tc.failScope]); got != 0 {
				t.Fatalf("%s 面板不应被创建，实际 %d", tc.failScope, got)
			}
			if len(platform.created) != 1 || platform.created[0].Scope != tc.wantScope {
				t.Fatalf("create calls = %#v, want 仅 %q", platform.created, tc.wantScope)
			}
		})
	}
}

// TestManualSyncRunsWhenAutoSyncDisabled pins the escape hatch from D-043
// 「决定 4」: command_panel_enabled=false skips the *automatic* sync, but the
// admin-triggered SyncCommandPanel must still reach the platform. A future
// refactor that moves the flag check into SyncCommandPanel would silently brick
// the manual button, and this test is what catches it.
func TestManualSyncRunsWhenAutoSyncDisabled(t *testing.T) {
	mgr, err := settings.New(context.Background(), nil, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if _, _, err := mgr.Apply(context.Background(), map[string]json.RawMessage{
		"command_panel_enabled": json.RawMessage("false"),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if mgr.Get().CommandPanelEnabled {
		t.Fatal("夹具没关掉 command_panel_enabled")
	}

	platform := newFakePanelPlatform()
	d := newCorePanelDispatcher(t, platform)
	d.settings = mgr

	// 自动同步被跳过（既有用例已钉），这里只钉手动路径照常执行。
	results, err := d.SyncCommandPanel(context.Background())
	if err != nil {
		t.Fatalf("手动同步在开关关闭时仍必须执行：%v", err)
	}
	if len(results) != 2 {
		t.Fatalf("手动同步应覆盖两个 scope，实际 %#v", results)
	}
	for _, res := range results {
		if res.Action != PanelActionCreated || res.PanelID == "" {
			t.Fatalf("手动同步结果 = %#v", res)
		}
	}

	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.created) != 2 {
		t.Fatalf("开关关闭不影响手动同步：create calls = %#v", platform.created)
	}
}
