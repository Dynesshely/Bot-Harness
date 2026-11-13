package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ConversationSummary is a rolling compression of messages older than
// ThroughMessageID. The original rows stay in `messages`: the timeline and the
// audit trail are never rewritten, only the model input is compacted.
type ConversationSummary struct {
	ConversationID   string `json:"conversation_id"`
	Summary          string `json:"summary"`
	ThroughMessageID int64  `json:"through_message_id"`
	Tokens           int    `json:"tokens"`
	UpdatedAt        string `json:"updated_at"`
}

// GetConversationSummary returns the current summary, or a zero value when the
// conversation has never been compacted.
func (s *Store) GetConversationSummary(ctx context.Context, conversationID string) (ConversationSummary, error) {
	var out ConversationSummary
	err := s.queryRowContext(ctx, `
SELECT conversation_id, summary, through_message_id, tokens, updated_at
FROM conversation_summaries WHERE conversation_id = ?`, conversationID).
		Scan(&out.ConversationID, &out.Summary, &out.ThroughMessageID, &out.Tokens, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ConversationSummary{ConversationID: conversationID}, nil
	}
	if err != nil {
		return ConversationSummary{}, err
	}
	return out, nil
}

// UpsertConversationSummary replaces the rolling summary and advances the
// through cursor atomically.
func (s *Store) UpsertConversationSummary(ctx context.Context, in ConversationSummary) error {
	if in.ConversationID == "" {
		return errors.New("store: conversation id is required")
	}
	if in.UpdatedAt == "" {
		in.UpdatedAt = nowRFC3339()
	}
	_, err := s.execContext(ctx, `
INSERT INTO conversation_summaries (conversation_id, summary, through_message_id, tokens, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (conversation_id) DO UPDATE SET
  summary = excluded.summary,
  through_message_id = excluded.through_message_id,
  tokens = excluded.tokens,
  updated_at = excluded.updated_at`,
		in.ConversationID, in.Summary, in.ThroughMessageID, in.Tokens, in.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: upsert conversation summary: %w", err)
	}
	return nil
}

// MessagesAfter returns up to limit of the NEWEST context messages with id >
// afterID, in chronological order. The previous ASC LIMIT returned the oldest
// window after the cursor, which made the model miss its own latest turns once
// a summary existed.
func (s *Store) MessagesAfter(ctx context.Context, conversationID string, afterID int64, limit int, includeUnaddressed bool) ([]Message, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	kinds := []any{conversationID, afterID, KindDialogue, KindManual}
	kindClause := "kind IN (?, ?)"
	if includeUnaddressed {
		kindClause = "kind IN (?, ?, ?, ?)"
		kinds = []any{conversationID, afterID, KindDialogue, KindManual, KindObserved, KindObservedBot}
	}
	query := `SELECT id, role, content, tokens, kind, speaker, speaker_openid, quote_index, attachments, created_at FROM (
  SELECT id, role, content, tokens, kind, speaker, speaker_openid, quote_index, attachments, created_at
  FROM messages WHERE conversation_id = ? AND id > ? AND ` + kindClause + ` ORDER BY id DESC LIMIT ?
) ORDER BY id ASC`
	kinds = append(kinds, limit)
	rows, err := s.queryContext(ctx, query, kinds...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}
