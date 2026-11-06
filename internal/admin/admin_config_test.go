package admin

// Configuration contract: the envelope shape the panel depends on, secrets that
// never come back, validation errors, and the platform-honest policy warnings.

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// dsnPassword extracts the password from a URL-shaped PostgreSQL DSN, so the
// contract tests can assert it never appears in a response. An unparseable DSN
// or one without a password yields "", which callers read as "nothing to look
// for" rather than "the empty string must not occur anywhere".
func dsnPassword(dsn string) string {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || u.User == nil {
		return ""
	}
	pw, _ := u.User.Password()
	return pw
}

func TestAdminGetConfigMasksSecretsAndShape(t *testing.T) {
	h := newAdminTestHarness(t)
	data := h.wantOK(t, h.get(t, "/api/config"))

	source, ok := data["source"].(map[string]any)
	if !ok {
		t.Fatalf("missing source block: %#v", data)
	}
	if source["file"] != ".env" {
		t.Fatalf("source.file = %v", source["file"])
	}
	if keys, _ := source["overridden_keys"].([]any); len(keys) == 0 {
		t.Fatalf("overridden_keys should list what the database overrides: %#v", source["overridden_keys"])
	}

	readonly, ok := data["readonly"].(map[string]any)
	if !ok {
		t.Fatalf("missing readonly block: %#v", data)
	}
	// The readonly block is engine-agnostic: {driver, storage, version}. The
	// bootstrap DB_PATH is deliberately *not* part of it (it is a SQLite-only
	// value and would be a lie on PostgreSQL).
	if readonly["version"] != "test" {
		t.Fatalf("readonly.version = %v", readonly["version"])
	}
	if readonly["driver"] != store.DriverSQLite {
		t.Fatalf("readonly.driver = %v, want %q", readonly["driver"], store.DriverSQLite)
	}
	if storage, _ := readonly["storage"].(string); storage == "" {
		t.Fatalf("readonly.storage must name the database, got %#v", readonly["storage"])
	}
	if _, present := readonly["db_path"]; present {
		t.Fatalf("readonly.db_path was removed from the contract: %#v", readonly)
	}

	// Secrets are write-only: the flags say whether one exists, the value is
	// always empty.
	for _, key := range []string{"qq_app_secret_set", "admin_token_set", "openai_api_key_set"} {
		if data[key] != true {
			t.Fatalf("%s = %v, want true after the harness seeded credentials", key, data[key])
		}
	}
	if data["openai_api_key_source"] != "db" {
		t.Fatalf("openai_api_key_source = %v", data["openai_api_key_source"])
	}

	editable, ok := data["editable"].(map[string]any)
	if !ok {
		t.Fatalf("missing editable block: %#v", data)
	}
	for _, key := range []string{"openai_api_key", "qq_app_secret", "admin_token"} {
		if v, present := editable[key]; !present || v != "" {
			t.Fatalf("editable.%s must be present and empty, got %#v", key, v)
		}
	}
	for _, key := range []string{
		"system_prompt", "model", "temperature", "max_tokens", "request_timeout_seconds",
		"context_max_turns", "context_max_tokens", "history_ttl_hours", "reply_max_chars",
		"reply_max_segments", "humanize_level", "memory_enabled", "web_enabled",
		"exec_enabled", "markdown_enabled", "tasks_enabled", "proactive_level",
		"manual_send_enabled", "quote_reply", "group_overrides", "admin_openids",
	} {
		if _, present := editable[key]; !present {
			t.Fatalf("editable is missing flat field %q (the panel would render it empty)", key)
		}
	}
	if editable["model"] != "test-model" {
		t.Fatalf("editable.model = %v", editable["model"])
	}
}

// TestAdminConfigReadonlyOnPostgres pins the PostgreSQL side of the same two
// contracts: GET /api/status must report the real engine, a maskable target and
// a real database size, and GET /api/config must report driver/storage without
// ever leaking the password or falling back to the bootstrap DB_PATH.
//
// Gated on PG_TEST_DSN: the harness opens that database through the real store
// (creating the schema), so point it at a scratch database, never at a live one.
//
// Re-running this test against the same database is safe: it never seeds a
// conversation, so the size/row assertions do not depend on prior runs.
func TestAdminConfigReadonlyOnPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PG_TEST_DSN"))
	if dsn == "" {
		t.Skip("PG_TEST_DSN not set; skipping the PostgreSQL admin contract")
	}
	password := dsnPassword(dsn)
	h := newAdminTestHarness(t, func(o *adminTestOpts) { o.pgDSN = dsn })

	// --- GET /api/status ----------------------------------------------
	status := h.wantOK(t, h.get(t, "/api/status"))
	storeBlock := status["store"].(map[string]any)
	if storeBlock["driver"] != store.DriverPostgres {
		t.Fatalf("store.driver = %v, want %q", storeBlock["driver"], store.DriverPostgres)
	}
	target, _ := storeBlock["target"].(string)
	if !strings.HasPrefix(target, "postgres://") {
		t.Fatalf("store.target = %q, want the postgres DSN", target)
	}
	if password != "" && strings.Contains(target, password) {
		t.Fatalf("store.target leaks the password: %q", target)
	}
	if storeBlock["path"] != "" {
		t.Fatalf("store.path must be empty on postgres, got %#v", storeBlock["path"])
	}
	if size, _ := storeBlock["size_bytes"].(float64); size <= 0 {
		t.Fatalf("store.size_bytes = %v, want pg_database_size > 0", storeBlock["size_bytes"])
	}

	// --- GET /api/config ----------------------------------------------
	data := h.wantOK(t, h.get(t, "/api/config"))
	readonly, ok := data["readonly"].(map[string]any)
	if !ok {
		t.Fatalf("missing readonly block: %#v", data)
	}
	if readonly["driver"] != store.DriverPostgres {
		t.Fatalf("readonly.driver = %v, want %q", readonly["driver"], store.DriverPostgres)
	}
	storage, _ := readonly["storage"].(string)
	if storage != target {
		t.Fatalf("readonly.storage = %q, want the same masked target as status (%q)", storage, target)
	}
	if password != "" && strings.Contains(storage, password) {
		t.Fatalf("readonly.storage leaks the password: %q", storage)
	}
	if _, present := readonly["db_path"]; present {
		t.Fatalf("readonly.db_path must be gone: %#v", readonly)
	}
	if readonly["version"] != "test" {
		t.Fatalf("readonly.version = %v", readonly["version"])
	}
}

func TestAdminPutConfigEnvelopeContract(t *testing.T) {
	h := newAdminTestHarness(t)

	put := h.put(t, "/api/config", map[string]any{"temperature": 0.33, "model": "test-model-2"})
	data := h.wantOK(t, put)

	// The front end reads data.settings (not data) — the regression this pins.
	updated, ok := data["settings"].(map[string]any)
	if !ok {
		t.Fatalf("PUT /api/config must return {settings: {...}}, keys = %#v", data)
	}
	if updated["temperature"] != 0.33 {
		t.Fatalf("settings.temperature = %v", updated["temperature"])
	}
	if updated["model"] != "test-model-2" {
		t.Fatalf("settings.model = %v", updated["model"])
	}
	for _, key := range []string{
		"system_prompt", "max_tokens", "context_max_turns", "reply_max_chars",
		"humanize_level", "memory_enabled", "web_enabled", "exec_enabled",
		"markdown_enabled", "tasks_enabled", "proactive_level",
	} {
		if _, present := updated[key]; !present {
			t.Fatalf("settings is missing flat field %q (the panel would clear that control)", key)
		}
	}
	if _, present := data["warnings"]; !present {
		t.Fatalf("warnings must always be present: %#v", data)
	}
	restart, ok := data["restart_required"].([]any)
	if !ok || len(restart) != 0 {
		t.Fatalf("restart_required = %#v", data["restart_required"])
	}
	// The API key is never echoed, even when the patch touches other fields.
	if v, present := updated["openai_api_key"]; !present || v != "" {
		t.Fatalf("settings.openai_api_key must be empty, got %#v", v)
	}

	// A partial patch leaves untouched fields alone.
	if updated["max_tokens"].(float64) != float64(settings.Defaults().MaxTokens) {
		t.Fatalf("max_tokens changed by an unrelated patch: %v", updated["max_tokens"])
	}

	// The edit is durable.
	got := h.wantOK(t, h.get(t, "/api/config"))
	editable := got["editable"].(map[string]any)
	if editable["temperature"] != 0.33 || editable["model"] != "test-model-2" {
		t.Fatalf("GET does not reflect the patch: %#v", editable)
	}

	// max_concurrent_llm is applied to the live semaphore immediately.
	h.wantOK(t, h.put(t, "/api/config", map[string]any{"max_concurrent_llm": 2}))
	status := h.wantOK(t, h.get(t, "/api/status"))
	aiBlock := status["ai"].(map[string]any)
	if aiBlock["max_concurrent"] != float64(2) {
		t.Fatalf("ai.max_concurrent = %v, want 2", aiBlock["max_concurrent"])
	}
}

func TestAdminPutConfigRejectsInvalidValues(t *testing.T) {
	h := newAdminTestHarness(t)

	cases := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"empty object", "{}"},
		{"not an object", "[1, 2]"},
		{"broken json", "not-json"},
		{"unknown key", "{\"no_such_setting\": 1}"},
		{"temperature out of range", "{\"temperature\": 9}"},
		{"bad base url", "{\"openai_base_url\": \"ftp://example.invalid/v1\"}"},
		{"unknown humanize level", "{\"humanize_level\": \"wild\"}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.rawRequest(t, http.MethodPut, "/api/config", tc.body)
			h.wantError(t, resp, http.StatusBadRequest, "invalid_request")
		})
	}

	// None of the rejected patches may have been partially applied.
	data := h.wantOK(t, h.get(t, "/api/config"))
	editable := data["editable"].(map[string]any)
	if editable["temperature"] != settings.Defaults().Temperature {
		t.Fatalf("rejected patch leaked through: temperature = %v", editable["temperature"])
	}
}

func TestAdminResetConfigRestoresDefaultsAndWarns(t *testing.T) {
	h := newAdminTestHarness(t)

	h.wantOK(t, h.put(t, "/api/config", map[string]any{"temperature": 0.5}))
	reset := h.post(t, "/api/actions/reset-config", nil)
	data := h.wantOK(t, reset)

	updated, ok := data["settings"].(map[string]any)
	if !ok {
		t.Fatalf("reset-config must return {settings: ...}: %#v", data)
	}
	if updated["temperature"] != settings.Defaults().Temperature {
		t.Fatalf("temperature after reset = %v", updated["temperature"])
	}
	if updated["openai_base_url"] != "" {
		t.Fatalf("openai_base_url after reset = %v", updated["openai_base_url"])
	}
	warnings, _ := data["warnings"].([]any)
	if len(warnings) == 0 {
		t.Fatalf("reset without an endpoint must warn that model calls will fail: %#v", data)
	}
	adminTestContains(t, warnings[0].(string), "未配置")
}

func TestAdminPoliciesAndPlatformWarnings(t *testing.T) {
	h := newAdminTestHarness(t)
	h.seedConversation(t, store.Conversation{
		ID: "group:" + adminTestGroup + ":U1", Scene: store.SceneGroupMember,
		GroupOpenID: adminTestGroup, MemberOpenID: "U1", MemberName: "小明",
	})
	if _, err := h.store.SetGroupName(t.Context(), adminTestGroup, "测试群A"); err != nil {
		t.Fatalf("set group name: %v", err)
	}

	// Before any platform probe the panel says it does not know yet.
	policies := h.wantOK(t, h.get(t, "/api/policies"))
	if policies["trigger_mode"] != settings.TriggerAtOnly || policies["context_mode"] != settings.ContextPerMember {
		t.Fatalf("policy defaults = %#v", policies)
	}
	requested, ok := policies["requested"].(map[string]any)
	if !ok || requested["trigger_mode"] != settings.TriggerAtOnly {
		t.Fatalf("requested block = %#v", policies["requested"])
	}
	adminTestContains(t, adminTestJoinedWarnings(policies), "尚未读取到任何群")

	// Probe one limited and one full-access group, then the warnings become
	// concrete and name the groups.
	h.wantOK(t, h.get(t, "/api/groups/"+adminTestGroupNoPr+"/state"))
	h.wantOK(t, h.get(t, "/api/groups/"+adminTestGroup+"/state"))

	policies = h.wantOK(t, h.get(t, "/api/policies"))
	warnings := adminTestJoinedWarnings(policies)
	adminTestContains(t, warnings, "已开启「接收所有消息」")
	platform := policies["platform"].(map[string]any)
	if platform["known_groups"] != float64(2) {
		t.Fatalf("known_groups = %v", platform["known_groups"])
	}
	fullGroups, _ := platform["full_access_groups"].([]any)
	if len(fullGroups) != 1 {
		t.Fatalf("full_access_groups = %#v", platform["full_access_groups"])
	}
	entry := fullGroups[0].(map[string]any)
	if entry["group_name"] != "测试群A" || entry["recv_msg_setting"] != "all" {
		t.Fatalf("platform entry = %#v", entry)
	}

	// E2 + a limited group: explain, do not block.
	put := h.put(t, "/api/policies", map[string]any{"trigger_mode": settings.TriggerAllMessages})
	policies = h.wantOK(t, put)
	if policies["trigger_mode"] != settings.TriggerAllMessages {
		t.Fatalf("trigger_mode = %v", policies["trigger_mode"])
	}
	warnings = adminTestJoinedWarnings(policies)
	adminTestContains(t, warnings, "仅 @机器人")
	adminTestContains(t, warnings, "接收所有消息")

	// include_unaddressed + per-member groups: the panel names the groups whose
	// effective context mode cannot follow the room.
	policies = h.wantOK(t, h.put(t, "/api/policies", map[string]any{
		"context_scope": settings.ContextIncludeUnaddressed,
	}))
	warnings = adminTestJoinedWarnings(policies)
	adminTestContains(t, warnings, "每人独立")
	adminTestContains(t, warnings, "测试群A")

	// Group overrides round-trip through the same document.
	policies = h.wantOK(t, h.put(t, "/api/policies", map[string]any{
		"group_overrides": []map[string]any{{
			"group_openid":         adminTestGroupNoPr,
			"trigger_mode":         settings.TriggerAtOnly,
			"quote_reply":          false,
			"rate_limit_group_qpm": 30,
		}},
	}))
	overrides, _ := policies["group_overrides"].([]any)
	if len(overrides) != 1 {
		t.Fatalf("group_overrides = %#v", policies["group_overrides"])
	}
	override := overrides[0].(map[string]any)
	if override["group_openid"] != adminTestGroupNoPr || override["quote_reply"] != false {
		t.Fatalf("override = %#v", override)
	}

	// Invalid policy values and malformed bodies are rejected.
	h.wantError(t, h.put(t, "/api/policies", map[string]any{"trigger_mode": "sometimes"}),
		http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.rawRequest(t, http.MethodPut, "/api/policies", ""),
		http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.rawRequest(t, http.MethodPut, "/api/policies", "[1]"),
		http.StatusBadRequest, "invalid_request")
}

func adminTestJoinedWarnings(policies map[string]any) string {
	raw, _ := policies["warnings"].([]any)
	out := ""
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out += s + "\n"
		}
	}
	return out
}

func TestAdminHealthTracksCredentialsAndToken(t *testing.T) {
	h := newAdminTestHarness(t)

	// Credentials exist but no access token has been fetched yet.
	resp := h.get(t, "/api/health")
	h.wantStatus(t, resp, http.StatusServiceUnavailable)
	adminTestContains(t, string(resp.body), "no valid access token")

	// Reloading the token makes the health check pass.
	reload := h.wantOK(t, h.post(t, "/api/actions/reload-token", nil))
	if reload["token_valid"] != true {
		t.Fatalf("token_valid = %v", reload["token_valid"])
	}
	resp = h.get(t, "/api/health")
	h.wantStatus(t, resp, http.StatusOK)
	adminTestContains(t, string(resp.body), "ok")

	// Without credentials the health check explains what is missing.
	h.wantOK(t, h.post(t, "/api/actions/reset-config", nil))
	resp = h.get(t, "/api/health")
	h.wantStatus(t, resp, http.StatusServiceUnavailable)
	adminTestContains(t, string(resp.body), "missing credentials")
}
