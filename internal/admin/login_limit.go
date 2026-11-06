package admin

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Login throttling keeps an attacker from guessing the admin token through the
// HTTP API. It is intentionally in-memory and per direct peer address: a
// restart clears it, and when a reverse proxy is in front every request looks
// like it comes from the proxy (configure the proxy to rate-limit as well).
const (
	loginFailureLimit  = 5
	loginFailureWindow = 5 * time.Minute
	// Bound the map so a scan from many source addresses cannot grow it
	// without limit.
	loginFailureMaxEntries = 4096
)

type loginFailureState struct {
	count int
	first time.Time
}

// loginLimiter implements a fixed window of failed logins per IP: up to
// loginFailureLimit failures in loginFailureWindow, after which every attempt
// (including the correct token) is rejected until the window expires.
type loginLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	failures map[string]loginFailureState
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		now:      time.Now,
		failures: make(map[string]loginFailureState),
	}
}

// allow reports whether ip may attempt a login right now.
func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	state, ok := l.failures[ip]
	if !ok {
		return true
	}
	if now.Sub(state.first) >= loginFailureWindow {
		delete(l.failures, ip)
		return true
	}
	return state.count < loginFailureLimit
}

// fail records one rejected login for ip.
func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	state, ok := l.failures[ip]
	if !ok || now.Sub(state.first) >= loginFailureWindow {
		l.failures[ip] = loginFailureState{count: 1, first: now}
		return
	}
	state.count++
	l.failures[ip] = state
}

// reset clears the failure window for ip after a successful login.
func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

func (l *loginLimiter) pruneLocked(now time.Time) {
	if len(l.failures) < loginFailureMaxEntries {
		return
	}
	for ip, state := range l.failures {
		if now.Sub(state.first) >= loginFailureWindow {
			delete(l.failures, ip)
		}
	}
}

// loginClientIP is the peer address without its port. X-Forwarded-For is
// deliberately ignored: trusting a client-supplied header here would let an
// attacker pick a fresh bucket per request.
func loginClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

// requestIsHTTPS reports whether the browser reached us over HTTPS, either
// directly or through a TLS-terminating reverse proxy. The proxy must send
// X-Forwarded-Proto; this trusts that header, so only deploy behind a proxy
// that overwrites it.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}
