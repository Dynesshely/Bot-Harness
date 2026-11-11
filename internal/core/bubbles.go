package core

import (
	"strings"
	"time"
	"unicode"

	"github.com/dynesshely/bot-harness/internal/settings"
)

// 把一条回复拆成几条「气泡」，并给每条算一个模拟打字的等待时间。
//
// 借鉴 MaiBot 的两条经验：
//   1. 短句先按标点概率性拆开，长回复像人一样分几条发，而不是一大段；
//   2. 每条气泡前按字数等一小会儿（打字时间），而不是只在整轮开始前等一次。
//
// 拆分的边界必须安全：代码块、链接、引用块整体不拆，避免把 Markdown 撕坏。

// bubbleSplitStrength maps the split level to the chance of breaking at a
// candidate boundary. off = never split.
func bubbleSplitStrength(level string, runes int) float64 {
	var base float64
	switch level {
	case "soft":
		base = 0.4
	case "medium":
		base = 0.6
	case "strong":
		base = 0.75
	default:
		return 0
	}
	// Longer answers read better as several bubbles; short ones stay whole.
	if runes >= 80 {
		base += 0.1
	}
	if base > 1 {
		base = 1
	}
	return base
}

// splitBubbles returns 1..maxSegments non-empty bubbles. `rnd` is injectable so
// tests can pin the split decisions.
func splitBubbles(text, level string, maxChars, maxSegments int, rnd func() float64) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	if maxSegments < 1 {
		maxSegments = 1
	}
	runes := len([]rune(trimmed))
	if level == "off" || runes < 30 || maxSegments == 1 {
		return []string{trimmed}
	}
	// 代码块 / 链接 / 引用块不拆：上下文里已有的例子表明拆开比整段更糟。
	if strings.Contains(trimmed, "```") || strings.Contains(trimmed, "http://") ||
		strings.Contains(trimmed, "https://") || strings.HasPrefix(trimmed, ">") {
		return []string{trimmed}
	}

	strength := bubbleSplitStrength(level, runes)
	if strength <= 0 {
		return []string{trimmed}
	}
	if rnd == nil {
		rnd = func() float64 { return 0 }
	}

	candidates := bubbleCandidates(trimmed)
	if len(candidates) <= 1 {
		return []string{trimmed}
	}
	out := make([]string, 0, maxSegments)
	cur := candidates[0]
	for _, next := range candidates[1:] {
		if len(out) < maxSegments-1 && rnd() < strength && len([]rune(cur))+len([]rune(next)) <= maxChars*2 {
			out = append(out, strings.TrimSpace(cur))
			cur = next
			continue
		}
		cur += next
	}
	if strings.TrimSpace(cur) != "" {
		out = append(out, strings.TrimSpace(cur))
	}
	if len(out) == 0 {
		return []string{trimmed}
	}
	return compactBubbles(out)
}

// bubbleCandidates splits at sentence boundaries first, keeping the punctuation
// with the sentence that precedes it. Commas are second-class boundaries: they
// only become candidates when the text has no sentence boundary at all.
func bubbleCandidates(text string) []string {
	strong := splitAfterAny(text, "。！？!?\n")
	if len(strong) > 1 {
		return strong
	}
	return splitAfterAny(text, "，,；;")
}

func splitAfterAny(text, marks string) []string {
	set := map[rune]bool{}
	for _, m := range marks {
		set[m] = true
	}
	var out []string
	var cur strings.Builder
	for _, r := range text {
		cur.WriteRune(r)
		if set[r] {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func compactBubbles(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// typingPause estimates how long a human would take to type one bubble.
//
// Chinese characters count 0.3s and latin/other 0.15s by default; a single
// character is multiplied by 3 so one-letter answers still feel deliberate.
func typingPause(text string, perChar float64, scale float64) time.Duration {
	if perChar <= 0 {
		return 0
	}
	if scale <= 0 {
		scale = 1
	}
	chinese := 0
	other := 0
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			chinese++
		} else if !unicode.IsSpace(r) {
			other++
		}
	}
	d := time.Duration(float64(chinese)*perChar*float64(time.Second)) +
		time.Duration(float64(other)*perChar*0.5*float64(time.Second))
	if chinese == 1 && other == 0 {
		d *= 3
	}
	return time.Duration(float64(d) * scale)
}

// bubblePauseBudget caps the total time a turn may spend on simulated typing.
// Unlike humanizeDelayForTurn this is a hard ceiling for the whole reply, not
// for the first bubble.
func bubblePauseBudget(s settings.Settings) time.Duration {
	if s.HumanizeTypingMaxSeconds <= 0 {
		return 0
	}
	return time.Duration(s.HumanizeTypingMaxSeconds) * time.Second
}
