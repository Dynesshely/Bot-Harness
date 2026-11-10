// Package affect implements the "emotional state" framework: five slowly
// drifting dimensions, event classification, prompt injection and reply-pacing
// hints. Everything here is optional: with the feature disabled (the default)
// the core pipeline behaves exactly as if this package did not exist.
package affect

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// DimensionKeys is the fixed set of dimensions every pack must define.
var DimensionKeys = []string{"valence", "energy", "stress", "closeness", "patience"}

// Dimension is one axis of the state, with its decay characteristics.
type Dimension struct {
	Key           string  `json:"key"`
	Label         string  `json:"label"`
	Min           float64 `json:"min"`
	Max           float64 `json:"max"`
	Baseline      float64 `json:"baseline"`
	HalfLifeHours float64 `json:"half_life_hours"`
}

// Coupling is a one-way cap: the source dimension can only lower (or, with a
// positive scale, leave untouched) the ceiling of the target. It never pulls a
// value up, so it cannot manufacture a mood out of nothing.
type Coupling struct {
	Source string  `json:"source"`
	Target string  `json:"target"`
	Mode   string  `json:"mode"`
	Base   float64 `json:"base"`
	Scale  float64 `json:"scale"`
	Floor  float64 `json:"floor"`
}

// EventDef is a safe, explicitly listed conversational event.
type EventDef struct {
	ID       string             `json:"id"`
	Label    string             `json:"label"`
	Tone     string             `json:"tone"`
	Deltas   map[string]float64 `json:"deltas"`
	Patterns []string           `json:"patterns"`
	DailyCap int                `json:"daily_cap"`
	// Strong widens the single-event clamp from ±6 to ±20.
	Strong bool `json:"strong,omitempty"`

	compiled []*regexp.Regexp
}

// Fragments maps a low/mid/high band to copy for one dimension.
type Fragments map[string][]string

// Injection holds the static, human-written copy used to build prompt blocks.
type Injection struct {
	SelfTalk      string               `json:"self_talk"`
	Fragments     map[string]Fragments `json:"fragments"`
	TimeFragments map[string][]string  `json:"time_fragments"`
}

// Band is one behaviour rule, evaluated top to bottom; the first match wins.
type Band struct {
	When             string  `json:"when"`
	MaxCharsFactor   float64 `json:"max_chars_factor"`
	MaxSegments      int     `json:"max_segments"`
	HumanizeScale    float64 `json:"humanize_scale"`
	SilenceBoost     float64 `json:"silence_boost"`
	InitiativeFactor float64 `json:"initiative_factor"`
	Tone             string  `json:"tone"`
}

// BehaviorSection groups the pack's behaviour rules.
type BehaviorSection struct {
	Bands []Band `json:"bands"`
}

// Preset is a named starting point, used by the admin panel and /mood preset.
type Preset struct {
	ID   string             `json:"id"`
	Name string             `json:"name"`
	Dims map[string]float64 `json:"dims"`
}

// Pack is the complete content bundle: dimensions, coupling, events, copy,
// behaviour bands and presets. A pack is immutable once loaded.
type Pack struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Dimensions  []Dimension     `json:"dimensions"`
	Coupling    []Coupling      `json:"coupling"`
	Events      []EventDef      `json:"events"`
	Injection   Injection       `json:"injection"`
	Behavior    BehaviorSection `json:"behavior"`
	Presets     []Preset        `json:"presets"`

	dims map[string]Dimension `json:"-"`
}

// PackInfo is the lightweight catalogue entry returned by Service.Packs.
type PackInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Dimensions  []string `json:"dimensions"`
	Events      int      `json:"events"`
	Presets     int      `json:"presets"`
}

//go:embed packs/*.json
var packFS embed.FS

const defaultPackID = "daily"

var (
	packsOnce sync.Once
	packs     map[string]*Pack
	packsErr  error
)

// BuiltinPacks returns every embedded pack, validated. The maps and packs are
// shared and must not be mutated.
func BuiltinPacks() (map[string]*Pack, error) {
	packsOnce.Do(func() {
		entries, err := packFS.ReadDir("packs")
		if err != nil {
			packsErr = fmt.Errorf("affect: read packs: %w", err)
			return
		}
		out := make(map[string]*Pack, len(entries))
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			raw, err := packFS.ReadFile("packs/" + e.Name())
			if err != nil {
				packsErr = fmt.Errorf("affect: read %s: %w", e.Name(), err)
				return
			}
			p, err := decodePack(raw)
			if err != nil {
				packsErr = fmt.Errorf("affect: pack %s: %w", e.Name(), err)
				return
			}
			if _, dup := out[p.ID]; dup {
				packsErr = fmt.Errorf("affect: duplicate pack id %q", p.ID)
				return
			}
			out[p.ID] = p
		}
		if len(out) == 0 {
			packsErr = fmt.Errorf("affect: no built-in packs")
			return
		}
		packs = out
	})
	return packs, packsErr
}

// DefaultPack returns the built-in "daily" pack, falling back to any valid pack
// when the configured id is unknown. A nil pack means the embedded data is
// broken; callers must handle that by behaving as if the feature is off.
func DefaultPack() *Pack {
	all, err := BuiltinPacks()
	if err != nil {
		return nil
	}
	if p := all[defaultPackID]; p != nil {
		return p
	}
	for _, p := range all {
		return p
	}
	return nil
}

// PackByID looks up a pack, returning the default when the id is empty/unknown.
func PackByID(id string) *Pack {
	all, err := BuiltinPacks()
	if err != nil {
		return nil
	}
	if p := all[strings.TrimSpace(id)]; p != nil {
		return p
	}
	return DefaultPack()
}

// PackInfos lists packs in stable id order.
func PackInfos() []PackInfo {
	all, err := BuiltinPacks()
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]PackInfo, 0, len(ids))
	for _, id := range ids {
		p := all[id]
		labels := make([]string, 0, len(p.Dimensions))
		for _, d := range p.Dimensions {
			labels = append(labels, d.Label)
		}
		out = append(out, PackInfo{
			ID: p.ID, Name: p.Name, Version: p.Version, Description: p.Description,
			Dimensions: labels, Events: len(p.Events), Presets: len(p.Presets),
		})
	}
	return out
}

func decodePack(raw []byte) (*Pack, error) {
	var p Pack
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	p.dims = make(map[string]Dimension, len(p.Dimensions))
	for _, d := range p.Dimensions {
		p.dims[d.Key] = d
	}
	return &p, nil
}

// Dimension returns one dimension definition.
func (p *Pack) Dimension(key string) (Dimension, bool) {
	if p == nil {
		return Dimension{}, false
	}
	d, ok := p.dims[key]
	return d, ok
}

// Dims returns the dimension definitions in canonical order.
func (p *Pack) Dims() []Dimension {
	if p == nil {
		return nil
	}
	return p.Dimensions
}

// PresetByID looks up a preset.
func (p *Pack) PresetByID(id string) (Preset, bool) {
	if p == nil {
		return Preset{}, false
	}
	for _, preset := range p.Presets {
		if preset.ID == id {
			return preset, true
		}
	}
	return Preset{}, false
}

// EventByID looks up an event definition.
func (p *Pack) EventByID(id string) (EventDef, bool) {
	if p == nil {
		return EventDef{}, false
	}
	for _, ev := range p.Events {
		if ev.ID == id {
			return ev, true
		}
	}
	return EventDef{}, false
}

func (p *Pack) validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("id must not be empty")
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("name must not be empty")
	}
	if len(p.Dimensions) != len(DimensionKeys) {
		return fmt.Errorf("must define exactly %d dimensions, got %d", len(DimensionKeys), len(p.Dimensions))
	}
	seenDims := map[string]bool{}
	for _, d := range p.Dimensions {
		if !containsKey(DimensionKeys, d.Key) || seenDims[d.Key] {
			return fmt.Errorf("unknown or duplicate dimension %q", d.Key)
		}
		seenDims[d.Key] = true
		if d.Min != 0 || d.Max != 100 {
			return fmt.Errorf("dimension %q must use [0,100], got [%v,%v]", d.Key, d.Min, d.Max)
		}
		if d.Baseline < d.Min || d.Baseline > d.Max {
			return fmt.Errorf("dimension %q baseline out of range", d.Key)
		}
		if d.HalfLifeHours <= 0 {
			return fmt.Errorf("dimension %q half_life_hours must be positive", d.Key)
		}
	}
	for _, key := range DimensionKeys {
		if !seenDims[key] {
			return fmt.Errorf("missing dimension %q", key)
		}
	}
	for i, c := range p.Coupling {
		if !containsKey(DimensionKeys, c.Source) || !containsKey(DimensionKeys, c.Target) {
			return fmt.Errorf("coupling[%d] names an unknown dimension", i)
		}
		if c.Mode != "cap" {
			return fmt.Errorf("coupling[%d] mode %q is not supported (only cap)", i, c.Mode)
		}
		if c.Floor < 0 || c.Floor > 100 {
			return fmt.Errorf("coupling[%d] floor out of range", i)
		}
	}
	seenEvents := map[string]bool{}
	for i := range p.Events {
		ev := &p.Events[i]
		if strings.TrimSpace(ev.ID) == "" || seenEvents[ev.ID] {
			return fmt.Errorf("events[%d] has an empty or duplicate id", i)
		}
		seenEvents[ev.ID] = true
		switch ev.Tone {
		case "positive", "negative", "neutral", "strong":
		default:
			return fmt.Errorf("event %q has invalid tone %q", ev.ID, ev.Tone)
		}
		if ev.DailyCap < 1 {
			return fmt.Errorf("event %q daily_cap must be >= 1", ev.ID)
		}
		for key := range ev.Deltas {
			if !containsKey(DimensionKeys, key) {
				return fmt.Errorf("event %q delta names unknown dimension %q", ev.ID, key)
			}
		}
		if len(ev.Patterns) == 0 {
			return fmt.Errorf("event %q has no patterns", ev.ID)
		}
		compiled := make([]*regexp.Regexp, 0, len(ev.Patterns))
		for _, pattern := range ev.Patterns {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("event %q pattern %q: %w", ev.ID, pattern, err)
			}
			compiled = append(compiled, re)
		}
		ev.compiled = compiled
	}
	for _, key := range DimensionKeys {
		frags, ok := p.Injection.Fragments[key]
		if !ok {
			return fmt.Errorf("injection.fragments is missing dimension %q", key)
		}
		for _, band := range []string{"low", "mid", "high"} {
			if len(frags[band]) == 0 {
				return fmt.Errorf("injection.fragments[%q][%q] is empty", key, band)
			}
		}
	}
	for i, band := range p.Behavior.Bands {
		if strings.TrimSpace(band.When) == "" {
			return fmt.Errorf("behavior.bands[%d].when is empty", i)
		}
		if band.MaxSegments < 0 || band.MaxSegments > 10 {
			return fmt.Errorf("behavior.bands[%d].max_segments out of range", i)
		}
		if band.MaxCharsFactor <= 0 || band.MaxCharsFactor > 2 {
			return fmt.Errorf("behavior.bands[%d].max_chars_factor must be within (0,2]", i)
		}
		if band.HumanizeScale <= 0 || band.HumanizeScale > 4 {
			return fmt.Errorf("behavior.bands[%d].humanize_scale must be within (0,4]", i)
		}
		if band.SilenceBoost < 0 || band.SilenceBoost > 1 {
			return fmt.Errorf("behavior.bands[%d].silence_boost must be within [0,1]", i)
		}
		if band.InitiativeFactor < 0 || band.InitiativeFactor > 2 {
			return fmt.Errorf("behavior.bands[%d].initiative_factor must be within [0,2]", i)
		}
	}
	seenPresets := map[string]bool{}
	for i, preset := range p.Presets {
		if strings.TrimSpace(preset.ID) == "" || seenPresets[preset.ID] {
			return fmt.Errorf("presets[%d] has an empty or duplicate id", i)
		}
		seenPresets[preset.ID] = true
		for key, v := range preset.Dims {
			if !containsKey(DimensionKeys, key) {
				return fmt.Errorf("preset %q names unknown dimension %q", preset.ID, key)
			}
			if v < 0 || v > 100 || math.IsNaN(v) {
				return fmt.Errorf("preset %q value for %q is out of range", preset.ID, key)
			}
		}
	}
	return nil
}

func containsKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}
