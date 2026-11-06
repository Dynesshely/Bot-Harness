package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Human status scopes.
const (
	HumanStatusGroup = "group"
	HumanStatusC2C   = "c2c"
)

// HumanStatusInput is one inferred activity state.
type HumanStatusInput struct {
	Scope          string
	ConversationID string
	GroupOpenID    string
	MemberOpenID   string
	DisplayName    string
	Status         string
	Confidence     float64
	Evidence       string
	UpdatedAt      string
	ExpiresAt      string
}

// HumanStatus is one stored inference.
type HumanStatus struct {
	ID             int64   `json:"id"`
	Scope          string  `json:"scope"`
	ConversationID string  `json:"conversation_id"`
	GroupOpenID    string  `json:"group_openid"`
	MemberOpenID   string  `json:"member_openid"`
	DisplayName    string  `json:"display_name"`
	Status         string  `json:"status"`
	Confidence     float64 `json:"confidence"`
	Evidence       string  `json:"evidence"`
	UpdatedAt      string  `json:"updated_at"`
	ExpiresAt      string  `json:"expires_at"`
}

// UpsertHumanStatus stores (or refreshes) the inference for one member in one
// conversation. The unique key is (scope, conversation_id, member_openid), so a
// re-inference replaces the previous state instead of piling up rows.
func (s *Store) UpsertHumanStatus(ctx context.Context, in HumanStatusInput) error {
	if strings.TrimSpace(in.Scope) == "" || strings.TrimSpace(in.ConversationID) == "" ||
		strings.TrimSpace(in.MemberOpenID) == "" {
		return fmt.Errorf("store: human status needs scope, conversation_id and member_openid")
	}
	if strings.TrimSpace(in.UpdatedAt) == "" {
		in.UpdatedAt = nowRFC3339()
	}
	if strings.TrimSpace(in.ExpiresAt) == "" {
		return fmt.Errorf("store: human status expires_at is required")
	}
	_, err := s.execContext(ctx, `
INSERT INTO human_status (
  scope, conversation_id, group_openid, member_openid, display_name,
  status, confidence, evidence, updated_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (scope, conversation_id, member_openid) DO UPDATE SET
  group_openid  = excluded.group_openid,
  display_name  = excluded.display_name,
  status        = excluded.status,
  confidence    = excluded.confidence,
  evidence      = excluded.evidence,
  updated_at    = excluded.updated_at,
  expires_at    = excluded.expires_at`,
		in.Scope, in.ConversationID, in.GroupOpenID, in.MemberOpenID, in.DisplayName,
		in.Status, in.Confidence, in.Evidence, in.UpdatedAt, in.ExpiresAt)
	return err
}

// ListHumanStatuses returns every stored inference of one conversation,
// including expired rows (callers filter by ExpiresAt; Prune removes them).
func (s *Store) ListHumanStatuses(ctx context.Context, conversationID string) ([]HumanStatus, error) {
	if strings.TrimSpace(conversationID) == "" {
		return nil, nil
	}
	rows, err := s.queryContext(ctx, `
SELECT id, scope, conversation_id, group_openid, member_openid, display_name,
       status, confidence, evidence, updated_at, expires_at
  FROM human_status
 WHERE conversation_id = ?
 ORDER BY updated_at DESC, id DESC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []HumanStatus{}
	for rows.Next() {
		var h HumanStatus
		if err := rows.Scan(&h.ID, &h.Scope, &h.ConversationID, &h.GroupOpenID,
			&h.MemberOpenID, &h.DisplayName, &h.Status, &h.Confidence,
			&h.Evidence, &h.UpdatedAt, &h.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// PruneExpiredHumanStatuses deletes inferences whose TTL has passed.
func (s *Store) PruneExpiredHumanStatuses(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.execContext(ctx, `DELETE FROM human_status WHERE expires_at < ?`,
		now.Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
