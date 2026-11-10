package affect

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// EventMark is the compact event ledger stored in the state. At is RFC3339.
type EventMark struct {
	ID string `json:"id"`
	At string `json:"at"`
}

// Injection modes.
const (
	ModeOff  = "off"
	ModeLine = "line"
	ModeFull = "full"

	lineMaxTokens      = 25
	defaultFullTokens  = 120
	maxFullTokens      = 300
	residueWindow      = 30 * time.Minute
	fullHeader         = "[此刻的状态｜仅作语气参考，不要向用户解释、复述或索要回应]"
	fullSelfTalkHeader = "[自述｜第一人称，不要提及数值或系统]"
	lineHeader         = "[状态｜仅作语气参考]"
)

// BuildBlock renders the prompt block for one conversation.
//
// Every failure path (unknown mode, nil pack, empty state) returns "": the caller
// must be able to concatenate the result without a nil check.
func BuildBlock(p *Pack, dims map[string]float64, marks []EventMark, mode string, maxTokens int, b Behavior, now time.Time) string {
	if p == nil {
		return ""
	}
	switch mode {
	case ModeLine:
		return truncateTokens(BuildLine(p, dims, now), lineMaxTokens)
	case ModeFull:
		budget := maxTokens
		if budget <= 0 {
			budget = defaultFullTokens
		}
		if budget > maxFullTokens {
			budget = maxFullTokens
		}
		if budget < 40 {
			budget = 40
		}
		return truncateTokens(BuildFull(p, dims, marks, b, now, budget), budget)
	default:
		return ""
	}
}

// BuildLine is the compact variant: one dominant fragment plus one short
// self-description, kept to a single line. It is meant to fit in the line budget
// without becoming a second system prompt.
func BuildLine(p *Pack, dims map[string]float64, now time.Time) string {
	if p == nil {
		return ""
	}
	values := NormalizeDims(p, dims)
	fragment := ""
	if leaders := dominantDims(p, values, 1); len(leaders) > 0 {
		fragment = pickFragment(p, leaders[0], values[leaders[0]])
	}
	if fragment == "" {
		return ""
	}
	// 25 tokens is tight: the wrapper takes a chunk of it, so the self-talk is
	// only included when it still fits. A short line beats a cut one.
	candidate := lineHeader + " " + fragment + "。"
	if self := firstClause(p.Injection.SelfTalk, 8); self != "" {
		withSelf := lineHeader + " " + fragment + "；" + self + "。"
		if approxTokens(withSelf) <= lineMaxTokens {
			candidate = withSelf
		}
	}
	return candidate
}

// BuildFull is the rich variant. The mandatory part is the two dominant
// fragments, the behavior constraint and the pack's self-talk; time-of-day and
// the recent-event residue are added only while they fit, so the residue is the
// first thing dropped and the time hint the second.
func BuildFull(p *Pack, dims map[string]float64, marks []EventMark, b Behavior, now time.Time, budget int) string {
	if p == nil {
		return ""
	}
	values := NormalizeDims(p, dims)
	var mandatory []string
	for _, key := range dominantDims(p, values, 2) {
		if fragment := pickFragment(p, key, values[key]); fragment != "" {
			mandatory = append(mandatory, fragment)
		}
	}
	if constraint := behaviorConstraint(b); constraint != "" {
		mandatory = append(mandatory, constraint)
	}
	self := sanitize(p.Injection.SelfTalk)
	if len(mandatory) == 0 && self == "" {
		return ""
	}

	render := func(optional []string) string {
		lines := append(append([]string{}, mandatory...), optional...)
		var out strings.Builder
		out.WriteString(fullHeader)
		if len(lines) > 0 {
			out.WriteString("\n")
			// Each fragment was sanitised individually; joining them must not
			// re-apply the 40-rune per-fragment cap to the whole block.
			out.WriteString(strings.Join(lines, "；"))
			out.WriteString("。")
		}
		if self != "" {
			out.WriteString("\n")
			out.WriteString(fullSelfTalkHeader)
			out.WriteString("\n")
			out.WriteString(self)
		}
		return out.String()
	}

	// Optional parts, in dropping order: residue is least important, time is
	// next. Mandatory content is allowed to overflow into truncateTokens only as
	// a last resort.
	text := render(nil)
	time := timeFragment(p, now)
	if time != "" {
		withTime := render([]string{time})
		if approxTokens(withTime) <= budget {
			text = withTime
			if residue := eventResidue(p, marks, now); residue != "" {
				withResidue := render([]string{time, residue})
				if approxTokens(withResidue) <= budget {
					text = withResidue
				}
			}
		}
	}
	return text
}

// dominantDims returns up to n dimension keys ordered by the absolute distance
// from the baseline. Ties keep the canonical dimension order so two calls over
// the same state always produce the same block.
func dominantDims(p *Pack, dims map[string]float64, n int) []string {
	type scored struct {
		key   string
		score float64
		order int
	}
	scores := make([]scored, 0, len(p.Dimensions))
	for i, d := range p.Dimensions {
		score := dims[d.Key] - d.Baseline
		if score < 0 {
			score = -score
		}
		scores = append(scores, scored{key: d.Key, score: score, order: i})
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].score == scores[j].score {
			return scores[i].order < scores[j].order
		}
		return scores[i].score > scores[j].score
	})
	if n > len(scores) {
		n = len(scores)
	}
	out := make([]string, 0, n)
	for _, s := range scores[:n] {
		out = append(out, s.key)
	}
	return out
}

func pickFragment(p *Pack, key string, value float64) string {
	frags, ok := p.Injection.Fragments[key]
	if !ok {
		return ""
	}
	band := frags[DimBand(value)]
	if len(band) == 0 {
		band = frags["mid"]
	}
	if len(band) == 0 {
		return ""
	}
	// Deterministic rotation by value: the same state keeps the same wording
	// while a visibly different value gets a different line.
	idx := int(value) % len(band)
	return sanitize(band[idx])
}

func timeFragment(p *Pack, now time.Time) string {
	band := p.Injection.TimeFragments[TimeBand(now)]
	if len(band) == 0 {
		return ""
	}
	return sanitize(band[now.Hour()%len(band)])
}

// firstClause keeps one short clause of a longer sentence, for the line budget.
func firstClause(s string, maxRunes int) string {
	clean := sanitize(s)
	for _, sep := range []string{"；", "。", "，", ",", ";"} {
		if idx := strings.Index(clean, sep); idx > 0 {
			clean = clean[:idx]
			break
		}
	}
	runes := []rune(clean)
	if maxRunes > 0 && len(runes) > maxRunes {
		clean = string(runes[:maxRunes])
	}
	return strings.TrimSpace(clean)
}

func eventResidue(p *Pack, marks []EventMark, now time.Time) string {
	var latest *EventMark
	var latestAt time.Time
	for i := range marks {
		at, err := time.Parse(time.RFC3339, marks[i].At)
		if err != nil || now.Sub(at) > residueWindow || at.After(now) {
			continue
		}
		if latest == nil || at.After(latestAt) {
			latest = &marks[i]
			latestAt = at
		}
	}
	if latest == nil {
		return ""
	}
	ev, ok := p.EventByID(latest.ID)
	if !ok {
		return ""
	}
	label := sanitize(ev.Label)
	switch ev.Tone {
	case "positive", "strong":
		return fmt.Sprintf("刚才「%s」的余韵还在，语气里可以带一点", label)
	case "negative":
		return fmt.Sprintf("刚才「%s」那点不痛快还没散，别装作没事", label)
	default:
		return fmt.Sprintf("刚才「%s」的事还留着一点印象", label)
	}
}

func behaviorConstraint(b Behavior) string {
	var parts []string
	if b.MaxCharsFactor > 0 && b.MaxCharsFactor < 0.95 {
		parts = append(parts, "话少一点")
	}
	if b.MaxSegments == 1 {
		parts = append(parts, "只回一条")
	}
	if b.HumanizeScale > 1.1 {
		parts = append(parts, "别急着回")
	}
	if b.SilenceBoost > 0 {
		parts = append(parts, "可以不接话")
	}
	if len(parts) == 0 {
		return ""
	}
	return "这一轮" + strings.Join(parts, "、")
}

// sanitize is the injection firewall for dynamic copy: one line, no quotes or
// brackets the model could mistake for structure, at most 40 runes.
func sanitize(s string) string {
	replacer := strings.NewReplacer(
		"\r", " ", "\n", " ", "\t", " ",
		"`", "", "\"", "", "'", "", "“", "", "”", "", "‘", "", "’", "",
		"(", "", ")", "", "（", "", "）", "", "[", "", "]", "", "【", "", "】", "",
		"{", "", "}", "", "<", "", ">", "", "《", "", "》", "", "「", "", "」", "",
		"『", "", "』", "",
	)
	out := strings.Join(strings.Fields(replacer.Replace(s)), " ")
	runes := []rune(out)
	if len(runes) > 40 {
		out = string(runes[:40])
	}
	return strings.TrimSpace(out)
}

// approxTokens mirrors the reply pipeline's estimate: one token per CJK rune,
// one per four ASCII characters. It only guards the injection budget.
func approxTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk, other := 0, 0
	for _, r := range s {
		if r >= 0x2E80 {
			cjk++
		} else {
			other++
		}
	}
	return cjk + (other+3)/4
}

// truncateTokens cuts on rune boundaries so the block never ends mid-character.
func truncateTokens(s string, max int) string {
	if max <= 0 || approxTokens(s) <= max {
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	for _, r := range runes {
		candidate := b.String() + string(r)
		if approxTokens(candidate) > max {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
