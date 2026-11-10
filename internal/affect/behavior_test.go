package affect

import "testing"

func TestBehaviorDefaultsWhenNoBandMatches(t *testing.T) {
	p := DefaultPack()
	dims := BaselineDims(p)
	got := BehaviorFor(p, dims)
	if got != DefaultBehavior() {
		t.Fatalf("baseline state should produce the default behavior: %+v", got)
	}
}

func TestBehaviorLowEnergyBand(t *testing.T) {
	p := DefaultPack()
	dims := BaselineDims(p)
	dims["energy"] = 20
	got := BehaviorFor(p, dims)
	if got.Tone != "low_battery" {
		t.Fatalf("low energy tone = %q, want low_battery", got.Tone)
	}
	if got.MaxCharsFactor >= 1 || got.MaxSegments != 1 {
		t.Fatalf("low energy should shorten replies: %+v", got)
	}
	if got.HumanizeScale <= 1 || got.SilenceBoost <= 0 {
		t.Fatalf("low energy should slow down and raise silence: %+v", got)
	}
}

func TestEvalWhen(t *testing.T) {
	dims := map[string]float64{"energy": 20, "valence": 80, "stress": 10}
	cases := []struct {
		expr string
		want bool
	}{
		{"energy<25", true},
		{"energy<=20", true},
		{"energy>20", false},
		{"energy<25 && valence>75", true},
		{"energy<25, stress>50", false},
		{"unknown<5", false},
		{"bogus", false},
	}
	for _, tc := range cases {
		if got := EvalWhen(tc.expr, dims); got != tc.want {
			t.Fatalf("EvalWhen(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestProactiveTone(t *testing.T) {
	if ProactiveTone(DefaultBehavior()) != "" {
		t.Fatal("default behavior should have no proactive tone")
	}
	if ProactiveTone(Behavior{Tone: "low_battery"}) == "" {
		t.Fatal("low_battery should have a proactive hint")
	}
}
