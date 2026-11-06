package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Commitment statuses. A commitment is an open loop: the bot keeps asking
// about it until the user explicitly says it is done, postponed or cancelled.
// There is deliberately no frequency ceiling and no automatic stop condition —
// the model chooses when to ask again (see internal/core/commitments.go).
const (
	CommitmentWaiting   = "waiting"
	CommitmentLeased    = "leased"
	CommitmentDone      = "done"
	CommitmentCancelled = "cancelled"
)

// CommitmentInput is one open loop to store.
type CommitmentInput struct {
	ConversationID string
	GroupOpenID    string
	MemberOpenID   string
	CreatorOpenID  string
	Text           string
	Priority       int
	// DeadlineAt is an optional RFC3339 instant, empty for none.
	DeadlineAt string
	// NextCheckAt is when the first nudge is due.
	NextCheckAt string
	Meta        string
}

// Commitment is one stored open loop.
type Commitment struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"conversation_id"`
	GroupOpenID    string `json:"group_openid"`
	MemberOpenID   string `json:"member_openid"`
	CreatorOpenID  string `json:"creator_openid"`
	Text           string `json:"text"`
	Status         string `json:"status"`
	Priority       int    `json:"priority"`
	DeadlineAt     string `json:"deadline_at"`
	NextCheckAt    string `json:"next_check_at"`
	LastNudgeAt    string `json:"last_nudge_at"`
	NudgeCount     int    `json:"nudge_count"`
	AckAt          string `json:"ack_at"`
	AckNote        string `json:"ack_note"`
	Meta           string `json:"meta"`
	CreatedAt      string `json:"created_at"`
}

// AddCommitment stores a waiting open loop and returns its id.
func (s *Store) AddCommitment(ctx context.Context, in CommitmentInput) (int64, error) {
	if strings.TrimSpace(in.NextCheckAt) == "" {
		return 0, fmt.Errorf("store: commitment next_check_at is required")
	}
	var id int64
	err := s.queryRowContext(ctx, `
INSERT INTO commitments (
  conversation_id, group_openid, member_openid, creator_openid, text, status,
  priority, deadline_at, next_check_at, last_nudge_at, nudge_count, ack_at,
  ack_note, meta, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, `+s.dialect.NullableTimeArg()+`, ?, `+s.dialect.EmptyTimeLiteral()+`, 0, `+s.dialect.EmptyTimeLiteral()+`, '', ?, ?)
RETURNING id`,
		in.ConversationID, in.GroupOpenID, in.MemberOpenID, in.CreatorOpenID,
		in.Text, CommitmentWaiting, in.Priority, in.DeadlineAt, in.NextCheckAt,
		in.Meta, nowRFC3339()).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// LeaseDueCommitments atomically claims due commitments.
//
// The lease is encoded in the row itself: status=leased plus next_check_at
// holding the lease deadline. A crashed worker therefore becomes claimable
// again as soon as the lease expires, without a separate lease_until column.
// The original due instant is not needed after the claim: a successful nudge
// reschedules from "now" anyway.
func (s *Store) LeaseDueCommitments(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Commitment, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	nowStr := now.Format(time.RFC3339)
	leaseStr := now.Add(lease).Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := s.txQueryContext(ctx, tx, `
SELECT id FROM commitments
 WHERE (status = ? AND next_check_at <= ?)
    OR (status = ? AND next_check_at <= ?)
 ORDER BY priority DESC, next_check_at ASC
 LIMIT ?`, CommitmentWaiting, nowStr, CommitmentLeased, nowStr, limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if len(ids) == 0 {
		return nil, tx.Commit()
	}

	claimed := make([]Commitment, 0, len(ids))
	for _, id := range ids {
		ok, err := s.claimCommitmentLease(ctx, tx, id, leaseStr)
		if err != nil {
			return nil, err
		}
		if !ok {
			// The row was closed between the SELECT above and this UPDATE. The
			// status guard is what keeps a terminal commitment terminal; the
			// affected-row count is how we notice. Skip it, don't resurrect it.
			continue
		}
		c, err := scanCommitment(s.txQueryRowContext(ctx, tx, commitmentSelect+` WHERE id = ?`, id))
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, *c)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

// claimCommitmentLease attempts the actual claim for one id. The UPDATE is
// conditional on the row still being open: a done/cancelled row that was closed
// after the lease SELECT must not be flipped back to leased. The affected-row
// count is the only reliable way to distinguish "claimed" from "already
// closed", so callers must not assume the id is still open.
func (s *Store) claimCommitmentLease(ctx context.Context, tx *sql.Tx, id int64, leaseStr string) (bool, error) {
	res, err := s.txExecContext(ctx, tx,
		`UPDATE commitments SET status = ?, next_check_at = ? WHERE id = ? AND status IN (?, ?)`,
		CommitmentLeased, leaseStr, id, CommitmentWaiting, CommitmentLeased)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

const commitmentSelect = `
SELECT id, conversation_id, group_openid, member_openid, creator_openid, text, status,
       priority, deadline_at, next_check_at, last_nudge_at, nudge_count, ack_at,
       ack_note, meta, created_at
  FROM commitments`

type commitmentScanner interface {
	Scan(dest ...any) error
}

func scanCommitment(row commitmentScanner) (*Commitment, error) {
	var (
		c                          Commitment
		deadline, lastNudge, ackAt nullableTimeText
	)
	if err := row.Scan(&c.ID, &c.ConversationID, &c.GroupOpenID, &c.MemberOpenID,
		&c.CreatorOpenID, &c.Text, &c.Status, &c.Priority, &deadline, &c.NextCheckAt,
		&lastNudge, &c.NudgeCount, &ackAt, &c.AckNote, &c.Meta, &c.CreatedAt); err != nil {
		return nil, err
	}
	c.DeadlineAt, c.LastNudgeAt, c.AckAt = deadline.Value, lastNudge.Value, ackAt.Value
	return &c, nil
}

// FinishCommitment closes an open loop as done or cancelled.
func (s *Store) FinishCommitment(ctx context.Context, id int64, status, ackNote string) error {
	ackAt := ""
	if status == CommitmentDone || status == CommitmentCancelled {
		ackAt = nowRFC3339()
	}
	_, err := s.execContext(ctx, `
UPDATE commitments
   SET status = ?, ack_note = ?, ack_at = `+s.dialect.NullableTimeArg()+`
 WHERE id = ?`, status, ackNote, ackAt, id)
	return err
}

// CancelOutcome reports what CancelCommitment did, so the admin API can map it
// to a status code without a second, racy read of the row.
type CancelOutcome string

const (
	// CancelOutcomeCanceled means the row was open and is now cancelled.
	CancelOutcomeCanceled CancelOutcome = "cancelled"
	// CancelOutcomeNotFound means no row has that id.
	CancelOutcomeNotFound CancelOutcome = "not_found"
	// CancelOutcomeNotOpen means the row exists but is already done/cancelled.
	CancelOutcomeNotOpen CancelOutcome = "not_open"
)

// CancelCommitment closes an open loop from the admin panel. Only waiting and
// leased rows are cancellable: done/cancelled are terminal, so a second cancel
// must report "not open" instead of silently rewriting the ack note/time.
//
// The UPDATE is conditional on the status so a scheduler lease racing this call
// cannot resurrect a terminal row; the follow-up SELECT only runs to tell
// "missing" apart from "already closed".
func (s *Store) CancelCommitment(ctx context.Context, id int64, ackNote string) (CancelOutcome, error) {
	res, err := s.execContext(ctx, `
UPDATE commitments
   SET status = ?, ack_note = ?, ack_at = `+s.dialect.NullableTimeArg()+`
 WHERE id = ? AND status IN (?, ?)`,
		CommitmentCancelled, ackNote, nowRFC3339(), id, CommitmentWaiting, CommitmentLeased)
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n > 0 {
		return CancelOutcomeCanceled, nil
	}

	var status string
	err = s.queryRowContext(ctx, `SELECT status FROM commitments WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return CancelOutcomeNotFound, nil
	}
	if err != nil {
		return "", err
	}
	return CancelOutcomeNotOpen, nil
}

// RescheduleCommitment moves an open commitment to waiting and sets its next
// check. An empty lastNudge leaves the previous nudge timestamp untouched (a
// user's postpone must not erase when the bot last spoke); nudgeInc is normally
// 1 after a delivered nudge and 0 for a postpone.
//
// The UPDATE is conditional on the row still being waiting/leased, so a nudge
// that was in flight while the user closed the promise cannot resurrect it.
// ok=false means the row is terminal (done/cancelled) or missing; callers must
// not write a timeline entry or bump counters for a nudge that lost that race.
func (s *Store) RescheduleCommitment(ctx context.Context, id int64, nextCheck, lastNudge string, nudgeInc int) (bool, error) {
	query := `UPDATE commitments
   SET status = ?, next_check_at = ?, nudge_count = nudge_count + ?`
	args := []any{CommitmentWaiting, nextCheck, nudgeInc}
	if strings.TrimSpace(lastNudge) != "" {
		query += `, last_nudge_at = ` + s.dialect.NullableTimeArg()
		args = append(args, lastNudge)
	}
	query += ` WHERE id = ? AND status IN (?, ?)`
	args = append(args, id, CommitmentWaiting, CommitmentLeased)
	res, err := s.execContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// SetCommitmentNextCheck re-arms a commitment after a failed delivery. The row
// returns to waiting so the next scheduler tick can retry it.
func (s *Store) SetCommitmentNextCheck(ctx context.Context, id int64, nextCheck string) error {
	_, err := s.execContext(ctx, `
UPDATE commitments SET status = ?, next_check_at = ?
 WHERE id = ? AND status IN (?, ?)`,
		CommitmentWaiting, nextCheck, id, CommitmentWaiting, CommitmentLeased)
	return err
}

// ListCommitments pages commitments newest first. Empty filters mean "all";
// conversationID and creator narrow the result.
func (s *Store) ListCommitments(ctx context.Context, conversationID, creator string, limit int) ([]Commitment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := commitmentSelect + ` WHERE 1=1`
	args := []any{}
	if strings.TrimSpace(conversationID) != "" {
		query += ` AND conversation_id = ?`
		args = append(args, conversationID)
	}
	if strings.TrimSpace(creator) != "" {
		query += ` AND creator_openid = ?`
		args = append(args, creator)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]Commitment, 0, limit)
	for rows.Next() {
		c, err := scanCommitment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ListOpenCommitmentsOfCreator returns one person's open loops (waiting or
// leased), soonest next check first.
func (s *Store) ListOpenCommitmentsOfCreator(ctx context.Context, creator string, limit int) ([]Commitment, error) {
	if strings.TrimSpace(creator) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.queryContext(ctx, commitmentSelect+`
 WHERE creator_openid = ? AND status IN (?, ?)
 ORDER BY next_check_at ASC, id ASC LIMIT ?`,
		creator, CommitmentWaiting, CommitmentLeased, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]Commitment, 0, limit)
	for rows.Next() {
		c, err := scanCommitment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// OpenCommitmentCount counts one person's open loops (per-person creation cap).
func (s *Store) OpenCommitmentCount(ctx context.Context, creator string) (int, error) {
	if strings.TrimSpace(creator) == "" {
		return 0, nil
	}
	var n int
	err := s.queryRowContext(ctx, `
SELECT COUNT(*) FROM commitments
 WHERE creator_openid = ? AND status IN (?, ?)`,
		creator, CommitmentWaiting, CommitmentLeased).Scan(&n)
	return n, err
}
