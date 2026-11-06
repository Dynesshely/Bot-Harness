package settings

// T15: Reset is "drop the tunable overrides", not "drop the credentials".
// Losing the admin token used to leave the panel authenticating an empty
// password; losing the model key used to silently break every model call.

import (
	"context"
	"strings"
	"testing"
)

func TestResetPreservesWriteOnlySecrets(t *testing.T) {
	m, st := newManager(t, false)
	ctx := context.Background()
	if _, _, err := m.Apply(ctx, mustPatch(t, `{
		"admin_token":"rotated-admin-token",
		"openai_api_key":"sk-live-key",
		"qq_app_secret":"qq-secret-value",
		"model":"other-model"
	}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reset, err := m.Reset(ctx)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if reset.AdminToken != "rotated-admin-token" {
		t.Fatalf("reset dropped the admin token: %q", reset.AdminToken)
	}
	if reset.OpenAIAPIKey != "sk-live-key" || reset.QQAppSecret != "qq-secret-value" {
		t.Fatalf("reset dropped credentials: key=%q secret=%q", reset.OpenAIAPIKey, reset.QQAppSecret)
	}
	// Tunables still go back to their built-in defaults.
	if reset.Model != Defaults().Model {
		t.Fatalf("model after reset = %q", reset.Model)
	}

	// The preserved credentials are persisted, not just kept in memory.
	reloaded, err := New(ctx, st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.Get()
	if got.AdminToken != "rotated-admin-token" || got.OpenAIAPIKey != "sk-live-key" || got.QQAppSecret != "qq-secret-value" {
		t.Fatalf("credentials did not survive the reload: %#v", got)
	}
	overridden := reloaded.OverriddenKeys()
	for _, key := range []string{"admin_token", "openai_api_key", "qq_app_secret"} {
		found := false
		for _, got := range overridden {
			if got == key {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s should still be a persisted override, got %v", key, overridden)
		}
	}
}

func TestResetGeneratesAMissingAdminToken(t *testing.T) {
	m, st := newManager(t, false)
	ctx := context.Background()

	// No token has ever been generated for this database.
	reset, err := m.Reset(ctx)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if !strings.HasPrefix(reset.AdminToken, "botharness-") || len(reset.AdminToken) < len("botharness-")+16 {
		t.Fatalf("generated token looks wrong: %q", reset.AdminToken)
	}
	if len(m.OverriddenKeys()) != 1 || m.OverriddenKeys()[0] != "admin_token" {
		t.Fatalf("only the generated token should be persisted, got %v", m.OverriddenKeys())
	}
	reloaded, err := New(ctx, st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Get().AdminToken != reset.AdminToken {
		t.Fatalf("generated token was not persisted: got %q want %q",
			reloaded.Get().AdminToken, reset.AdminToken)
	}
}

// Reset is deterministic about credentials: two resets in a row keep the same
// token instead of rotating it on every click.
func TestResetDoesNotRotateAPreservedToken(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()
	if _, _, err := m.Apply(ctx, mustPatch(t, `{"admin_token":"stable-token"}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	first, err := m.Reset(ctx)
	if err != nil {
		t.Fatalf("reset 1: %v", err)
	}
	second, err := m.Reset(ctx)
	if err != nil {
		t.Fatalf("reset 2: %v", err)
	}
	if first.AdminToken != "stable-token" || second.AdminToken != "stable-token" {
		t.Fatalf("token rotated: %q -> %q", first.AdminToken, second.AdminToken)
	}
}
