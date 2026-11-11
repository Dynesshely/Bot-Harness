package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/settings"
)

// 备用表达风格按概率轮换，以及引用回复的概率分档。
//
// 两者都由 Dispatcher 的随机源驱动：测试把 roll pin 成常数，断言的是「判定逻辑」
// 而不是抽样结果。

func styleTestDispatcher(t *testing.T) (*Dispatcher, *settings.Manager) {
	t.Helper()
	sm, err := settings.New(context.Background(), nil, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	return &Dispatcher{settings: sm}, sm
}

func applyStyleSettings(t *testing.T, sm *settings.Manager, raw string) {
	t.Helper()
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &patch); err != nil {
		t.Fatalf("bad patch: %v", err)
	}
	if _, _, err := sm.Apply(context.Background(), patch); err != nil {
		t.Fatalf("apply %s: %v", raw, err)
	}
}

func TestStyleVariantAppearsInPromptAtFullProbability(t *testing.T) {
	d, sm := styleTestDispatcher(t)
	applyStyleSettings(t, sm, `{
		"humanize_level":"off",
		"reply_style_variants":["用 1–2 个字回复","带一点翻译腔"],
		"reply_style_variant_probability":1
	}`)
	// roll 落在 [0,1)：概率 1 时每一掷都命中，选哪一条由同一掷决定。
	d.SetStyleRoll(func() float64 { return 0.1 })
	prompt := d.promptFor(sm.Get(), settings.GroupConfig{HumanizeLevel: HumanizeOff})
	if !strings.Contains(prompt, "本轮说话方式：用 1–2 个字回复") {
		t.Fatalf("概率 1 时系统提示词应包含第一条变体，实际：%s", prompt)
	}
	if strings.Contains(prompt, "带一点翻译腔") {
		t.Fatalf("一轮只应抽一条变体，实际：%s", prompt)
	}

	// 同一份配置换一掷，抽到第二条。
	d.SetStyleRoll(func() float64 { return 0.9 })
	prompt = d.promptFor(sm.Get(), settings.GroupConfig{HumanizeLevel: HumanizeOff})
	if !strings.Contains(prompt, "带一点翻译腔") {
		t.Fatalf("另一掷应抽到第二条变体，实际：%s", prompt)
	}
}

func TestStyleVariantAbsentAtZeroProbability(t *testing.T) {
	d, sm := styleTestDispatcher(t)
	applyStyleSettings(t, sm, `{
		"humanize_level":"off",
		"reply_style_variants":["用 1–2 个字回复"],
		"reply_style_variant_probability":0
	}`)
	// 即使 roll 为 0（最小），概率 0 也不该命中。
	d.SetStyleRoll(func() float64 { return 0 })
	prompt := d.promptFor(sm.Get(), settings.GroupConfig{HumanizeLevel: HumanizeOff})
	if strings.Contains(prompt, "本轮说话方式") || strings.Contains(prompt, "1–2 个字") {
		t.Fatalf("概率 0 时不应注入任何变体，实际：%s", prompt)
	}

	// roll 未中（>= 概率）时同样不注入。
	applyStyleSettings(t, sm, `{"reply_style_variant_probability":0.25}`)
	d.SetStyleRoll(func() float64 { return 0.9 })
	if prompt := d.promptFor(sm.Get(), settings.GroupConfig{HumanizeLevel: HumanizeOff}); strings.Contains(prompt, "本轮说话方式") {
		t.Fatalf("未掷中时不应注入变体，实际：%s", prompt)
	}
}

func TestStyleVariantEmptyListIsOff(t *testing.T) {
	d, sm := styleTestDispatcher(t)
	applyStyleSettings(t, sm, `{"humanize_level":"off","reply_style_variant_probability":1}`)
	d.SetStyleRoll(func() float64 { return 0 })
	if prompt := d.promptFor(sm.Get(), settings.GroupConfig{HumanizeLevel: HumanizeOff}); strings.Contains(prompt, "本轮说话方式") {
		t.Fatalf("空数组 = 不启用，实际：%s", prompt)
	}
}

// 引用频率分档：never 永不引用；occasionally 0.1 引用、0.5 不引用；
// often 0.5 引用。空档位回退到旧布尔（legacy），只认识旧字段的调用方因此
// 不会被悄悄降级成 30%。
func TestShouldQuoteFrequencyBands(t *testing.T) {
	cases := []struct {
		name string
		mode string
		roll float64
		want bool
	}{
		{name: "never 不引用", mode: settings.QuoteReplyNever, roll: 0.01, want: false},
		{name: "never 最小 roll 也不引用", mode: settings.QuoteReplyNever, roll: 0, want: false},
		{name: "occasionally 0.1 引用", mode: settings.QuoteReplyOccasionally, roll: 0.1, want: true},
		{name: "occasionally 0.5 不引用", mode: settings.QuoteReplyOccasionally, roll: 0.5, want: false},
		{name: "often 0.5 引用", mode: settings.QuoteReplyOften, roll: 0.5, want: true},
		{name: "often 0.9 不引用", mode: settings.QuoteReplyOften, roll: 0.9, want: false},
		{name: "空档位回退 legacy=false（不引用）", mode: "", roll: 0.1, want: false},
		{name: "空档位回退 legacy=false（0.5 也不引用）", mode: "", roll: 0.5, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dispatcher{}
			roll := tc.roll
			d.SetQuoteRoll(func() float64 { return roll })
			if got := d.shouldQuote(tc.mode, false); got != tc.want {
				t.Fatalf("shouldQuote(%q) with roll %v = %v，期望 %v", tc.mode, tc.roll, got, tc.want)
			}
		})
	}
}
