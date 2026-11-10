package settings

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// 后端配置面的 golden：前端/CI 拿 internal/settings/testdata/config-keys.json
// 与自己那份键集合比对。改了配置项却没更新 golden 时，这里会失败。
func TestConfigKeysJSONMatchesGolden(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "config-keys.json"))
	if err != nil {
		t.Fatalf("读不到 golden：%v（它应随仓库提交）", err)
	}
	got, err := ConfigKeysJSON()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("配置键清单与 golden 不一致：\n --- got ---\n%s\n --- want ---\n%s\n"+
			"改配置项后请运行：bash scripts/check-config-keys.sh --write", got, want)
	}
}

// 导出的两组键必须完整、有序、无重复，并且覆盖 GroupOverride 的每个字段。
func TestConfigKeysAreCompleteAndSorted(t *testing.T) {
	manifest := ConfigKeys()
	if len(manifest.Settings) == 0 || len(manifest.GroupOverride) == 0 {
		t.Fatalf("清单不能为空：%+v", manifest)
	}

	seen := map[string]bool{}
	for _, key := range manifest.Settings {
		if seen[key] {
			t.Fatalf("settings 有重复键 %q", key)
		}
		seen[key] = true
	}
	for i := 1; i < len(manifest.Settings); i++ {
		if manifest.Settings[i-1] > manifest.Settings[i] {
			t.Fatalf("settings 未排序：%v", manifest.Settings)
		}
	}
	for _, key := range []string{"admin_token", "group_overrides", "openai_api_key", "web_search_provider", "humanize_level"} {
		if !seen[key] {
			t.Fatalf("settings 清单缺少 %q：%v", key, manifest.Settings)
		}
	}

	wantOverride := []string{
		"affect", "affect_inject_mode", "context_mode", "context_scope", "enabled",
		"exec", "group_openid", "humanize", "memory", "polish", "proactive",
		"quote_reply", "rate_limit_group_qpm", "reply_to_bots", "tasks", "tools",
		"trigger_mode", "web",
	}
	if !reflect.DeepEqual(manifest.GroupOverride, wantOverride) {
		t.Fatalf("GroupOverride 键集合不对：\n got %v\nwant %v", manifest.GroupOverride, wantOverride)
	}
}
