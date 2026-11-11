package settings

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 口吻轮换与引用频率分档这两组键的默认值、写入、边界与落库。
//
// 它们都是纯表达层配置：默认值必须让老配置的行为不发生任何变化
// （reply_style_variants 空 = 不启用；quote_reply_mode 由旧的 quote_reply 布尔推导）。

func TestReplyStyleVariantDefaults(t *testing.T) {
	m, _ := newManager(t, false)
	got := m.Get()
	if len(got.ReplyStyleVariants) != 0 {
		t.Fatalf("reply_style_variants 默认应为空（不启用），实际 %#v", got.ReplyStyleVariants)
	}
	if got.ReplyStyleVariantProbability != ReplyStyleVariantProbabilityDefault {
		t.Fatalf("reply_style_variant_probability 默认 %v，实际 %v",
			ReplyStyleVariantProbabilityDefault, got.ReplyStyleVariantProbability)
	}
}

func TestReplyStyleVariantsApplyAndPersist(t *testing.T) {
	m, st := newManager(t, false)
	ctx := context.Background()

	// 空白项被去掉、重复项只留一条、首尾空白去掉。
	updated, _, err := m.Apply(ctx, mustPatch(t, `{
		"reply_style_variants": ["  用 1–2 个字回复 ", "", "带一点翻译腔", "用 1–2 个字回复", "   "],
		"reply_style_variant_probability": 0.6
	}`))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := []string{"用 1–2 个字回复", "带一点翻译腔"}
	if len(updated.ReplyStyleVariants) != len(want) {
		t.Fatalf("变体应去空去重，实际 %#v", updated.ReplyStyleVariants)
	}
	for i := range want {
		if updated.ReplyStyleVariants[i] != want[i] {
			t.Fatalf("变体[%d] = %q，期望 %q", i, updated.ReplyStyleVariants[i], want[i])
		}
	}
	if updated.ReplyStyleVariantProbability != 0.6 {
		t.Fatalf("概率没生效：%v", updated.ReplyStyleVariantProbability)
	}

	reloaded, err := New(ctx, st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.Get()
	if len(got.ReplyStyleVariants) != len(want) || got.ReplyStyleVariantProbability != 0.6 {
		t.Fatalf("两组键没有落库：%#v / %v", got.ReplyStyleVariants, got.ReplyStyleVariantProbability)
	}

	// 清空 = 关掉这个功能，不残留旧条目。
	cleared, _, err := m.Apply(ctx, mustPatch(t, `{"reply_style_variants": []}`))
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if len(cleared.ReplyStyleVariants) != 0 {
		t.Fatalf("清空后不应残留变体：%#v", cleared.ReplyStyleVariants)
	}
}

func TestReplyStyleVariantsBounds(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()

	// 每条 <= 200 rune：200 可以，201 不行。
	if _, _, err := m.Apply(ctx, mustPatch(t, `{"reply_style_variants":["`+
		strings.Repeat("字", ReplyStyleVariantMaxRunes)+`"]}`)); err != nil {
		t.Fatalf("%d rune 的变体应被接受：%v", ReplyStyleVariantMaxRunes, err)
	}
	if _, _, err := m.Apply(ctx, mustPatch(t, `{"reply_style_variants":["`+
		strings.Repeat("字", ReplyStyleVariantMaxRunes+1)+`"]}`)); err == nil {
		t.Fatalf("超过 %d rune 的变体应被拒绝", ReplyStyleVariantMaxRunes)
	}

	// 最多 8 条：8 可以，9 不行。
	eight := make([]string, 0, ReplyStyleVariantMaxItems)
	for i := 0; i < ReplyStyleVariantMaxItems; i++ {
		eight = append(eight, "风格 "+string(rune('a'+i)))
	}
	if _, _, err := m.Apply(ctx, variantPatch(t, eight)); err != nil {
		t.Fatalf("%d 条变体应被接受：%v", ReplyStyleVariantMaxItems, err)
	}
	nine := append(append([]string{}, eight...), "风格 i")
	if _, _, err := m.Apply(ctx, variantPatch(t, nine)); err == nil {
		t.Fatalf("超过 %d 条变体应被拒绝", ReplyStyleVariantMaxItems)
	}
}

func variantPatch(t *testing.T, variants []string) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"reply_style_variants": variants})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(raw, &patch); err != nil {
		t.Fatalf("patch: %v", err)
	}
	return patch
}

func TestReplyStyleVariantProbabilityBounds(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()

	for _, raw := range []string{
		`{"reply_style_variant_probability": 0}`,
		`{"reply_style_variant_probability": 0.25}`,
		`{"reply_style_variant_probability": 1}`,
	} {
		if _, _, err := m.Apply(ctx, mustPatch(t, raw)); err != nil {
			t.Fatalf("应接受 %s：%v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"reply_style_variant_probability": -0.01}`,
		`{"reply_style_variant_probability": 1.01}`,
	} {
		if _, _, err := m.Apply(ctx, mustPatch(t, raw)); err == nil {
			t.Fatalf("应拒绝越界概率 %s", raw)
		}
	}
}

// 旧布尔与档位必须始终同步：写档位同时改布尔，写布尔同时改档位。
func TestQuoteReplyModeMirrorsLegacyBoolean(t *testing.T) {
	m, st := newManager(t, false)
	ctx := context.Background()

	got := m.Get()
	if got.QuoteReplyMode != QuoteReplyOccasionally {
		t.Fatalf("quote_reply_mode 默认 occasionally（30%%），实际 %q", got.QuoteReplyMode)
	}
	if !got.QuoteReply {
		t.Fatal("默认档位下 quote_reply 布尔应为 true")
	}

	// 写档位 → 布尔跟着变。
	updated, _, err := m.Apply(ctx, mustPatch(t, `{"quote_reply_mode":"never"}`))
	if err != nil {
		t.Fatalf("apply mode: %v", err)
	}
	if updated.QuoteReplyMode != QuoteReplyNever || updated.QuoteReply {
		t.Fatalf("mode=never 应同时把布尔置为 false：%q / %v", updated.QuoteReplyMode, updated.QuoteReply)
	}

	// 写旧布尔 → 档位跟着变（true = occasionally）。
	updated, _, err = m.Apply(ctx, mustPatch(t, `{"quote_reply":true}`))
	if err != nil {
		t.Fatalf("apply boolean: %v", err)
	}
	if updated.QuoteReplyMode != QuoteReplyOccasionally || !updated.QuoteReply {
		t.Fatalf("quote_reply=true 应映射为 occasionally：%q / %v", updated.QuoteReplyMode, updated.QuoteReply)
	}

	// often 也必须是布尔 true（大小写与空白都归一）。
	updated, _, err = m.Apply(ctx, mustPatch(t, `{"quote_reply_mode":" Often "}`))
	if err != nil {
		t.Fatalf("apply often: %v", err)
	}
	if updated.QuoteReplyMode != QuoteReplyOften || !updated.QuoteReply {
		t.Fatalf("often 应被接受并保持布尔 true：%q / %v", updated.QuoteReplyMode, updated.QuoteReply)
	}

	// 非法档位当场拒绝。
	if _, _, err := m.Apply(ctx, mustPatch(t, `{"quote_reply_mode":"sometimes"}`)); err == nil {
		t.Fatal("非法 quote_reply_mode 应被拒绝")
	}

	reloaded, err := New(ctx, st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Get().QuoteReplyMode != QuoteReplyOften {
		t.Fatalf("档位没有落库：%q", reloaded.Get().QuoteReplyMode)
	}
}

// 升级前的库里只有 quote_reply=false：读取时必须映射成 never，而不是默认档位。
func TestLegacyQuoteReplyFalseLoadsAsNever(t *testing.T) {
	_, st := newManager(t, false)
	ctx := context.Background()
	if err := st.SaveSettings(ctx, map[string]string{"quote_reply": "false"}); err != nil {
		t.Fatalf("seed legacy boolean: %v", err)
	}
	m, err := New(ctx, st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := m.Get()
	if got.QuoteReplyMode != QuoteReplyNever || got.QuoteReply {
		t.Fatalf("旧 quote_reply=false 应映射为 never：%q / %v", got.QuoteReplyMode, got.QuoteReply)
	}
	if m.GroupConfig("G1").QuoteReplyMode != QuoteReplyNever {
		t.Fatal("按群解析也应得到 never")
	}
}

func TestQuoteReplyRates(t *testing.T) {
	cases := map[string]float64{
		QuoteReplyNever:        0,
		QuoteReplyOccasionally: 0.3,
		QuoteReplyOften:        0.7,
		"":                     0.3, // 空 = 跟随默认档位
		"unknown":              0.3,
	}
	for mode, want := range cases {
		if got := QuoteReplyRate(mode); got != want {
			t.Fatalf("QuoteReplyRate(%q) = %v，期望 %v", mode, got, want)
		}
	}
}

// 按群覆盖仍是旧布尔语义：false → never（且可以压过全局档位），
// true → occasionally，清掉覆盖 → 跟随全局档位；全局 never 时群覆盖打不开。
func TestGroupQuoteOverrideMapsOntoModes(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()

	if _, _, err := m.Apply(ctx, mustPatch(t, `{"quote_reply_mode":"often"}`)); err != nil {
		t.Fatalf("apply mode: %v", err)
	}
	off := false
	if err := m.SetGroupQuoteReply(ctx, "G1", &off); err != nil {
		t.Fatalf("set group: %v", err)
	}
	if got := m.GroupConfig("G1"); got.QuoteReplyMode != QuoteReplyNever || got.QuoteReply {
		t.Fatalf("按群 false 应映射为 never：%q / %v", got.QuoteReplyMode, got.QuoteReply)
	}
	if got := m.GroupConfig("G2"); got.QuoteReplyMode != QuoteReplyOften || !got.QuoteReply {
		t.Fatalf("未覆盖的群应跟随全局 often：%q / %v", got.QuoteReplyMode, got.QuoteReply)
	}

	// 群打开 → occasionally（旧的布尔粒度，无法表达 often）。
	on := true
	if err := m.SetGroupQuoteReply(ctx, "G1", &on); err != nil {
		t.Fatalf("set group on: %v", err)
	}
	if got := m.GroupConfig("G1"); got.QuoteReplyMode != QuoteReplyOccasionally || !got.QuoteReply {
		t.Fatalf("按群 true 应映射为 occasionally：%q / %v", got.QuoteReplyMode, got.QuoteReply)
	}

	// 清掉覆盖 → 回到全局档位。
	if err := m.SetGroupQuoteReply(ctx, "G1", nil); err != nil {
		t.Fatalf("clear group: %v", err)
	}
	if got := m.GroupConfig("G1"); got.QuoteReplyMode != QuoteReplyOften || !got.QuoteReply {
		t.Fatalf("清掉覆盖后应回到全局 often：%q / %v", got.QuoteReplyMode, got.QuoteReply)
	}

	// 全局 never 时，群里的 true 也不能把引用打开。
	if _, _, err := m.Apply(ctx, mustPatch(t, `{"quote_reply_mode":"never"}`)); err != nil {
		t.Fatalf("apply never: %v", err)
	}
	if err := m.SetGroupQuoteReply(ctx, "G1", &on); err != nil {
		t.Fatalf("set group on again: %v", err)
	}
	if got := m.GroupConfig("G1"); got.QuoteReplyMode != QuoteReplyNever || got.QuoteReply {
		t.Fatalf("全局 never 时群覆盖不能打开引用：%q / %v", got.QuoteReplyMode, got.QuoteReply)
	}
}
