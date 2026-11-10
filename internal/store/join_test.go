package store

import (
	"context"
	"os"
	"testing"

	"github.com/dynesshely/bot-harness/internal/testpg"
)

// joinStoreContract is the engine-independent half of the join-gate store
// tests: SQLite always runs it, PostgreSQL runs the same body against a
// throwaway database when PG_TEST_DSN is configured.
func joinStoreContract(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()

	// --- recent group messages ------------------------------------------------
	convs := []Conversation{
		{ID: "join-conv-a", Scene: SceneGroupMember, GroupOpenID: "G-JOIN-1", GroupName: "join one"},
		{ID: "join-conv-b", Scene: SceneGroupMember, GroupOpenID: "G-JOIN-1", GroupName: "join one"},
		{ID: "join-conv-c", Scene: SceneGroupMember, GroupOpenID: "G-JOIN-2", GroupName: "join two"},
	}
	for i := range convs {
		if err := s.EnsureConversation(ctx, &convs[i]); err != nil {
			t.Fatalf("seed conversation %s: %v", convs[i].ID, err)
		}
	}
	appendMsg := func(conv, role, speaker, text string) int64 {
		t.Helper()
		id, err := s.AppendMessageID(ctx, MessageInput{
			ConversationID: conv, Role: role, Speaker: speaker, Content: text,
			Kind: KindObserved,
		})
		if err != nil {
			t.Fatalf("append message to %s: %v", conv, err)
		}
		return id
	}
	firstA := appendMsg("join-conv-a", "user", "小明", "第一句")
	appendMsg("join-conv-c", "user", "别的群", "不该出现")
	secondA := appendMsg("join-conv-a", "assistant", "Kaku", "第二句")
	firstB := appendMsg("join-conv-b", "user", "小红", "第三句")

	got, err := s.RecentGroupMessages(ctx, "G-JOIN-1", 2)
	if err != nil {
		t.Fatalf("recent group messages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("recent group messages = %+v, want 2 rows", got)
	}
	// Newest two by id, returned oldest first: secondA then firstB.
	if got[0].ID != secondA || got[1].ID != firstB {
		t.Fatalf("recent group messages ids = %d,%d, want %d,%d",
			got[0].ID, got[1].ID, secondA, firstB)
	}
	if got[0].ConversationID != "join-conv-a" || got[0].Role != "assistant" ||
		got[0].Speaker != "Kaku" || got[0].Content != "第二句" || got[0].CreatedAt == "" {
		t.Fatalf("recent group message fields = %+v", got[0])
	}
	// The limit keeps the newest window, not the oldest.
	limited, err := s.RecentGroupMessages(ctx, "G-JOIN-1", 1)
	if err != nil {
		t.Fatalf("limited recent group messages: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != firstB {
		t.Fatalf("limited recent group messages = %+v, want only id %d", limited, firstB)
	}
	if none, err := s.RecentGroupMessages(ctx, "G-JOIN-none", 10); err != nil || len(none) != 0 {
		t.Fatalf("messages for unknown group = %+v err=%v", none, err)
	}
	// firstA is older than the window above; the store must not have dropped it.
	if all, _ := s.RecentGroupMessages(ctx, "G-JOIN-1", 10); len(all) != 3 || all[0].ID != firstA {
		t.Fatalf("full recent group messages = %+v", all)
	}

	// --- decisions ------------------------------------------------------------
	const (
		t1 = "2026-01-01T09:00:00+08:00"
		t2 = "2026-01-01T09:05:00+08:00"
		t3 = "2026-01-01T09:10:00+08:00"
		t4 = "2026-01-01T09:15:00+08:00"
	)
	seed := []JoinDecision{
		{GroupOpenID: "G-JOIN-1", ConversationID: "join-conv-a", MessageID: "m1", Speaker: "小明",
			Preview: "有人懂 Go 吗", Joined: true, Confidence: 0.82, Reason: "求助",
			TopicsJSON: `{"topics":[]}`, Model: "test-model", LatencyMS: 120, CreatedAt: t1},
		{GroupOpenID: "G-JOIN-1", ConversationID: "join-conv-a", MessageID: "m2", Speaker: "小明",
			Preview: "闲聊", Joined: false, Confidence: 0.2, Reason: "cooldown", LatencyMS: 0, CreatedAt: t2},
		{GroupOpenID: "G-JOIN-2", ConversationID: "join-conv-c", MessageID: "m3", Speaker: "小红",
			Preview: "另一个群", Joined: true, Confidence: 0.7, Reason: "话题延续", Model: "test-model", LatencyMS: 90, CreatedAt: t3},
		{GroupOpenID: "G-JOIN-1", ConversationID: "join-conv-b", MessageID: "m4", Speaker: "小红",
			Preview: "换了会话", Joined: false, Confidence: 0.1, Reason: "闲聊", LatencyMS: 0, CreatedAt: t4},
	}
	for _, d := range seed {
		if err := s.AppendJoinDecision(ctx, d); err != nil {
			t.Fatalf("append decision %+v: %v", d, err)
		}
	}
	// An empty created_at is filled in by the store.
	if err := s.AppendJoinDecision(ctx, JoinDecision{
		GroupOpenID: "G-JOIN-1", ConversationID: "join-conv-a", MessageID: "m5",
		Joined: false, Reason: "gate error: timeout",
	}); err != nil {
		t.Fatalf("append decision without created_at: %v", err)
	}
	if last, err := s.LastJoinDecisionAt(ctx, "G-JOIN-1"); err != nil || last == "" {
		t.Fatalf("last decision at = %q err=%v, want a timestamp", last, err)
	}

	byGroup, err := s.ListJoinDecisions(ctx, "G-JOIN-1", 10)
	if err != nil {
		t.Fatalf("list decisions: %v", err)
	}
	if len(byGroup) != 4 {
		t.Fatalf("group decisions = %d, want 4", len(byGroup))
	}
	// Newest first: m5 (now), then t4, t2, t1.
	if byGroup[0].MessageID != "m5" || byGroup[1].MessageID != "m4" ||
		byGroup[2].MessageID != "m2" || byGroup[3].MessageID != "m1" {
		t.Fatalf("group decision order = %s,%s,%s,%s",
			byGroup[0].MessageID, byGroup[1].MessageID, byGroup[2].MessageID, byGroup[3].MessageID)
	}
	if !byGroup[3].Joined || byGroup[3].Confidence != 0.82 || byGroup[3].LatencyMS != 120 ||
		byGroup[3].TopicsJSON != `{"topics":[]}` || byGroup[3].CreatedAt != t1 {
		t.Fatalf("decision round-trip = %+v", byGroup[3])
	}

	all, err := s.ListJoinDecisions(ctx, "", 10)
	if err != nil {
		t.Fatalf("list all decisions: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("all decisions = %d, want 5", len(all))
	}
	if capped, _ := s.ListJoinDecisions(ctx, "", 2); len(capped) != 2 {
		t.Fatalf("limited decisions = %+v", capped)
	}
	if none, _ := s.ListJoinDecisions(ctx, "G-JOIN-none", 10); len(none) != 0 {
		t.Fatalf("decisions for unknown group = %+v", none)
	}

	// Counts used by the gate's rate limits.
	// t2, t4 and the auto-timestamped m5 fall into the window.
	if n, err := s.CountJoinDecisionsSince(ctx, "G-JOIN-1", t2, false); err != nil || n != 3 {
		t.Fatalf("count since t2 = %d err=%v, want 3", n, err)
	}
	if n, err := s.CountJoinDecisionsSince(ctx, "G-JOIN-1", t1, true); err != nil || n != 1 {
		t.Fatalf("joined since t1 = %d err=%v, want 1", n, err)
	}
	if n, err := s.CountJoinDecisionsSince(ctx, "G-JOIN-1", t4, true); err != nil || n != 0 {
		t.Fatalf("joined since t4 = %d err=%v, want 0", n, err)
	}
	if last, err := s.LastJoinDecisionAt(ctx, "G-JOIN-2"); err != nil || last != t3 {
		t.Fatalf("last decision at G-JOIN-2 = %q err=%v, want %q", last, err, t3)
	}
	if last, err := s.LastJoinDecisionAt(ctx, "G-JOIN-none"); err != nil || last != "" {
		t.Fatalf("last decision at unknown group = %q err=%v, want empty", last, err)
	}

	// --- topics ---------------------------------------------------------------
	if err := s.SaveJoinTopics(ctx, "G-JOIN-1", `{"topics":[{"id":"t1"}]}`, t1); err != nil {
		t.Fatalf("save topics: %v", err)
	}
	if err := s.SaveJoinTopics(ctx, "G-JOIN-1", `{"topics":[{"id":"t2"}]}`, t3); err != nil {
		t.Fatalf("upsert topics: %v", err)
	}
	topics, err := s.GetJoinTopics(ctx, "G-JOIN-1")
	if err != nil || topics == nil {
		t.Fatalf("get topics = %+v err=%v", topics, err)
	}
	if topics.GroupOpenID != "G-JOIN-1" || topics.TopicsJSON != `{"topics":[{"id":"t2"}]}` ||
		topics.AnalyzedAt != t3 {
		t.Fatalf("topics round-trip = %+v", topics)
	}
	if missing, err := s.GetJoinTopics(ctx, "G-JOIN-2"); err != nil || missing != nil {
		t.Fatalf("missing topics = %+v err=%v, want nil", missing, err)
	}

	// --- cascade --------------------------------------------------------------
	// Clearing one conversation drops its decisions; the group topic cache is
	// group-scoped and survives (other conversations still use it).
	if err := s.ClearConversationMessages(ctx, "join-conv-a"); err != nil {
		t.Fatalf("clear conversation a: %v", err)
	}
	if left, _ := s.ListJoinDecisions(ctx, "G-JOIN-1", 10); len(left) != 1 || left[0].ConversationID != "join-conv-b" {
		t.Fatalf("decisions after clearing conv a = %+v", left)
	}
	if topics, _ := s.GetJoinTopics(ctx, "G-JOIN-1"); topics == nil {
		t.Fatal("topic cache must survive a per-conversation clear")
	}
	// Decisions of the other conversation still cascade independently.
	if err := s.ClearConversationMessages(ctx, "join-conv-b"); err != nil {
		t.Fatalf("clear conversation b: %v", err)
	}
	if left, _ := s.ListJoinDecisions(ctx, "G-JOIN-1", 10); len(left) != 0 {
		t.Fatalf("decisions survived clearing every conversation of the group: %+v", left)
	}

	// Clear-all drops both tables.
	if err := s.SaveJoinTopics(ctx, "G-JOIN-1", "{}", t4); err != nil {
		t.Fatalf("reseed topics: %v", err)
	}
	if err := s.ClearAllConversations(ctx); err != nil {
		t.Fatalf("clear all conversations: %v", err)
	}
	if left, _ := s.ListJoinDecisions(ctx, "", 10); len(left) != 0 {
		t.Fatalf("decisions survived clear-all: %+v", left)
	}
	if topics, _ := s.GetJoinTopics(ctx, "G-JOIN-1"); topics != nil {
		t.Fatalf("topics survived clear-all: %+v", topics)
	}
}

func TestJoinStoreSQLite(t *testing.T) {
	s, err := Open(t.TempDir() + "/join.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	joinStoreContract(t, s)
}

// TestJoinStorePostgres runs the same contract on PostgreSQL. It needs
// PG_TEST_DSN, and pgScratchDatabase refuses the production database before
// opening a connection.
func TestJoinStorePostgres(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN not set; skipping the PostgreSQL join store test")
	}
	testpg.Guard(t, dsn)
	s, err := Open(pgScratchDatabase(t))
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	joinStoreContract(t, s)
}

// TestJoinTablesExistOnFreshDatabase pins the two tables (and their indexes)
// to the base schema rather than only to migration 0014.
func TestJoinTablesExistOnFreshDatabase(t *testing.T) {
	s, err := Open(t.TempDir() + "/join-fresh.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	for _, table := range []string{"join_decisions", "join_topics"} {
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
		"SELECT COUNT(*) FROM schema_migrations WHERE name = ?", "0014_join_gate").Scan(&applied); err != nil {
		t.Fatalf("check migration: %v", err)
	}
	if applied != 1 {
		t.Fatalf("0014_join_gate recorded %d times, want 1", applied)
	}
}

// TestJoinMigrationUpgradeKeepsData simulates a database that stopped at 0013
// (no join tables, no 0014 row) and reopens it: both tables must appear, the
// migration must be recorded once, and existing rows must survive.
func TestJoinMigrationUpgradeKeepsData(t *testing.T) {
	path := t.TempDir() + "/join-upgrade.db"
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open initial store: %v", err)
	}
	ctx := context.Background()
	if err := s.EnsureConversation(ctx, &Conversation{
		ID: "join-old-conv", Scene: SceneGroupMember, GroupOpenID: "G-OLD", GroupName: "老群",
	}); err != nil {
		_ = s.Close()
		t.Fatalf("seed conversation: %v", err)
	}
	for _, stmt := range []string{
		"DROP TABLE join_decisions",
		"DROP TABLE join_topics",
		"DELETE FROM schema_migrations WHERE name = '0014_join_gate'",
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

	for _, table := range []string{"join_decisions", "join_topics"} {
		cols, err := upgraded.tableColumns(ctx, table)
		if err != nil {
			t.Fatalf("inspect %s after upgrade: %v", table, err)
		}
		if len(cols) == 0 {
			t.Fatalf("table %s missing after upgrade", table)
		}
	}
	conv, err := upgraded.GetConversation(ctx, "join-old-conv")
	if err != nil || conv == nil || conv.GroupName != "老群" {
		t.Fatalf("old conversation after upgrade = %+v err=%v", conv, err)
	}
	var applied int
	if err := upgraded.queryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE name = '0014_join_gate'").Scan(&applied); err != nil {
		t.Fatalf("check migration: %v", err)
	}
	if applied != 1 {
		t.Fatalf("0014_join_gate recorded %d times, want 1", applied)
	}
}
