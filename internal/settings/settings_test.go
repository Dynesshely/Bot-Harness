package settings

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dynesshely/bot-harness/internal/store"
)

func newManager(t *testing.T, _ bool) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	base := Defaults()
	m, err := New(context.Background(), st, base)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return m, st
}

func TestDefaultsAreEDecisions(t *testing.T) {
	m, _ := newManager(t, false)
	got := m.Get()
	if got.TriggerMode != TriggerAtOnly {
		t.Fatalf("default trigger mode should be E1 (%s), got %s", TriggerAtOnly, got.TriggerMode)
	}
	if got.ContextMode != ContextPerMember {
		t.Fatalf("default context mode should be C1 (%s), got %s", ContextPerMember, got.ContextMode)
	}
	if got.Model != "gpt-4o-mini" {
		t.Fatalf("model should come from the built-in default, got %q", got.Model)
	}
}

func TestApplyRejectsUnknownField(t *testing.T) {
	m, _ := newManager(t, false)
	patch := map[string]json.RawMessage{"nope": json.RawMessage(`1`)}
	if _, _, err := m.Apply(context.Background(), patch); err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

func TestApplyValidatesRanges(t *testing.T) {
	m, _ := newManager(t, false)
	cases := []string{
		`{"temperature": 5}`,
		`{"max_tokens": 0}`,
		`{"reply_max_segments": 9}`,
		`{"context_max_turns": -1}`,
		`{"trigger_mode": "sometimes"}`,
		`{"manual_send_max_segments": 0}`,
		`{"manual_send_max_segments": 11}`,
		`{"manual_send_per_minute": -1}`,
		`{"manual_send_per_conversation_qpm": 601}`,
	}
	for _, raw := range cases {
		var patch map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &patch); err != nil {
			t.Fatalf("bad test fixture %s: %v", raw, err)
		}
		if _, _, err := m.Apply(context.Background(), patch); err == nil {
			t.Fatalf("expected validation to reject %s", raw)
		}
	}
}

// token 上限：默认值（context 256k / max_tokens 1024）与放宽后的边界。
func TestTokenLimitsDefaultsAndRanges(t *testing.T) {
	m, _ := newManager(t, false)
	got := m.Get()
	if got.ContextMaxTokens != 256000 {
		t.Fatalf("context_max_tokens 默认应为 256000，实际 %d", got.ContextMaxTokens)
	}
	if got.MaxTokens != 1024 {
		t.Fatalf("max_tokens 默认应保持 1024（不要抬高默认输出预算），实际 %d", got.MaxTokens)
	}

	good := []string{
		`{"context_max_tokens": 256}`,
		`{"context_max_tokens": 256000}`,
		`{"context_max_tokens": 10000000}`,
		`{"max_tokens": 1}`,
		`{"max_tokens": 1024}`,
		`{"max_tokens": 10000000}`,
	}
	for _, raw := range good {
		if _, _, err := m.Apply(context.Background(), mustPatch(t, raw)); err != nil {
			t.Fatalf("应接受边界值 %s：%v", raw, err)
		}
	}

	bad := []string{
		`{"context_max_tokens": 255}`,
		`{"context_max_tokens": 10000001}`,
		`{"max_tokens": 0}`,
		`{"max_tokens": 10000001}`,
	}
	for _, raw := range bad {
		if _, _, err := m.Apply(context.Background(), mustPatch(t, raw)); err == nil {
			t.Fatalf("应拒绝越界值 %s", raw)
		}
	}
}

// 触发模式与上下文模式不再受环境变量门控：无论平台权限如何，都按存储值生效。
func TestE2AndC2ArePlainStoredConfiguration(t *testing.T) {
	m, st := newManager(t, false)

	updated, _, err := m.Apply(context.Background(),
		mustPatch(t, `{"trigger_mode":"all_messages","context_mode":"shared"}`))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if updated.TriggerMode != TriggerAllMessages || updated.ContextMode != ContextShared {
		t.Fatalf("unexpected modes %#v", updated)
	}
	if eff := m.Effective(); eff.TriggerMode != TriggerAllMessages || eff.ContextMode != ContextShared {
		t.Fatalf("生效值应与存储值一致，实际 %#v", eff)
	}

	// 重启后仍然生效（配置在数据库里，不受 .env 影响）
	reloaded, err := New(context.Background(), st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Get(); got.TriggerMode != TriggerAllMessages || got.ContextMode != ContextShared {
		t.Fatalf("重启后配置丢失: %#v", got)
	}
}

// 存量配置里写着 E2/C2 时，必须原样保留而不是被强制降级。
func TestStoredE2C2SurvivesWithoutAnyEnvFlag(t *testing.T) {
	_, st := newManager(t, false)
	if err := st.SaveSettings(context.Background(), map[string]string{
		"trigger_mode": `"all_messages"`,
		"context_mode": `"shared"`,
	}); err != nil {
		t.Fatalf("seed overrides: %v", err)
	}
	m, err := New(context.Background(), st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	eff := m.Effective()
	if eff.TriggerMode != TriggerAllMessages || eff.ContextMode != ContextShared {
		t.Fatalf("存量配置被改写了: %#v", eff)
	}
}

func TestApplyPersistsAndReloads(t *testing.T) {
	m, st := newManager(t, false)
	if _, _, err := m.Apply(context.Background(), mustPatch(t, `{"model":"other","rate_limit_user_qpm":3,"blocked_keywords":["a"," b ","a"]}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	reloaded, err := New(context.Background(), st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.Get()
	if got.Model != "other" {
		t.Fatalf("override not persisted, model=%q", got.Model)
	}
	if got.RateLimitUserQPM != 3 {
		t.Fatalf("override not persisted, qpm=%d", got.RateLimitUserQPM)
	}
	if len(got.BlockedKeywords) != 2 || got.BlockedKeywords[0] != "a" || got.BlockedKeywords[1] != "b" {
		t.Fatalf("keywords should be trimmed and de-duplicated, got %v", got.BlockedKeywords)
	}
	if keys := reloaded.OverriddenKeys(); len(keys) != 3 {
		t.Fatalf("expected 3 overridden keys, got %v", keys)
	}
}

func TestResetRestoresBuiltInDefaults(t *testing.T) {
	m, _ := newManager(t, false)
	if _, _, err := m.Apply(context.Background(), mustPatch(t, `{"model":"other"}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	reset, err := m.Reset(context.Background())
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if reset.Model != "gpt-4o-mini" {
		t.Fatalf("reset should restore the built-in default, got %q", reset.Model)
	}
	// Credentials are not overrides: a reset must never leave the panel with an
	// empty password, so a missing admin token is generated and persisted.
	keys := m.OverriddenKeys()
	if len(keys) != 1 || keys[0] != "admin_token" {
		t.Fatalf("only the preserved/generated admin token should remain, got %v", keys)
	}
	if reset.AdminToken == "" {
		t.Fatal("reset must not leave the admin token empty")
	}
}

func TestGroupConfigOverrideAndGate(t *testing.T) {
	m, _ := newManager(t, false)
	if _, _, err := m.Apply(context.Background(), mustPatch(t, `{
		"group_overrides":[{"group_openid":"G1","enabled":false,"rate_limit_group_qpm":9}]
	}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	cfg := m.GroupConfig("G1")
	if cfg.Enabled {
		t.Fatal("G1 should be disabled")
	}
	if cfg.RateLimitGroup != 9 {
		t.Fatalf("expected the per-group limit to win, got %d", cfg.RateLimitGroup)
	}

	other := m.GroupConfig("G2")
	if !other.Enabled || other.RateLimitGroup != m.Get().RateLimitGroupQPM {
		t.Fatalf("unexpected config for an unlisted group: %#v", other)
	}

	// 按群覆盖同样直接生效，不再被环境变量改写。
	if _, _, err := m.Apply(context.Background(), mustPatch(t, `{
		"group_overrides":[{"group_openid":"G1","trigger_mode":"all_messages","context_mode":"shared"}]
	}`)); err != nil {
		t.Fatalf("apply group override: %v", err)
	}
	applied := m.GroupConfig("G1")
	if applied.TriggerMode != TriggerAllMessages || applied.ContextMode != ContextShared {
		t.Fatalf("按群覆盖应原样生效，实际 %#v", applied)
	}
}

func mustPatch(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &patch); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return patch
}

// 前端用 null 表示「跟随全局」，必须能清掉已有的按群覆盖值。
func TestNullClearsAGroupOverrideField(t *testing.T) {
	m, st := newManager(t, false)
	ctx := context.Background()

	if _, _, err := m.Apply(ctx, mustPatch(t, `{"group_overrides":[
		{"group_openid":"G1","context_scope":"include_unaddressed","reply_to_bots":false}
	]}`)); err != nil {
		t.Fatalf("seed override: %v", err)
	}
	// 前端「跟随全局」会发来 null
	updated, _, err := m.Apply(ctx, mustPatch(t, `{"group_overrides":[
		{"group_openid":"G1","context_scope":null,"reply_to_bots":null}
	]}`))
	if err != nil {
		t.Fatalf("clear override: %v", err)
	}
	if len(updated.GroupOverrides) != 1 {
		t.Fatalf("覆盖行数异常: %#v", updated.GroupOverrides)
	}
	got := updated.GroupOverrides[0]
	if got.ContextScope != "" {
		t.Fatalf("context_scope 应被 null 清空，实际 %q", got.ContextScope)
	}
	if got.ReplyToBots != nil {
		t.Fatalf("reply_to_bots 应被 null 清空，实际 %#v", got.ReplyToBots)
	}

	// 重新加载后仍是空的
	reloaded, err := New(ctx, st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if v := reloaded.Get().GroupOverrides[0].ContextScope; v != "" {
		t.Fatalf("落库后仍应为空，实际 %q", v)
	}
}

// 手动发送的四个键必须可改、可存、可读回（后台要能不重启就关掉它）。
func TestManualSendSettingsRoundTrip(t *testing.T) {
	m, st := newManager(t, false)
	base := m.Effective()
	if !base.ManualSendEnabled {
		t.Fatal("手动发送默认应当开启")
	}
	if base.ManualSendMaxSegments != 5 || base.ManualSendPerMinute != 20 || base.ManualSendPerConversationQPM != 6 {
		t.Fatalf("默认值不对：%+v", base)
	}

	var patch map[string]json.RawMessage
	raw := `{"manual_send_enabled": false, "manual_send_per_minute": 3,
	         "manual_send_per_conversation_qpm": 1, "manual_send_max_segments": 2}`
	if err := json.Unmarshal([]byte(raw), &patch); err != nil {
		t.Fatalf("bad patch: %v", err)
	}
	if _, _, err := m.Apply(context.Background(), patch); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := m.Effective()
	if got.ManualSendEnabled || got.ManualSendPerMinute != 3 || got.ManualSendPerConversationQPM != 1 || got.ManualSendMaxSegments != 2 {
		t.Fatalf("应用后取值不对：%+v", got)
	}

	// 重新从存储加载，确认落盘而不是只在内存里
	reloaded, err := New(context.Background(), st,
		Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again := reloaded.Effective(); again.ManualSendEnabled || again.ManualSendMaxSegments != 2 {
		t.Fatalf("重新加载后取值不对：%+v", again)
	}
}

// 引用回复：全局默认开启，可按群覆盖，也能清掉覆盖回到全局。
func TestGroupQuoteReplyOverride(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()

	if !m.Get().QuoteReply {
		t.Fatal("引用回复默认应当开启")
	}
	if !m.GroupConfig("G1").QuoteReply {
		t.Fatal("没有覆盖时应跟随全局")
	}

	off := false
	if err := m.SetGroupQuoteReply(ctx, "G1", &off); err != nil {
		t.Fatalf("set: %v", err)
	}
	if m.GroupConfig("G1").QuoteReply {
		t.Fatal("按群关闭应生效")
	}
	if !m.GroupConfig("G2").QuoteReply {
		t.Fatal("其它群不该受影响")
	}
	if ov := m.GroupQuoteReplyOverride("G1"); ov == nil || *ov {
		t.Fatalf("覆盖值应为 false，实际 %#v", ov)
	}

	// 清掉覆盖 → 回到跟随全局，并且不留空记录
	if err := m.SetGroupQuoteReply(ctx, "G1", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !m.GroupConfig("G1").QuoteReply {
		t.Fatal("清掉覆盖后应跟随全局（开启）")
	}
	if m.GroupQuoteReplyOverride("G1") != nil {
		t.Fatal("覆盖应当被清掉")
	}
	if len(m.Get().GroupOverrides) != 0 {
		t.Fatalf("空的覆盖不该留在文档里：%#v", m.Get().GroupOverrides)
	}

	// 全局关掉后，没有覆盖的群跟着关
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"quote_reply": false}`), &patch); err != nil {
		t.Fatalf("bad patch: %v", err)
	}
	if _, _, err := m.Apply(ctx, patch); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.GroupConfig("G2").QuoteReply {
		t.Fatal("全局关闭后无覆盖的群应当关闭")
	}
}

// 模型端点只来自数据库：默认是空的（不再从 .env 取），可改、可清空，非法地址当场拒绝。
func TestOpenAIBaseURLSetting(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()

	// 默认必须为空：.env 已经不参与，端点只能由后台配置。
	base := Defaults()
	if base.OpenAIBaseURL != "" {
		t.Fatalf("默认端点应为空（不再读 .env），实际 %q", base.OpenAIBaseURL)
	}

	apply := func(raw string) error {
		var patch map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &patch); err != nil {
			t.Fatalf("bad patch %s: %v", raw, err)
		}
		_, _, err := m.Apply(ctx, patch)
		return err
	}

	if err := apply(`{"openai_base_url":"http://127.0.0.1:9999/v1/"}`); err != nil {
		t.Fatalf("合法地址应被接受：%v", err)
	}
	if got := m.Get().OpenAIBaseURL; got != "http://127.0.0.1:9999/v1" {
		t.Fatalf("保存后应规范化，实际 %q", got)
	}

	for _, bad := range []string{
		`{"openai_base_url":"127.0.0.1:9999/v1"}`, // 没有 scheme
		`{"openai_base_url":"ftp://example.com/v1"}`,
		`{"openai_base_url":"https://"}`,
		`{"openai_base_url":"://x"}`,
	} {
		if err := apply(bad); err == nil {
			t.Fatalf("非法地址应被拒绝：%s", bad)
		}
	}

	// 清空 = 回到「未配置」（不再是回退 .env）
	if err := apply(`{"openai_base_url":""}`); err != nil {
		t.Fatalf("清空应被接受：%v", err)
	}
	if got := m.Get().OpenAIBaseURL; got != "" {
		t.Fatalf("清空应保存为空串，实际 %q", got)
	}
}

// API Key：只来自数据库，可设置、两边留白会去掉，清空表示未配置。
func TestOpenAIAPIKeySetting(t *testing.T) {
	m, _ := newManager(t, false)
	if Defaults().OpenAIAPIKey != "" {
		t.Fatal("密钥默认应为空（不再读 .env）")
	}
	apply := func(raw string) error {
		var patch map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &patch); err != nil {
			t.Fatalf("bad patch %s: %v", raw, err)
		}
		_, _, err := m.Apply(context.Background(), patch)
		return err
	}

	if err := apply(`{"openai_api_key":"  sk-panel  "}`); err != nil {
		t.Fatalf("设置密钥应被接受：%v", err)
	}
	if got := m.Get().OpenAIAPIKey; got != "sk-panel" {
		t.Fatalf("两侧空白应被去掉，实际 %q", got)
	}
	if err := apply(`{"openai_api_key":""}`); err != nil {
		t.Fatalf("清空密钥应被接受：%v", err)
	}
	if got := m.Get().OpenAIAPIKey; got != "" {
		t.Fatalf("应保存为空串，实际 %q", got)
	}
}

// Markdown 默认开启（平台缺权限时发送侧会自动降级，所以默认开是安全的）。
func TestMarkdownDefaultsOn(t *testing.T) {
	if !Defaults().MarkdownEnabled {
		t.Fatal("markdown 默认应为开启")
	}
	m, _ := newManager(t, false)
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"markdown_enabled": false}`), &patch); err != nil {
		t.Fatalf("bad patch: %v", err)
	}
	if _, _, err := m.Apply(context.Background(), patch); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.Get().MarkdownEnabled {
		t.Fatal("应可关闭")
	}
}

// 一次性把遗留 .env 值搬进数据库；已有覆盖的键不动，重复执行是空操作。
func TestSeedFromEnvImportsLegacyValuesOnce(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()

	// 先手工覆盖一个键，确认环境变量不会盖掉它。
	if _, _, err := m.Apply(ctx, mustPatch(t, `{"http_addr":":9999"}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	env := map[string]string{
		"QQ_APP_ID":         "123456",
		"QQ_APP_SECRET":     "s3cret",
		"QQ_INTENTS":        "33554432",
		"QQ_SANDBOX":        "true",
		"ADMIN_TOKEN":       "legacy-token",
		"HTTP_ADDR":         ":7777", // 已被覆盖，应保留 :9999
		"TLS_EXTRA_CA_FILE": "/a.pem:/b.pem",
		"LOG_LEVEL":         "debug",
		"MEDIA_MAX_BYTES":   "1048576",
		"QQ_TRANSPORT":      "both",
	}
	seeded, err := m.SeedFromEnv(ctx, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(seeded) != 9 {
		t.Fatalf("应导入 9 个键（HTTP_ADDR 已有覆盖除外），实际 %d：%v", len(seeded), seeded)
	}

	got := m.Get()
	if got.QQAppID != "123456" || got.QQAppSecret != "s3cret" {
		t.Fatalf("QQ 凭证没导入：%+v", got.QQAppID)
	}
	if got.QQIntents != 33554432 || !got.QQSandbox || got.QQTransport != "both" {
		t.Fatalf("传输相关没导入：intents=%d sandbox=%v transport=%s",
			got.QQIntents, got.QQSandbox, got.QQTransport)
	}
	if got.AdminToken != "legacy-token" || got.LogLevel != "debug" {
		t.Fatalf("运维项没导入：token=%q level=%q", got.AdminToken, got.LogLevel)
	}
	if len(got.TLSExtraCAFiles) != 2 {
		t.Fatalf("CA 路径列表没导入：%v", got.TLSExtraCAFiles)
	}
	if got.HTTPAddr != ":9999" {
		t.Fatalf("已有覆盖被环境变量覆盖了：%q", got.HTTPAddr)
	}

	// 第二次执行：什么都不该再导入。
	again, err := m.SeedFromEnv(ctx, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("重复启动不该再导入，实际 %v", again)
	}
}

// 秘密不进 KnownKeys 之外的通道：序列化后能被清空，且不会因为空值覆盖掉旧秘密。
func TestSecretKeysRoundTrip(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()

	if _, _, err := m.Apply(ctx, mustPatch(t, `{"qq_app_secret":"abc","admin_token":"tok"}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := m.Get()
	if got.QQAppSecret != "abc" || got.AdminToken != "tok" {
		t.Fatalf("秘密没存进去：%q %q", got.QQAppSecret, got.AdminToken)
	}
	// 空串 = 清空（后台「清除」语义），绝不是「保持原值」以外的含义。
	if _, _, err := m.Apply(ctx, mustPatch(t, `{"qq_app_secret":""}`)); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if m.Get().QQAppSecret != "" {
		t.Fatal("空串应清空密钥")
	}
}

// 两阶段生成（润色）的六个键：默认值、applyKey、校验、按群解析。
func TestPolishSettingsDefaults(t *testing.T) {
	m, _ := newManager(t, false)
	got := m.Get()
	if !got.PolishEnabled {
		t.Fatal("polish_enabled 默认应为 true")
	}
	if got.PolishModel != "" {
		t.Fatalf("polish_model 默认应为空（跟随主模型），实际 %q", got.PolishModel)
	}
	if got.PolishTemperature != 0.7 {
		t.Fatalf("polish_temperature 默认 0.7，实际 %v", got.PolishTemperature)
	}
	if got.PolishMaxTokens != 0 {
		t.Fatalf("polish_max_tokens 默认 0（自动），实际 %d", got.PolishMaxTokens)
	}
	if got.PolishTimeoutSeconds != 20 {
		t.Fatalf("polish_timeout_seconds 默认 20，实际 %d", got.PolishTimeoutSeconds)
	}
	if got.PolishMinChars != 0 {
		t.Fatalf("polish_min_chars 默认 0，实际 %d", got.PolishMinChars)
	}
	// 默认跟随全局：GroupConfig 必须把 polish_enabled 解析进去。
	if !m.GroupConfig("G1").PolishEnabled {
		t.Fatal("未覆盖的群应跟随全局 polish_enabled")
	}
}

func TestPolishSettingsApplyAndPersist(t *testing.T) {
	m, st := newManager(t, false)
	ctx := context.Background()

	updated, _, err := m.Apply(ctx, mustPatch(t, `{
		"polish_enabled": false,
		"polish_model": "  cheap-model  ",
		"polish_temperature": 1.25,
		"polish_max_tokens": 512,
		"polish_timeout_seconds": 45,
		"polish_min_chars": 40
	}`))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if updated.PolishEnabled {
		t.Fatal("polish_enabled 应为 false")
	}
	if updated.PolishModel != "cheap-model" {
		t.Fatalf("polish_model 应去掉首尾空白，实际 %q", updated.PolishModel)
	}
	if updated.PolishTemperature != 1.25 || updated.PolishMaxTokens != 512 ||
		updated.PolishTimeoutSeconds != 45 || updated.PolishMinChars != 40 {
		t.Fatalf("润色键没有生效：%#v", updated)
	}
	if !updated.PolishEnabled == true && updated.PolishEnabled == false && updated.PolishModel == "" {
		t.Fatal("unreachable")
	}

	reloaded, err := New(ctx, st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.Get()
	if got.PolishEnabled || got.PolishModel != "cheap-model" || got.PolishMaxTokens != 512 ||
		got.PolishTimeoutSeconds != 45 || got.PolishMinChars != 40 || got.PolishTemperature != 1.25 {
		t.Fatalf("润色键没有落库：%#v", got)
	}
}

func TestPolishSettingsRanges(t *testing.T) {
	m, _ := newManager(t, false)
	bad := []string{
		`{"polish_temperature": -0.1}`,
		`{"polish_temperature": 2.1}`,
		`{"polish_max_tokens": -1}`,
		`{"polish_max_tokens": 8193}`,
		`{"polish_max_tokens": 32000}`,
		`{"polish_max_tokens": 64000}`,
		`{"polish_timeout_seconds": 4}`,
		`{"polish_timeout_seconds": 121}`,
		`{"polish_min_chars": -1}`,
		`{"polish_min_chars": 2001}`,
	}
	for _, raw := range bad {
		if _, _, err := m.Apply(context.Background(), mustPatch(t, raw)); err == nil {
			t.Fatalf("应拒绝越界值 %s", raw)
		}
	}
	good := []string{
		`{"polish_temperature": 0}`,
		`{"polish_temperature": 2}`,
		`{"polish_max_tokens": 0}`,
		`{"polish_max_tokens": 1}`,
		`{"polish_max_tokens": 64}`,
		`{"polish_max_tokens": 8192}`,
		`{"polish_timeout_seconds": 5}`,
		`{"polish_timeout_seconds": 120}`,
		`{"polish_min_chars": 0}`,
		`{"polish_min_chars": 2000}`,
	}
	for _, raw := range good {
		if _, _, err := m.Apply(context.Background(), mustPatch(t, raw)); err != nil {
			t.Fatalf("应接受边界值 %s：%v", raw, err)
		}
	}
}

// 按群开关：nil 跟随全局，显式值覆盖全局，并且落库后仍然生效。
func TestPolishGroupOverride(t *testing.T) {
	m, st := newManager(t, false)
	ctx := context.Background()

	if !m.GroupConfig("G1").PolishEnabled {
		t.Fatal("默认应跟随全局 true")
	}
	if _, _, err := m.Apply(ctx, mustPatch(t, `{
		"group_overrides":[{"group_openid":"G1","polish":false}]
	}`)); err != nil {
		t.Fatalf("apply override: %v", err)
	}
	if m.GroupConfig("G1").PolishEnabled {
		t.Fatal("按群 polish:false 应关掉该群的润色")
	}
	if !m.GroupConfig("G2").PolishEnabled {
		t.Fatal("未覆盖的群不应受影响")
	}

	// 全局关掉后，显式的按群 true 仍然能把它打开（与其它按群开关同构）。
	if _, _, err := m.Apply(ctx, mustPatch(t, `{"polish_enabled":false}`)); err != nil {
		t.Fatalf("disable globally: %v", err)
	}
	if m.GroupConfig("G1").PolishEnabled {
		t.Fatal("G1 仍应是关的")
	}
	if m.GroupConfig("G2").PolishEnabled {
		t.Fatal("全局关掉后 G2 也应关闭")
	}
	if _, _, err := m.Apply(ctx, mustPatch(t, `{
		"group_overrides":[{"group_openid":"G1","polish":true}]
	}`)); err != nil {
		t.Fatalf("re-apply override: %v", err)
	}
	if !m.GroupConfig("G1").PolishEnabled {
		t.Fatal("按群 polish:true 应能覆盖全局关闭")
	}

	// null = 跟随全局，且不会把整行按群设置删掉以外的行为搞乱。
	reloaded, err := New(ctx, st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reloaded.GroupConfig("G1").PolishEnabled {
		t.Fatal("落库后按群开关应保持")
	}
	// null = 跟随全局：清掉后该群回到全局值（全局此时是 false）。
	if _, _, err := m.Apply(ctx, mustPatch(t, `{
		"group_overrides":[{"group_openid":"G1","tools":false,"polish":null}]
	}`)); err != nil {
		t.Fatalf("clear override: %v", err)
	}
	if m.GroupConfig("G1").PolishEnabled {
		t.Fatal("polish:null 应回到全局值（false）")
	}
	if again := m.Get().GroupOverrides; len(again) != 1 || again[0].Polish != nil || again[0].Tools == nil {
		t.Fatalf("null 只应清掉 polish，其它按群设置必须留下：%#v", again)
	}
}

// command_panel_enabled 是普通布尔开关：默认开、可关、会落库，没有取值范围。
func TestCommandPanelEnabledSetting(t *testing.T) {
	m, st := newManager(t, false)
	if !m.Get().CommandPanelEnabled {
		t.Fatal("command_panel_enabled 默认应为 true")
	}

	updated, _, err := m.Apply(context.Background(), mustPatch(t, `{"command_panel_enabled":false}`))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if updated.CommandPanelEnabled {
		t.Fatal("apply 后应为 false")
	}

	reloaded, err := New(context.Background(), st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Get().CommandPanelEnabled {
		t.Fatal("关闭状态应落库并在重启后保持")
	}
	if _, _, err := reloaded.Apply(context.Background(), mustPatch(t, `{"command_panel_enabled":true}`)); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if !reloaded.Get().CommandPanelEnabled {
		t.Fatal("重新开启后应为 true")
	}
}
