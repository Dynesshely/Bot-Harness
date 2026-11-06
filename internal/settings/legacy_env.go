package settings

import (
	"sort"
	"strings"
)

// retiredEnvKeys lists environment variables that earlier versions read but that
// the process ignores completely now: the corresponding knobs live in the
// database (D-033 / D-036). Keeping the list here — next to legacyEnvKeys — means
// the startup diagnostic can stay a pure function and be tested without a
// process environment.
//
// Deliberately absent:
//   - DB_PATH / ENV_FILE: still the only bootstrap values.
//   - HTTP_PROXY / HTTPS_PROXY / NO_PROXY: still used by net/http and compose.
//   - ADMIN_BIND: a docker-compose variable (host port binding), not read by Go.
var retiredEnvKeys = []string{
	"ALLOW_DEGRADED",
	"CONTEXT_MAX_TOKENS",
	"CONTEXT_MAX_TURNS",
	"CONTEXT_SCOPE",
	"EARLY_REPLY_DELAY_SECONDS",
	"EARLY_REPLY_ENABLED",
	"EARLY_REPLY_RECALL",
	"EARLY_REPLY_TEXT",
	"GROUP_COOLDOWN_SECONDS",
	"HISTORY_TTL_HOURS",
	"MAX_CONCURRENT_LLM",
	"OPENAI_API_KEY",
	"OPENAI_BASE_URL",
	"OPENAI_MAX_TOKENS",
	"OPENAI_MODEL",
	"OPENAI_TEMPERATURE",
	"OPENAI_TIMEOUT_SECONDS",
	"PLATFORM_STATE_REFRESH_SECONDS",
	"QQ_API_BASE",
	"QQ_FULL_GROUP_MODE",
	"RATE_LIMIT_GROUP_QPM",
	"RATE_LIMIT_USER_QPM",
	"REPLY_FOOTER_ENABLED",
	"REPLY_MAX_CHARS",
	"REPLY_MAX_SEGMENTS",
	"REPLY_TO_BOTS",
	"VISION_ENABLED",
	"VISION_MAX_IMAGES",
}

// LegacyEnvReport is what DiagnoseLegacyEnv found in the process environment.
//
// The two lists are split because the fixes differ: retired keys can simply be
// deleted, while seeded keys were imported once and are now shadowed by the
// database (so editing them is a silent no-op, not a reconfiguration).
type LegacyEnvReport struct {
	// Retired are old .env keys nothing reads any more.
	Retired []string
	// Seeded are old .env keys that SeedFromEnv imported on the first start.
	// The database wins from then on.
	Seeded []string
	// AdminTokenStale means ADMIN_TOKEN is still set and no longer matches the
	// effective database token — the exact trap the old "read the token from
	// .env" SOP walked into.
	AdminTokenStale bool
}

// Empty reports whether there is nothing left over to warn about.
func (r LegacyEnvReport) Empty() bool {
	return len(r.Retired) == 0 && len(r.Seeded) == 0 && !r.AdminTokenStale
}

// DiagnoseLegacyEnv inspects the environment for leftovers from the .env era.
//
// It is pure: lookup is injected (os.Getenv in the binary, a map in tests), and
// the effective settings are passed in so the admin-token comparison uses the
// value the server will actually authenticate with. Secrets never leave this
// function: only variable names and one boolean are reported.
func DiagnoseLegacyEnv(lookup func(string) string, effective Settings) LegacyEnvReport {
	if lookup == nil {
		return LegacyEnvReport{}
	}
	var report LegacyEnvReport
	for _, env := range retiredEnvKeys {
		if strings.TrimSpace(lookup(env)) != "" {
			report.Retired = append(report.Retired, env)
		}
	}
	// legacyEnvKeys is a map, so sort the names for a stable log line.
	seeded := make([]string, 0, len(legacyEnvKeys))
	for env := range legacyEnvKeys {
		seeded = append(seeded, env)
	}
	sort.Strings(seeded)
	for _, env := range seeded {
		if strings.TrimSpace(lookup(env)) != "" {
			report.Seeded = append(report.Seeded, env)
		}
	}
	sort.Strings(report.Retired)

	legacyToken := strings.TrimSpace(lookup("ADMIN_TOKEN"))
	if legacyToken != "" && legacyToken != strings.TrimSpace(effective.AdminToken) {
		report.AdminTokenStale = true
	}
	return report
}
