package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// recentSpeakersBase is the fixed instant the RecentSpeakers fixtures are
// anchored to. It is not time.Now(): the 23h/24h/25h boundaries must be exact,
// and PostgreSQL stores created_at as timestamptz while SQLite stores the
// RFC3339 text, so every fixture and the cutoff are written in the same +08:00
// zone the store scans timestamps into.
var recentSpeakersBase = time.Date(2026, 11, 4, 14, 0, 0, 0, scanLocation)

// seedRecentSpeakerMessage inserts one message with an explicit created_at.
// AppendMessage always stamps "now", so the time-window tests cannot use it.
func seedRecentSpeakerMessage(t *testing.T, s *Store, conversationID, name, openID, at string) {
	t.Helper()
	if _, err := s.execContext(context.Background(), `
INSERT INTO messages (conversation_id, role, content, tokens, kind, speaker, speaker_openid, quote_index, attachments, addressed, created_at)
VALUES (?, 'user', '闲聊一句', 0, ?, ?, ?, '', '', 1, ?)`,
		conversationID, KindObserved, name, openID, at); err != nil {
		t.Fatalf("seed message (%s/%s): %v", name, at, err)
	}
}

// seedRecentSpeakersFixture builds the same fixture on any Store, so the SQLite
// test and the PostgreSQL test pin identical semantics.
//
// Timeline around the 24h cutoff (all times +08:00, base = 2026-11-04 14:00):
//
//	U1 小明      25h ago  → outside
//	U1 小明      23h ago  → inside
//	U1 小明改名   2h ago  → inside, newest name wins
//	U2 小红       1h ago  → inside
//	U3 阿强      24h ago  → exactly on the cutoff, inside (created_at >= since)
//	"" (bot)      1h ago  → no openid, never in the roster
//	U9 other conv 1h ago  → different conversation, never in the roster
func seedRecentSpeakersFixture(t *testing.T, s *Store) time.Time {
	t.Helper()
	const conv = "conv-recent-speakers"
	since := recentSpeakersBase.Add(-24 * time.Hour)
	at := func(d time.Duration) string {
		return recentSpeakersBase.Add(-d).Format(time.RFC3339)
	}
	seedRecentSpeakerMessage(t, s, conv, "小明", "U1", at(25*time.Hour))
	seedRecentSpeakerMessage(t, s, conv, "小明", "U1", at(23*time.Hour))
	seedRecentSpeakerMessage(t, s, conv, "小明改名", "U1", at(2*time.Hour))
	seedRecentSpeakerMessage(t, s, conv, "小红", "U2", at(1*time.Hour))
	seedRecentSpeakerMessage(t, s, conv, "阿强", "U3", at(24*time.Hour))
	seedRecentSpeakerMessage(t, s, conv, "", "", at(1*time.Hour))
	seedRecentSpeakerMessage(t, s, "conv-other", "路人", "U9", at(1*time.Hour))
	return since
}

// assertRecentSpeakers is the shared contract: one row per speaker, newest
// activity first, the 24h boundary inclusive, and the limit honoured.
func assertRecentSpeakers(t *testing.T, s *Store, since time.Time) {
	t.Helper()
	ctx := context.Background()

	got, err := s.RecentSpeakers(ctx, "conv-recent-speakers", since, 10)
	if err != nil {
		t.Fatalf("RecentSpeakers: %v", err)
	}
	want := []SpeakerActivity{
		{OpenID: "U2", Name: "小红", LastAt: recentSpeakersBase.Add(-1 * time.Hour).Format(time.RFC3339), Messages: 1},
		{OpenID: "U1", Name: "小明改名", LastAt: recentSpeakersBase.Add(-2 * time.Hour).Format(time.RFC3339), Messages: 2},
		{OpenID: "U3", Name: "阿强", LastAt: recentSpeakersBase.Add(-24 * time.Hour).Format(time.RFC3339), Messages: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("recent speakers = %+v, want %d rows", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recent speakers[%d] = %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}

	limited, err := s.RecentSpeakers(ctx, "conv-recent-speakers", since, 2)
	if err != nil {
		t.Fatalf("RecentSpeakers(limit=2): %v", err)
	}
	if len(limited) != 2 || limited[0].OpenID != "U2" || limited[1].OpenID != "U1" {
		t.Fatalf("limit=2 = %+v, want the two newest speakers (U2, U1)", limited)
	}

	none, err := s.RecentSpeakers(ctx, "conv-recent-speakers", since, 0)
	if err != nil || none != nil {
		t.Fatalf("limit=0 = %+v, %v; want nil, nil", none, err)
	}
	empty, err := s.RecentSpeakers(ctx, "  ", since, 10)
	if err != nil || empty != nil {
		t.Fatalf("empty conversation = %+v, %v; want nil, nil", empty, err)
	}
}

func TestRecentSpeakersSQLite(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "speakers.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	assertRecentSpeakers(t, s, seedRecentSpeakersFixture(t, s))
}

// TestRecentSpeakersPostgres pins the same semantics on PostgreSQL, where
// created_at is timestamptz and the RFC3339 cutoff is cast by the server. It is
// gated on PG_TEST_DSN and always targets a throwaway scratch database, never a
// database whose name is production (testpg.Guard).
func TestRecentSpeakersPostgres(t *testing.T) {
	dsn := pgScratchDatabase(t)
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	assertRecentSpeakers(t, s, seedRecentSpeakersFixture(t, s))
}
