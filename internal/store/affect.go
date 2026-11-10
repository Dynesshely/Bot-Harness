package store

// Affect state persistence.
//
// Three tables, one per concern:
//
//	affect_states  — one row per conversation: current dimensions (JSON), the
//	                 ids of the events already applied, the pause deadline and
//	                 the optimistic-lock version;
//	affect_events  — append-only log of classified events (tone/source/at);
//	affect_history — periodic snapshots of the five dimensions for the chart.
//
// The state row is the only one that is read-modify-written, so it carries a
// version column: SaveAffectState refuses to overwrite a row whose version has
// moved on and returns ErrAffectConflict, which lets affect.Service reload and
// retry instead of losing an event.
//
// Times are RFC3339 strings at the Go boundary, exactly like the rest of the
// store. On PostgreSQL the columns are TIMESTAMPTZ and the nullable ones are
// bound through dialect.NullableTimeArg / read through nullableTimeText, the
// same convention commitments and human_status use.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrAffectConflict is returned by SaveAffectState when the stored version no
// longer matches the caller's. The caller should reload the row and retry.
var ErrAffectConflict = errors.New("store: affect state version conflict")

// Affect dimensions are fixed by the contract (internal/affect owns the pack
// metadata); the store deliberately stays generic: Dims is just a map.
const (
	AffectDefaultPackID = "daily"
)

// AffectEventMark is one event id already applied to a state.
//
// Only the id and instant are kept: the log table holds the full record, and
// the state only needs to remember *which* events it consumed (for daily caps)
// and *when*.
type AffectEventMark struct {
	ID string `json:"id"`
	At string `json:"at"`
}

// AffectState is the persisted mood of one conversation.
type AffectState struct {
	ConversationID string             `json:"conversation_id"`
	PackID         string             `json:"pack_id"`
	Dims           map[string]float64 `json:"dims"`
	Events         []AffectEventMark  `json:"events"`
	// PausedUntil is an RFC3339 instant; empty means "not paused".
	PausedUntil string `json:"paused_until"`
	TickedAt    string `json:"ticked_at"`
	UpdatedAt   string `json:"updated_at"`
	Version     int64  `json:"version"`
}

// AffectEventLog is one row of the classified-event log.
type AffectEventLog struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"conversation_id"`
	EventID        string `json:"event_id"`
	Tone           string `json:"tone"`
	Source         string `json:"source"`
	CreatedAt      string `json:"created_at"`
}

// AffectPoint is one historical snapshot.
type AffectPoint struct {
	At   string             `json:"at"`
	Dims map[string]float64 `json:"dims"`
}

// affectStateSelect is the single SELECT shape used by GetAffectState and
// ListAffectConversations.
const affectStateSelect = `
SELECT conversation_id, pack_id, dims, events, paused_until, ticked_at, updated_at, version
  FROM affect_states`

// GetAffectState returns one conversation's state, or (nil, nil) when no row
// exists. The two-value convention matters: "never seen this conversation" is a
// normal case for Observe/Block, not an error.
func (s *Store) GetAffectState(ctx context.Context, conversationID string) (*AffectState, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, nil
	}
	row := s.queryRowContext(ctx, affectStateSelect+` WHERE conversation_id = ?`, conversationID)
	st, err := scanAffectState(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return st, nil
}

type affectStateScanner interface {
	Scan(dest ...any) error
}

func scanAffectState(row affectStateScanner) (*AffectState, error) {
	var (
		st                   AffectState
		dimsJSON, eventsJSON string
		paused, ticked, upd  nullableTimeText
	)
	if err := row.Scan(&st.ConversationID, &st.PackID, &dimsJSON, &eventsJSON,
		&paused, &ticked, &upd, &st.Version); err != nil {
		return nil, err
	}
	st.PausedUntil, st.TickedAt, st.UpdatedAt = paused.Value, ticked.Value, upd.Value
	st.Dims = map[string]float64{}
	if strings.TrimSpace(dimsJSON) != "" {
		if err := json.Unmarshal([]byte(dimsJSON), &st.Dims); err != nil {
			return nil, fmt.Errorf("store: decode affect dims: %w", err)
		}
	}
	st.Events = []AffectEventMark{}
	if strings.TrimSpace(eventsJSON) != "" {
		if err := json.Unmarshal([]byte(eventsJSON), &st.Events); err != nil {
			return nil, fmt.Errorf("store: decode affect events: %w", err)
		}
	}
	return &st, nil
}

// SaveAffectState persists st under optimistic locking.
//
//	version == 0 → INSERT with version 1. If a row already exists this is a
//	               conflict (someone else created it first), not an overwrite.
//	version  > 0 → UPDATE ... WHERE version = st.Version, writing Version+1.
//	               Zero affected rows means the row is gone or has moved on;
//	               both are reported as ErrAffectConflict so the caller reloads.
//
// On success st.Version and st.UpdatedAt are updated in place, so the caller can
// keep using the same struct for the next write.
func (s *Store) SaveAffectState(ctx context.Context, st *AffectState) error {
	if st == nil {
		return errors.New("store: nil affect state")
	}
	st.ConversationID = strings.TrimSpace(st.ConversationID)
	if st.ConversationID == "" {
		return errors.New("store: affect state needs conversation_id")
	}
	if st.PackID == "" {
		st.PackID = AffectDefaultPackID
	}
	dims, err := json.Marshal(nonNilDims(st.Dims))
	if err != nil {
		return fmt.Errorf("store: encode affect dims: %w", err)
	}
	events, err := json.Marshal(nonNilEventMarks(st.Events))
	if err != nil {
		return fmt.Errorf("store: encode affect events: %w", err)
	}
	if strings.TrimSpace(st.UpdatedAt) == "" {
		st.UpdatedAt = nowRFC3339()
	}

	if st.Version <= 0 {
		_, err = s.execContext(ctx, `
INSERT INTO affect_states (
  conversation_id, pack_id, dims, events, paused_until, ticked_at, updated_at, version
) VALUES (?, ?, ?, ?, `+s.dialect.NullableTimeArg()+`, `+s.dialect.NullableTimeArg()+`, ?, 1)`,
			st.ConversationID, st.PackID, string(dims), string(events),
			st.PausedUntil, st.TickedAt, st.UpdatedAt)
		if err != nil {
			// A UNIQUE violation on the primary key is the "created by someone
			// else between our read and this insert" race. Both engines report
			// it as an error string with no portable sentinel, so probe the
			// row: a present row means the insert lost, anything else means the
			// write genuinely failed.
			if exists, probeErr := s.affectStateExists(ctx, st.ConversationID); probeErr == nil && exists {
				return ErrAffectConflict
			}
			return err
		}
		st.Version = 1
		return nil
	}

	res, err := s.execContext(ctx, `
UPDATE affect_states
   SET pack_id = ?, dims = ?, events = ?,
       paused_until = `+s.dialect.NullableTimeArg()+`,
       ticked_at = `+s.dialect.NullableTimeArg()+`,
       updated_at = ?, version = version + 1
 WHERE conversation_id = ? AND version = ?`,
		st.PackID, string(dims), string(events), st.PausedUntil, st.TickedAt,
		st.UpdatedAt, st.ConversationID, st.Version)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAffectConflict
	}
	st.Version++
	return nil
}

func (s *Store) affectStateExists(ctx context.Context, conversationID string) (bool, error) {
	var one int
	err := s.queryRowContext(ctx,
		`SELECT 1 FROM affect_states WHERE conversation_id = ?`, conversationID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListAffectConversations returns the conversations that have an affect state,
// most recently updated first, which is the natural order for the admin picker.
func (s *Store) ListAffectConversations(ctx context.Context, limit int) ([]string, error) {
	limit = clampAffectLimit(limit, 50, 200)
	rows, err := s.queryContext(ctx, `
SELECT conversation_id FROM affect_states
 ORDER BY updated_at DESC, conversation_id ASC
 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AppendAffectEvent appends one classified event. at may be empty, in which
// case the store fills in the current instant.
func (s *Store) AppendAffectEvent(ctx context.Context, conversationID, eventID, tone, source, at string) error {
	conversationID = strings.TrimSpace(conversationID)
	eventID = strings.TrimSpace(eventID)
	if conversationID == "" || eventID == "" {
		return errors.New("store: affect event needs conversation_id and event_id")
	}
	if strings.TrimSpace(at) == "" {
		at = nowRFC3339()
	}
	_, err := s.execContext(ctx, `
INSERT INTO affect_events (conversation_id, event_id, tone, source, created_at)
VALUES (?, ?, ?, ?, ?)`, conversationID, eventID, tone, source, at)
	return err
}

// ListAffectEvents returns the newest events of one conversation.
func (s *Store) ListAffectEvents(ctx context.Context, conversationID string, limit int) ([]AffectEventLog, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return []AffectEventLog{}, nil
	}
	limit = clampAffectLimit(limit, 50, 200)
	rows, err := s.queryContext(ctx, `
SELECT id, conversation_id, event_id, tone, source, created_at
  FROM affect_events
 WHERE conversation_id = ?
 ORDER BY created_at DESC, id DESC
 LIMIT ?`, conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []AffectEventLog{}
	for rows.Next() {
		var (
			e       AffectEventLog
			created nullableTimeText
		)
		if err := rows.Scan(&e.ID, &e.ConversationID, &e.EventID, &e.Tone, &e.Source, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = created.Value
		out = append(out, e)
	}
	return out, rows.Err()
}

// AppendAffectHistory records one snapshot of the five dimensions.
func (s *Store) AppendAffectHistory(ctx context.Context, conversationID, at string, dims map[string]float64) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("store: affect history needs conversation_id")
	}
	if strings.TrimSpace(at) == "" {
		at = nowRFC3339()
	}
	encoded, err := json.Marshal(nonNilDims(dims))
	if err != nil {
		return fmt.Errorf("store: encode affect history dims: %w", err)
	}
	_, err = s.execContext(ctx, `
INSERT INTO affect_history (conversation_id, at, dims) VALUES (?, ?, ?)`,
		conversationID, at, string(encoded))
	return err
}

// ListAffectHistory returns snapshots at or after since (empty = no lower
// bound), oldest first so a chart can plot them without reversing. limit is
// applied to the newest rows, then the window is returned chronologically.
func (s *Store) ListAffectHistory(ctx context.Context, conversationID, since string, limit int) ([]AffectPoint, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return []AffectPoint{}, nil
	}
	limit = clampAffectLimit(limit, 200, 1000)

	query := `
SELECT at, dims FROM affect_history
 WHERE conversation_id = ?`
	args := []any{conversationID}
	if strings.TrimSpace(since) != "" {
		query += ` AND at >= ?`
		args = append(args, since)
	}
	query += ` ORDER BY at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	// Read newest-first (so LIMIT keeps the latest window) and emit oldest-first.
	out := []AffectPoint{}
	for rows.Next() {
		var (
			p    AffectPoint
			at   nullableTimeText
			dims string
		)
		if err := rows.Scan(&at, &dims); err != nil {
			return nil, err
		}
		p.At = at.Value
		p.Dims = map[string]float64{}
		if strings.TrimSpace(dims) != "" {
			if err := json.Unmarshal([]byte(dims), &p.Dims); err != nil {
				return nil, fmt.Errorf("store: decode affect history dims: %w", err)
			}
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// PruneAffect deletes history older than historyBefore and events older than
// eventsBefore. An empty cutoff leaves that table alone.
func (s *Store) PruneAffect(ctx context.Context, historyBefore, eventsBefore string) error {
	if strings.TrimSpace(historyBefore) != "" {
		if _, err := s.execContext(ctx,
			`DELETE FROM affect_history WHERE at < ?`, historyBefore); err != nil {
			return fmt.Errorf("store: prune affect history: %w", err)
		}
	}
	if strings.TrimSpace(eventsBefore) != "" {
		if _, err := s.execContext(ctx,
			`DELETE FROM affect_events WHERE created_at < ?`, eventsBefore); err != nil {
			return fmt.Errorf("store: prune affect events: %w", err)
		}
	}
	return nil
}

// ClearAffectForConversation removes every affect row for one conversation.
// Conversation deletion only clears messages/conversations, so without this the
// state, history and event log would survive as orphan rows (and keep showing up
// in the admin selector, outliving the user's delete action).
func (s *Store) ClearAffectForConversation(ctx context.Context, conversationID string) error {
	for _, table := range []string{"affect_events", "affect_history", "affect_states"} {
		if _, err := s.execContext(ctx,
			`DELETE FROM `+table+` WHERE conversation_id = ?`, conversationID); err != nil {
			return fmt.Errorf("store: clear %s: %w", table, err)
		}
	}
	return nil
}

// ClearAllAffect is the cascade for "clear all conversations".
func (s *Store) ClearAllAffect(ctx context.Context) error {
	for _, table := range []string{"affect_events", "affect_history", "affect_states"} {
		if _, err := s.execContext(ctx, `DELETE FROM `+table); err != nil {
			return fmt.Errorf("store: clear %s: %w", table, err)
		}
	}
	return nil
}

// clampAffectLimit applies the same default/ceiling policy to every list call.
func clampAffectLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

func nonNilDims(dims map[string]float64) map[string]float64 {
	if dims == nil {
		return map[string]float64{}
	}
	return dims
}

func nonNilEventMarks(marks []AffectEventMark) []AffectEventMark {
	if marks == nil {
		return []AffectEventMark{}
	}
	return marks
}

// Ensure time is referenced even if a future edit drops the only direct use;
// the file documents the RFC3339 boundary it relies on.
var _ = time.RFC3339
