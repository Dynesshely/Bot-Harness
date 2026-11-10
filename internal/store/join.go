package store

// Join-gate persistence.
//
// Two tables:
//
//	join_decisions — one row per evaluation, whether the bot joined or not. It
//	                 is both the audit trail shown in the admin panel and the
//	                 source of truth for the per-group cooldown / hourly / daily
//	                 rate limits (joined rows only).
//	join_topics    — the latest topic split the model produced for a group.
//	                 Group-scoped (there is one row per group, not per
//	                 conversation): the gate analyses the room, not one member.
//
// Times are RFC3339 strings at the Go boundary like the rest of the store. On
// PostgreSQL the columns are TIMESTAMPTZ and the timestamp columns are read
// through nullableTimeText, so an empty result (MAX over no rows) is the empty
// string instead of a scan error.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// GroupMessage is one row of the group-wide context window the join gate feeds
// to the model. It deliberately carries the conversation id so the decision row
// can point back at the exact message that triggered it.
type GroupMessage struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"conversation_id"`
	Role           string `json:"role"`
	Speaker        string `json:"speaker"`
	Content        string `json:"content"`
	CreatedAt      string `json:"created_at"`
}

// JoinDecision is one completed gate evaluation.
type JoinDecision struct {
	ID             int64   `json:"id"`
	GroupOpenID    string  `json:"group_openid"`
	ConversationID string  `json:"conversation_id"`
	MessageID      string  `json:"message_id"`
	Speaker        string  `json:"speaker"`
	Preview        string  `json:"preview"`
	Joined         bool    `json:"joined"`
	Confidence     float64 `json:"confidence"`
	Reason         string  `json:"reason"`
	TopicsJSON     string  `json:"topics"`
	Model          string  `json:"model"`
	LatencyMS      int     `json:"latency_ms"`
	CreatedAt      string  `json:"created_at"`
}

// JoinTopics is the cached topic split of one group.
type JoinTopics struct {
	GroupOpenID string `json:"group_openid"`
	TopicsJSON  string `json:"topics"`
	AnalyzedAt  string `json:"analyzed_at"`
}

// clampJoinLimit applies the list policy used by the store: default when the
// caller did not ask for a specific window, ceiling when it asks for too many.
func clampJoinLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

// RecentGroupMessages returns the newest messages seen in every conversation of
// one group, oldest first.
//
// The gate needs a chronological window: the query takes the latest rows by id
// and reverses them in Go, exactly like RecentMessages, so LIMIT keeps the
// newest window instead of the oldest one. All kinds are included on purpose —
// observed chatter and other bots' messages are often the reason not to join,
// so the model has to see them.
func (s *Store) RecentGroupMessages(ctx context.Context, groupOpenID string, limit int) ([]GroupMessage, error) {
	groupOpenID = strings.TrimSpace(groupOpenID)
	if groupOpenID == "" {
		return []GroupMessage{}, nil
	}
	limit = clampJoinLimit(limit, 20, 200)
	rows, err := s.queryContext(ctx, `
SELECT m.id, m.conversation_id, m.role, m.speaker, m.content, m.created_at
  FROM messages m
  JOIN conversations c ON c.id = m.conversation_id
 WHERE c.group_openid = ?
 ORDER BY m.id DESC
 LIMIT ?`, groupOpenID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []GroupMessage{}
	for rows.Next() {
		var (
			m       GroupMessage
			created nullableTimeText
		)
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Speaker, &m.Content, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = created.Value
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// AppendJoinDecision records one evaluation. created_at is filled in when the
// caller leaves it empty; the rest of the fields are stored verbatim because
// the gate's fail-closed path writes partial decisions on purpose.
func (s *Store) AppendJoinDecision(ctx context.Context, d JoinDecision) error {
	if strings.TrimSpace(d.CreatedAt) == "" {
		d.CreatedAt = nowRFC3339()
	}
	_, err := s.execContext(ctx, `
INSERT INTO join_decisions (
  group_openid, conversation_id, message_id, speaker, preview, joined,
  confidence, reason, topics, model, latency_ms, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.GroupOpenID, d.ConversationID, d.MessageID, d.Speaker, d.Preview,
		boolToInt(d.Joined), d.Confidence, d.Reason, d.TopicsJSON, d.Model,
		d.LatencyMS, d.CreatedAt)
	return err
}

// ListJoinDecisions returns the newest decisions, optionally narrowed to one
// group (empty group means "all groups"). The admin panel renders this list.
func (s *Store) ListJoinDecisions(ctx context.Context, groupOpenID string, limit int) ([]JoinDecision, error) {
	groupOpenID = strings.TrimSpace(groupOpenID)
	limit = clampJoinLimit(limit, 50, 200)

	query := `
SELECT id, group_openid, conversation_id, message_id, speaker, preview, joined,
       confidence, reason, topics, model, latency_ms, created_at
  FROM join_decisions`
	args := []any{}
	if groupOpenID != "" {
		query += ` WHERE group_openid = ?`
		args = append(args, groupOpenID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []JoinDecision{}
	for rows.Next() {
		var (
			d       JoinDecision
			joined  int
			created nullableTimeText
		)
		if err := rows.Scan(&d.ID, &d.GroupOpenID, &d.ConversationID, &d.MessageID,
			&d.Speaker, &d.Preview, &joined, &d.Confidence, &d.Reason,
			&d.TopicsJSON, &d.Model, &d.LatencyMS, &created); err != nil {
			return nil, err
		}
		d.Joined = joined == 1
		d.CreatedAt = created.Value
		out = append(out, d)
	}
	return out, rows.Err()
}

// SaveJoinTopics replaces the cached topic split of one group. There is one row
// per group, so a newer analysis supersedes the older one instead of piling up
// unbounded history.
func (s *Store) SaveJoinTopics(ctx context.Context, groupOpenID, topicsJSON, analyzedAt string) error {
	groupOpenID = strings.TrimSpace(groupOpenID)
	if groupOpenID == "" {
		return errors.New("store: join topics need group_openid")
	}
	if strings.TrimSpace(analyzedAt) == "" {
		analyzedAt = nowRFC3339()
	}
	_, err := s.execContext(ctx, `
INSERT INTO join_topics (group_openid, topics, analyzed_at) VALUES (?, ?, ?)
ON CONFLICT (group_openid) DO UPDATE SET
  topics      = excluded.topics,
  analyzed_at = excluded.analyzed_at`,
		groupOpenID, topicsJSON, analyzedAt)
	return err
}

// GetJoinTopics returns the cached topics of one group, or (nil, nil) when the
// group has never been analysed. The two-value convention keeps "not analysed
// yet" a normal case for the gate instead of an error.
func (s *Store) GetJoinTopics(ctx context.Context, groupOpenID string) (*JoinTopics, error) {
	groupOpenID = strings.TrimSpace(groupOpenID)
	if groupOpenID == "" {
		return nil, nil
	}
	var (
		t        JoinTopics
		analyzed nullableTimeText
	)
	err := s.queryRowContext(ctx, `
SELECT group_openid, topics, analyzed_at FROM join_topics WHERE group_openid = ?`,
		groupOpenID).Scan(&t.GroupOpenID, &t.TopicsJSON, &analyzed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.AnalyzedAt = analyzed.Value
	return &t, nil
}

// CountJoinDecisionsSince counts the evaluations of one group at or after since.
// joinOnly restricts the count to the decisions that actually joined, which is
// what the hourly / daily rate limits are made of.
func (s *Store) CountJoinDecisionsSince(ctx context.Context, groupOpenID, since string, joinOnly bool) (int, error) {
	query := `SELECT COUNT(*) FROM join_decisions WHERE group_openid = ? AND created_at >= ?`
	args := []any{groupOpenID, since}
	if joinOnly {
		query += ` AND joined = 1`
	}
	var n int
	if err := s.queryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// LastJoinDecisionAt returns the instant of the newest *model evaluation* of
// one group, or "" when the group has never been evaluated. Cheap-gate skips
// (too short, cooldown, rate cap) are stored with an empty model and must NOT
// reset the evaluation cooldown: otherwise a busy group would push the next
// real evaluation forward forever.
func (s *Store) LastJoinDecisionAt(ctx context.Context, groupOpenID string) (string, error) {
	var at nullableTimeText
	err := s.queryRowContext(ctx,
		`SELECT MAX(created_at) FROM join_decisions WHERE group_openid = ? AND model <> ''`, groupOpenID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return at.Value, nil
}

// ClearJoinForConversation is the cascade for "delete / clear one conversation".
//
// Only decisions are conversation-scoped; join_topics is keyed by group and is
// shared by every conversation of that group (and survives a per-conversation
// clear), so it is left to ClearAllJoin. Removing it here would throw away the
// background another member's timeline still uses.
func (s *Store) ClearJoinForConversation(ctx context.Context, conversationID string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil
	}
	if _, err := s.execContext(ctx,
		`DELETE FROM join_decisions WHERE conversation_id = ?`, conversationID); err != nil {
		return fmt.Errorf("store: clear join decisions: %w", err)
	}
	return nil
}

// ClearAllJoin is the cascade for "clear all conversations": drop both tables.
func (s *Store) ClearAllJoin(ctx context.Context) error {
	for _, table := range []string{"join_decisions", "join_topics"} {
		if _, err := s.execContext(ctx, `DELETE FROM `+table); err != nil {
			return fmt.Errorf("store: clear %s: %w", table, err)
		}
	}
	return nil
}
