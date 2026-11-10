package core

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/affect"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// newMoodTestDispatcher wires the minimal collaborators /mood needs: a real
// SQLite store, a settings manager with affect enabled and the real service.
func newMoodTestDispatcher(t *testing.T) (*Dispatcher, *settings.Manager, *store.Store, *affect.Service) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/mood.db")
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
	d := New(Options{Settings: sm, Store: st, Affect: svc, Logger: logger})
	return d, sm, st, svc
}

func moodInbound() *Inbound {
	return &Inbound{
		Scene: "group", AtBot: true, GroupOpenID: "G-MOOD",
		MemberOpenID: "U-ADMIN", MemberName: "管理员", Content: "/mood",
	}
}

func TestMoodRegisteredAsAdminCommand(t *testing.T) {
	d, _, _, _ := newMoodTestDispatcher(t)
	cmd, _, ok := d.lookupCommand(&Inbound{Scene: "c2c", Content: "mood"})
	if !ok || cmd == nil || cmd.name != moodName {
		t.Fatalf("bare mood lookup = %+v ok=%v", cmd, ok)
	}
	if !cmd.adminOnly {
		t.Fatal("/mood must be adminOnly")
	}
	if n := len([]rune(cmd.desc)); n > 30 {
		t.Fatalf("desc = %q (%d runes), want <= 30", cmd.desc, n)
	}
	if cmd.usage != moodUsage {
		t.Fatalf("usage = %q, want %q", cmd.usage, moodUsage)
	}

	s := settings.Defaults()
	s.AdminOpenIDs = []string{"U-ADMIN"}
	in := moodInbound()
	if !d.commandAllowed(cmd, s, in) {
		t.Fatal("admin should be allowed to run /mood")
	}
	in.MemberOpenID = "U-RANDOM"
	if d.commandAllowed(cmd, s, in) {
		t.Fatal("non-admin must be denied /mood")
	}
}

func TestMoodStatusOutput(t *testing.T) {
	d, sm, st, _ := newMoodTestDispatcher(t)
	ctx := context.Background()
	conv := resolveConversation(moodInbound(), sm.Effective().ContextMode)
	if err := st.SaveAffectState(ctx, &store.AffectState{
		ConversationID: conv.ID, PackID: "daily",
		Dims: map[string]float64{
			"valence": 61.5, "energy": 42, "stress": 33.3,
			"closeness": 70, "patience": 55,
		},
		UpdatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	out, err := runMood(ctx, d, moodInbound(), sm.Effective(), nil)
	if err != nil {
		t.Fatalf("runMood: %v", err)
	}
	for _, want := range []string{"心情", "精力", "压力", "熟悉度", "耐心", "61.5", "运行中"} {
		if !strings.Contains(out.Text, want) {
			t.Fatalf("status output missing %q:\n%s", want, out.Text)
		}
	}

	// Missing state is a readable message, not an error and not a panic.
	other := moodInbound()
	other.MemberOpenID = "U-NEW"
	outOther, err := runMood(ctx, d, other, sm.Effective(), nil)
	if err != nil {
		t.Fatalf("runMood without state: %v", err)
	}
	if !strings.Contains(outOther.Text, "还没有情绪状态记录") {
		t.Fatalf("missing-state output = %q", outOther.Text)
	}
}

func TestMoodPauseResumePresetReset(t *testing.T) {
	d, sm, st, _ := newMoodTestDispatcher(t)
	ctx := context.Background()
	in := moodInbound()
	conv := resolveConversation(in, sm.Effective().ContextMode)

	out, err := runMood(ctx, d, in, sm.Effective(), []string{"pause", "30"})
	if err != nil || !strings.Contains(out.Text, "已暂停") {
		t.Fatalf("pause = %q err=%v", out.Text, err)
	}
	stored, _ := st.GetAffectState(ctx, conv.ID)
	if stored == nil || stored.PausedUntil == "" {
		t.Fatalf("state after pause = %+v", stored)
	}
	if until, err := time.Parse(time.RFC3339, stored.PausedUntil); err != nil || until.Before(time.Now().Add(25*time.Minute)) {
		t.Fatalf("paused_until = %q", stored.PausedUntil)
	}

	// Out-of-range and non-numeric minutes are usage errors, not mutations.
	for _, bad := range []string{"0", "1441", "abc"} {
		out, err := runMood(ctx, d, in, sm.Effective(), []string{"pause", bad})
		if err != nil || !strings.Contains(out.Text, "1–1440") {
			t.Fatalf("pause %q = %q err=%v", bad, out.Text, err)
		}
	}

	out, err = runMood(ctx, d, in, sm.Effective(), []string{"resume"})
	if err != nil || !strings.Contains(out.Text, "已恢复") {
		t.Fatalf("resume = %q err=%v", out.Text, err)
	}
	stored, _ = st.GetAffectState(ctx, conv.ID)
	if stored == nil || stored.PausedUntil != "" {
		t.Fatalf("state after resume = %+v", stored)
	}

	out, err = runMood(ctx, d, in, sm.Effective(), []string{"preset", "calm"})
	if err != nil || !strings.Contains(out.Text, "已应用预设") {
		t.Fatalf("preset = %q err=%v", out.Text, err)
	}
	stored, _ = st.GetAffectState(ctx, conv.ID)
	if stored == nil || stored.Dims["valence"] != 60 || stored.Dims["patience"] != 65 {
		t.Fatalf("state after preset = %+v", stored)
	}

	out, err = runMood(ctx, d, in, sm.Effective(), []string{"reset"})
	if err != nil || !strings.Contains(out.Text, "已重置") {
		t.Fatalf("reset = %q err=%v", out.Text, err)
	}
	stored, _ = st.GetAffectState(ctx, conv.ID)
	if stored == nil || stored.Dims["valence"] != 55 {
		t.Fatalf("state after reset = %+v", stored)
	}
}

func TestMoodUsageForBadArguments(t *testing.T) {
	d, sm, _, _ := newMoodTestDispatcher(t)
	ctx := context.Background()
	in := moodInbound()
	for _, args := range [][]string{
		{"status", "extra"},
		{"pause"},
		{"preset"},
		{"resume", "extra"},
		{"reset", "extra"},
		{"unknown"},
	} {
		out, err := runMood(ctx, d, in, sm.Effective(), args)
		if err != nil {
			t.Fatalf("runMood %v err=%v", args, err)
		}
		if !strings.Contains(out.Text, moodUsage) {
			t.Fatalf("args %v output = %q, want usage", args, out.Text)
		}
	}
}
