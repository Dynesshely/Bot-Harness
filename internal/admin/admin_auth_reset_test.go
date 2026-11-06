package admin

// T15 security regression: an empty admin token must never authenticate, and a
// config reset must not throw the credentials away.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/settings"
)

// 重置只回退「可调配置」，三个只进不出的凭据必须原样保留：
// 尤其是口令 —— 它一旦变空，空 Bearer / 空口令就会被判为已认证。
func TestAdminResetConfigKeepsCredentials(t *testing.T) {
	h := newAdminTestHarness(t)
	login := h.post(t, "/api/auth/login", map[string]any{"token": adminTestToken})
	h.wantOK(t, login)
	cookie := adminTestCookieValue(t, login)

	// 先改几项可调配置，证明 reset 确实回退了它们。
	h.wantOK(t, h.put(t, "/api/config", map[string]any{
		"temperature": 0.5, "model": "rotated-model",
	}))

	data := h.wantOK(t, h.post(t, "/api/actions/reset-config", nil))
	updated, ok := data["settings"].(map[string]any)
	if !ok {
		t.Fatalf("reset-config must return {settings: ...}: %#v", data)
	}
	// 可调项回到内置默认。
	if updated["model"] != settings.Defaults().Model {
		t.Fatalf("model after reset = %v", updated["model"])
	}
	if updated["temperature"] != settings.Defaults().Temperature {
		t.Fatalf("temperature after reset = %v", updated["temperature"])
	}
	// 口令没变：旧的 Bearer 与旧 cookie 都还能用。
	h.wantOK(t, h.get(t, "/api/status"))
	h.wantOK(t, h.request(t, http.MethodGet, "/api/status", nil, adminTestWithCookie(cookie)))

	// GET 的 *_set 仍然为 true，响应体里三个秘密依旧为空。
	got := h.wantOK(t, h.get(t, "/api/config"))
	for _, key := range []string{"openai_api_key_set", "qq_app_secret_set", "admin_token_set"} {
		if got[key] != true {
			t.Fatalf("%s = %v, credentials must survive a reset", key, got[key])
		}
	}
	editable := got["editable"].(map[string]any)
	for _, key := range []string{"openai_api_key", "qq_app_secret", "admin_token"} {
		if v, present := editable[key]; !present || v != "" {
			t.Fatalf("editable.%s leaked after reset: %#v", key, v)
		}
	}
	// 端点不是凭据，重置后应回到未配置并给出警告；口令保留则不该出现「重新生成」。
	warnings := adminTestJoinedWarnings(data)
	adminTestContains(t, warnings, "模型端点")
	if strings.Contains(warnings, "口令") {
		t.Fatalf("token was preserved, so no regeneration warning is expected: %q", warnings)
	}

	// 凭据真的落库了，而不是只在内存里。
	reloaded, err := settings.New(t.Context(), h.store, settings.Defaults())
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	cur := reloaded.Get()
	if cur.AdminToken != adminTestToken {
		t.Fatalf("admin token after reset = %q", cur.AdminToken)
	}
	if cur.OpenAIAPIKey != "test-key" || cur.QQAppSecret != "test-app-secret" {
		t.Fatalf("credentials were dropped by reset: key=%q secret=%q", cur.OpenAIAPIKey, cur.QQAppSecret)
	}

	// 旧口令仍能登录。
	h.wantOK(t, h.post(t, "/api/auth/login", map[string]any{"token": adminTestToken}))
}

// 口令为空时，空 Bearer / 空口令 / 空密钥 HMAC 出来的 cookie 都不能通过。
func TestAdminRejectsEmptyCredentials(t *testing.T) {
	h := newAdminTestHarness(t)
	h.applySettings(t, map[string]any{"admin_token": ""})

	// 直接调用认证函数：HTTP 层会把头值两端的空白规范化，这里要看真正的判定。
	bearer := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	bearer.Header.Set("Authorization", "Bearer ")
	if h.server.authenticated(bearer) {
		t.Fatal("empty bearer must not authenticate when the token is empty")
	}
	spaced := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	spaced.Header.Set("Authorization", "Bearer    ")
	if h.server.authenticated(spaced) {
		t.Fatal("whitespace-only bearer must not authenticate")
	}
	empty := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	empty.Header.Set("Authorization", "Bearer    ")
	if h.server.tokenEqual(strings.TrimPrefix(empty.Header.Get("Authorization"), "Bearer ")) {
		t.Fatal("tokenEqual must reject an empty provided token")
	}

	// 空口令登录：不能发 cookie。
	h.wantError(t, h.post(t, "/api/auth/login", map[string]any{"token": ""}),
		http.StatusUnauthorized, "invalid_token")
	h.wantError(t, h.post(t, "/api/auth/login", map[string]any{}),
		http.StatusUnauthorized, "invalid_token")

	// 用空密钥伪造出的 cookie（deriveSession 的历史实现）。
	mac := hmac.New(sha256.New, []byte(""))
	mac.Write([]byte("bot-harness-admin-session-v1"))
	forged := hex.EncodeToString(mac.Sum(nil))
	if forged == h.server.deriveSession() {
		t.Fatal("deriveSession must not produce a value for an empty token")
	}
	h.wantError(t, h.request(t, http.MethodGet, "/api/status", nil, adminTestWithCookie(forged)),
		http.StatusUnauthorized, "unauthorized")

	// 空 cookie 也不行。
	h.wantError(t, h.request(t, http.MethodGet, "/api/status", nil, adminTestWithCookie("")),
		http.StatusUnauthorized, "unauthorized")

	// 普通请求（带旧的非空 Bearer）同样不能通过。
	h.wantError(t, h.get(t, "/api/status"), http.StatusUnauthorized, "unauthorized")
}

// 极端路径：reset 之前口令就是空的（例如从未生成过）。Reset 必须补一个并写库，
// 响应里要明确告诉人「口令已重新生成」。
func TestAdminResetHandlerRegeneratesAMissingToken(t *testing.T) {
	h := newAdminTestHarness(t)
	h.applySettings(t, map[string]any{"admin_token": ""})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/actions/reset-config", nil)
	h.server.handleResetConfig(rec, req)

	resp := &adminTestResponse{status: rec.Code, body: rec.Body.Bytes()}
	resp.decodeEnvelope()
	if resp.status != http.StatusOK {
		t.Fatalf("reset handler status = %d, body %s", resp.status, string(resp.body))
	}
	data := resp.data(t)
	warnings := adminTestJoinedWarnings(data)
	adminTestContains(t, warnings, "口令")

	reloaded, err := settings.New(t.Context(), h.store, settings.Defaults())
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	token := reloaded.Get().AdminToken
	if token == "" {
		t.Fatal("reset must generate and persist a missing admin token")
	}
	if !strings.HasPrefix(token, "botharness-") {
		t.Fatalf("generated token looks wrong: %q", token)
	}
	// 新口令立即可用。
	body, _ := json.Marshal(map[string]any{"token": token})
	login := h.rawRequest(t, http.MethodPost, "/api/auth/login", string(body))
	h.wantOK(t, login)
}
