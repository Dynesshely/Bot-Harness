package settings

import (
	"context"
	"encoding/json"
	"testing"
)

// 三个新键的默认值、范围与枚举必须能被后台「原样写回」：界面能选出来的值
// 后端必须全盘接受，反之非法值必须在 PUT 当场被拒。
func TestHumanizeSegmentAndTypingKeys(t *testing.T) {
	def := Defaults()
	if def.HumanizeSegmentSplit != "medium" {
		t.Fatalf("humanize_segment_split 默认值 = %q", def.HumanizeSegmentSplit)
	}
	if def.HumanizeTypingSecondsPerChar != 0.3 {
		t.Fatalf("humanize_typing_seconds_per_char 默认值 = %v", def.HumanizeTypingSecondsPerChar)
	}
	if def.HumanizeTypingMaxSeconds != 12 {
		t.Fatalf("humanize_typing_max_seconds 默认值 = %d", def.HumanizeTypingMaxSeconds)
	}

	known := map[string]bool{}
	for _, k := range KnownKeys() {
		known[k] = true
	}
	for _, k := range []string{
		"humanize_segment_split",
		"humanize_typing_seconds_per_char",
		"humanize_typing_max_seconds",
	} {
		if !known[k] {
			t.Fatalf("%s 必须出现在 KnownKeys 里", k)
		}
	}

	m, st := newManager(t, false)

	apply := func(raw string) error {
		var patch map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &patch); err != nil {
			t.Fatalf("bad patch %s: %v", raw, err)
		}
		_, _, err := m.Apply(context.Background(), patch)
		return err
	}

	// 合法值：四个档位、边界内的数值。
	for _, split := range []string{"off", "soft", "medium", "strong"} {
		if err := apply(`{"humanize_segment_split":"` + split + `"}`); err != nil {
			t.Fatalf("segment_split=%s 应被接受：%v", split, err)
		}
	}
	if err := apply(`{"humanize_typing_seconds_per_char":0.05}`); err != nil {
		t.Fatalf("下界应被接受：%v", err)
	}
	if err := apply(`{"humanize_typing_seconds_per_char":1.0}`); err != nil {
		t.Fatalf("上界应被接受：%v", err)
	}
	if err := apply(`{"humanize_typing_max_seconds":0}`); err != nil {
		t.Fatalf("0（不加等待）应被接受：%v", err)
	}
	if err := apply(`{"humanize_typing_max_seconds":30}`); err != nil {
		t.Fatalf("30 应被接受：%v", err)
	}

	// 非法值：枚举外、越界。
	bad := []string{
		`{"humanize_segment_split":"wild"}`,
		`{"humanize_typing_seconds_per_char":0.01}`,
		`{"humanize_typing_seconds_per_char":1.5}`,
		`{"humanize_typing_max_seconds":-1}`,
		`{"humanize_typing_max_seconds":31}`,
	}
	for _, raw := range bad {
		if err := apply(raw); err == nil {
			t.Fatalf("%s 应被拒绝", raw)
		}
	}

	// 落库的值要能在 reload 后读回来（golden 之外的持久化路径）。
	if err := apply(`{"humanize_segment_split":"strong","humanize_typing_seconds_per_char":0.6,"humanize_typing_max_seconds":7}`); err != nil {
		t.Fatalf("apply: %v", err)
	}
	reloaded, err := New(context.Background(), st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.Get()
	if got.HumanizeSegmentSplit != "strong" || got.HumanizeTypingSecondsPerChar != 0.6 || got.HumanizeTypingMaxSeconds != 7 {
		t.Fatalf("持久化丢失：%+v", got)
	}
}
