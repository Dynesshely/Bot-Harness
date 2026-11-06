package admin

// Authentication contract: cookie session, bearer token, logout, and the fact
// that rotating the admin token invalidates every session that was already open.

import (
	"net/http"
	"strings"
	"testing"
)

func adminTestCookieValue(t *testing.T, resp *adminTestResponse) string {
	t.Helper()
	for _, raw := range resp.header.Values("Set-Cookie") {
		if !strings.HasPrefix(raw, sessionCookie+"=") {
			continue
		}
		pair := strings.SplitN(raw, ";", 2)[0]
		_, value, _ := strings.Cut(pair, "=")
		return value
	}
	t.Fatalf("response did not set the %s cookie (headers %v)", sessionCookie, resp.header)
	return ""
}

func adminTestWithCookie(value string) func(*http.Request) {
	return func(req *http.Request) {
		req.Header.Del("Authorization")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
	}
}

func adminTestWithoutAuth() func(*http.Request) {
	return func(req *http.Request) { req.Header.Del("Authorization") }
}

func TestAdminAuthLoginCookieBearerAndLogout(t *testing.T) {
	h := newAdminTestHarness(t)

	// /api/auth/me is public and answers truthfully about the session.
	me := h.request(t, http.MethodGet, "/api/auth/me", nil, adminTestWithoutAuth())
	data := h.wantOK(t, me)
	if data["authenticated"] != false || data["auth_mode"] != "none" {
		t.Fatalf("unauthenticated me = %#v", data)
	}
	if data["username"] != "admin" {
		t.Fatalf("username = %v", data["username"])
	}

	// Wrong token.
	h.wantError(t, h.post(t, "/api/auth/login", map[string]any{"token": "nope"}),
		http.StatusUnauthorized, "invalid_token")
	// Malformed and unknown-field bodies.
	h.wantError(t, h.rawRequest(t, http.MethodPost, "/api/auth/login", `{"token":`),
		http.StatusBadRequest, "invalid_request")
	h.wantError(t, h.post(t, "/api/auth/login", map[string]any{"token": adminTestToken, "extra": 1}),
		http.StatusBadRequest, "invalid_request")

	login := h.post(t, "/api/auth/login", map[string]any{"token": adminTestToken})
	h.wantOK(t, login)
	cookie := adminTestCookieValue(t, login)
	if cookie == "" {
		t.Fatal("session cookie must carry a value")
	}
	setCookie := strings.Join(login.header.Values("Set-Cookie"), " ")
	for _, want := range []string{"HttpOnly", "Path=/", "SameSite=Lax"} {
		adminTestContains(t, setCookie, want)
	}

	// The cookie works on a protected endpoint.
	status := h.request(t, http.MethodGet, "/api/status", nil, adminTestWithCookie(cookie))
	h.wantOK(t, status)
	me = h.request(t, http.MethodGet, "/api/auth/me", nil, adminTestWithCookie(cookie))
	data = h.wantOK(t, me)
	if data["authenticated"] != true || data["auth_mode"] != "token" {
		t.Fatalf("cookie-authenticated me = %#v", data)
	}

	// So does a bearer token (curl / scripts).
	me = h.get(t, "/api/auth/me")
	if data = h.wantOK(t, me); data["authenticated"] != true {
		t.Fatalf("bearer-authenticated me = %#v", data)
	}

	// Logout clears the cookie.
	logout := h.post(t, "/api/auth/logout", nil)
	h.wantOK(t, logout)
	adminTestContains(t, strings.Join(logout.header.Values("Set-Cookie"), " "), sessionCookie+"=;")
	// An empty session cookie is not authenticated.
	me = h.request(t, http.MethodGet, "/api/auth/me", nil, adminTestWithCookie(""))
	if data = h.wantOK(t, me); data["authenticated"] != false {
		t.Fatalf("empty cookie must not authenticate: %#v", data)
	}

	// Every protected route rejects anonymous callers before touching handlers.
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/status"},
		{http.MethodGet, "/api/config"},
		{http.MethodGet, "/api/conversations"},
		{http.MethodPost, "/api/actions/reconnect"},
		{http.MethodDelete, "/api/conversations"},
	} {
		resp := h.request(t, route.method, route.path, nil, adminTestWithoutAuth())
		h.wantError(t, resp, http.StatusUnauthorized, "unauthorized")
	}

	// Unknown API paths are JSON 404s, never a silent fall-through to the SPA.
	h.wantError(t, h.get(t, "/api/definitely-not-a-route"), http.StatusNotFound, "not_found")

	// Everything else is the SPA shell.
	spa := h.get(t, "/some/client/route")
	h.wantStatus(t, spa, http.StatusOK)
	adminTestContains(t, strings.ToLower(string(spa.body)), "html")
}

func TestAdminTokenRotationInvalidatesOpenSessions(t *testing.T) {
	h := newAdminTestHarness(t)

	login := h.post(t, "/api/auth/login", map[string]any{"token": adminTestToken})
	h.wantOK(t, login)
	cookie := adminTestCookieValue(t, login)

	// Rotate the token through the panel.
	put := h.put(t, "/api/config", map[string]any{"admin_token": "rotated-token"})
	h.wantOK(t, put)

	// The old session value was derived from the old token, so both the cookie
	// and the old bearer now fail.
	h.wantError(t, h.request(t, http.MethodGet, "/api/status", nil, adminTestWithCookie(cookie)),
		http.StatusUnauthorized, "unauthorized")
	h.wantError(t, h.get(t, "/api/status"), http.StatusUnauthorized, "unauthorized")

	// New credential works, old one no longer logs in.
	newReq := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer rotated-token")
	}
	h.wantOK(t, h.request(t, http.MethodGet, "/api/status", nil, newReq))
	h.wantError(t, h.post(t, "/api/auth/login", map[string]any{"token": adminTestToken}),
		http.StatusUnauthorized, "invalid_token")
	h.wantOK(t, h.post(t, "/api/auth/login", map[string]any{"token": "rotated-token"}))
}
