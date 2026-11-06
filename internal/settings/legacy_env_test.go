package settings

import (
	"reflect"
	"sort"
	"testing"
)

// lookupFrom builds the injected environment lookup from a map.
func lookupFrom(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// 全干净的进程环境：没有任何遗留键，也不该报警。
func TestDiagnoseLegacyEnvOnACleanEnvironment(t *testing.T) {
	effective := Defaults()
	effective.AdminToken = "db-token"

	report := DiagnoseLegacyEnv(lookupFrom(map[string]string{}), effective)
	if !report.Empty() {
		t.Fatalf("干净环境不该有遗留项：%+v", report)
	}
}

// 已废弃的键（OPENAI_* / REPLY_* / 采样与限流等）只出现在 Retired 里。
func TestDiagnoseLegacyEnvReportsRetiredKeys(t *testing.T) {
	env := map[string]string{
		"OPENAI_API_KEY":     "sk-legacy",
		"OPENAI_BASE_URL":    "https://one-api.internal.example/v1",
		"REPLY_MAX_CHARS":    "500",
		"CONTEXT_MAX_TURNS":  "8",
		"QQ_FULL_GROUP_MODE": "true",
		// 空白值等于没设置，不该被算作遗留项。
		"VISION_ENABLED": "   ",
	}
	report := DiagnoseLegacyEnv(lookupFrom(env), Defaults())
	want := []string{"CONTEXT_MAX_TURNS", "OPENAI_API_KEY", "OPENAI_BASE_URL", "QQ_FULL_GROUP_MODE", "REPLY_MAX_CHARS"}
	if !reflect.DeepEqual(report.Retired, want) {
		t.Fatalf("Retired 不对：\n got %v\nwant %v", report.Retired, want)
	}
	if len(report.Seeded) != 0 {
		t.Fatalf("这些键不该出现在 Seeded 里：%v", report.Seeded)
	}
	// 密钥只以变量名出现，绝不带值。
	for _, name := range report.Retired {
		if name == env[name] {
			t.Fatalf("报告里不能包含值：%q", name)
		}
	}
}

// 一次性导入过的键单独一组：它们仍被 SeedFromEnv 读取一次，但之后数据库优先。
func TestDiagnoseLegacyEnvSeparatesSeededKeys(t *testing.T) {
	env := map[string]string{
		"QQ_APP_ID":         "123456",
		"QQ_APP_SECRET":     "should-never-be-echoed",
		"ADMIN_TOKEN":       "db-token", // 与库内一致 -> 不算过期
		"HTTP_ADDR":         ":8080",
		"TLS_EXTRA_CA_FILE": "/app/certs/ca.pem",
	}
	effective := Defaults()
	effective.AdminToken = "db-token"

	report := DiagnoseLegacyEnv(lookupFrom(env), effective)
	want := []string{"ADMIN_TOKEN", "HTTP_ADDR", "QQ_APP_ID", "QQ_APP_SECRET", "TLS_EXTRA_CA_FILE"}
	if !reflect.DeepEqual(report.Seeded, want) {
		t.Fatalf("Seeded 不对：\n got %v\nwant %v", report.Seeded, want)
	}
	if len(report.Retired) != 0 {
		t.Fatalf("这些键不该出现在 Retired 里：%v", report.Retired)
	}
	if report.AdminTokenStale {
		t.Fatal("legacy token 与库内一致时不该报过期")
	}
}

// .env 的 ADMIN_TOKEN 与库内不一致 = 正是「拿 .env 的 token 登不进去」那个事故。
func TestDiagnoseLegacyEnvFlagsAStaleAdminToken(t *testing.T) {
	env := map[string]string{"ADMIN_TOKEN": "old-env-token"}
	effective := Defaults()
	effective.AdminToken = "rotated-db-token"

	report := DiagnoseLegacyEnv(lookupFrom(env), effective)
	if !report.AdminTokenStale {
		t.Fatal("不一致时应报过期")
	}
	if report.Empty() {
		t.Fatal("有遗留项时 Empty 必须为 false")
	}
}

// 数据库还没生成口令时，只要 .env 里有一个 token 就算不一致（会触发一次 WARN）。
func TestDiagnoseLegacyEnvStaleWhenDatabaseHasNoToken(t *testing.T) {
	report := DiagnoseLegacyEnv(lookupFrom(map[string]string{"ADMIN_TOKEN": "from-env"}), Defaults())
	if !report.AdminTokenStale {
		t.Fatal("库内为空而 .env 有时应报过期")
	}

	// 两边都空：没有可说的。
	if got := DiagnoseLegacyEnv(lookupFrom(map[string]string{}), Defaults()); got.AdminTokenStale {
		t.Fatal("两边都空时不该报过期")
	}
}

// nil lookup（理论上不会发生）不能 panic。
func TestDiagnoseLegacyEnvToleratesANilLookup(t *testing.T) {
	if report := DiagnoseLegacyEnv(nil, Defaults()); !report.Empty() {
		t.Fatalf("nil lookup 应返回空报告：%+v", report)
	}
}

// 两份清单必须互斥且有序：重叠会让同一条键同时出现在两条 WARN 里，
// 也说明「还读一次」和「完全不再读」被混为一谈。
func TestLegacyEnvKeyListsAreDisjointAndSorted(t *testing.T) {
	if !sort.StringsAreSorted(retiredEnvKeys) {
		t.Fatalf("retiredEnvKeys 未排序：%v", retiredEnvKeys)
	}
	seen := map[string]bool{}
	for _, env := range retiredEnvKeys {
		if seen[env] {
			t.Fatalf("retiredEnvKeys 有重复项：%s", env)
		}
		seen[env] = true
		if _, ok := legacyEnvKeys[env]; ok {
			t.Fatalf("%s 同时出现在两份清单里", env)
		}
	}
	// 仍然是 bootstrap / compose / net-http 职责的变量不能被报告为遗留。
	for _, live := range []string{"DB_PATH", "ENV_FILE", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ADMIN_BIND"} {
		if seen[live] {
			t.Fatalf("%s 仍在使用，不该进 retiredEnvKeys", live)
		}
		if _, ok := legacyEnvKeys[live]; ok {
			t.Fatalf("%s 仍在使用，不该进 legacyEnvKeys", live)
		}
	}
}
