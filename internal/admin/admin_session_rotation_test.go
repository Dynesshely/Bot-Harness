package admin

// T18: the login cookie must be derived from the token that is current *now*,
// not from whatever the token was when the process started.

import (
	"net/http"
	"testing"
)

func TestAdminLoginCookieFollowsTokenRotation(t *testing.T) {
	h := newAdminTestHarness(t)

	first := h.post(t, "/api/auth/login", map[string]any{"token": adminTestToken})
	h.wantOK(t, first)
	oldCookie := adminTestCookieValue(t, first)
	h.wantOK(t, h.request(t, http.MethodGet, "/api/status", nil, adminTestWithCookie(oldCookie)))

	// Rotate the token through the panel.
	h.wantOK(t, h.put(t, "/api/config", map[string]any{"admin_token": "rotated-token"}))

	// The old credential and the old cookie are gone.
	h.wantError(t, h.get(t, "/api/status"), http.StatusUnauthorized, "unauthorized")
	h.wantError(t, h.request(t, http.MethodGet, "/api/status", nil, adminTestWithCookie(oldCookie)),
		http.StatusUnauthorized, "unauthorized")
	h.wantError(t, h.post(t, "/api/auth/login", map[string]any{"token": adminTestToken}),
		http.StatusUnauthorized, "invalid_token")

	// Logging in with the new token must hand out a cookie that actually works.
	relogin := h.post(t, "/api/auth/login", map[string]any{"token": "rotated-token"})
	h.wantOK(t, relogin)
	newCookie := adminTestCookieValue(t, relogin)
	if newCookie == "" || newCookie == oldCookie {
		t.Fatalf("new session cookie = %q (old = %q)", newCookie, oldCookie)
	}
	h.wantOK(t, h.request(t, http.MethodGet, "/api/status", nil, adminTestWithCookie(newCookie)))
	// The bearer path keeps working too.
	h.wantOK(t, h.request(t, http.MethodGet, "/api/status", nil, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer rotated-token")
	}))
}

func TestAdminEmptyTokenLoginSetsNoCookie(t *testing.T) {
	h := newAdminTestHarness(t)
	h.applySettings(t, map[string]any{"admin_token": ""})

	for _, body := range []map[string]any{
		{"token": ""},
		{"token": "   "},
		{"token": "anything"},
		{},
	} {
		resp := h.post(t, "/api/auth/login", body)
		h.wantError(t, resp, http.StatusUnauthorized, "invalid_token")
		if cookies := resp.header.Values("Set-Cookie"); len(cookies) != 0 {
			t.Fatalf("login with an empty configured token must not set a cookie, got %v", cookies)
		}
	}
}
