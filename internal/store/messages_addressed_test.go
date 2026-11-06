package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 升级路径：0010 之前的库没有 messages.addressed。
// 迁移后存量 dialogue 默认 addressed=1（已寻址 → 不会被当成主动额度来源，
// 升级不会从旧历史里突然攒出额度），kind=observed 的旁观发言回填 0。
func TestMigrationAddressedDefaultsToAddressedAndBackfillsObserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	// 0010 之前的 messages 表（含 0001-0009 加过的列），外加已应用的迁移记录。
	if _, err := raw.Exec(`
CREATE TABLE schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  tokens INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL DEFAULT 'dialogue',
  speaker TEXT NOT NULL DEFAULT '',
  speaker_openid TEXT NOT NULL DEFAULT '',
  quote_index TEXT NOT NULL DEFAULT '',
  attachments TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
INSERT INTO messages (conversation_id, role, content, kind, created_at) VALUES
  ('c1', 'user', '@机器人 在吗', 'dialogue', '2026-10-30T10:00:00+08:00'),
  ('c1', 'user', '群里闲聊', 'observed', '2026-10-30T10:01:00+08:00'),
  ('c1', 'assistant', '机器人回复', 'dialogue', '2026-10-30T10:02:00+08:00');
`); err != nil {
		t.Fatalf("build old schema: %v", err)
	}
	for _, name := range []string{
		"0001_messages_kind_speaker", "0002_backfill_message_speakers",
		"0003_backfill_shared_speakers", "0004_messages_attachments",
		"0005_messages_speaker_openid", "0006_backfill_message_speaker_openid",
		"0007_records_delivery", "0008_messages_quote_index", "0009_tasks_creator",
	} {
		if _, err := raw.Exec(
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, '2026-10-30T00:00:00+08:00')`,
			name); err != nil {
			t.Fatalf("mark migration %s: %v", name, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open upgraded store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	cols, err := s.tableColumns(context.Background(), "messages")
	if err != nil {
		t.Fatalf("table columns: %v", err)
	}
	if !cols["addressed"] {
		t.Fatal("0010 应给老库加上 messages.addressed")
	}

	rows, err := s.db.Query(`SELECT content, addressed FROM messages ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]int{}
	for rows.Next() {
		var (
			content   string
			addressed int
		)
		if err := rows.Scan(&content, &addressed); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[content] = addressed
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	want := map[string]int{"@机器人 在吗": 1, "群里闲聊": 0, "机器人回复": 1}
	for content, addressed := range want {
		if got[content] != addressed {
			t.Fatalf("存量行 %q 的 addressed 应为 %d，实际 %d（全部：%v）",
				content, addressed, got[content], got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("行数不对：%v", got)
	}
}
