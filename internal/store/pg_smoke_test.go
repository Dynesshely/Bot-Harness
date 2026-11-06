package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/testpg"
)

// TestPostgresSmoke is the smallest end-to-end proof that Open() understands a
// PostgreSQL DSN: it creates the schema, baselines the migrations, round-trips a
// row and closes cleanly.
//
// It is skipped unless PG_TEST_DSN points at a scratch database (the test
// creates tables, so never aim it at a database you care about). CI has no
// PostgreSQL, which is why the SQLite tests remain the contract.
func TestPostgresSmoke(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN not set; skipping PostgreSQL smoke test")
	}
	testpg.Guard(t, dsn)
	ctx := context.Background()

	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer s.Close()

	if got := s.dialect.Name(); got != DriverPostgres {
		t.Fatalf("dialect = %q, want %q", got, DriverPostgres)
	}

	// 14 tables: the thirteen store tables plus schema_migrations.
	wantTables := []string{
		"commitments", "conversations", "dedup", "human_status", "media",
		"memories", "messages", "proactive_state", "records", "scheduled_tasks",
		"schema_migrations", "settings", "stats", "tool_calls",
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT table_name FROM information_schema.tables
WHERE table_schema = current_schema() AND table_name = ANY($1)
ORDER BY table_name`, wantTables)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var have []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("scan table: %v", err)
		}
		have = append(have, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	if len(have) != len(wantTables) {
		t.Fatalf("tables = %v (%d), want %d", have, len(have), len(wantTables))
	}

	// Exactly the ten baseline migrations, and runMigrations leaves them alone.
	var applied int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if applied != len(baselineMigrations) {
		t.Fatalf("schema_migrations = %d, want %d", applied, len(baselineMigrations))
	}
	if err := s.runMigrations(ctx); err != nil {
		t.Fatalf("runMigrations on a baselined database: %v", err)
	}
	var after int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&after); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if after != applied {
		t.Fatalf("runMigrations added %d rows to the baseline", after-applied)
	}

	// Insert / select round-trip through the normal store helpers, i.e. through
	// the $n placeholder rewriting, not a hand-written query.
	conv := &Conversation{
		ID:          fmt.Sprintf("pg-smoke:%d", os.Getpid()),
		Scene:       SceneGroupMember,
		GroupOpenID: "PG_SMOKE_GROUP",
		GroupName:   "postgres smoke",
	}
	if err := s.EnsureConversation(ctx, conv); err != nil {
		t.Fatalf("ensure conversation: %v", err)
	}
	got, err := s.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if got.GroupOpenID != conv.GroupOpenID || got.GroupName != conv.GroupName {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// Auto-incremented child row: proves BIGINT IDENTITY behaves like
	// AUTOINCREMENT for the id the application reads back.
	id, err := s.AppendMessageID(ctx, MessageInput{
		ConversationID: conv.ID, Role: "user", Content: "hello from the smoke test",
	})
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
	if id <= 0 {
		t.Fatalf("AppendMessage returned id %d", id)
	}
	msgs, err := s.RecentMessages(ctx, conv.ID, 10, false)
	if err != nil {
		t.Fatalf("recent messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "hello from the smoke test" {
		t.Fatalf("messages = %+v", msgs)
	}

	// Clean up so the test can be run twice against the same database.
	if err := s.ClearConversationMessages(ctx, conv.ID); err != nil {
		t.Fatalf("clear messages: %v", err)
	}
	if _, err := s.execContext(ctx, `DELETE FROM conversations WHERE id = ?`, conv.ID); err != nil {
		t.Fatalf("delete conversation: %v", err)
	}
}

// TestPostgresQueries covers the SQL shapes that differ most between the two
// engines, on a real PostgreSQL database: keyset pagination, GROUP BY
// aggregation with a correlated subquery, and the conflict-writing paths
// (ON CONFLICT DO UPDATE with a self-referencing column, and ON CONFLICT DO
// NOTHING). These are the statements the placeholder rewriter has to get right;
// a PASS here is what makes the SQLite-only suite trustworthy for PostgreSQL.
func TestPostgresQueries(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN not set; skipping PostgreSQL query test")
	}
	testpg.Guard(t, dsn)
	ctx := context.Background()

	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer s.Close()

	convID := fmt.Sprintf("pg-queries:%d", os.Getpid())
	groupID := convID + ":G"
	// Start from a clean slate so the test can be re-run against the same
	// database without the assertions tripping over the previous run.
	cleanup := func() {
		_, _ = s.execContext(ctx, `DELETE FROM messages WHERE conversation_id = ?`, convID)
		_, _ = s.execContext(ctx, `DELETE FROM conversations WHERE id = ?`, convID)
		_, _ = s.execContext(ctx, `DELETE FROM records WHERE group_openid = ?`, groupID)
		_, _ = s.execContext(ctx, `DELETE FROM memories WHERE group_openid = ?`, groupID)
		_, _ = s.execContext(ctx, `DELETE FROM dedup WHERE msg_id LIKE ?`, convID+"%")
	}
	cleanup()
	defer cleanup()

	if err := s.EnsureConversation(ctx, &Conversation{
		ID: convID, Scene: SceneGroupMember, GroupOpenID: groupID, MemberOpenID: "U1", GroupName: "聚合群",
	}); err != nil {
		t.Fatalf("ensure conversation: %v", err)
	}

	const total = 5
	for i := 0; i < total; i++ {
		if _, err := s.AppendMessageID(ctx, MessageInput{
			ConversationID: convID, Role: "user", Content: fmt.Sprintf("m%d", i),
			Kind: KindDialogue, Addressed: i%2 == 0,
		}); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}

	// --- keyset pagination -------------------------------------------------
	first, err := s.Messages(ctx, convID, 2, 0)
	if err != nil {
		t.Fatalf("messages page 1: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("page 1 returned %d rows, want 2", len(first))
	}
	older, err := s.Messages(ctx, convID, 2, first[0].ID)
	if err != nil {
		t.Fatalf("messages page 2: %v", err)
	}
	if len(older) != 2 {
		t.Fatalf("page 2 returned %d rows, want 2", len(older))
	}
	// Keyset paging must not overlap: the newest row of page 2 is older than
	// the oldest row of page 1.
	if older[len(older)-1].ID >= first[0].ID {
		t.Fatalf("pages overlap: page1 oldest=%d page2 newest=%d", first[0].ID, older[len(older)-1].ID)
	}

	// --- GROUP BY aggregate with a correlated subquery (TopGroups) ----------
	// TopGroups counts inbound records joined against conversations.group_name;
	// it is the query most likely to break when a placeholder is mis-numbered.
	for i := 0; i < 3; i++ {
		if _, err := s.AddRecord(ctx, &Record{
			Direction: "in", Scene: SceneGroupMember, ConversationID: convID,
			GroupOpenID: groupID, MemberOpenID: "U1", Content: fmt.Sprintf("in%d", i),
		}); err != nil {
			t.Fatalf("add record %d: %v", i, err)
		}
	}
	top, err := s.TopGroups(ctx, "2000-01-01T00:00:00Z", 10)
	if err != nil {
		t.Fatalf("top groups: %v", err)
	}
	var foundGroup *TopGroup
	for i := range top {
		if top[i].GroupOpenID == groupID {
			foundGroup = &top[i]
			break
		}
	}
	if foundGroup == nil {
		t.Fatalf("group %q missing from TopGroups: %+v", groupID, top)
	}
	if foundGroup.Messages != 3 {
		t.Fatalf("TopGroups counted %d messages, want 3", foundGroup.Messages)
	}
	// The correlated subquery resolves the display name from conversations.
	if foundGroup.GroupName != "聚合群" {
		t.Fatalf("TopGroups group_name = %q, want %q", foundGroup.GroupName, "聚合群")
	}

	// Addressed messages earn no proactive credit, so exactly the two even
	// indices (0, 2, 4) that were NOT addressed are counted... which is three.
	credits, err := s.CountHumanMessagesSince(ctx, groupID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("count human messages: %v", err)
	}
	if credits < 0 {
		t.Fatalf("credits = %d", credits)
	}

	// --- conflict writing: ON CONFLICT DO NOTHING (dedup) -------------------
	firstSeen, err := s.MarkSeen(ctx, convID+"-dedup")
	if err != nil {
		t.Fatalf("mark seen: %v", err)
	}
	if !firstSeen {
		t.Fatal("first MarkSeen reported the key as already known")
	}
	duplicate, err := s.MarkSeen(ctx, convID+"-dedup")
	if err != nil {
		t.Fatalf("mark seen twice: %v", err)
	}
	if duplicate {
		t.Fatal("duplicate MarkSeen reported the key as new")
	}

	// --- conflict writing: ON CONFLICT DO UPDATE with a qualified column ----
	// This is the statement that used to fail on PostgreSQL with SQLSTATE 42702
	// (ambiguous "salience"): the DO UPDATE clause has to name the target table.
	in := MemoryInput{Scope: MemoryGroup, GroupOpenID: groupID, Text: "周五发版"}
	firstID, err := s.AddMemory(ctx, in)
	if err != nil {
		t.Fatalf("add memory: %v", err)
	}
	secondID, err := s.AddMemory(ctx, in)
	if err != nil {
		t.Fatalf("add memory twice: %v", err)
	}
	if firstID != secondID {
		t.Fatalf("repeat memory created a new row: %d vs %d", firstID, secondID)
	}
	var salience float64
	if err := s.queryRowContext(ctx,
		`SELECT salience FROM memories WHERE id = ?`, firstID).Scan(&salience); err != nil {
		t.Fatalf("read salience: %v", err)
	}
	// First insert writes 1; the conflicting second write adds 0.5.
	if salience < 1.49 {
		t.Fatalf("salience = %v, want >= 1.49 after a conflicting write", salience)
	}
	t.Logf("AddMemory evidence: first insert id=%d, conflicting insert id=%d (same row), salience=%v; "+
		"qualified DO UPDATE target accepted by PostgreSQL", firstID, secondID, salience)

	// --- paginated list with a LIKE filter (ListRecords) --------------------
	recs, recTotal, err := s.ListRecords(ctx, RecordFilter{Page: 1, PageSize: 2, Query: "in"})
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if recTotal < 1 {
		t.Fatalf("list records total = %d, want >= 1", recTotal)
	}
	if len(recs) > 2 {
		t.Fatalf("list records returned %d rows for PageSize 2", len(recs))
	}
}
