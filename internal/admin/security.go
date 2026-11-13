package admin

import (
	"net/http"
	"net/url"
	"strings"
)

// securityHeaders is applied to every admin response. The SPA is same-origin
// and does not need inline scripts; inline styles are required by Vue/Tailwind
// runtime style bindings.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self'; connect-src 'self'; font-src 'self' data:; "+
				"frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		if isStateChanging(r.Method) && !sameOriginRequest(r) {
			writeError(w, http.StatusForbidden, "cross_site_request", "拒绝跨站写请求")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isStateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// sameOriginRequest rejects browser-originated cross-site writes. Requests from
// curl/tests without Origin or Referer are allowed: the bearer token is still
// required and this gate exists to stop a browser holding the cookie.
func sameOriginRequest(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		origin = strings.TrimSpace(r.Header.Get("Referer"))
	}
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := r.Host
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		if i := strings.IndexByte(forwarded, ','); i >= 0 {
			forwarded = forwarded[:i]
		}
		if forwarded = strings.TrimSpace(forwarded); forwarded != "" {
			host = forwarded
		}
	}
	return strings.EqualFold(u.Host, host)
}
