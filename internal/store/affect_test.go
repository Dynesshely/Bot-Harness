package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/testpg"
)

// affectStoreContract is the engine-independent part of the affect store
// tests: SQLite always runs it, PostgreSQL runs the same body against a
// throwaway database when PG_TEST_DSN is configured.
func affectStoreContract(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()

	// Missing rows are not an error.
	got, err := s.GetAffectState(ctx, "conv-1")
	if err != nil {
		t.Fatalf("get missing state: %v", err)
	}
	if got != nil {
		t.Fatalf("missing state = %+v, want nil", got)
	}

	base := "2026-01-01T10:00:00+08:00"
	st := &AffectState{
		ConversationID: "conv-1",
		PackID:         "daily",
		Dims:           map[string]float64{"valence": 55, "energy": 70.5, "stress": 20},
		Events:         []AffectEventMark{{ID: "praise", At: base}},
		PausedUntil:    "2026-01-01T11:00:00+08:00",
		TickedAt:       base,
		UpdatedAt:      base,
	}
	if err := s.SaveAffectState(ctx, st); err != nil {
		t.Fatalf("insert state: %v", err)
	}
	if st.Version != 1 {
		t.Fatalf("insert version = %d, want 1", st.Version)
	}

	stored, err := s.GetAffectState(ctx, "conv-1")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if stored == nil {
		t.Fatal("state missing after insert")
	}
	if stored.PackID != "daily" || stored.Version != 1 {
		t.Fatalf("stored = %+v", stored)
	}
	if stored.Dims["valence"] != 55 || stored.Dims["energy"] != 70.5 {
		t.Fatalf("dims = %v", stored.Dims)
	}
	if len(stored.Events) != 1 || stored.Events[0].ID != "praise" {
		t.Fatalf("events = %+v", stored.Events)
	}
	if stored.PausedUntil != "2026-01-01T11:00:00+08:00" {
		t.Fatalf("paused_until = %q", stored.PausedUntil)
	}
	if stored.TickedAt != base || stored.UpdatedAt != base {
		t.Fatalf("times = ticked %q updated %q", stored.TickedAt, stored.UpdatedAt)
	}

	// A state with Version 0 cannot overwrite an existing row.
	dup := &AffectState{ConversationID: "conv-1", PackID: "daily", Dims: map[string]float64{"valence": 1}}
	if err := s.SaveAffectState(ctx, dup); !errors.Is(err, ErrAffectConflict) {
		t.Fatalf("duplicate insert error = %v, want ErrAffectConflict", err)
	}

	// Version CAS: stale version loses, current version wins and bumps to 2.
	stale := *stored
	stale.Version = 0
	stale.Dims = map[string]float64{"valence": 1}
	if err := s.SaveAffectState(ctx, &stale); !errors.Is(err, ErrAffectConflict) {
		t.Fatalf("stale save error = %v, want ErrAffectConflict", err)
	}
	stored.Dims = map[string]float64{"valence": 61.25, "energy": 68, "stress": 22, "closeness": 40, "patience": 75}
	stored.PausedUntil = ""
	stored.UpdatedAt = "2026-01-01T10:05:00+08:00"
	if err := s.SaveAffectState(ctx, stored); err != nil {
		t.Fatalf("update state: %v", err)
	}
	if stored.Version != 2 {
		t.Fatalf("update version = %d, want 2", stored.Version)
	}
	// Reusing the pre-update version is a conflict again.
	retry := *stored
	retry.Version = 1
	if err := s.SaveAffectState(ctx, &retry); !errors.Is(err, ErrAffectConflict) {
		t.Fatalf("replayed save error = %v, want ErrAffectConflict", err)
	}
	after, _ := s.GetAffectState(ctx, "conv-1")
	if after == nil || after.Version != 2 || after.Dims["patience"] != 75 || after.PausedUntil != "" {
		t.Fatalf("state after update = %+v", after)
	}

	// Conversations are ordered by updated_at DESC and respect the limit.
	second := &AffectState{
		ConversationID: "conv-2", PackID: "daily",
		Dims:      map[string]float64{"valence": 30},
		UpdatedAt: "2026-01-01T09:00:00+08:00",
	}
	if err := s.SaveAffectState(ctx, second); err != nil {
		t.Fatalf("insert second state: %v", err)
	}
	ids, err := s.ListAffectConversations(ctx, 10)
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(ids) != 2 || ids[0] != "conv-1" || ids[1] != "conv-2" {
		t.Fatalf("conversations = %v, want [conv-1 conv-2]", ids)
	}
	if limited, _ := s.ListAffectConversations(ctx, 1); len(limited) != 1 || limited[0] != "conv-1" {
		t.Fatalf("limited conversations = %v", limited)
	}

	// Event log: append-only, newest first, limit respected.
	for _, e := range []struct{ id, tone, at string }{
		{"praise", "positive", "2026-01-01T09:00:00+08:00"},
		{"insult", "negative", "2026-01-01T09:01:00+08:00"},
		{"praise", "positive", "2026-01-01T09:02:00+08:00"},
	} {
		if err := s.AppendAffectEvent(ctx, "conv-1", e.id, e.tone, "rules", e.at); err != nil {
			t.Fatalf("append event %s: %v", e.id, err)
		}
	}
	events, err := s.ListAffectEvents(ctx, "conv-1", 2)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 || events[0].CreatedAt != "2026-01-01T09:02:00+08:00" ||
		events[1].EventID != "insult" || events[0].Source != "rules" {
		t.Fatalf("events = %+v", events)
	}
	if none, _ := s.ListAffectEvents(ctx, "conv-none", 10); len(none) != 0 {
		t.Fatalf("events for unknown conversation = %+v", none)
	}

	// History: newest window returned chronologically, since filters, limit caps.
	for _, p := range []struct {
		at  string
		val float64
	}{
		{"2026-01-01T08:00:00+08:00", 10},
		{"2026-01-01T08:30:00+08:00", 20},
		{"2026-01-01T09:00:00+08:00", 30},
		{"2026-01-01T09:30:00+08:00", 40},
	} {
		if err := s.AppendAffectHistory(ctx, "conv-1", p.at, map[string]float64{"valence": p.val}); err != nil {
			t.Fatalf("append history %s: %v", p.at, err)
		}
	}
	points, err := s.ListAffectHistory(ctx, "conv-1", "", 3)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(points) != 3 || points[0].Dims["valence"] != 20 || points[2].Dims["valence"] != 40 {
		t.Fatalf("history window = %+v", points)
	}
	if points[0].At != "2026-01-01T08:30:00+08:00" || points[2].At != "2026-01-01T09:30:00+08:00" {
		t.Fatalf("history order = %+v", points)
	}
	filtered, err := s.ListAffectHistory(ctx, "conv-1", "2026-01-01T09:00:00+08:00", 10)
	if err != nil {
		t.Fatalf("list filtered history: %v", err)
	}
	if len(filtered) != 2 || filtered[0].Dims["valence"] != 30 || filtered[1].Dims["valence"] != 40 {
		t.Fatalf("filtered history = %+v", filtered)
	}

	// Prune removes only rows older than the cutoffs.
	if err := s.PruneAffect(ctx, "2026-01-01T09:10:00+08:00", "2026-01-01T09:02:00+08:00"); err != nil {
		t.Fatalf("prune: %v", err)
	}
	left, _ := s.ListAffectHistory(ctx, "conv-1", "", 10)
	if len(left) != 1 || left[0].Dims["valence"] != 40 {
		t.Fatalf("history after prune = %+v", left)
	}
	leftEvents, _ := s.ListAffectEvents(ctx, "conv-1", 10)
	if len(leftEvents) != 1 || leftEvents[0].CreatedAt != "2026-01-01T09:02:00+08:00" {
		t.Fatalf("events after prune = %+v", leftEvents)
	}
	// Empty cutoffs are a no-op, not a delete-everything.
	if err := s.PruneAffect(ctx, "", ""); err != nil {
		t.Fatalf("empty prune: %v", err)
	}
	if left, _ := s.ListAffectEvents(ctx, "conv-1", 10); len(left) != 1 {
		t.Fatalf("empty prune removed events: %+v", left)
	}
}

func TestClearConversationCascadesAffect(t *testing.T) {
	s, err := Open(t.TempDir() + "/cascade.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	at := "2026-01-01T10:00:00+08:00"
	if err := s.SaveAffectState(ctx, &AffectState{
		ConversationID: "conv-cascade", PackID: "daily",
		Dims: map[string]float64{"valence": 60}, TickedAt: at, UpdatedAt: at,
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := s.AppendAffectEvent(ctx, "conv-cascade", "praise", "positive", "rules", at); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if err := s.AppendAffectHistory(ctx, "conv-cascade", at, map[string]float64{"valence": 60}); err != nil {
		t.Fatalf("append history: %v", err)
	}
	if err := s.ClearConversationMessages(ctx, "conv-cascade"); err != nil {
		t.Fatalf("clear conversation: %v", err)
	}
	if st, err := s.GetAffectState(ctx, "conv-cascade"); err != nil || st != nil {
		t.Fatalf("state survived clear: st=%+v err=%v", st, err)
	}
	if events, err := s.ListAffectEvents(ctx, "conv-cascade", 10); err != nil || len(events) != 0 {
		t.Fatalf("events survived clear: %+v err=%v", events, err)
	}
	if points, err := s.ListAffectHistory(ctx, "conv-cascade", "2000-01-01T00:00:00Z", 10); err != nil || len(points) != 0 {
		t.Fatalf("history survived clear: %+v err=%v", points, err)
	}
}

func TestAffectStoreSQLite(t *testing.T) {
	s, err := Open(t.TempDir() + "/affect.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	affectStoreContract(t, s)
}

// TestAffectStorePostgres runs the same contract on PostgreSQL. It needs
// PG_TEST_DSN, and pgScratchDatabase refuses the production database before
// opening a connection.
func TestAffectStorePostgres(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN not set; skipping the PostgreSQL affect store test")
	}
	testpg.Guard(t, dsn)
	s, err := Open(pgScratchDatabase(t))
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	affectStoreContract(t, s)
}

// TestAffectTablesExistOnFreshDatabase pins the three tables to the base
// schema, not just to migration 0013.
func TestAffectTablesExistOnFreshDatabase(t *testing.T) {
	s, err := Open(t.TempDir() + "/fresh.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	for _, table := range []string{"affect_states", "affect_events", "affect_history"} {
		cols, err := s.tableColumns(ctx, table)
		if err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if len(cols) == 0 {
			t.Fatalf("table %s does not exist", table)
		}
	}
	var applied int
	if err := s.queryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE name = ?", "0013_affect").Scan(&applied); err != nil {
		t.Fatalf("check migration: %v", err)
	}
	if applied != 1 {
		t.Fatalf("0013_affect recorded %d times, want 1", applied)
	}
}

// TestAffectMigrationUpgradeKeepsData simulates a database that stopped at
// 0012 (no affect tables, no 0013 row) and reopens it: the three tables must
// appear, the migration must be recorded once, and existing rows must survive.
func TestAffectMigrationUpgradeKeepsData(t *testing.T) {
	path := t.TempDir() + "/upgrade.db"
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open initial store: %v", err)
	}
	ctx := context.Background()
	if err := s.EnsureConversation(ctx, &Conversation{
		ID: "old-conv", Scene: SceneGroupMember, GroupOpenID: "G1", GroupName: "老群",
	}); err != nil {
		_ = s.Close()
		t.Fatalf("seed conversation: %v", err)
	}
	// Roll the affect part back to the "0012-only" shape.
	for _, stmt := range []string{
		"DROP TABLE affect_history",
		"DROP TABLE affect_events",
		"DROP TABLE affect_states",
		"DELETE FROM schema_migrations WHERE name = '0013_affect'",
	} {
		if _, err := s.execContext(ctx, stmt); err != nil {
			_ = s.Close()
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("reopen upgraded store: %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })

	for _, table := range []string{"affect_states", "affect_events", "affect_history"} {
		cols, err := upgraded.tableColumns(context.Background(), table)
		if err != nil {
			t.Fatalf("inspect %s after upgrade: %v", table, err)
		}
		if len(cols) == 0 {
			t.Fatalf("table %s missing after upgrade", table)
		}
	}
	conv, err := upgraded.GetConversation(context.Background(), "old-conv")
	if err != nil || conv == nil || conv.GroupName != "老群" {
		t.Fatalf("old conversation after upgrade = %+v err=%v", conv, err)
	}
	var applied int
	if err := upgraded.queryRowContext(context.Background(),
		"SELECT COUNT(*) FROM schema_migrations WHERE name = '0013_affect'").Scan(&applied); err != nil {
		t.Fatalf("check migration: %v", err)
	}
	if applied != 1 {
		t.Fatalf("0013_affect recorded %d times, want 1", applied)
	}
}

// Compile-time guard: the fixture times above are RFC3339 and the store
// contract depends on that, so keep the format in one obvious place.
var _ = time.RFC3339
