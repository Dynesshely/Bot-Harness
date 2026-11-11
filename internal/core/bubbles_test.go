package core

import (
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/settings"
)

// always / never pin the injected random source, so a split decision is not
// dependent on math/rand inside a test.
func alwaysSplit() float64 { return 0 }
func neverSplit() float64  { return 0.99 }

func TestSplitBubblesShortTextStaysWhole(t *testing.T) {
	text := "好的，我看看。"
	if got := splitBubbles(text, HumanizeStrong, 1200, 3, alwaysSplit); len(got) != 1 || got[0] != text {
		t.Fatalf("短文本不该拆：%#v", got)
	}
	// 29 rune 仍在「不拆」的一侧（阈值是 < 30）。
	near := strings.Repeat("字", 28) + "。"
	if len([]rune(near)) != 29 {
		t.Fatalf("fixture 长度 = %d", len([]rune(near)))
	}
	if got := splitBubbles(near, HumanizeStrong, 1200, 3, alwaysSplit); len(got) != 1 {
		t.Fatalf("29 rune 不该拆：%#v", got)
	}
}

func TestSplitBubblesOffAndUnknownStayWhole(t *testing.T) {
	text := strings.Repeat("这是一句完整的话。", 10)
	for _, level := range []string{HumanizeOff, ""} {
		if got := splitBubbles(text, level, 1200, 3, alwaysSplit); len(got) != 1 {
			t.Fatalf("level=%q 不该拆：%#v", level, got)
		}
	}
	// 未知档位按 off 处理，而不是悄悄当成某个生效档。
	if got := splitBubbles(text, "wild", 1200, 3, alwaysSplit); len(got) != 1 {
		t.Fatalf("未知档位不该拆：%#v", got)
	}
}

func TestSplitBubblesLongTextSplitsIntoTwoOrThree(t *testing.T) {
	text := "第一句话讲背景。第二句话讲原因。第三句话给结论。第四句话补充说明。"
	got := splitBubbles(text, HumanizeMedium, 1200, 3, alwaysSplit)
	if len(got) < 2 || len(got) > 3 {
		t.Fatalf("长文本应拆成 2–3 条，实际 %d：%#v", len(got), got)
	}
	if joined := strings.Join(got, ""); joined != text {
		t.Fatalf("拆分丢了内容：%q vs %q", joined, text)
	}
	for i, b := range got {
		if strings.TrimSpace(b) == "" {
			t.Fatalf("第 %d 条是空的：%#v", i, got)
		}
	}
	// 随机源说「不拆」时必须还原成一条。
	if got := splitBubbles(text, HumanizeMedium, 1200, 3, neverSplit); len(got) != 1 {
		t.Fatalf("rnd 不拆时应保持一条：%#v", got)
	}
}

func TestSplitBubblesNeverExceedsMaxSegments(t *testing.T) {
	text := strings.Repeat("一句话。", 40)
	for _, max := range []int{1, 2, 3} {
		if got := splitBubbles(text, HumanizeStrong, 1200, max, alwaysSplit); len(got) > max {
			t.Fatalf("maxSegments=%d 被突破：%d 条", max, len(got))
		}
	}
}

func TestSplitBubblesKeepsConstructsIntact(t *testing.T) {
	cases := map[string]string{
		"fenced code": "看这个\n\n\x60\x60\x60go\nfmt.Println(1)\nfmt.Println(2)\n\x60\x60\x60\n就明白了，很简单的。",
		"inline code": "把 \x60a.b.c\x60 换成 \x60x.y.z\x60 就行了，其它地方不要动。",
		"url":         "参考 https://example.com/a/very/long/path?x=1&y=2 这一篇，里面写得很清楚。",
		"quote block": "> 引用一段很长的话，长到足以被当作一个完整的句子。\n\n然后是我的补充说明。",
	}
	for name, text := range cases {
		got := splitBubbles(text, HumanizeStrong, 1200, 3, alwaysSplit)
		if len(got) != 1 {
			t.Fatalf("%s 不该被拆：%#v", name, got)
		}
	}
}

// 结构外的句子仍可拆：保护的是结构内部，不是整条回复。
func TestSplitBubblesStillSplitsAroundConstructs(t *testing.T) {
	text := "先看这段代码。\n\n\x60\x60\x60go\nfmt.Println(1)\n\x60\x60\x60\n\n" +
		"解释一下它做了什么。\n\n最后还有一个结论要补充。"
	got := splitBubbles(text, HumanizeStrong, 1200, 3, alwaysSplit)
	if len(got) < 2 {
		t.Fatalf("结构外应该能拆：%#v", got)
	}
	for _, b := range got {
		if strings.Count(b, "\x60\x60\x60")%2 != 0 {
			t.Fatalf("围栏被撕开：%q", b)
		}
	}
}

func TestSplitBubblesRespectsMaxChars(t *testing.T) {
	var parts []string
	for i := 0; i < 30; i++ {
		parts = append(parts, "这是一句很短的话。")
	}
	text := strings.Join(parts, "")
	const max = 60
	for _, b := range splitBubbles(text, HumanizeStrong, max, 3, alwaysSplit) {
		if n := len([]rune(b)); n > max {
			t.Fatalf("气泡超过 reply_max_chars：%d > %d（%q）", n, max, b)
		}
	}
}

func TestSplitBubblesProbabilityByLength(t *testing.T) {
	short := strings.Repeat("这是一句话。", 6) // 36 rune
	long := strings.Repeat("这是一句话。", 15) // 90 rune
	if n := len([]rune(short)); n <= bubbleNoSplitRunes || n > bubbleLongRunes {
		t.Fatalf("fixture 应落在 30–80，实际 %d", n)
	}
	if n := len([]rune(long)); n <= bubbleLongRunes {
		t.Fatalf("fixture 应超过 80，实际 %d", n)
	}
	pShort := bubbleSplitProbability(HumanizeMedium, len([]rune(short)))
	pLong := bubbleSplitProbability(HumanizeMedium, len([]rune(long)))
	if pShort < 0.45 || pShort > 0.55 {
		t.Fatalf("30–80 的基准概率应约 0.5，实际 %v", pShort)
	}
	if pLong < 0.65 || pLong > 0.75 {
		t.Fatalf(">80 的基准概率应约 0.7，实际 %v", pLong)
	}
	if bubbleSplitProbability(HumanizeSoft, 90) >= bubbleSplitProbability(HumanizeMedium, 90) {
		t.Fatal("soft 应比 medium 保守")
	}
	if bubbleSplitProbability(HumanizeMedium, 90) >= bubbleSplitProbability(HumanizeStrong, 90) {
		t.Fatal("strong 应比 medium 更爱拆")
	}
	if bubbleSplitProbability(HumanizeOff, 90) != 0 {
		t.Fatal("off 不该拆")
	}
}

func TestTypingPauseWeightsChinese(t *testing.T) {
	zh := typingPause("你好世界", 0.3, 1)
	en := typingPause("abcd", 0.3, 1)
	if zh != 1200*time.Millisecond || en != 600*time.Millisecond {
		t.Fatalf("中文 0.3s/字、英文 0.15s/字：zh=%v en=%v", zh, en)
	}
	if one := typingPause("好", 0.3, 1); one != 900*time.Millisecond {
		t.Fatalf("单字应 ×3：%v", one)
	}
	if scaling := typingPause("你好", 0.3, 2); scaling != 1200*time.Millisecond {
		t.Fatalf("scale 应等比缩放：%v", scaling)
	}
	if typingPause("你好", 0, 1) != 0 {
		t.Fatal("perChar=0 表示不等待")
	}
}

func TestBubbleWaitsRespectBudgetAndElapsed(t *testing.T) {
	bubbles := []string{"第一段", "第二段内容", "第三段内容"}
	waits := bubbleWaits(bubbles, 0.3, 1, 12*time.Second, 0)
	if len(waits) != len(bubbles) {
		t.Fatalf("waits 长度应与气泡数一致：%d vs %d", len(waits), len(bubbles))
	}
	if waits[0] != 0 {
		t.Fatalf("第一条气泡前不该有逐段等待：%v", waits[0])
	}
	var total time.Duration
	for _, w := range waits {
		total += w
	}
	if total <= 0 || total > 12*time.Second {
		t.Fatalf("总等待应在 (0, 12s]，实际 %v", total)
	}

	// 已耗时 10s：只剩 2s 预算，必须被压到 2s 以内。
	total = 0
	for _, w := range bubbleWaits(bubbles, 0.3, 1, 12*time.Second, 10*time.Second) {
		total += w
	}
	if total > 2*time.Second {
		t.Fatalf("扣除已耗时后仍超预算：%v", total)
	}

	// 已耗时超过预算：零等待。
	for _, w := range bubbleWaits(bubbles, 0.3, 1, 12*time.Second, 30*time.Second) {
		if w != 0 {
			t.Fatalf("超预算时不该再等：%v", w)
		}
	}

	// humanize_typing_max_seconds=0：不加逐段等待。
	for _, w := range bubbleWaits(bubbles, 0.3, 1, 0, 0) {
		if w != 0 {
			t.Fatalf("上限为 0 时不该等待：%v", w)
		}
	}

	// scale 等比缩放。
	full := bubbleWaits(bubbles, 0.3, 1, 12*time.Second, 0)
	half := bubbleWaits(bubbles, 0.3, 0.5, 12*time.Second, 0)
	if half[1] >= full[1] {
		t.Fatalf("scale 应缩短等待：half=%v full=%v", half[1], full[1])
	}
}

func TestBubblePauseBudget(t *testing.T) {
	if got := bubblePauseBudget(settings.Settings{HumanizeTypingMaxSeconds: 12}); got != 12*time.Second {
		t.Fatalf("预算 = %v", got)
	}
	if got := bubblePauseBudget(settings.Settings{HumanizeTypingMaxSeconds: 0}); got != 0 {
		t.Fatalf("0 应表示不加等待，实际 %v", got)
	}
}
