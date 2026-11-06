package admin

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A wrong token is cheap to try; after five failures the same peer must get
// 429 even if it finally supplies the correct token.
func TestAdminLoginRateLimitPerIP(t *testing.T) {
	h := newAdminTestHarness(t)

	for i := 0; i < loginFailureLimit; i++ {
		h.wantError(t, h.post(t, "/api/auth/login", map[string]any{"token": "wrong"}),
			http.StatusUnauthorized, "invalid_token")
	}
	blocked := h.post(t, "/api/auth/login", map[string]any{"token": adminTestToken})
	h.wantError(t, blocked, http.StatusTooManyRequests, "too_many_attempts")
	if blocked.header.Get("Retry-After") == "" {
		t.Fatal("429 response must tell the client when to retry")
	}
}

func TestLoginLimiterWindowAndReset(t *testing.T) {
	limiter := newLoginLimiter()
	now := time.Unix(1_700_000_000, 0)
	limiter.now = func() time.Time { return now }

	for i := 0; i < loginFailureLimit; i++ {
		if !limiter.allow("203.0.113.9") {
			t.Fatalf("attempt %d must be allowed", i+1)
		}
		limiter.fail("203.0.113.9")
	}
	if limiter.allow("203.0.113.9") {
		t.Fatal("peer must be blocked after the failure limit")
	}
	if !limiter.allow("203.0.113.10") {
		t.Fatal("another peer must not inherit the block")
	}

	// A successful login (or the window expiring) clears the block.
	now = now.Add(loginFailureWindow + time.Second)
	if !limiter.allow("203.0.113.9") {
		t.Fatal("window expiry must unblock the peer")
	}
	limiter.fail("203.0.113.9")
	limiter.reset("203.0.113.9")
	if !limiter.allow("203.0.113.9") {
		t.Fatal("reset must clear the failure state")
	}
}

// The cookie must be Secure when a TLS-terminating reverse proxy says the
// original request was HTTPS, while a plain HTTP request stays usable.
func TestAdminLoginCookieSecureBehindProxy(t *testing.T) {
	h := newAdminTestHarness(t)

	withProto := func(req *http.Request) {
		req.Header.Set("X-Forwarded-Proto", "https")
	}
	login := h.request(t, http.MethodPost, "/api/auth/login",
		map[string]any{"token": adminTestToken}, withProto)
	h.wantOK(t, login)
	setCookie := strings.Join(login.header.Values("Set-Cookie"), " ")
	if !strings.Contains(setCookie, "Secure") {
		t.Fatalf("proxy HTTPS login must set Secure: %q", setCookie)
	}

	plain := h.post(t, "/api/auth/login", map[string]any{"token": adminTestToken})
	h.wantOK(t, plain)
	if strings.Contains(strings.Join(plain.header.Values("Set-Cookie"), " "), "Secure") {
		t.Fatal("plain HTTP login must keep the cookie usable (no Secure)")
	}
}
