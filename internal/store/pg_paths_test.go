package store

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// pgPathsOpen opens a throwaway PostgreSQL database (see pgScratchDatabase) and
// fails the test with a readable message when PG_TEST_DSN is not set.
func pgPathsOpen(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dsn := pgScratchDatabase(t)
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open scratch postgres: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, context.Background()
}

// Settings are upserted by key inside one transaction; the timestamp column is
// a non-null timestamptz, so this also proves the write path accepts RFC3339
// strings and that reads come back with the +08:00 suffix.
func TestPostgresSettingsUpsert(t *testing.T) {
	s, ctx := pgPathsOpen(t)
	key := fmt.Sprintf("pg.paths.%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = s.execContext(context.Background(), `DELETE FROM settings WHERE k = ?`, key) })

	if err := s.SaveSettings(ctx, map[string]string{key: "v1"}); err != nil {
		t.Fatalf("save v1: %v", err)
	}
	got, err := s.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got[key] != "v1" {
		t.Fatalf("settings[%q] = %q, want v1", key, got[key])
	}

	if err := s.SaveSettings(ctx, map[string]string{key: "v2"}); err != nil {
		t.Fatalf("upsert v2: %v", err)
	}
	got, err = s.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got[key] != "v2" {
		t.Fatalf("settings[%q] = %q after upsert, want v2", key, got[key])
	}

	var updatedAt string
	if err := s.queryRowContext(ctx,
		`SELECT updated_at FROM settings WHERE k = ?`, key).Scan(&updatedAt); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if !strings.HasSuffix(updatedAt, "+08:00") {
		t.Fatalf("settings.updated_at = %q, want a +08:00 suffix", updatedAt)
	}
}

// stats.bucket is a TIMESTAMPTZ primary key and SaveStats is an upsert: the
// second write must replace the counters of the same bucket, not duplicate it
// or fail on the primary key.
func TestPostgresStatsUpsert(t *testing.T) {
	s, ctx := pgPathsOpen(t)
	bucket := "2026-11-01T12:00:00+08:00"
	since := "2026-11-01T00:00:00+08:00"
	t.Cleanup(func() { _, _ = s.execContext(context.Background(), `DELETE FROM stats WHERE bucket = ?`, bucket) })

	if err := s.SaveStats(ctx, []StatBucket{{Bucket: bucket, In: 1, Out: 2, Errors: 0, LatencySumMS: 10, LatencyCount: 1, Tokens: 3}}); err != nil {
		t.Fatalf("save stats: %v", err)
	}
	find := func() *StatBucket {
		t.Helper()
		buckets, err := s.LoadStats(ctx, since)
		if err != nil {
			t.Fatalf("load stats: %v", err)
		}
		for i := range buckets {
			if buckets[i].Bucket == bucket {
				return &buckets[i]
			}
		}
		return nil
	}
	first := find()
	if first == nil || first.In != 1 || first.Out != 2 || first.Tokens != 3 {
		t.Fatalf("first stats write read back as %+v", first)
	}

	if err := s.SaveStats(ctx, []StatBucket{{Bucket: bucket, In: 5, Out: 6, Errors: 1, LatencySumMS: 30, LatencyCount: 3, Tokens: 9}}); err != nil {
		t.Fatalf("upsert stats: %v", err)
	}
	second := find()
	if second == nil || second.In != 5 || second.Out != 6 || second.Errors != 1 ||
		second.LatencySumMS != 30 || second.LatencyCount != 3 || second.Tokens != 9 {
		t.Fatalf("upserted stats read back as %+v", second)
	}
}

// The task lease is the one multi-statement transaction on the read path: it
// selects pending rows, marks them leased and reads them back inside one
// transaction. On PostgreSQL the "unset lease" predicate is IS NULL and an
// expired lease is a timestamptz comparison, so both branches need coverage.
func TestPostgresTaskLeaseTransaction(t *testing.T) {
	s, ctx := pgPathsOpen(t)
	now := time.Now()
	tag := fmt.Sprintf("pg-lease-%d", now.UnixNano())
	cleanup := func() {
		_, _ = s.execContext(context.Background(), `DELETE FROM scheduled_tasks WHERE group_openid = ?`, tag+":G")
	}
	cleanup()
	t.Cleanup(cleanup)

	var ids []int64
	for i := 0; i < 2; i++ {
		id, err := s.AddTask(ctx, TaskInput{
			ConversationID: tag, GroupOpenID: tag + ":G", MemberOpenID: "U1",
			CreatorOpenID: "U1", Kind: "reminder", Text: fmt.Sprintf("%s-%d", tag, i),
			DueAt: now.Add(-time.Minute).Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("add task %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	// First lease: the unset lease_until is NULL, i.e. IS NULL on PostgreSQL.
	leased, err := s.LeaseDueTasks(ctx, now, 10, time.Minute)
	if err != nil {
		t.Fatalf("lease tasks: %v", err)
	}
	seen := map[int64]ScheduledTask{}
	for _, task := range leased {
		seen[task.ID] = task
	}
	for _, id := range ids {
		task, ok := seen[id]
		if !ok {
			t.Fatalf("task %d was not leased: %+v", id, leased)
		}
		if task.Status != TaskLeased || task.Attempts != 1 {
			t.Fatalf("leased task %d = status %q attempts %d, want leased/1", id, task.Status, task.Attempts)
		}
		if !strings.HasSuffix(task.LeaseUntil, "+08:00") {
			t.Fatalf("leased task %d lease_until = %q, want a +08:00 suffix", id, task.LeaseUntil)
		}
		if task.SentAt != "" {
			t.Fatalf("leased task %d sent_at = %q, want empty", id, task.SentAt)
		}
	}

	// A second pass must not hand the same rows out again while leased.
	again, err := s.LeaseDueTasks(ctx, now.Add(30*time.Second), 10, time.Minute)
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	for _, task := range again {
		for _, id := range ids {
			if task.ID == id {
				t.Fatalf("task %d was leased twice: %+v", id, task)
			}
		}
	}

	// Expire the lease by hand. The current query only selects status='pending'
	// rows, so a leased row is NOT recovered even after its lease expires — a
	// crashed worker leaves it 'leased' forever. That gap is reported to the
	// team lead separately (internal/store/store.go:1601-1604); this assertion
	// pins today's behaviour so a future fix has to update it deliberately.
	if _, err := s.execContext(ctx,
		`UPDATE scheduled_tasks SET lease_until = ? WHERE id = ?`,
		now.Add(-2*time.Minute).Format(time.RFC3339), ids[0]); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	released, err := s.LeaseDueTasks(ctx, now.Add(time.Minute), 10, time.Minute)
	if err != nil {
		t.Fatalf("re-lease after expiry: %v", err)
	}
	for i := range released {
		if released[i].ID == ids[0] {
			t.Fatalf("expired-lease recovery is not implemented today, but task %d was re-leased: %+v",
				ids[0], released[i])
		}
	}
	// The lease timestamp itself is a timestamptz comparison against now, so a
	// past value is readable and comparable on PostgreSQL.
	var expired string
	if err := s.queryRowContext(ctx,
		`SELECT lease_until FROM scheduled_tasks WHERE id = ?`, ids[0]).Scan(&expired); err != nil {
		t.Fatalf("read expired lease: %v", err)
	}
	if !strings.HasSuffix(expired, "+08:00") {
		t.Fatalf("expired lease_until = %q, want a +08:00 suffix", expired)
	}

	// Finishing clears the lease and stamps sent_at.
	if err := s.FinishTask(ctx, ids[0], TaskSent, ""); err != nil {
		t.Fatalf("finish task: %v", err)
	}
	sent, err := s.ListTasks(ctx, TaskSent, 100, 0)
	if err != nil {
		t.Fatalf("list sent tasks: %v", err)
	}
	var done *ScheduledTask
	for i := range sent {
		if sent[i].ID == ids[0] {
			done = &sent[i]
		}
	}
	if done == nil {
		t.Fatalf("task %d missing from the sent list", ids[0])
	}
	if !strings.HasSuffix(done.SentAt, "+08:00") || done.LeaseUntil != "" {
		t.Fatalf("finished task = sent_at %q lease_until %q, want +08:00 and empty", done.SentAt, done.LeaseUntil)
	}
}

// tableColumns is dialect-aware: SQLite answers a PRAGMA, PostgreSQL answers
// information_schema. The migration probing depends on it, so prove it works on
// the engine that does not have a PRAGMA.
func TestPostgresTableColumns(t *testing.T) {
	s, ctx := pgPathsOpen(t)
	cols, err := s.tableColumns(ctx, "messages")
	if err != nil {
		t.Fatalf("tableColumns(messages): %v", err)
	}
	for _, want := range []string{"id", "conversation_id", "addressed", "created_at"} {
		if !cols[want] {
			t.Fatalf("tableColumns(messages) is missing %q: %v", want, cols)
		}
	}
	if len(cols) != 12 {
		t.Fatalf("tableColumns(messages) returned %d columns, want 12: %v", len(cols), cols)
	}
}

// The startup log prints driver + Target(); Target() must be the redacted DSN so
// a PostgreSQL password never reaches the log file.
func TestPostgresDriverAndRedactedTarget(t *testing.T) {
	dsn := pgScratchDatabase(t)
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open scratch postgres: %v", err)
	}
	defer func() { _ = s.Close() }()

	if got := s.Driver(); got != DriverPostgres {
		t.Fatalf("Driver() = %q, want %q", got, DriverPostgres)
	}
	target := s.Target()
	if !strings.Contains(target, "postgres://") || !strings.Contains(target, "redacted") {
		t.Fatalf("Target() = %q, want a redacted postgres DSN", target)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	if parsed.User != nil {
		if password, ok := parsed.User.Password(); ok && password != "" {
			if strings.Contains(target, password) {
				t.Fatalf("Target() leaked the PostgreSQL password: %q", target)
			}
		}
	}

	// The handle uses PostgreSQL semantics for size and path.
	if s.Path() != target {
		t.Fatalf("Path() = %q, want the same redacted target", s.Path())
	}
	if size := s.SizeBytes(); size <= 0 {
		t.Fatalf("SizeBytes() = %d, want the pg_database_size result", size)
	}
}
