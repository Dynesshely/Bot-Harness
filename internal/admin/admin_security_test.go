package admin

// Security / contract fixes (T9): write-only secrets must stay write-only on
// every response, and paging responses must echo the values that were actually
// applied instead of the raw request.

import (
	"net/http"
	"testing"
)

func TestAdminPutConfigDoesNotEchoSecrets(t *testing.T) {
	h := newAdminTestHarness(t)

	// The harness seeds qq_app_secret, admin_token and openai_api_key.
	bodies := []map[string]any{
		{"temperature": 0.42},
		{"model": "test-model"},
		{"qq_app_secret": "rotated-qq-secret"},
	}
	for i, body := range bodies {
		data := h.wantOK(t, h.put(t, "/api/config", body))
		updated, ok := data["settings"].(map[string]any)
		if !ok {
			t.Fatalf("body %d: missing settings object: %#v", i, data)
		}
		for _, key := range []string{"openai_api_key", "qq_app_secret", "admin_token"} {
			if v, present := updated[key]; !present || v != "" {
				t.Fatalf("body %d: PUT /api/config echoed secret %q = %#v", i, key, v)
			}
		}
		// A patch that only touches unrelated fields must not clear them either.
		got := h.wantOK(t, h.get(t, "/api/config"))
		for _, key := range []string{"openai_api_key_set", "qq_app_secret_set", "admin_token_set"} {
			if got[key] != true {
				t.Fatalf("body %d: %s = %v, the secret must still exist", i, key, got[key])
			}
		}
	}
}

func TestAdminResetConfigMasksSecrets(t *testing.T) {
	h := newAdminTestHarness(t)

	// Reset clears the database overrides; the response must still not hand the
	// current (pre-reset) secrets back to the caller.
	data := h.wantOK(t, h.post(t, "/api/actions/reset-config", nil))
	updated, ok := data["settings"].(map[string]any)
	if !ok {
		t.Fatalf("reset-config must return {settings: ...}: %#v", data)
	}
	for _, key := range []string{"openai_api_key", "qq_app_secret", "admin_token"} {
		if v, present := updated[key]; !present || v != "" {
			t.Fatalf("reset-config echoed secret %q = %#v", key, v)
		}
	}
	// The token is preserved, and a wrong one is still rejected (an empty one is
	// covered by TestAdminRejectsEmptyCredentials).
	login := h.rawRequest(t, http.MethodPost, "/api/auth/login", `{"token":"garbage"}`)
	if login.status != http.StatusUnauthorized {
		t.Fatalf("a wrong token must not log in after reset, status = %d", login.status)
	}
}

func TestAdminPageSizeEchoesEffectiveValue(t *testing.T) {
	h := newAdminTestHarness(t)
	seedAdminFixture(t, h)

	conversations := []struct {
		query string
		want  float64
	}{
		{"", 20},
		{"?page_size=999", 20},
		{"?page_size=201", 20},
		{"?page_size=0", 20},
		{"?page_size=-5", 20},
		{"?page_size=abc", 20},
		{"?page_size=1", 1},
		{"?page_size=200", 200},
	}
	for _, tc := range conversations {
		data := h.wantOK(t, h.get(t, "/api/conversations"+tc.query))
		if adminTestNumber(t, data, "page_size") != tc.want {
			t.Fatalf("conversations%s echoed page_size = %v, want %v", tc.query, data["page_size"], tc.want)
		}
		if items := adminTestItems(t, data); float64(len(items)) > tc.want {
			t.Fatalf("conversations%s returned %d items, more than the echoed page_size %v", tc.query, len(items), tc.want)
		}
	}

	messages := []struct {
		query string
		want  float64
	}{
		{"", 50},
		{"?page_size=999", 50},
		{"?page_size=0", 50},
		{"?page_size=-1", 50},
		{"?page_size=1", 1},
	}
	for _, tc := range messages {
		data := h.wantOK(t, h.get(t, "/api/messages"+tc.query))
		if adminTestNumber(t, data, "page_size") != tc.want {
			t.Fatalf("messages%s echoed page_size = %v, want %v", tc.query, data["page_size"], tc.want)
		}
		if items := adminTestItems(t, data); float64(len(items)) > tc.want {
			t.Fatalf("messages%s returned %d items, more than the echoed page_size %v", tc.query, len(items), tc.want)
		}
	}
}
