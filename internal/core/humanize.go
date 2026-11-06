package core

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/settings"
)

// Humanize levels. "off" keeps the bot instant; "medium" is the default and the
// only level tuned so far.
const (
	HumanizeOff    = "off"
	HumanizeSoft   = "soft"
	HumanizeMedium = "medium"
	HumanizeStrong = "strong"
)

// humanizeParams is the numeric side of "sounding like a person": how long to
// wait, how much to pause between bubbles, and how often to stay quiet.
type humanizeParams struct {
	// BaseDelay is the minimum "reading" pause before the first bubble.
	BaseDelay time.Duration
	// Per10Runes approximates typing speed.
	Per10Runes time.Duration
	// MaxDelay caps the whole pause.
	MaxDelay time.Duration
	// Jitter is the ±fraction applied to the delay.
	Jitter float64
	// SegmentPause is the gap between bubbles of one answer.
	SegmentPause time.Duration
	// SilenceProb is the chance of ignoring an *unaddressed* message.
	SilenceProb float64
}

func humanizeParamsFor(level string) humanizeParams {
	switch level {
	case HumanizeSoft:
		return humanizeParams{
			BaseDelay: 400 * time.Millisecond, Per10Runes: 250 * time.Millisecond,
			MaxDelay: 3 * time.Second, Jitter: 0.3,
			SegmentPause: 250 * time.Millisecond, SilenceProb: 0,
		}
	case HumanizeMedium:
		return humanizeParams{
			BaseDelay: 800 * time.Millisecond, Per10Runes: 600 * time.Millisecond,
			MaxDelay: 8 * time.Second, Jitter: 0.4,
			SegmentPause: 650 * time.Millisecond, SilenceProb: 0.10,
		}
	case HumanizeStrong:
		return humanizeParams{
			BaseDelay: 1500 * time.Millisecond, Per10Runes: 900 * time.Millisecond,
			MaxDelay: 15 * time.Second, Jitter: 0.5,
			SegmentPause: 1100 * time.Millisecond, SilenceProb: 0.25,
		}
	default: // off
		return humanizeParams{}
	}
}

// humanizeDelay computes the pause before the first bubble.
//
// `jitter` is a value in [-1, 1] so the caller can inject randomness (and tests
// can pin it). The caller subtracts whatever the model already spent, so a slow
// turn is not delayed further on top.
func humanizeDelay(level string, runes int, scale, jitter float64) time.Duration {
	p := humanizeParamsFor(level)
	if p.MaxDelay == 0 {
		return 0
	}
	if scale <= 0 {
		scale = 1
	}
	d := p.BaseDelay + time.Duration(runes/10)*p.Per10Runes
	d = d + time.Duration(float64(d)*p.Jitter*jitter)
	d = time.Duration(float64(d) * scale)
	if d < 0 {
		return 0
	}
	if limit := time.Duration(float64(p.MaxDelay) * scale); d > limit {
		return limit
	}
	return d
}

// segmentPause is the gap between two bubbles of the same answer.
func segmentPause(level string, scale float64) time.Duration {
	p := humanizeParamsFor(level)
	if p.SegmentPause == 0 {
		return 0
	}
	if scale <= 0 {
		scale = 1
	}
	// ±35% so the cadence never feels metronomic.
	f := 1 + (rand.Float64()*0.7 - 0.35)
	return time.Duration(float64(p.SegmentPause) * scale * f)
}

// silenceProbability is the chance of not answering a message that was not
// addressed to the bot. Messages that @ it are always answered.
func silenceProbability(level string) float64 {
	return humanizeParamsFor(level).SilenceProb
}

// moodLine turns the wall clock and the group's recent activity into one short
// instruction. It is derived rather than persisted: a mood that drifts on its own
// would be both surprising and hard to explain in the audit trail.
func moodLine(now time.Time, busy bool) string {
	hour := now.Hour()
	switch {
	case hour >= 1 && hour < 6:
		return "现在是深夜，对方多半在熬夜：回复更短、更轻，不要长篇大论。"
	case hour >= 6 && hour < 11:
		return "现在是早上：语气可以清爽一点。"
	case hour >= 22:
		return "现在接近深夜：回复简短一点，别拖住对方。"
	}
	if busy {
		return "群里正聊得热闹：可以更松弛、更短，不要抢话。"
	}
	return ""
}

// humanizePrompt is the prompt-side half of the feature: style guards, persona
// and the current mood. Empty when the level is off.
func humanizePrompt(level string, persona string, now time.Time, busy bool) string {
	if level == HumanizeOff || level == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("说话方式：像群里一个真实的人，不要像客服或文档。")
	b.WriteString("\n- 默认不用列表和标题，除非内容本来就需要分点；不要用 Markdown 表格。")
	b.WriteString("\n- 不要说「作为 AI」「根据我的训练数据」这类话，也不要重复对方的问题。")
	b.WriteString("\n- 一句话能说清就别写三段；拿不准就直说拿不准。")
	b.WriteString("\n- 记得对方说过的事时，可以自然地提一句，但不要刻意炫耀记性。")
	if p := strings.TrimSpace(persona); p != "" {
		b.WriteString("\n人设：" + p)
	}
	if m := moodLine(now, busy); m != "" {
		b.WriteString("\n" + m)
	}
	return b.String()
}

// busyWindow decides whether the group has been chatty lately.
func busyWindow(level string) int32 {
	switch level {
	case HumanizeStrong:
		return 5
	case HumanizeMedium:
		return 3
	default:
		return 0
	}
}

// humanizeDelayForTurn subtracts the time the model already spent, so the total
// wait feels like reading + typing rather than reading + typing + generation.
func humanizeDelayForTurn(level string, runes int, scale float64, elapsed time.Duration) time.Duration {
	target := humanizeDelay(level, runes, scale, rand.Float64()*2-1)
	if elapsed >= target {
		return 0
	}
	return target - elapsed
}

// humanizeLevel resolves the effective level for a group (override > global).
func humanizeLevel(s settings.Settings, cfg settings.GroupConfig) string {
	if cfg.HumanizeLevel != "" {
		return cfg.HumanizeLevel
	}
	if s.HumanizeLevel != "" {
		return s.HumanizeLevel
	}
	return HumanizeMedium
}

// describeHumanize is used by the settings page preview and logs.
func describeHumanize(level string, scale float64) string {
	p := humanizeParamsFor(level)
	if p.MaxDelay == 0 {
		return "off：立即回复，无停顿"
	}
	return fmt.Sprintf("%s：基础 %v + 每 10 字 %v（上限 %v），分段间隔约 %v，未 @ 时沉默概率 %.0f%%",
		level, p.BaseDelay, p.Per10Runes, time.Duration(float64(p.MaxDelay)*scale),
		p.SegmentPause, p.SilenceProb*100)
}

// noteInbound records that a message arrived (for the "busy group" signal).
func (d *Dispatcher) noteInbound() {
	now := time.Now()
	d.recentMu.Lock()
	defer d.recentMu.Unlock()
	d.recentIn = append(d.recentIn, now)
	if len(d.recentIn) > 64 {
		d.recentIn = d.recentIn[len(d.recentIn)-64:]
	}
}

// groupBusy reports whether the group is chatty right now.
func (d *Dispatcher) groupBusy(level string) bool {
	threshold := busyWindow(level)
	if threshold <= 0 {
		return false
	}
	cutoff := time.Now().Add(-5 * time.Minute)
	d.recentMu.Lock()
	defer d.recentMu.Unlock()
	n := 0
	for _, at := range d.recentIn {
		if at.After(cutoff) {
			n++
		}
	}
	return n >= int(threshold)
}

// promptFor resolves the prompt for a specific group: base + memory + humanize.
func (d *Dispatcher) promptFor(s settings.Settings, cfg settings.GroupConfig) string {
	prompt := d.ResolvePrompt()
	level := humanizeLevel(s, cfg)
	if extra := humanizePrompt(level, s.Persona, time.Now(), d.groupBusy(level)); extra != "" {
		prompt += "\n\n" + extra
	}
	return prompt
}

// rollSilence returns a number in [0,1); tests override it to make the decision
// deterministic.
func (d *Dispatcher) rollSilence() float64 {
	if d.silenceRoll != nil {
		return d.silenceRoll()
	}
	return rand.Float64()
}
