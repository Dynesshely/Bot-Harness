package affect

import "testing"

func TestBuiltinPacksAreValid(t *testing.T) {
	all, err := BuiltinPacks()
	if err != nil {
		t.Fatalf("built-in packs must be valid: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no packs embedded")
	}
	daily := all["daily"]
	if daily == nil {
		t.Fatal("the daily pack must exist")
	}
	if len(daily.Dimensions) != len(DimensionKeys) {
		t.Fatalf("daily pack dimensions = %d, want %d", len(daily.Dimensions), len(DimensionKeys))
	}
	if len(daily.Events) < 10 {
		t.Fatalf("daily pack has %d events, want at least 10", len(daily.Events))
	}
	if len(daily.Coupling) < 2 {
		t.Fatalf("daily pack has %d couplings, want at least 2", len(daily.Coupling))
	}
	if len(daily.Presets) < 4 {
		t.Fatalf("daily pack has %d presets, want at least 4", len(daily.Presets))
	}
	for _, ev := range daily.Events {
		if len(ev.compiled) != len(ev.Patterns) {
			t.Fatalf("event %q patterns were not compiled", ev.ID)
		}
	}
	for _, key := range DimensionKeys {
		if _, ok := daily.Injection.Fragments[key]; !ok {
			t.Fatalf("missing fragments for %q", key)
		}
	}
	for _, band := range []string{"night", "dawn", "morning", "noon", "afternoon", "evening"} {
		if len(daily.Injection.TimeFragments[band]) == 0 {
			t.Fatalf("missing time fragments for %q", band)
		}
	}
}

func TestPackInfosAndLookup(t *testing.T) {
	infos := PackInfos()
	if len(infos) == 0 || infos[0].ID != "daily" {
		t.Fatalf("PackInfos should list daily first: %+v", infos)
	}
	if got := PackByID("does-not-exist"); got == nil || got.ID != "daily" {
		t.Fatalf("unknown pack should fall back to daily, got %+v", got)
	}
	if got := PackByID(""); got == nil || got.ID != "daily" {
		t.Fatalf("empty pack id should fall back to daily, got %+v", got)
	}
	if d := DefaultPack(); d == nil || d.Name == "" {
		t.Fatalf("default pack missing: %+v", d)
	}
}

func TestPackValidationRejectsBadData(t *testing.T) {
	raw := []byte(`{
		"id": "bad", "name": "bad", "version": "1",
		"dimensions": [{"key":"valence","label":"x","min":0,"max":100,"baseline":50,"half_life_hours":1}],
		"injection": {"fragments": {}},
		"behavior": {"bands": []}
	}`)
	if _, err := decodePack(raw); err == nil {
		t.Fatal("a pack with missing dimensions must be rejected")
	}
}
