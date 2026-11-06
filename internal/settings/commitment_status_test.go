package settings

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCommitmentAndHumanStatusDefaults(t *testing.T) {
	d := Defaults()
	if !d.CommitmentEnabled || d.CommitmentMaxOpenPerMember != 10 {
		t.Fatalf("commitment defaults = enabled:%v max:%d", d.CommitmentEnabled, d.CommitmentMaxOpenPerMember)
	}
	if !d.HumanStatusEnabled || d.HumanStatusTriggerMessages != 20 || d.HumanStatusTTLMinutes != 120 {
		t.Fatalf("human status defaults = enabled:%v trigger:%d ttl:%d",
			d.HumanStatusEnabled, d.HumanStatusTriggerMessages, d.HumanStatusTTLMinutes)
	}
	if d.HumanStatusTimelineMessages != 120 || !d.HumanStatusVisionEnabled || d.HumanStatusVisionModel != "" {
		t.Fatalf("human status timeline/vision defaults = timeline:%d vision:%v model:%q",
			d.HumanStatusTimelineMessages, d.HumanStatusVisionEnabled, d.HumanStatusVisionModel)
	}
}

func TestCommitmentAndHumanStatusApplyAndValidate(t *testing.T) {
	ctx := context.Background()
	m, err := New(ctx, nil, Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	patch := map[string]json.RawMessage{
		"commitment_enabled":             json.RawMessage("false"),
		"commitment_max_open_per_member": json.RawMessage("25"),
		"human_status_enabled":           json.RawMessage("false"),
		"human_status_trigger_messages":  json.RawMessage("50"),
		"human_status_ttl_minutes":       json.RawMessage("30"),
		"human_status_timeline_messages": json.RawMessage("240"),
		"human_status_vision_enabled":    json.RawMessage("false"),
		"human_status_vision_model":      json.RawMessage(`"vision-test"`),
	}
	got, _, err := m.Apply(ctx, patch)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got.CommitmentEnabled || got.CommitmentMaxOpenPerMember != 25 ||
		got.HumanStatusEnabled || got.HumanStatusTriggerMessages != 50 || got.HumanStatusTTLMinutes != 30 {
		t.Fatalf("applied settings = %+v", got)
	}
	if got.HumanStatusTimelineMessages != 240 || got.HumanStatusVisionEnabled ||
		got.HumanStatusVisionModel != "vision-test" {
		t.Fatalf("applied timeline/vision settings = %+v", got)
	}

	cases := []map[string]json.RawMessage{
		{"commitment_max_open_per_member": json.RawMessage("0")},
		{"commitment_max_open_per_member": json.RawMessage("51")},
		{"human_status_trigger_messages": json.RawMessage("4")},
		{"human_status_trigger_messages": json.RawMessage("201")},
		{"human_status_ttl_minutes": json.RawMessage("9")},
		{"human_status_ttl_minutes": json.RawMessage("1441")},
		{"human_status_timeline_messages": json.RawMessage("19")},
		{"human_status_timeline_messages": json.RawMessage("401")},
	}
	for _, patch := range cases {
		if _, _, err := m.Apply(ctx, patch); err == nil {
			t.Fatalf("patch %v should be rejected", patch)
		}
	}
}
