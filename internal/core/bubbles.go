package core

import (
	"math/rand"
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
// 拆分的边界必须安全：代码围栏、行内代码、URL、Markdown 引用块**内部**不拆，
// 否则会把这些结构撕成两半。

// bubble split tuning. 30 rune 以下不拆（一句话没必要分两条）；30–80 约 0.5，
// 更长的约 0.7 —— 越长越像「说了好几件事」，拆开的收益越大。
const (
	bubbleNoSplitRunes = 30
	bubbleLongRunes    = 80
	bubbleProbShort    = 0.5
	bubbleProbLong     = 0.7
	// bubbleMinRunes 防止拆出「嗯。」这种没有信息量的碎句：两侧都够长才拆。
	bubbleMinRunes = 8
	// bubbleFence 是代码围栏的起始标记（三个反引号）。
	bubbleFence = "```"
	// bubbleTick 是行内代码的反引号。
	bubbleTick rune = 0x60
	// 强烈边界（句末）与次级边界（分句）。
	bubbleSentenceMarks = "。！？!?\n"
	bubbleClauseMarks   = "，,；;"
)

// bubbleSplitProbability maps the configured split level and the reply length to
// the chance of breaking at one candidate boundary. off / unknown = never.
//
// 长度决定基准概率（题目要求的 30–80 ≈ 0.5、>80 ≈ 0.7），档位只做缩放：
// soft 更保守、strong 更爱拆，medium 保持题目给的基准。
func bubbleSplitProbability(level string, runes int) float64 {
	var factor float64
	switch level {
	case HumanizeSoft:
		factor = 0.7
	case HumanizeMedium:
		factor = 1
	case HumanizeStrong:
		factor = 1.2
	default: // off / ""
		return 0
	}
	base := bubbleProbShort
	if runes > bubbleLongRunes {
		base = bubbleProbLong
	}
	p := base * factor
	if p > 0.95 {
		p = 0.95
	}
	return p
}

// splitBubbles returns 1..maxSegments non-empty bubbles.
//
// 纯函数：随机源通过 rnd 注入（nil = math/rand），所以测试能钉住「必拆 / 不拆」。
// 每段都不超过 maxChars，因此交给 sender 后不会因二次切分突破
// reply_max_segments —— 那条路会走「内容过长已截断」，半截回复不能算完整成功。
func splitBubbles(text, level string, maxChars, maxSegments int, rnd func() float64) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	if maxChars < 50 {
		maxChars = 50
	}
	if maxSegments < 1 {
		maxSegments = 1
	}
	runes := len([]rune(trimmed))
	if level == HumanizeOff || level == "" || runes < bubbleNoSplitRunes || maxSegments == 1 {
		return []string{trimmed}
	}
	strength := bubbleSplitProbability(level, runes)
	if strength <= 0 {
		return []string{trimmed}
	}
	if rnd == nil {
		rnd = rand.Float64
	}

	// 候选句按标点切好，再按 maxChars 打包成「一条气泡最多能装多少」的块。
	groups := groupCandidates(trimmed, bubbleCandidates(trimmed), maxChars)
	if len(groups) <= 1 {
		return []string{trimmed}
	}

	// 用概率决定相邻块是否独立成条；不拆时合并，但绝不让一条超过 maxChars。
	var pieces []string
	cur := groups[0]
	for _, next := range groups[1:] {
		if bubbleRuneLen(cur)+bubbleRuneLen(next) > maxChars {
			pieces = append(pieces, cur)
			cur = next
			continue
		}
		if rnd() < strength && bubbleRuneLen(cur) >= bubbleMinRunes && bubbleRuneLen(next) >= bubbleMinRunes {
			pieces = append(pieces, cur)
			cur = next
			continue
		}
		cur += next
	}
	if strings.TrimSpace(cur) != "" {
		pieces = append(pieces, cur)
	}
	if len(pieces) <= 1 {
		return []string{trimmed}
	}

	// 每段必须落在 maxChars 内；如果一段还是超长（没有可拆的标点），按 rune 硬切。
	var bounded []string
	for _, piece := range pieces {
		bounded = append(bounded, hardSplitBubble(piece, maxChars)...)
	}
	pieces = bounded

	// 只有「总长度装得下 maxSegments 条」时才合并回上限；否则宁可多几条，
	// 让 sender 按 reply_max_segments 显式截断，也不要秘密丢掉内容。
	if len(pieces) > maxSegments {
		total := 0
		for _, piece := range pieces {
			total += bubbleRuneLen(piece)
		}
		if total <= maxSegments*maxChars {
			pieces = rebalanceBubbles(pieces, maxSegments, maxChars)
		}
	}
	return compactBubbles(pieces)
}

// hardSplitBubble cuts one bubble at maxChars runes. It only runs when no
// punctuation boundary was available, so the alternative is losing content.
func hardSplitBubble(text string, maxChars int) []string {
	if maxChars < 1 {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return []string{text}
	}
	out := make([]string, 0, len(runes)/maxChars+1)
	for len(runes) > maxChars {
		out = append(out, string(runes[:maxChars]))
		runes = runes[maxChars:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}

// rebalanceBubbles merges adjacent bubbles down to maxSegments while keeping the
// original order. It groups by rune budget so one bubble does not end up with
// almost everything; when the text genuinely cannot fit, the last bubbles still
// carry the remainder and the sender truncates explicitly.
func rebalanceBubbles(in []string, maxSegments int, maxChars int) []string {
	if maxSegments < 1 || len(in) <= maxSegments {
		return in
	}
	total := 0
	for _, s := range in {
		total += bubbleRuneLen(s)
	}
	target := total / maxSegments
	if target < 1 {
		target = 1
	}
	out := make([]string, 0, maxSegments)
	var cur strings.Builder
	curLen := 0
	for i, s := range in {
		remainingBubbles := maxSegments - len(out)
		if curLen > 0 && (curLen+bubbleRuneLen(s) > target*2 || curLen+bubbleRuneLen(s) > maxChars) && remainingBubbles > 1 {
			out = append(out, cur.String())
			cur.Reset()
			curLen = 0
		}
		cur.WriteString(s)
		curLen += bubbleRuneLen(s)
		_ = i
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// bubbleCandidates splits at sentence boundaries first, keeping the punctuation
// with the sentence that precedes it. Commas are second-class boundaries: they
// only become candidates when the text has no sentence boundary at all.
//
// 落在代码围栏、行内代码、URL、引用块**内部**的分隔符被跳过（protected mask）。
func bubbleCandidates(text string) []string {
	protected := bubbleProtectedMask(text)
	// 引用块整体不拆：它本来就是「引用别人的话」，撕开就失去意义。
	if strings.HasPrefix(strings.TrimSpace(text), ">") || strings.Contains(text, "\n>") {
		return []string{text}
	}
	sentences := splitAfterAny(text, bubbleSentenceMarks, protected)
	if len(sentences) > 1 {
		return sentences
	}
	// 含代码 / 行内代码 / URL 时不退到逗号边界：结构内部的逗号不可靠，
	// 结构外只有一个句子时贸然按逗号拆反而会撕开上下文。
	if hasProtectedConstruct(text) {
		return sentences
	}
	return splitAfterAny(text, bubbleClauseMarks, protected)
}

// hasProtectedConstruct reports whether text carries a construct we treat as a
// hard boundary (fenced code, inline code, URL, quote block).
func hasProtectedConstruct(text string) bool {
	return strings.Contains(text, bubbleFence) ||
		strings.ContainsRune(text, bubbleTick) ||
		strings.Contains(text, "http://") || strings.Contains(text, "https://") ||
		strings.HasPrefix(strings.TrimSpace(text), ">") || strings.Contains(text, "\n>")
}

// splitAfterAny breaks after any rune in marks, skipping boundaries that sit
// inside a protected region.
func splitAfterAny(text, marks string, protected []bool) []string {
	set := map[rune]bool{}
	for _, m := range marks {
		set[m] = true
	}
	var out []string
	var cur strings.Builder
	for i, r := range []rune(text) {
		cur.WriteRune(r)
		if !set[r] {
			continue
		}
		if i < len(protected) && protected[i] {
			continue
		}
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// bubbleProtectedMask marks every rune that belongs to a construct we must not
// break: fenced code blocks, inline code, bare URLs and Markdown quote lines
// (> …). Newline positions inside those regions are protected too, which is what
// keeps a fence or a quoted line in one piece.
func bubbleProtectedMask(text string) []bool {
	runes := []rune(text)
	mask := make([]bool, len(runes))
	mark := func(from, to int) {
		for i := from; i < to && i < len(mask); i++ {
			mask[i] = true
		}
	}

	inFence := false
	pos := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		lineRunes := []rune(line)
		trimmed := strings.TrimSpace(line)
		start := pos
		end := pos + len(lineRunes)
		pos = end

		switch {
		case strings.HasPrefix(trimmed, bubbleFence):
			mark(start, end)
			inFence = !inFence
		case inFence:
			mark(start, end)
		case strings.HasPrefix(trimmed, ">"):
			mark(start, end)
		default:
			markInlineConstructs(lineRunes, start, mark)
		}
	}
	return mask
}

// markInlineConstructs protects inline code spans and bare URLs of one line.
func markInlineConstructs(lineRunes []rune, offset int, mark func(int, int)) {
	inCode := false
	for i := 0; i < len(lineRunes); i++ {
		if lineRunes[i] == bubbleTick {
			inCode = !inCode
			mark(offset+i, offset+i+1)
			continue
		}
		if inCode {
			mark(offset+i, offset+i+1)
			continue
		}
		if r := lineRunes[i]; r == 'h' || r == 'H' {
			rest := strings.ToLower(string(lineRunes[i:]))
			if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
				j := i
				for j < len(lineRunes) && !unicode.IsSpace(lineRunes[j]) {
					j++
				}
				mark(offset+i, offset+j)
				i = j - 1
			}
		}
	}
}

// groupCandidates keeps the sentence candidates that are already short enough to
// be one platform message. A single candidate longer than maxChars means the text
// has no safe boundary at all (a long code fence, a long URL, one enormous
// sentence) — then there is nothing to gain from splitting, so the whole text is
// handed back and the sender's SplitText keeps its existing behaviour (including
// balancing fences and appending the truncation marker).
func groupCandidates(text string, candidates []string, maxChars int) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if bubbleRuneLen(candidate) > maxChars {
			return []string{text}
		}
		out = append(out, candidate)
	}
	if len(out) == 0 {
		return []string{text}
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
// Chinese characters count perChar seconds and latin/other half of that; a
// single Chinese character is multiplied by 3 so one-letter answers still feel
// deliberate. scale multiplies the whole pause (humanize_delay_scale).
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
// for the first bubble. 0 = no extra wait at all.
func bubblePauseBudget(s settings.Settings) time.Duration {
	if s.HumanizeTypingMaxSeconds <= 0 {
		return 0
	}
	return time.Duration(s.HumanizeTypingMaxSeconds) * time.Second
}

// bubbleWaits turns the per-bubble typing estimate into the exact waits the
// sender will perform. waits[i] is the pause **before** bubble i; the first
// bubble keeps the existing humanize reading pause, so waits[0] is always 0.
//
// 整轮新增等待 = min(预算 − 已耗时, Σ 各段打字时间)：模型/润色已经花掉的时间
// 从预算里扣，慢回合不会在答案出来之后再等一遍。
func bubbleWaits(bubbles []string, perChar, scale float64, budget, elapsed time.Duration) []time.Duration {
	waits := make([]time.Duration, len(bubbles))
	if len(bubbles) <= 1 {
		return waits
	}
	remaining := budget - elapsed
	if remaining <= 0 {
		return waits
	}
	for i := 1; i < len(bubbles); i++ {
		pause := typingPause(bubbles[i], perChar, scale)
		if pause <= 0 {
			continue
		}
		if pause > remaining {
			pause = remaining
		}
		waits[i] = pause
		remaining -= pause
	}
	return waits
}

// rollSplit returns a number in [0,1) for the bubble-split decisions. Tests
// override it (Dispatcher.SetSplitRoll) so a reply either always or never splits.
func (d *Dispatcher) rollSplit() float64 {
	if d.splitRoll != nil {
		return d.splitRoll()
	}
	return rand.Float64()
}

// runeLen counts runes without allocating a slice for the common short case.
func bubbleRuneLen(s string) int { return len([]rune(s)) }
