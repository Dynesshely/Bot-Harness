package settings

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAffectDefaults(t *testing.T) {
	m, _ := newManager(t, false)
	got := m.Get()
	if got.AffectEnabled {
		t.Fatal("affect must be off by default")
	}
	if got.AffectPack != "daily" {
		t.Fatalf("affect_pack default = %q, want daily", got.AffectPack)
	}
	if got.AffectInjectMode != "line" {
		t.Fatalf("affect_inject_mode default = %q, want line", got.AffectInjectMode)
	}
	if got.AffectEventClassifier != "rules" {
		t.Fatalf("affect_event_classifier default = %q, want rules", got.AffectEventClassifier)
	}
	if got.AffectTickSeconds != 300 {
		t.Fatalf("affect_tick_seconds default = %d, want 300", got.AffectTickSeconds)
	}
	if got.AffectInjectMaxTokens != 120 {
		t.Fatalf("affect_inject_max_tokens default = %d, want 120", got.AffectInjectMaxTokens)
	}
}

func TestAffectKeysApplyAndValidate(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()

	good := map[string]json.RawMessage{
		"affect_enabled":           json.RawMessage("true"),
		"affect_pack":              json.RawMessage("\"daily\""),
		"affect_inject_mode":       json.RawMessage("\"full\""),
		"affect_event_classifier":  json.RawMessage("\"off\""),
		"affect_tick_seconds":      json.RawMessage("60"),
		"affect_inject_max_tokens": json.RawMessage("40"),
	}
	got, _, err := m.Apply(ctx, good)
	if err != nil {
		t.Fatalf("valid affect settings rejected: %v", err)
	}
	if !got.AffectEnabled || got.AffectInjectMode != "full" || got.AffectEventClassifier != "off" ||
		got.AffectTickSeconds != 60 || got.AffectInjectMaxTokens != 40 {
		t.Fatalf("affect settings not applied: %+v", got)
	}

	bad := []map[string]json.RawMessage{
		{"affect_inject_mode": json.RawMessage("\"sometimes\"")},
		{"affect_event_classifier": json.RawMessage("\"maybe\"")},
		{"affect_tick_seconds": json.RawMessage("59")},
		{"affect_tick_seconds": json.RawMessage("1801")},
		{"affect_inject_max_tokens": json.RawMessage("39")},
		{"affect_inject_max_tokens": json.RawMessage("301")},
		{"affect_pack": json.RawMessage("\"\"")},
		{"group_overrides": json.RawMessage("[{\"group_openid\":\"G1\",\"affect_inject_mode\":\"loud\"}]")},
	}
	for _, patch := range bad {
		if _, _, err := m.Apply(ctx, patch); err == nil {
			t.Fatalf("invalid affect patch accepted: %v", patch)
		}
	}
}

func TestAffectGroupOverrideResolves(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()
	if _, _, err := m.Apply(ctx, mustPatch(t, "{\"affect_enabled\":true,\"group_overrides\":[{\"group_openid\":\"G1\",\"affect\":true,\"affect_inject_mode\":\"full\"},{\"group_openid\":\"G3\",\"affect\":false}]}")); err != nil {
		t.Fatalf("apply override: %v", err)
	}
	cfg := m.GroupConfig("G1")
	if !cfg.AffectEnabled || cfg.AffectInjectMode != "full" {
		t.Fatalf("group affect override not resolved: %+v", cfg)
	}
	other := m.GroupConfig("G2")
	if !other.AffectEnabled {
		t.Fatalf("a group without an override must follow the global switch: %+v", other)
	}
	off := m.GroupConfig("G3")
	if off.AffectEnabled {
		t.Fatalf("group affect=false must win over the global switch: %+v", off)
	}
}

func TestAffectGroupOverrideCannotEnableGlobalOff(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()
	if _, _, err := m.Apply(ctx, mustPatch(t, "{\"group_overrides\":[{\"group_openid\":\"G1\",\"affect\":true}]}")); err != nil {
		t.Fatalf("apply override: %v", err)
	}
	if cfg := m.GroupConfig("G1"); cfg.AffectEnabled {
		t.Fatalf("global off is a master kill switch, got %+v", cfg)
	}
}
