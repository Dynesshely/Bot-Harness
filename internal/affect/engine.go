package affect

import (
	"math"
	"time"
)

// Engine constants. They are deliberately package-level and tiny: the state is
// meant to drift, not to be a second config surface.
const (
	// maxStep caps one decay step. After a long pause (a weekend, a redeploy)
	// everything is back at its baseline; simulating weeks of exponential decay
	// would only produce floating-point noise.
	maxStep = 24 * time.Hour

	// eventClamp / strongEventClamp bound one event's total push per dimension.
	eventClamp       = 6.0
	strongEventClamp = 20.0

	// couplingSettleRate is how fast the part above a coupled cap decays.
	couplingSettleRate = 0.4

	// repeatWindow is the window used to damp the same event twice in a row.
	repeatWindow = time.Hour
)

// Clamp bounds v to [lo, hi].
func Clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Round2 keeps stored values at two decimals so JSON stays readable and two
// nodes looking at the same state agree.
func Round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// BaselineDims builds a fresh state at the pack's baselines.
func BaselineDims(p *Pack) map[string]float64 {
	out := map[string]float64{}
	if p == nil {
		return out
	}
	for _, d := range p.Dimensions {
		out[d.Key] = d.Baseline
	}
	return out
}

// NormalizeDims fills missing dimensions from the baseline, clamps everything
// into [0,100] and rounds to two decimals. It never mutates the input.
func NormalizeDims(p *Pack, dims map[string]float64) map[string]float64 {
	out := map[string]float64{}
	if p == nil {
		for k, v := range dims {
			out[k] = Round2(Clamp(v, 0, 100))
		}
		return out
	}
	for _, d := range p.Dimensions {
		v, ok := dims[d.Key]
		if !ok || math.IsNaN(v) {
			v = d.Baseline
		}
		out[d.Key] = Round2(Clamp(v, d.Min, d.Max))
	}
	return out
}

// Decay pulls each dimension back towards its baseline with a half-life. The
// baseline itself is stable: this models "the mood passes", not "the mood
// resets to zero".
func Decay(p *Pack, dims map[string]float64, dt time.Duration) map[string]float64 {
	out := NormalizeDims(p, dims)
	if p == nil || dt <= 0 {
		return out
	}
	if dt > maxStep {
		dt = maxStep
	}
	hours := dt.Hours()
	for _, d := range p.Dimensions {
		if d.HalfLifeHours <= 0 {
			continue
		}
		factor := math.Exp(-math.Ln2 * hours / d.HalfLifeHours)
		out[d.Key] = Round2(Clamp(d.Baseline+(out[d.Key]-d.Baseline)*factor, d.Min, d.Max))
	}
	return out
}

// Couple applies the pack's one-way caps. A cap can only lower a dimension;
// whatever sits above the cap melts away at a fixed exponential rate. The caps
// read a snapshot of the pre-coupling state so their order in the pack cannot
// change the result.
func Couple(p *Pack, dims map[string]float64, dt time.Duration) map[string]float64 {
	out := NormalizeDims(p, dims)
	if p == nil || len(p.Coupling) == 0 {
		return out
	}
	if dt > maxStep {
		dt = maxStep
	}
	if dt < 0 {
		dt = 0
	}
	before := make(map[string]float64, len(out))
	for k, v := range out {
		before[k] = v
	}
	settle := math.Exp(-couplingSettleRate * dt.Hours())
	for _, c := range p.Coupling {
		if c.Mode != "cap" {
			continue
		}
		source, ok := before[c.Source]
		if !ok {
			continue
		}
		capValue := Clamp(c.Base+c.Scale*source, c.Floor, 100)
		target, ok := out[c.Target]
		if !ok || target <= capValue {
			continue
		}
		out[c.Target] = Round2(Clamp(capValue+(target-capValue)*settle, 0, 100))
	}
	return out
}

// Step advances the state by dt: decay towards the baseline, then the coupling
// caps. Callers cap dt themselves only when they want a shorter horizon; Step
// enforces the 24h hard limit.
func Step(p *Pack, dims map[string]float64, dt time.Duration) map[string]float64 {
	if dt <= 0 {
		return NormalizeDims(p, dims)
	}
	if dt > maxStep {
		dt = maxStep
	}
	return Couple(p, Decay(p, dims, dt), dt)
}

// EventFactor damps repeated events: the first one counts fully, then
// 1/2, 1/3, ... This keeps a spammy "哈哈哈" burst from maxing the state.
func EventFactor(repeats int) float64 {
	if repeats < 0 {
		repeats = 0
	}
	return 1 / float64(repeats+1)
}

// EventApplication is one event that fired during a single Observe, together
// with how many times it already happened inside repeatWindow.
type EventApplication struct {
	Event   *EventDef
	Repeats int
}

// ApplyEvent adds one event's deltas. repeats is how many times the same event
// already fired inside repeatWindow. Every dimension is clamped into the
// dimension's range; the event's own magnitude is clamped to ±6 (±20 when the
// pack marks it strong).
func ApplyEvent(p *Pack, ev *EventDef, dims map[string]float64, repeats int) map[string]float64 {
	if ev == nil {
		return NormalizeDims(p, dims)
	}
	return ApplyEvents(p, dims, []EventApplication{{Event: ev, Repeats: repeats}})
}

// ApplyEvents applies a whole Observe batch. The *sum* of the batch is clamped
// to ±6 per dimension (strong events widen it to ±20), so a message that trips
// four patterns cannot push a dimension four times as far as one pattern.
func ApplyEvents(p *Pack, dims map[string]float64, apps []EventApplication) map[string]float64 {
	out := NormalizeDims(p, dims)
	if p == nil || len(apps) == 0 {
		return out
	}
	strong := false
	total := map[string]float64{}
	for _, app := range apps {
		if app.Event == nil {
			continue
		}
		if app.Event.Strong || app.Event.Tone == "strong" {
			strong = true
		}
		factor := EventFactor(app.Repeats)
		for key, delta := range app.Event.Deltas {
			if _, ok := p.Dimension(key); !ok {
				continue
			}
			total[key] += delta * factor
		}
	}
	limit := eventClamp
	if strong {
		limit = strongEventClamp
	}
	for key, push := range total {
		d, ok := p.Dimension(key)
		if !ok {
			continue
		}
		out[key] = Round2(Clamp(out[key]+Clamp(push, -limit, limit), d.Min, d.Max))
	}
	return out
}

// ApplyPreset returns the preset's dimensions, normalised against the pack.
// Unknown dimension keys are ignored; missing ones fall back to the baseline.
func ApplyPreset(p *Pack, preset Preset) map[string]float64 {
	out := BaselineDims(p)
	for key, v := range preset.Dims {
		if _, ok := p.Dimension(key); ok {
			out[key] = v
		}
	}
	return NormalizeDims(p, out)
}

// DimBand maps a value to low/mid/high using the pack's copy bands. The cutoffs
// are deliberately wide: a dimension should feel like "roughly low" rather than
// flicker between adjectives across a two-point drift.
func DimBand(v float64) string {
	switch {
	case v < 35:
		return "low"
	case v < 65:
		return "mid"
	default:
		return "high"
	}
}

// TimeBand maps the local clock to the pack's time_fragments keys.
func TimeBand(now time.Time) string {
	switch hour := now.Hour(); {
	case hour >= 23 || hour < 5:
		return "night"
	case hour >= 5 && hour < 7:
		return "dawn"
	case hour >= 7 && hour < 11:
		return "morning"
	case hour >= 11 && hour < 14:
		return "noon"
	case hour >= 14 && hour < 18:
		return "afternoon"
	default:
		return "evening"
	}
}
