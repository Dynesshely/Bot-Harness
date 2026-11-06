package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Conversation scenes. These live here as the single source of truth; the
// core package re-exports them.
const (
	SceneGroupMember = "group_member"
	SceneGroupShared = "group_shared"
	SceneC2C         = "c2c"
)

// ErrGroupMismatch is returned when a conversation does not belong to the group
// the merge was requested for. Merging across groups would mix unrelated
// members' history together, so it is refused outright.
var ErrGroupMismatch = errors.New("conversation does not belong to this group")

// MergeResult summarises a conversation merge.
type MergeResult struct {
	GroupOpenID          string   `json:"group_openid"`
	TargetID             string   `json:"target_id"`
	SourceIDs            []string `json:"source_ids"`
	MovedMessages        int      `json:"moved_messages"`
	DedupedMessages      int      `json:"deduped_messages"`
	DeletedConversations int      `json:"deleted_conversations"`
	TargetMessageCount   int      `json:"target_message_count"`
	TargetTurnCount      int      `json:"target_turn_count"`
	DryRun               bool     `json:"dry_run"`
}

// MergeGroupConversations folds every conversation of one group into targetID.
//
// It is the supported way to clean up the per-member conversations left behind
// after a group switches to shared context (C2).
//
// Safety properties, in order of importance:
//  1. The group is the boundary: sources are selected with
//     `WHERE group_openid = ?` and every row is re-checked afterwards, so a
//     merge can never pull in another group's history.
//  2. Only group scenes participate; a C2C conversation has no group and can
//     therefore never be a source (and is rejected if it ever appears).
//  3. Everything happens in one transaction: either all messages move and all
//     sources disappear, or nothing changes.
//  4. Message rows are re-pointed, not copied, so ids (and therefore ordering)
//     are preserved and running the merge twice cannot duplicate anything.
func (s *Store) MergeGroupConversations(ctx context.Context, groupOpenID, targetID string, dryRun bool) (*MergeResult, error) {
	if strings.TrimSpace(groupOpenID) == "" {
		return nil, errors.New("group_openid is required")
	}

	rows, err := s.queryContext(ctx,
		`SELECT id, scene, group_openid FROM conversations WHERE group_openid = ?`, groupOpenID)
	if err != nil {
		return nil, err
	}
	type convo struct{ id, scene, group string }
	var all []convo
	for rows.Next() {
		var c convo
		if err := rows.Scan(&c.id, &c.scene, &c.group); err != nil {
			rows.Close()
			return nil, err
		}
		// Defence in depth: the WHERE clause already guarantees this, but a
		// cross-group merge is bad enough to check twice.
		if c.group != groupOpenID {
			rows.Close()
			return nil, fmt.Errorf("%w: %s belongs to %s, not %s", ErrGroupMismatch, c.id, c.group, groupOpenID)
		}
		if c.scene != SceneGroupMember && c.scene != SceneGroupShared {
			rows.Close()
			return nil, fmt.Errorf("%w: scene %q cannot be merged", ErrGroupMismatch, c.scene)
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(all) == 0 {
		return nil, ErrNotFound
	}

	if targetID == "" {
		for _, c := range all {
			if c.scene == SceneGroupShared {
				targetID = c.id
				break
			}
		}
	}
	if targetID == "" {
		return nil, errors.New("no target conversation: create the shared one first")
	}

	foundTarget := false
	sources := make([]string, 0, len(all))
	for _, c := range all {
		if c.id == targetID {
			foundTarget = true
			continue
		}
		sources = append(sources, c.id)
	}
	if !foundTarget {
		// The target must itself live in this group; otherwise a caller could
		// name any conversation and absorb another group's members into it.
		return nil, fmt.Errorf("%w: target %s is not a conversation of group %s", ErrGroupMismatch, targetID, groupOpenID)
	}

	result := &MergeResult{
		GroupOpenID: groupOpenID,
		TargetID:    targetID,
		SourceIDs:   sources,
		DryRun:      dryRun,
	}
	if len(sources) == 0 {
		return result, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(sources)), ",")
	args := make([]any, 0, len(sources)+1)
	for _, id := range sources {
		args = append(args, id)
	}

	if err := s.queryRowContext(ctx,
		"SELECT COUNT(*) FROM messages WHERE conversation_id IN ("+placeholders+")",
		args...).Scan(&result.MovedMessages); err != nil {
		return nil, err
	}

	if dryRun {
		return result, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	moveArgs := append([]any{targetID}, args...)
	if _, err := s.txExecContext(ctx, tx, "UPDATE messages SET conversation_id = ? WHERE conversation_id IN ("+placeholders+")",
		moveArgs...); err != nil {
		return nil, fmt.Errorf("move messages: %w", err)
	}

	// Another bot's messages are mirrored into every conversation of the group,
	// so folding them together leaves identical copies behind.
	var before, after int
	if err := s.txQueryRowContext(ctx, tx, `SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND kind = ?`,
		targetID, KindObservedBot).Scan(&before); err != nil {
		return nil, err
	}
	if _, err := s.txExecContext(ctx, tx, `
DELETE FROM messages
WHERE conversation_id = ? AND kind = ?
  AND id NOT IN (
    SELECT MIN(id) FROM messages
    WHERE conversation_id = ? AND kind = ?
    GROUP BY content, speaker, created_at
  )`, targetID, KindObservedBot, targetID, KindObservedBot); err != nil {
		return nil, fmt.Errorf("dedupe mirrored bot messages: %w", err)
	}
	if err := s.txQueryRowContext(ctx, tx, `SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND kind = ?`,
		targetID, KindObservedBot).Scan(&after); err != nil {
		return nil, err
	}
	result.DedupedMessages = before - after

	if _, err := s.txExecContext(ctx, tx, "DELETE FROM conversations WHERE id IN ("+placeholders+")", args...); err != nil {
		return nil, fmt.Errorf("delete source conversations: %w", err)
	}
	result.DeletedConversations = len(sources)

	if err := s.recountConversation(ctx, tx, targetID); err != nil {
		return nil, err
	}
	if err := s.txQueryRowContext(ctx, tx, `SELECT message_count, turn_count FROM conversations WHERE id = ?`, targetID).
		Scan(&result.TargetMessageCount, &result.TargetTurnCount); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// recountConversation rebuilds the denormalised counters of one conversation.
func (s *Store) recountConversation(ctx context.Context, tx *sql.Tx, conversationID string) error {
	_, err := s.txExecContext(ctx, tx, `
UPDATE conversations SET
  message_count = (SELECT COUNT(*) FROM messages WHERE conversation_id = ?),
  turn_count    = (SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND role = 'assistant'),
  token_estimate = (SELECT COALESCE(SUM(tokens), 0) FROM messages WHERE conversation_id = ?),
  last_active_at = COALESCE(
      (SELECT MAX(created_at) FROM messages WHERE conversation_id = ?),
      last_active_at)
WHERE id = ?`, conversationID, conversationID, conversationID, conversationID, conversationID)
	return err
}

// GroupConversationSummary is the per-group view used by the merge dialog.
type GroupConversationSummary struct {
	GroupOpenID   string         `json:"group_openid"`
	GroupName     string         `json:"group_name"`
	SharedID      string         `json:"shared_conversation_id"`
	Conversations []Conversation `json:"conversations"`
}

// MergeableGroups lists the groups that have more than one conversation, i.e.
// the ones worth merging.
func (s *Store) MergeableGroups(ctx context.Context, limit int) ([]GroupConversationSummary, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.queryContext(ctx, `
SELECT group_openid, COUNT(*) AS n
FROM conversations
WHERE group_openid <> ''
GROUP BY group_openid
HAVING n > 1
ORDER BY n DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	var groups []string
	for rows.Next() {
		var gid string
		var n int
		if err := rows.Scan(&gid, &n); err != nil {
			rows.Close()
			return nil, err
		}
		groups = append(groups, gid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	out := make([]GroupConversationSummary, 0, len(groups))
	for _, gid := range groups {
		items, err := s.conversationsOfGroup(ctx, gid)
		if err != nil {
			return nil, err
		}
		summary := GroupConversationSummary{GroupOpenID: gid, Conversations: items}
		for _, c := range items {
			if c.GroupName != "" {
				summary.GroupName = c.GroupName
			}
			if c.Scene == SceneGroupShared {
				summary.SharedID = c.ID
			}
		}
		out = append(out, summary)
	}
	return out, nil
}

func (s *Store) conversationsOfGroup(ctx context.Context, groupOpenID string) ([]Conversation, error) {
	rows, err := s.queryContext(ctx, `
SELECT id, scene, group_openid, member_openid, group_name, member_name, enabled,
       message_count, turn_count, token_estimate, created_at, last_active_at
FROM conversations WHERE group_openid = ? ORDER BY message_count DESC`, groupOpenID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}
