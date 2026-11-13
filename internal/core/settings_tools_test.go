package core

import (
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/settings"
)

func TestRenderSettingsNeverLeaksSecrets(t *testing.T) {
	s := settings.Defaults()
	s.OpenAIAPIKey = "sk-test-secret"
	s.QQAppSecret = "qq-app-secret-value"
	s.AdminToken = "admin-token-value"
	out := renderSettings(s)
	for _, secret := range []string{"sk-test-secret", "qq-app-secret-value", "admin-token-value"} {
		if strings.Contains(out, secret) {
			t.Fatalf("renderSettings leaked %q: %s", secret, out)
		}
	}
}

func TestDescribeDiffSkipsSecrets(t *testing.T) {
	before := settings.Defaults()
	after := before
	after.AdminToken = "new-admin-token"
	after.QQAppSecret = "new-qq-secret"
	after.OpenAIAPIKey = "new-openai-key"
	diff := describeDiff(before, after)
	for _, secret := range []string{"new-admin-token", "new-qq-secret", "new-openai-key"} {
		if strings.Contains(diff, secret) {
			t.Fatalf("describeDiff leaked %q: %s", secret, diff)
		}
	}
}

func TestSettingsWriteDeniesCredentials(t *testing.T) {
	for _, key := range []string{"openai_api_key", "qq_app_secret", "admin_token", "admin_openids"} {
		if _, denied := settingsDeniedKeys[key]; !denied {
			t.Fatalf("%s must be denied through chat settings_write", key)
		}
	}
}
