package qq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TokenStatus is the admin-visible state of the app access token.
type TokenStatus struct {
	Valid            bool   `json:"valid"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
	Refreshes        int64  `json:"refreshes"`
	LastError        string `json:"last_error"`
}

// TokenManager owns the QQ app access token.
//
// The platform returns HTTP 200 even for failures, so success is decided by
// the presence of access_token (and by the business `code` field).
type TokenManager struct {
	appID  string
	secret string
	url    string
	http   *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
	refreshes int64
	lastErr   string
}

// NewTokenManager builds a token manager for the given API base URL.
func NewTokenManager(appID, secret, apiBase string, hc *http.Client) *TokenManager {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &TokenManager{
		appID:  appID,
		secret: secret,
		url:    strings.TrimRight(apiBase, "/") + "/app/getAppAccessToken",
		http:   hc,
	}
}

// Token returns a usable token, refreshing it when it is missing or about to
// expire. Pass force to refresh unconditionally.
func (t *TokenManager) Token(ctx context.Context, force bool) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !force && t.token != "" && time.Now().Before(t.expiresAt.Add(-60*time.Second)) {
		return t.token, nil
	}
	token, ttl, err := t.fetch(ctx)
	if err != nil {
		t.lastErr = err.Error()
		return "", err
	}
	t.token = token
	t.expiresAt = time.Now().Add(time.Duration(ttl) * time.Second)
	t.refreshes++
	t.lastErr = ""
	return t.token, nil
}

// Status reports the current token state for the admin panel.
func (t *TokenManager) Status() TokenStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := TokenStatus{
		Valid:     t.token != "",
		Refreshes: t.refreshes,
		LastError: t.lastErr,
	}
	if !t.expiresAt.IsZero() {
		remaining := int(time.Until(t.expiresAt).Seconds())
		if remaining < 0 {
			remaining = 0
			st.Valid = false
		}
		st.ExpiresInSeconds = remaining
	}
	return st
}

// Cached returns the current token without refreshing, if any.
func (t *TokenManager) Cached() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.token
}

func (t *TokenManager) fetch(ctx context.Context) (string, int, error) {
	payload, err := json.Marshal(map[string]string{
		"appId":        t.appID,
		"clientSecret": t.secret,
	})
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(payload))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("get access token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, fmt.Errorf("get access token: read body: %w", err)
	}

	var parsed struct {
		AccessToken string  `json:"access_token"`
		ExpiresIn   flexInt `json:"expires_in"`
		Code        int     `json:"code"`
		Message     string  `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", 0, fmt.Errorf("get access token: HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	if parsed.AccessToken == "" {
		return "", 0, fmt.Errorf("get access token failed: code=%d message=%s", parsed.Code, parsed.Message)
	}
	ttl := int(parsed.ExpiresIn)
	if ttl <= 0 {
		ttl = 7200
	}
	return parsed.AccessToken, ttl, nil
}

// flexInt accepts both `7200` and `"7200"`, which the platform has used.
type flexInt int

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(bytes.TrimSpace(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		v, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return err
		}
		n = int(v)
	}
	*f = flexInt(n)
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
