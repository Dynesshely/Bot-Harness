package admin

import (
	"net/http"
	"testing"
)

func TestAdminSecurityHeadersAndCrossSiteGuard(t *testing.T) {
	h := newAdminTestHarness(t)

	health := h.request(t, "GET", "/api/health", nil, func(r *http.Request) {
		r.Header.Del("Authorization")
	})
	for _, header := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy", "Cache-Control"} {
		if health.header.Get(header) == "" {
			t.Fatalf("missing security header %s", header)
		}
	}

	cross := h.request(t, "POST", "/api/auth/login", map[string]string{"token": adminTestToken}, func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example")
	})
	if cross.status != 403 {
		t.Fatalf("cross-site login status = %d, want 403", cross.status)
	}

	same := h.request(t, "POST", "/api/auth/login", map[string]string{"token": adminTestToken}, func(r *http.Request) {
		r.Header.Set("Origin", h.ts.URL)
	})
	if same.status == 403 {
		t.Fatalf("same-origin login must not be blocked by CSRF gate")
	}
}
