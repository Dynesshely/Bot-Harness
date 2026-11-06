package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SpeakerActivity is one member who spoke in a conversation inside a time
// window: who they are, when they last said something, and how much.
//
// It backs /human-status: the roster is "everyone who spoke in the last 24
// hours", ordered newest-first, so the command can bound both the number of
// people it asks the model about and *which* people (only recent speakers).
type SpeakerActivity struct {
	OpenID   string `json:"openid"`
	Name     string `json:"name"`
	LastAt   string `json:"last_at"`
	Messages int    `json:"messages"`
}

// RecentSpeakers returns the members who posted at least one message in the
// conversation at or after since, one row per member, newest activity first,
// capped at limit rows.
//
// Aggregation and ordering happen in SQL so a busy group never has to ship its
// whole 24-hour history into memory just to count speakers:
//
//	SELECT speaker_openid, MAX(created_at) AS last_at, COUNT(*) AS messages, name
//	  FROM messages
//	 WHERE conversation_id = ? AND created_at >= ? AND speaker_openid <> ''
//	 GROUP BY conversation_id, speaker_openid
//	 ORDER BY last_at DESC, speaker_openid
//	 LIMIT ?
//
// The name is the newest non-empty speaker of that member (a correlated
// subquery keyed on the grouped openid + conversation id, so it is one name per
// member, not a random one per group). conversation_id is part of the GROUP BY
// only so PostgreSQL accepts the correlated reference to it; the WHERE clause
// already fixes it to a single value.
//
// Details that matter on both engines:
//   - created_at is TEXT RFC3339 on SQLite and TIMESTAMPTZ on PostgreSQL. The
//     cutoff is bound as an RFC3339 string in both cases: pgx sends a Go string
//     in text format, and PostgreSQL casts it to timestamptz for the comparison,
//     so the result does not depend on the connection's DateStyle. Do not switch
//     this to a time.Time bind — SQLite stores text and would compare the
//     literal's format instead of its instant.
//   - speaker_openid <> ” skips assistant rows and observed bot rows: they
//     carry no member openid, and a "speaker" without one cannot be analysed.
//   - the ORDER BY sorts the alias last_at, so the two engines cannot disagree
//     about which expression is being sorted; the openid tie-break keeps the
//     LIMIT boundary deterministic when two members spoke in the same second.
//   - the existing idx_messages_conversation(conversation_id, id) index narrows
//     the scan to one conversation. The time predicate still runs over that
//     conversation's rows rather than an index range, which is acceptable: the
//     alternative is a new (conversation_id, created_at) index for every install
//     (a migration on both engines) for a query an admin runs by hand.
func (s *Store) RecentSpeakers(ctx context.Context, conversationID string, since time.Time, limit int) ([]SpeakerActivity, error) {
	if strings.TrimSpace(conversationID) == "" {
		return nil, nil
	}
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.queryContext(ctx, `
SELECT speaker_openid,
       MAX(created_at) AS last_at,
       COUNT(*)        AS messages,
       COALESCE((
         SELECT m2.speaker FROM messages m2
          WHERE m2.conversation_id = messages.conversation_id
            AND m2.speaker_openid = messages.speaker_openid
            AND m2.speaker <> ''
          ORDER BY m2.created_at DESC, m2.id DESC
          LIMIT 1
       ), '') AS name
  FROM messages
 WHERE conversation_id = ? AND created_at >= ? AND speaker_openid <> ''
 GROUP BY conversation_id, speaker_openid
 ORDER BY last_at DESC, speaker_openid
 LIMIT ?`, conversationID, since.Format(time.RFC3339), limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent speakers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []SpeakerActivity{}
	for rows.Next() {
		var item SpeakerActivity
		var lastAt nullableTimeText
		if err := rows.Scan(&item.OpenID, &lastAt, &item.Messages, &item.Name); err != nil {
			return nil, fmt.Errorf("store: scan recent speaker: %w", err)
		}
		item.LastAt = lastAt.Value
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: recent speakers: %w", err)
	}
	return out, nil
}
