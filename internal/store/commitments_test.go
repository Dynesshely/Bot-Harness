package store

import (
	"context"
	"testing"
	"time"
)

// everyCommitmentStore runs f against SQLite and, when PG_TEST_DSN is set,
// against a throwaway PostgreSQL database. The SQL is shared, so both engines
// must pass the same contract.
func everyCommitmentStore(t *testing.T, f func(*testing.T, *Store)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		s, err := Open(t.TempDir() + "/commitments.db")
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		f(t, s)
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := pgScratchDatabase(t)
		s, err := Open(dsn)
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		f(t, s)
	})
}

func TestCommitmentLeaseRescheduleAndFinish(t *testing.T) {
	everyCommitmentStore(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		now := time.Now()
		due, err := s.AddCommitment(ctx, CommitmentInput{
			ConversationID: "conv-1", GroupOpenID: "G1", MemberOpenID: "U1",
			CreatorOpenID: "U1", Text: "吃药", Priority: 2,
			NextCheckAt: now.Add(-time.Minute).Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("add due: %v", err)
		}
		future, err := s.AddCommitment(ctx, CommitmentInput{
			ConversationID: "conv-1", GroupOpenID: "G1", MemberOpenID: "U1",
			CreatorOpenID: "U1", Text: "交报告", Priority: 1,
			NextCheckAt: now.Add(time.Hour).Format(time.RFC3339),
			DeadlineAt:  now.Add(24 * time.Hour).Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("add future: %v", err)
		}

		leased, err := s.LeaseDueCommitments(ctx, now, 10, time.Minute)
		if err != nil || len(leased) != 1 || leased[0].ID != due {
			t.Fatalf("lease = %+v err=%v", leased, err)
		}
		if leased[0].Status != CommitmentLeased || leased[0].NudgeCount != 0 || leased[0].LastNudgeAt != "" {
			t.Fatalf("leased row = %+v", leased[0])
		}
		// A second lease inside the window must not hand it out again.
		again, _ := s.LeaseDueCommitments(ctx, now, 10, time.Minute)
		if len(again) != 0 {
			t.Fatalf("leased twice: %+v", again)
		}
		// After the lease expires the row is recoverable (crash recovery).
		recovered, _ := s.LeaseDueCommitments(ctx, now.Add(2*time.Minute), 10, time.Minute)
		if len(recovered) != 1 || recovered[0].ID != due {
			t.Fatalf("expired lease not recovered: %+v", recovered)
		}

		lastNudge := now.Format(time.RFC3339)
		next := now.Add(30 * time.Minute)
		if ok, err := s.RescheduleCommitment(ctx, due, next.Format(time.RFC3339), lastNudge, 1); err != nil || !ok {
			t.Fatalf("reschedule: ok=%v err=%v", ok, err)
		}
		items, err := s.ListCommitments(ctx, "conv-1", "U1", 10)
		if err != nil || len(items) != 2 {
			t.Fatalf("list = %+v err=%v", items, err)
		}
		var first Commitment
		for _, item := range items {
			if item.ID == due {
				first = item
			}
		}
		if first.Status != CommitmentWaiting || first.NudgeCount != 1 || first.LastNudgeAt == "" {
			t.Fatalf("rescheduled row = %+v", first)
		}
		if first.DeadlineAt != "" {
			t.Fatalf("deadline should stay unset: %+v", first)
		}
		if n, _ := s.OpenCommitmentCount(ctx, "U1"); n != 2 {
			t.Fatalf("open count = %d, want 2", n)
		}

		// Failure backoff also re-arms a leased row.
		if err := s.SetCommitmentNextCheck(ctx, due, now.Add(5*time.Minute).Format(time.RFC3339)); err != nil {
			t.Fatalf("set next check: %v", err)
		}
		if err := s.FinishCommitment(ctx, due, CommitmentDone, "已经吃过"); err != nil {
			t.Fatalf("finish: %v", err)
		}
		if n, _ := s.OpenCommitmentCount(ctx, "U1"); n != 1 {
			t.Fatalf("open count after finish = %d, want 1", n)
		}
		all, _ := s.ListCommitments(ctx, "conv-1", "U1", 10)
		var closed Commitment
		for _, item := range all {
			if item.ID == due {
				closed = item
			}
		}
		if closed.Status != CommitmentDone || closed.AckNote != "已经吃过" || closed.AckAt == "" {
			t.Fatalf("closed row = %+v", closed)
		}

		// Ownership isolation: another creator sees nothing and cannot lease.
		other, _ := s.ListOpenCommitmentsOfCreator(ctx, "U-OTHER", 10)
		if len(other) != 0 {
			t.Fatalf("other creator should have no commitments: %+v", other)
		}
		open, _ := s.ListOpenCommitmentsOfCreator(ctx, "U1", 10)
		if len(open) != 1 || open[0].ID != future {
			t.Fatalf("open list = %+v", open)
		}
	})
}

// TestTerminalCommitmentCannotBeResurrected is the regression for the race
// where a nudge was in flight while the user closed the promise: the nudge's
// RescheduleCommitment must lose to the terminal row, and the lease claim must
// not flip it back to leased either.
func TestTerminalCommitmentCannotBeResurrected(t *testing.T) {
	everyCommitmentStore(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		now := time.Now()
		id, err := s.AddCommitment(ctx, CommitmentInput{
			ConversationID: "conv-race", MemberOpenID: "U1", CreatorOpenID: "U1",
			Text: "交周报", NextCheckAt: now.Add(-time.Minute).Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("add: %v", err)
		}

		// The scheduler claims it; this is the leased state the user then closes.
		leased, err := s.LeaseDueCommitments(ctx, now, 10, 2*time.Minute)
		if err != nil || len(leased) != 1 || leased[0].ID != id {
			t.Fatalf("lease = %+v err=%v", leased, err)
		}
		if err := s.FinishCommitment(ctx, id, CommitmentDone, "已经交了"); err != nil {
			t.Fatalf("finish: %v", err)
		}
		before := getCommitment(t, s, id)

		// The in-flight nudge now tries to move it back to waiting: it must
		// report "not open" and leave status/next_check/nudge_count untouched.
		ok, err := s.RescheduleCommitment(ctx, id,
			now.Add(8*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339), 1)
		if err != nil || ok {
			t.Fatalf("reschedule terminal: ok=%v err=%v", ok, err)
		}
		after := getCommitment(t, s, id)
		if after.Status != CommitmentDone || after.NextCheckAt != before.NextCheckAt ||
			after.NudgeCount != before.NudgeCount || after.AckNote != "已经交了" {
			t.Fatalf("terminal row was resurrected by the nudge: before=%+v after=%+v", before, after)
		}

		// Stale-lease race: the lease SELECT already returned the id, but the
		// row is terminal by the time the guarded UPDATE runs.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		claimed, err := s.claimCommitmentLease(ctx, tx, id, now.Add(30*time.Minute).Format(time.RFC3339))
		if err != nil {
			t.Fatalf("claim terminal: %v", err)
		}
		if claimed {
			t.Fatal("a terminal commitment must not be claimable")
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}

		// And a full scheduler pass (even after the old lease would have
		// expired) must not hand it out again.
		again, err := s.LeaseDueCommitments(ctx, now.Add(10*time.Minute), 10, time.Minute)
		if err != nil || len(again) != 0 {
			t.Fatalf("terminal row re-leased: %+v err=%v", again, err)
		}
		final := getCommitment(t, s, id)
		if final.Status != CommitmentDone || final.NextCheckAt != before.NextCheckAt {
			t.Fatalf("final row = %+v, want done with next_check %q", final, before.NextCheckAt)
		}
	})
}

func getCommitment(t *testing.T, s *Store, id int64) Commitment {
	t.Helper()
	items, err := s.ListCommitments(context.Background(), "conv-race", "U1", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("commitment %d not found", id)
	return Commitment{}
}

func TestCancelCommitmentOnlyClosesOpenRows(t *testing.T) {
	everyCommitmentStore(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		now := time.Now()
		future := now.Add(time.Hour).Format(time.RFC3339)
		open, err := s.AddCommitment(ctx, CommitmentInput{
			ConversationID: "conv-1", MemberOpenID: "U1", CreatorOpenID: "U1",
			Text: "交周报", NextCheckAt: future,
		})
		if err != nil {
			t.Fatalf("add open: %v", err)
		}
		canceled, err := s.CancelCommitment(ctx, open, "后台取消")
		if err != nil || canceled != CancelOutcomeCanceled {
			t.Fatalf("cancel open = %q err=%v", canceled, err)
		}
		items, _ := s.ListCommitments(ctx, "conv-1", "U1", 10)
		if len(items) != 1 || items[0].Status != CommitmentCancelled ||
			items[0].AckNote != "后台取消" || items[0].AckAt == "" {
			t.Fatalf("cancelled row = %+v", items)
		}

		// A second cancel is not "cancel again": the terminal row keeps its note.
		again, err := s.CancelCommitment(ctx, open, "后台取消")
		if err != nil || again != CancelOutcomeNotOpen {
			t.Fatalf("cancel terminal = %q err=%v", again, err)
		}
		missing, err := s.CancelCommitment(ctx, 999999, "后台取消")
		if err != nil || missing != CancelOutcomeNotFound {
			t.Fatalf("cancel missing = %q err=%v", missing, err)
		}

		// done is terminal too, even though it was closed by the user, not us.
		done, err := s.AddCommitment(ctx, CommitmentInput{
			ConversationID: "conv-1", MemberOpenID: "U1", CreatorOpenID: "U1",
			Text: "吃药", NextCheckAt: future,
		})
		if err != nil {
			t.Fatalf("add done: %v", err)
		}
		if err := s.FinishCommitment(ctx, done, CommitmentDone, "已经吃过"); err != nil {
			t.Fatalf("finish: %v", err)
		}
		afterDone, err := s.CancelCommitment(ctx, done, "后台取消")
		if err != nil || afterDone != CancelOutcomeNotOpen {
			t.Fatalf("cancel done = %q err=%v", afterDone, err)
		}

		// A leased row (scheduler already claimed it) is still cancellable.
		leased, err := s.AddCommitment(ctx, CommitmentInput{
			ConversationID: "conv-1", MemberOpenID: "U2", CreatorOpenID: "U2",
			Text: "发方案", NextCheckAt: now.Add(-time.Minute).Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("add leased: %v", err)
		}
		if claimed, _ := s.LeaseDueCommitments(ctx, now, 10, time.Minute); len(claimed) != 1 || claimed[0].ID != leased {
			t.Fatalf("lease = %+v", claimed)
		}
		leasedOutcome, err := s.CancelCommitment(ctx, leased, "后台取消")
		if err != nil || leasedOutcome != CancelOutcomeCanceled {
			t.Fatalf("cancel leased = %q err=%v", leasedOutcome, err)
		}
	})
}

func TestHumanStatusUpsertListAndPrune(t *testing.T) {
	everyCommitmentStore(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		now := time.Now()
		in := HumanStatusInput{
			Scope: HumanStatusGroup, ConversationID: "conv-1", GroupOpenID: "G1",
			MemberOpenID: "U1", DisplayName: "小明", Status: "打游戏",
			Confidence: 0.8, Evidence: "说在开黑",
			UpdatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
		}
		if err := s.UpsertHumanStatus(ctx, in); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		in.Status, in.Confidence = "工作", 0.6
		in.UpdatedAt = now.Add(time.Minute).Format(time.RFC3339)
		if err := s.UpsertHumanStatus(ctx, in); err != nil {
			t.Fatalf("upsert again: %v", err)
		}
		second := in
		second.MemberOpenID, second.DisplayName, second.Status = "U2", "小红", "通勤"
		if err := s.UpsertHumanStatus(ctx, second); err != nil {
			t.Fatalf("upsert second: %v", err)
		}

		items, err := s.ListHumanStatuses(ctx, "conv-1")
		if err != nil || len(items) != 2 {
			t.Fatalf("list = %+v err=%v", items, err)
		}
		var one HumanStatus
		for _, item := range items {
			if item.MemberOpenID == "U1" {
				one = item
			}
		}
		if one.Status != "工作" || one.Confidence != 0.6 {
			t.Fatalf("upsert did not replace the row: %+v", one)
		}

		// Expired rows are pruned by their expires_at.
		if err := s.UpsertHumanStatus(ctx, HumanStatusInput{
			Scope: HumanStatusC2C, ConversationID: "c2c:U3", MemberOpenID: "U3",
			Status: "在线", Confidence: 0.4,
			UpdatedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
			ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("upsert expired: %v", err)
		}
		n, err := s.PruneExpiredHumanStatuses(ctx, now)
		if err != nil || n != 1 {
			t.Fatalf("prune = %d err=%v", n, err)
		}
		left, _ := s.ListHumanStatuses(ctx, "c2c:U3")
		if len(left) != 0 {
			t.Fatalf("expired row survived: %+v", left)
		}
	})
}

// The two new tables are versioned migrations, so both must be creatable on an
// existing database even if the base schema never saw them.
func TestCommitmentAndHumanStatusMigrations(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir() + "/legacy.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for _, m := range []string{"0011_commitments", "0012_human_status"} {
		if _, err := s.execContext(ctx, `DELETE FROM schema_migrations WHERE name = ?`, m); err != nil {
			t.Fatalf("forget migration %s: %v", m, err)
		}
	}
	if _, err := s.execContext(ctx, `DROP TABLE commitments`); err != nil {
		t.Fatalf("drop commitments: %v", err)
	}
	if _, err := s.execContext(ctx, `DROP TABLE human_status`); err != nil {
		t.Fatalf("drop human_status: %v", err)
	}
	if err := s.runMigrations(ctx); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	for _, table := range []string{"commitments", "human_status"} {
		cols, err := s.tableColumns(ctx, table)
		if err != nil || len(cols) == 0 {
			t.Fatalf("table %s was not recreated: %v", table, err)
		}
	}
	var applied int
	if err := s.queryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE name IN (?, ?)`,
		"0011_commitments", "0012_human_status").Scan(&applied); err != nil || applied != 2 {
		t.Fatalf("migration records = %d err=%v", applied, err)
	}
}
