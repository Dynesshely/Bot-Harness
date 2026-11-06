package core

import (
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

func TestCreditsFor(t *testing.T) {
	cases := []struct{ humans, ratio, max, want int }{
		{0, 10, 3, 0},
		{9, 10, 3, 0},
		{10, 10, 3, 1},
		{29, 10, 3, 2},
		{1000, 10, 3, 3}, // 上限 3，长期沉默不会攒出一波爆发
		{5, 0, 3, 0},     // 非法比例
	}
	for _, c := range cases {
		if got := creditsFor(c.humans, c.ratio, c.max); got != c.want {
			t.Fatalf("creditsFor(%d,%d,%d)=%d，期望 %d", c.humans, c.ratio, c.max, got, c.want)
		}
	}
}

func TestQuietHours(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 30, h, 0, 0, 0, time.Local) }
	// 23 → 8 跨午夜
	for _, h := range []int{23, 0, 3, 7} {
		if !inQuietHours(at(h), 23, 8) {
			t.Fatalf("%d 点应在静默时段", h)
		}
	}
	for _, h := range []int{8, 12, 22} {
		if inQuietHours(at(h), 23, 8) {
			t.Fatalf("%d 点不该静默", h)
		}
	}
	// 同一小时视为不启用
	if inQuietHours(at(3), 5, 5) {
		t.Fatal("start==end 应视为不启用静默")
	}
}

func TestProactiveGateExplainsEveryRefusal(t *testing.T) {
	base := settings.Settings{
		ProactiveLevel: ProactiveAuto, ProactiveRatio: 10, ProactiveMaxCredits: 3,
		ProactiveQuietStart: 23, ProactiveQuietEnd: 8, ProactiveMinIntervalMinutes: 10,
	}
	noon := time.Date(2026, 10, 30, 14, 0, 0, 0, time.Local)

	if n, why := proactiveGate(base, store.ProactiveState{}, noon, 0); n != 0 || why == "" {
		t.Fatalf("额度不足时应有说明：n=%d why=%q", n, why)
	}
	if n, _ := proactiveGate(base, store.ProactiveState{}, noon, 25); n != 2 {
		t.Fatalf("25 条人类消息应换 2 点额度，实际 %d", n)
	}

	threeAM := time.Date(2026, 10, 30, 3, 0, 0, 0, time.Local)
	if n, why := proactiveGate(base, store.ProactiveState{}, threeAM, 50); n != 0 || why != "静默时段" {
		t.Fatalf("深夜必须拒绝：n=%d why=%q", n, why)
	}

	recent := store.ProactiveState{LastSentAt: noon.Add(-2 * time.Minute).Format(time.RFC3339)}
	if n, why := proactiveGate(base, recent, noon, 50); n != 0 || why == "" {
		t.Fatalf("间隔不足应拒绝：n=%d why=%q", n, why)
	}

	streaky := store.ProactiveState{SilentStreak: 2}
	if n, why := proactiveGate(base, streaky, noon, 50); n != 0 {
		t.Fatalf("连续自说自话应降级：n=%d why=%q", n, why)
	}

	off := base
	off.ProactiveLevel = "off"
	if n, _ := proactiveGate(off, store.ProactiveState{}, noon, 50); n != 0 {
		t.Fatal("全局关闭时不发")
	}
}
