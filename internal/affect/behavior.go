package affect

import (
	"fmt"
	"strconv"
	"strings"
)

// Behavior is the soft, reply-pacing side of the state. Zero/one values mean
// "unchanged": a default Behavior leaves the pipeline exactly as it was.
type Behavior struct {
	MaxCharsFactor   float64 `json:"max_chars_factor"`
	MaxSegments      int     `json:"max_segments"`
	HumanizeScale    float64 `json:"humanize_scale"`
	SilenceBoost     float64 `json:"silence_boost"`
	InitiativeFactor float64 `json:"initiative_factor"`
	Tone             string  `json:"tone,omitempty"`
	Reason           string  `json:"reason,omitempty"`
}

// DefaultBehavior is the identity element: multiplying by it changes nothing.
func DefaultBehavior() Behavior {
	return Behavior{
		MaxCharsFactor:   1,
		MaxSegments:      0,
		HumanizeScale:    1,
		SilenceBoost:     0,
		InitiativeFactor: 1,
	}
}

// BehaviorFor picks the first matching band. Bands are evaluated in pack order,
// so more specific conditions go first. A nil pack or an unparsable expression
// yields the default behavior rather than an error: pacing must never break a
// reply.
func BehaviorFor(p *Pack, dims map[string]float64) Behavior {
	out := DefaultBehavior()
	if p == nil {
		return out
	}
	values := NormalizeDims(p, dims)
	for _, band := range p.Behavior.Bands {
		if !EvalWhen(band.When, values) {
			continue
		}
		out.MaxCharsFactor = band.MaxCharsFactor
		if out.MaxCharsFactor <= 0 {
			out.MaxCharsFactor = 1
		}
		out.MaxSegments = band.MaxSegments
		out.HumanizeScale = band.HumanizeScale
		if out.HumanizeScale <= 0 {
			out.HumanizeScale = 1
		}
		out.SilenceBoost = Clamp(band.SilenceBoost, 0, 1)
		// A missing initiative_factor must not silently turn proactive off.
		out.InitiativeFactor = band.InitiativeFactor
		if out.InitiativeFactor == 0 {
			out.InitiativeFactor = 1
		}
		out.Tone = band.Tone
		out.Reason = band.When
		return out
	}
	return out
}

// EvalWhen evaluates a small comparison expression such as "energy<25" or
// "valence>75 && energy>60". Only && (and comma) are supported: the pack format
// does not need OR, and keeping the grammar tiny keeps it auditable.
func EvalWhen(expr string, dims map[string]float64) bool {
	terms := splitTerms(expr)
	if len(terms) == 0 {
		return false
	}
	for _, term := range terms {
		if !evalTerm(term, dims) {
			return false
		}
	}
	return true
}

func splitTerms(expr string) []string {
	replaced := strings.ReplaceAll(expr, "&&", ",")
	parts := strings.Split(replaced, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func evalTerm(term string, dims map[string]float64) bool {
	op := ""
	idx := -1
	for _, candidate := range []string{"<=", ">=", "==", "<", ">"} {
		if i := strings.Index(term, candidate); i >= 0 {
			op, idx = candidate, i
			break
		}
	}
	if idx <= 0 {
		return false
	}
	key := strings.TrimSpace(term[:idx])
	want, err := strconv.ParseFloat(strings.TrimSpace(term[idx+len(op):]), 64)
	if err != nil {
		return false
	}
	got, ok := dims[key]
	if !ok {
		return false
	}
	switch op {
	case "<":
		return got < want
	case "<=":
		return got <= want
	case ">":
		return got > want
	case ">=":
		return got >= want
	case "==":
		return got == want
	}
	return false
}

// ProactiveTone is a short phrase used by the proactive prompt when the pack
// wants the bot to feel less energetic. Empty means "no extra hint".
func ProactiveTone(b Behavior) string {
	switch b.Tone {
	case "low_battery":
		return "现在有点乏，别硬撑着多说。"
	case "low":
		return "现在兴致不高，语气别太热闹。"
	case "tense":
		return "现在有点绷着，说话短一点。"
	case "tired":
		return "现在有点困，简单说。"
	case "up":
		return "现在状态不错，可以自然一点。"
	default:
		return ""
	}
}

// Describe renders a compact debug line for logs.
func Describe(b Behavior) string {
	return fmt.Sprintf("chars=%.2f segments=%d humanize=%.2f silence=%.2f initiative=%.2f tone=%s",
		b.MaxCharsFactor, b.MaxSegments, b.HumanizeScale, b.SilenceBoost, b.InitiativeFactor, b.Tone)
}
