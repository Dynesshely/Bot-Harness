package core

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/affect"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

func newAffectIntegration(t *testing.T) (*Dispatcher, *settings.Manager, *store.Store, *affect.Service) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/core-affect.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sm, err := settings.New(context.Background(), st, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := affect.NewService(st, sm, logger, affect.NewMetrics())
	return &Dispatcher{affect: svc, settings: sm, st: st, log: logger}, sm, st, svc
}

func enableAffect(t *testing.T, sm *settings.Manager) {
	t.Helper()
	patch := map[string]json.RawMessage{
		"affect_enabled":     json.RawMessage("true"),
		"affect_inject_mode": json.RawMessage("\"full\""),
	}
	if _, _, err := sm.Apply(context.Background(), patch); err != nil {
		t.Fatalf("enable affect: %v", err)
	}
}

func TestAffectNilIsInert(t *testing.T) {
	d := &Dispatcher{}
	now := time.Now()
	if got := d.affectBlock(context.Background(), "c1", settings.Defaults(), settings.GroupConfig{}, now); got != "" {
		t.Fatalf("nil service Block = %q", got)
	}
	if got := d.affectShortLine(context.Background(), "c1", settings.Defaults(), settings.GroupConfig{}, now); got != "" {
		t.Fatalf("nil service ShortLine = %q", got)
	}
	if got := d.affectBehavior(context.Background(), "c1", now, settings.GroupConfig{}); got != affect.DefaultBehavior() {
		t.Fatalf("nil service Behavior = %+v", got)
	}
	if got := d.affectSilenceBoost(context.Background(), "c1", settings.GroupConfig{}); got != 0 {
		t.Fatalf("nil service silence boost = %v", got)
	}
	if got := appendSystemBlock("base", ""); got != "base" {
		t.Fatalf("empty block changed the prompt: %q", got)
	}
}

func TestApplyAffectBehaviorUsesLocalCopy(t *testing.T) {
	base := settings.Defaults()
	base.ReplyMaxChars = 1000
	base.ReplyMaxSegments = 4
	base.HumanizeDelayScale = 1
	base.HumanizeLevel = "strong"
	cfg := settings.GroupConfig{HumanizeLevel: "strong"}

	got, level := applyAffectBehavior(base, cfg, affect.Behavior{
		MaxCharsFactor: 0.5, MaxSegments: 2, HumanizeScale: 1.5,
		SilenceBoost: 0.1, InitiativeFactor: 0.5, Tone: "low_battery",
	})
	if got.ReplyMaxChars != 500 || got.ReplyMaxSegments != 2 {
		t.Fatalf("local settings not adjusted: %+v", got)
	}
	if got.HumanizeDelayScale != 1.5 {
		t.Fatalf("humanize scale = %v, want 1.5", got.HumanizeDelayScale)
	}
	if level != HumanizeMedium {
		t.Fatalf("low battery should soften the strong level, got %q", level)
	}
	if base.ReplyMaxChars != 1000 || base.ReplyMaxSegments != 4 || base.HumanizeDelayScale != 1 {
		t.Fatalf("the original settings were mutated: %+v", base)
	}

	// Default behavior must be an exact no-op.
	same, sameLevel := applyAffectBehavior(base, cfg, affect.DefaultBehavior())
	if same.ReplyMaxChars != base.ReplyMaxChars || same.ReplyMaxSegments != base.ReplyMaxSegments ||
		same.HumanizeDelayScale != base.HumanizeDelayScale || sameLevel != HumanizeStrong {
		t.Fatalf("default behavior changed the turn: %+v level=%q", same, sameLevel)
	}
}

func TestObserveAndBlockIntegration(t *testing.T) {
	d, sm, st, _ := newAffectIntegration(t)
	enableAffect(t, sm)
	ctx := context.Background()
	now := time.Now()
	conv := "core-affect-conv"

	d.observeAffect(ctx, conv, "这也太厉害了吧")

	stored, err := st.GetAffectState(ctx, conv)
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if stored == nil {
		t.Fatal("Observe did not persist a state")
	}
	if stored.Dims["valence"] <= 55 {
		t.Fatalf("praise should raise valence, got %.2f", stored.Dims["valence"])
	}

	cfg := sm.GroupConfig("G1")
	block := d.affectBlock(ctx, conv, sm.Effective(), cfg, now)
	if block == "" {
		t.Fatal("enabled service should render a Block")
	}
	systemPrompt := appendSystemBlock("基础提示词", block)
	if !strings.Contains(systemPrompt, "基础提示词") || !strings.Contains(systemPrompt, "此刻的状态") {
		t.Fatalf("system prompt = %q", systemPrompt)
	}
	if !strings.Contains(block, "仅作语气参考") {
		t.Fatalf("Block = %q", block)
	}
	line := d.affectShortLine(ctx, conv, sm.Effective(), cfg, now)
	if line == "" {
		t.Fatal("enabled service should render a ShortLine")
	}
}

func TestAffectGroupOffDisablesBehaviorAndMode(t *testing.T) {
	d, sm, st, _ := newAffectIntegration(t)
	enableAffect(t, sm)
	ctx := context.Background()
	now := time.Now()
	conv := "core-affect-group-off"
	if err := st.SaveAffectState(ctx, &store.AffectState{
		ConversationID: conv, PackID: "daily",
		Dims:     map[string]float64{"valence": 40, "energy": 10, "stress": 30, "closeness": 50, "patience": 60},
		TickedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	on := sm.GroupConfig("G1")
	if got := d.affectBehavior(ctx, conv, now, on); got == affect.DefaultBehavior() || got.Tone != "low_battery" {
		t.Fatalf("enabled group should derive low_battery, got %+v", got)
	}

	off := on
	off.AffectEnabled = false
	if got := d.affectBehavior(ctx, conv, now, off); got != affect.DefaultBehavior() {
		t.Fatalf("group affect=false must disable behavior, got %+v", got)
	}

	modeOff := on
	modeOff.AffectInjectMode = affect.ModeOff
	if got := d.affectBehavior(ctx, conv, now, modeOff); got != affect.DefaultBehavior() {
		t.Fatalf("inject_mode=off must disable behavior, got %+v", got)
	}
}

func TestAffectGlobalOffWinsOverGroupOverride(t *testing.T) {
	d, sm, _, _ := newAffectIntegration(t)
	// Global stays off; the group override asks for affect. §20: global off wins.
	cfg := settings.GroupConfig{AffectEnabled: true, AffectInjectMode: "full"}
	if got := d.affectBlock(context.Background(), "c1", sm.Effective(), cfg, time.Now()); got != "" {
		t.Fatalf("global off must suppress the block, got %q", got)
	}
}
