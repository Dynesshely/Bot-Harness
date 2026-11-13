package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/testpg"
)

// postgresTimestampColumns is every (table.column) the harness stores as a
// timestamp. Kept next to the schema it pins rather than derived from it, so a
// new time column that misses the timestamptz treatment fails this test.
var postgresTimestampColumns = []string{
	"affect_events.created_at",
	"affect_history.at",
	"affect_states.paused_until",
	"affect_states.ticked_at",
	"affect_states.updated_at",
	"commitments.created_at",
	"commitments.deadline_at",
	"commitments.last_nudge_at",
	"commitments.next_check_at",
	"commitments.ack_at",
	"conversations.created_at",
	"conversations.last_active_at",
	"conversation_summaries.updated_at",
	"dedup.created_at",
	"expressions.created_at",
	"expressions.updated_at",
	"human_status.expires_at",
	"human_status.updated_at",
	"join_decisions.created_at",
	"join_topics.analyzed_at",
	"media.created_at",
	"memories.created_at",
	"memories.last_used_at",
	"messages.created_at",
	"proactive_state.last_sent_at",
	"proactive_state.updated_at",
	"records.created_at",
	"scheduled_tasks.created_at",
	"scheduled_tasks.due_at",
	"scheduled_tasks.lease_until",
	"scheduled_tasks.sent_at",
	"schema_migrations.applied_at",
	"settings.updated_at",
	"stats.bucket",
	"tool_calls.created_at",
}

// postgresNullableTimestampColumns are the timestamp columns where the empty
// string is the "unset" value. They must be nullable on PostgreSQL.
var postgresNullableTimestampColumns = map[string]bool{
	"affect_states.paused_until":   true,
	"affect_states.ticked_at":      true,
	"commitments.ack_at":           true,
	"commitments.deadline_at":      true,
	"commitments.last_nudge_at":    true,
	"memories.last_used_at":        true,
	"proactive_state.last_sent_at": true,
	"proactive_state.updated_at":   true,
	"scheduled_tasks.lease_until":  true,
	"scheduled_tasks.sent_at":      true,
}

// TestPostgresTimestampColumnTypes asserts the physical target schema. A TEXT
// column left over from an older database would make every read return the old
// text verbatim and every write accept the RFC3339 string as TEXT, so the Go
// semantics would silently drift; information_schema is the only place that
// difference is visible before the behavioural tests start passing for the
// wrong reason.
func TestPostgresTimestampColumnTypes(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN not set; skipping the PostgreSQL timestamp schema test")
	}
	testpg.Guard(t, dsn)
	ctx := context.Background()
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer s.Close()

	for _, key := range postgresTimestampColumns {
		table, column, ok := strings.Cut(key, ".")
		if !ok {
			t.Fatalf("bad column key %q", key)
		}
		var dataType, nullable string
		err := s.db.QueryRowContext(ctx,
			"SELECT data_type, is_nullable FROM information_schema.columns\n"+
				" WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2",
			table, column).Scan(&dataType, &nullable)
		if err != nil {
			t.Errorf("%s: read information_schema: %v", key, err)
			continue
		}
		if dataType != "timestamp with time zone" {
			t.Errorf("%s: data_type = %q, want timestamp with time zone", key, dataType)
		}
		wantNullable := "NO"
		if postgresNullableTimestampColumns[key] {
			wantNullable = "YES"
		}
		if nullable != wantNullable {
			t.Errorf("%s: is_nullable = %q, want %q", key, nullable, wantNullable)
		}
	}

	// Completeness check: a new timestamp column that was never added to the
	// list above must turn this test red, otherwise the guard silently stops
	// covering the schema it claims to pin.
	expected := make(map[string]bool, len(postgresTimestampColumns))
	for _, key := range postgresTimestampColumns {
		expected[key] = true
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT table_name, column_name FROM information_schema.columns "+
			"WHERE table_schema = current_schema() AND data_type = 'timestamp with time zone'")
	if err != nil {
		t.Fatalf("list timestamptz columns: %v", err)
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("scan timestamptz column: %v", err)
		}
		key := table + "." + column
		seen[key] = true
		if !expected[key] {
			t.Errorf("%s: timestamp with time zone column is missing from postgresTimestampColumns", key)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate timestamptz columns: %v", err)
	}
	for key := range expected {
		if !seen[key] {
			t.Errorf("%s: listed timestamp column does not exist in the schema", key)
		}
	}
	t.Logf("%d time columns checked: all data_type=\"timestamp with time zone\" (%d nullable, %d NOT NULL)",
		len(postgresTimestampColumns), len(postgresNullableTimestampColumns),
		len(postgresTimestampColumns)-len(postgresNullableTimestampColumns))
}

// TestPostgresTimestampStrings is the behavioural half: the store still speaks
// RFC3339 strings, reads always carry the +08:00 offset, and a nullable column
// written as "" reads back as "" (while being NULL in the database).
func TestPostgresTimestampStrings(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN not set; skipping the PostgreSQL timestamp read-back test")
	}
	testpg.Guard(t, dsn)
	ctx := context.Background()
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer s.Close()

	tag := fmt.Sprintf("pg-ts-%d", time.Now().UnixNano())
	convID := tag
	groupID := tag + ":G"
	memberID := tag + ":M"
	statsBucket := "2026-11-01T12:00:00+08:00"
	cleanup := func() {
		_, _ = s.execContext(ctx, "DELETE FROM memories WHERE group_openid = ?", groupID)
		_, _ = s.execContext(ctx, "DELETE FROM scheduled_tasks WHERE group_openid = ?", groupID)
		_, _ = s.execContext(ctx, "DELETE FROM proactive_state WHERE group_openid = ?", groupID)
		_, _ = s.execContext(ctx, "DELETE FROM conversations WHERE id = ?", convID)
		_, _ = s.execContext(ctx, "DELETE FROM stats WHERE bucket = ?", statsBucket)
	}
	cleanup()
	defer cleanup()

	// --- non-nullable: written as RFC3339 and read back in +08:00 ------------
	if err := s.EnsureConversation(ctx, &Conversation{
		ID: convID, Scene: SceneGroupMember, GroupOpenID: groupID, MemberOpenID: memberID, GroupName: "时间测试",
	}); err != nil {
		t.Fatalf("ensure conversation: %v", err)
	}
	conv, err := s.GetConversation(ctx, convID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if !strings.HasSuffix(conv.CreatedAt, "+08:00") || !strings.HasSuffix(conv.LastActiveAt, "+08:00") {
		t.Fatalf("conversation timestamps = %q / %q, want a +08:00 suffix", conv.CreatedAt, conv.LastActiveAt)
	}
	var pgType string
	if err := s.queryRowContext(ctx,
		"SELECT pg_typeof(created_at)::text FROM conversations WHERE id = ?", convID).Scan(&pgType); err != nil {
		t.Fatalf("read pg_typeof(created_at): %v", err)
	}
	if pgType != "timestamp with time zone" {
		t.Fatalf("pg_typeof(conversations.created_at) = %q, want timestamp with time zone", pgType)
	}

	// --- nullable: "" is stored as NULL and read back as "" ------------------
	if err := s.SaveProactiveState(ctx, ProactiveState{
		GroupOpenID: groupID, LastSentAt: "", SilentStreak: 2, Sends: 3, LastReason: "unset",
	}); err != nil {
		t.Fatalf("save proactive state: %v", err)
	}
	st, err := s.GetProactiveState(ctx, groupID)
	if err != nil {
		t.Fatalf("get proactive state: %v", err)
	}
	if st.LastSentAt != "" {
		t.Fatalf("empty last_sent_at read back as %q, want the empty string", st.LastSentAt)
	}
	if !strings.HasSuffix(st.UpdatedAt, "+08:00") {
		t.Fatalf("proactive updated_at = %q, want a +08:00 suffix", st.UpdatedAt)
	}
	var lastSentIsNull bool
	if err := s.queryRowContext(ctx,
		"SELECT last_sent_at IS NULL FROM proactive_state WHERE group_openid = ?", groupID).Scan(&lastSentIsNull); err != nil {
		t.Fatalf("read last_sent_at IS NULL: %v", err)
	}
	if !lastSentIsNull {
		t.Fatal("the empty last_sent_at was stored as a non-NULL timestamptz")
	}
	t.Logf("nullable read-back evidence: proactive_state.last_sent_at=\"\" → %q (NULL in PG: %v); updated_at → %q",
		st.LastSentAt, lastSentIsNull, st.UpdatedAt)

	// A non-empty value in any offset is normalised to +08:00 on read.
	if err := s.SaveProactiveState(ctx, ProactiveState{
		GroupOpenID: groupID, LastSentAt: "2026-11-01T17:14:40Z", SilentStreak: 0, Sends: 4, LastReason: "set",
	}); err != nil {
		t.Fatalf("save proactive state with a timestamp: %v", err)
	}
	st, err = s.GetProactiveState(ctx, groupID)
	if err != nil {
		t.Fatalf("get proactive state with a timestamp: %v", err)
	}
	const wantLastSent = "2026-11-02T01:14:40+08:00"
	if st.LastSentAt != wantLastSent {
		t.Fatalf("last_sent_at = %q, want %q", st.LastSentAt, wantLastSent)
	}
	t.Logf("non-empty read-back evidence: 2026-11-01T17:14:40Z → %q (suffix +08:00: %v)",
		st.LastSentAt, strings.HasSuffix(st.LastSentAt, "+08:00"))
	states, err := s.ListProactiveStates(ctx)
	if err != nil {
		t.Fatalf("list proactive states: %v", err)
	}
	found := false
	for i := range states {
		if states[i].GroupOpenID == groupID {
			found = true
			if states[i].LastSentAt != wantLastSent || !strings.HasSuffix(states[i].UpdatedAt, "+08:00") {
				t.Fatalf("ListProactiveStates returned %+v", states[i])
			}
		}
	}
	if !found {
		t.Fatal("ListProactiveStates did not return the saved group")
	}

	// --- scheduled tasks: lease/sent columns are nullable --------------------
	taskID, err := s.AddTask(ctx, TaskInput{
		ConversationID: convID, GroupOpenID: groupID, MemberOpenID: memberID,
		CreatorOpenID: memberID, Kind: "reminder", Text: tag, DueAt: nowRFC3339(),
	})
	if err != nil {
		t.Fatalf("add task: %v", err)
	}
	tasks, err := s.ListTasks(ctx, "", 100, 0)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	var pending *ScheduledTask
	for i := range tasks {
		if tasks[i].ID == taskID {
			pending = &tasks[i]
		}
	}
	if pending == nil {
		t.Fatalf("task %d is missing from ListTasks", taskID)
	}
	if pending.LeaseUntil != "" || pending.SentAt != "" {
		t.Fatalf("new task timestamps = lease %q / sent %q, want empty", pending.LeaseUntil, pending.SentAt)
	}

	claimed, err := s.LeaseDueTasks(ctx, time.Now().Add(time.Minute), 50, time.Minute)
	if err != nil {
		t.Fatalf("lease due tasks: %v", err)
	}
	var leased *ScheduledTask
	for i := range claimed {
		if claimed[i].ID == taskID {
			leased = &claimed[i]
		}
	}
	if leased == nil {
		t.Fatalf("task %d was not leased", taskID)
	}
	if !strings.HasSuffix(leased.LeaseUntil, "+08:00") {
		t.Fatalf("lease_until = %q, want a +08:00 suffix", leased.LeaseUntil)
	}
	if leased.SentAt != "" {
		t.Fatalf("sent_at = %q before the task was sent, want empty", leased.SentAt)
	}

	if err := s.FinishTask(ctx, taskID, TaskSent, ""); err != nil {
		t.Fatalf("finish task: %v", err)
	}
	finished, err := s.ListTasks(ctx, TaskSent, 100, 0)
	if err != nil {
		t.Fatalf("list sent tasks: %v", err)
	}
	var done *ScheduledTask
	for i := range finished {
		if finished[i].ID == taskID {
			done = &finished[i]
		}
	}
	if done == nil {
		t.Fatalf("task %d is missing from the sent tasks", taskID)
	}
	if !strings.HasSuffix(done.SentAt, "+08:00") {
		t.Fatalf("sent_at = %q after FinishTask, want a +08:00 suffix", done.SentAt)
	}
	if done.LeaseUntil != "" {
		t.Fatalf("lease_until = %q after FinishTask, want empty", done.LeaseUntil)
	}
	t.Logf("task read-back evidence: sent_at → %q (suffix +08:00: %v), lease_until → %q",
		done.SentAt, strings.HasSuffix(done.SentAt, "+08:00"), done.LeaseUntil)

	// --- memories: last_used_at is nullable ----------------------------------
	memID, err := s.AddMemory(ctx, MemoryInput{
		Scope: MemoryMember, GroupOpenID: groupID, MemberOpenID: memberID,
		Kind: "fact", Text: tag, Source: "test",
	})
	if err != nil {
		t.Fatalf("add memory: %v", err)
	}
	memories, err := s.ListMemories(ctx, MemoryMember, groupID, memberID, 10, 0)
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	var mem *Memory
	for i := range memories {
		if memories[i].ID == memID {
			mem = &memories[i]
		}
	}
	if mem == nil {
		t.Fatalf("memory %d is missing from ListMemories", memID)
	}
	if mem.LastUsedAt != "" {
		t.Fatalf("fresh memory last_used_at = %q, want empty", mem.LastUsedAt)
	}
	if !strings.HasSuffix(mem.CreatedAt, "+08:00") {
		t.Fatalf("memory created_at = %q, want a +08:00 suffix", mem.CreatedAt)
	}

	if err := s.TouchMemories(ctx, []int64{memID}); err != nil {
		t.Fatalf("touch memories: %v", err)
	}
	visible, err := s.VisibleMemories(ctx, MemoryMember, groupID, memberID, "", 10)
	if err != nil {
		t.Fatalf("visible memories: %v", err)
	}
	var used *Memory
	for i := range visible {
		if visible[i].ID == memID {
			used = &visible[i]
		}
	}
	if used == nil {
		t.Fatalf("memory %d is missing from VisibleMemories", memID)
	}
	if !strings.HasSuffix(used.LastUsedAt, "+08:00") {
		t.Fatalf("last_used_at = %q after TouchMemories, want a +08:00 suffix", used.LastUsedAt)
	}

	// --- stats.bucket is a timestamp and a primary key ------------------------
	if err := s.SaveStats(ctx, []StatBucket{{Bucket: statsBucket, In: 1, Out: 2}}); err != nil {
		t.Fatalf("save stats: %v", err)
	}
	buckets, err := s.LoadStats(ctx, "2026-11-01T00:00:00+08:00")
	if err != nil {
		t.Fatalf("load stats: %v", err)
	}
	foundBucket := false
	for _, b := range buckets {
		if b.Bucket == statsBucket {
			foundBucket = true
		}
	}
	if !foundBucket {
		t.Fatalf("stats bucket %q was not read back in +08:00 form", statsBucket)
	}
}
