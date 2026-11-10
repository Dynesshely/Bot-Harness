package affect

import (
	"math"
	"testing"
	"time"
)

func TestDecayTowardsBaselineNotZero(t *testing.T) {
	p := DefaultPack()
	dims := BaselineDims(p)
	dims["valence"] = 90
	got := Decay(p, dims, time.Hour)
	if got["valence"] >= 90 || got["valence"] <= p.dims["valence"].Baseline {
		t.Fatalf("valence should move towards the baseline, got %.2f", got["valence"])
	}
	// One call is capped at 24h, so the value is closer to the baseline but
	// never below it (and certainly never zero).
	long := Decay(p, map[string]float64{"valence": 90}, 80*time.Hour)
	capped := Decay(p, map[string]float64{"valence": 90}, 24*time.Hour)
	if math.Abs(long["valence"]-capped["valence"]) > 0.01 {
		t.Fatalf("dt must be capped at 24h: %v vs %v", long["valence"], capped["valence"])
	}
	if long["valence"] < p.dims["valence"].Baseline {
		t.Fatalf("decay must not undershoot the baseline, got %.2f", long["valence"])
	}
	if long["valence"] == 0 {
		t.Fatal("decay must land on the baseline, not zero")
	}
}

func TestStepCapsDtAt24Hours(t *testing.T) {
	p := DefaultPack()
	a := Step(p, map[string]float64{"valence": 90}, 24*time.Hour)
	b := Step(p, map[string]float64{"valence": 90}, 1000*time.Hour)
	if a["valence"] != b["valence"] {
		t.Fatalf("dt must be capped at 24h: %v vs %v", a["valence"], b["valence"])
	}
}

func TestCouplingIsOneWayCap(t *testing.T) {
	p := DefaultPack()
	base := BaselineDims(p)
	// High stress caps patience, and patience above the cap decays towards it.
	base["stress"] = 90
	base["patience"] = 100
	capped := Couple(p, base, time.Hour)
	if capped["patience"] >= 100 {
		t.Fatalf("high stress must cap patience, got %.2f", capped["patience"])
	}
	// Low stress must never lift a low patience value.
	low := BaselineDims(p)
	low["stress"] = 0
	low["patience"] = 10
	if got := Couple(p, low, time.Hour); got["patience"] != 10 {
		t.Fatalf("coupling must not lift the target: %.2f", got["patience"])
	}
}

func TestEventClampAndRepeatDamping(t *testing.T) {
	p := DefaultPack()
	ev, ok := p.EventByID("praise")
	if !ok {
		t.Fatal("praise event missing")
	}
	dims := BaselineDims(p)
	first := ApplyEvent(p, &ev, dims, 0)
	second := ApplyEvent(p, &ev, dims, 1)
	third := ApplyEvent(p, &ev, dims, 2)
	d1 := first["valence"] - dims["valence"]
	d2 := second["valence"] - dims["valence"]
	d3 := third["valence"] - dims["valence"]
	if !(d1 > d2 && d2 > d3) {
		t.Fatalf("repeats must damp: %.2f %.2f %.2f", d1, d2, d3)
	}
	strong := EventDef{ID: "s", Label: "s", Tone: "negative", Strong: true,
		Deltas: map[string]float64{"stress": 100}, DailyCap: 1}
	got := ApplyEvent(p, &strong, dims, 0)
	if got["stress"]-dims["stress"] > strongEventClamp+0.01 {
		t.Fatalf("strong event clamp exceeded: %.2f", got["stress"]-dims["stress"])
	}
}

func TestApplyPreset(t *testing.T) {
	p := DefaultPack()
	preset, ok := p.PresetByID("low_battery")
	if !ok {
		t.Fatal("low_battery preset missing")
	}
	got := ApplyPreset(p, preset)
	if got["energy"] != 20 {
		t.Fatalf("preset energy = %.2f, want 20", got["energy"])
	}
	if got["stress"] != 55 {
		t.Fatalf("preset stress = %.2f, want 55", got["stress"])
	}
}

func TestDimBandAndTimeBand(t *testing.T) {
	if DimBand(10) != "low" || DimBand(50) != "mid" || DimBand(90) != "high" {
		t.Fatal("DimBand cutoffs changed unexpectedly")
	}
	cases := map[int]string{2: "night", 6: "dawn", 9: "morning", 12: "noon", 15: "afternoon", 20: "evening"}
	for hour, want := range cases {
		now := time.Date(2026, 1, 1, hour, 0, 0, 0, time.Local)
		if got := TimeBand(now); got != want {
			t.Fatalf("hour %d -> %q, want %q", hour, got, want)
		}
	}
}
