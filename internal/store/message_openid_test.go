package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"testing"
)

// AppendMessage 必须把发言人 openid 一起落库——昵称在同一个群里会重复，
// 后台时间线要靠 openid 才能判断到底是谁说的。
func TestAppendMessageRoundTripsSpeakerOpenID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	if err := s.AppendMessage(ctx, MessageInput{
		ConversationID: "C1", Role: "user", Content: "你好",
		Speaker: "小明", SpeakerOpenID: "U1", Kind: KindDialogue,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.AppendMessage(ctx, MessageInput{
		ConversationID: "C1", Role: "assistant", Content: "你好呀",
		SpeakerOpenID: "BOT-IN-G1", Kind: KindDialogue,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	msgs, err := s.Messages(ctx, "C1", 10, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(msgs))
	}
	if msgs[0].SpeakerOpenID != "U1" {
		t.Fatalf("用户轮次应带发言人 openid，实际 %q", msgs[0].SpeakerOpenID)
	}
	if msgs[1].SpeakerOpenID != "BOT-IN-G1" {
		t.Fatalf("助手轮次应带机器人群内 openid，实际 %q", msgs[1].SpeakerOpenID)
	}
}

// 旧库升级：新增列 + 从审计表回填。
//
// 回填只在「同一会话内该内容的审计记录都指向同一个人」时进行。
// 三个人都说过「好的」时宁可留空，也不能把 openid 按到别人头上。
func TestLegacyDatabaseGainsSpeakerOpenIDColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	// 用当前 schema 起一个旧库：只把 speaker_openid 那一列删掉，其余保持一致，
	// 这样以后 schema 再改也不会让这个测试失效。
	legacySchema := regexp.MustCompile(`(?m)^\s*speaker_openid\s+TEXT NOT NULL DEFAULT '',\n`).ReplaceAllString(schema, "")
	if legacySchema == schema {
		t.Fatal("没能从 schema 里去掉 speaker_openid，测试的前置条件失效了")
	}
	if _, err := legacy.Exec(legacySchema); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	stmts := []string{
		`INSERT INTO conversations (id, scene, group_openid, created_at, last_active_at)
		   VALUES ('C1','group_shared','G1','2026-01-01T00:00:00+08:00','2026-01-01T00:03:00+08:00')`,
		`INSERT INTO messages (conversation_id, role, content, kind, speaker, created_at) VALUES
		   ('C1','user','你好','dialogue','小明','2026-01-01T00:00:00+08:00'),
		   ('C1','user','好的','dialogue','小王','2026-01-01T00:01:00+08:00'),
		   ('C1','user','好的','dialogue','小李','2026-01-01T00:02:00+08:00'),
		   ('C1','assistant','嗯嗯','dialogue','','2026-01-01T00:03:00+08:00')`,
		`INSERT INTO records (direction, conversation_id, content, member_openid, created_at) VALUES
		   ('in','C1','你好','U1','2026-01-01T00:00:00+08:00'),
		   ('in','C1','好的','U2','2026-01-01T00:01:00+08:00'),
		   ('in','C1','好的','U3','2026-01-01T00:02:00+08:00')`,
	}
	for _, q := range stmts {
		if _, err := legacy.Exec(q); err != nil {
			t.Fatalf("legacy setup %q: %v", q, err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open upgraded: %v", err)
	}
	defer func() { _ = s.Close() }()

	msgs, err := s.Messages(context.Background(), "C1", 10, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(msgs))
	}
	if msgs[0].SpeakerOpenID != "U1" {
		t.Fatalf("唯一匹配的消息应回填 openid，实际 %q", msgs[0].SpeakerOpenID)
	}
	for _, m := range msgs[1:3] {
		if m.SpeakerOpenID != "" {
			t.Fatalf("有歧义的内容不能猜一个 openid，实际 %q", m.SpeakerOpenID)
		}
	}
	if msgs[3].SpeakerOpenID != "" {
		t.Fatalf("助手消息没有回填来源，实际 %q", msgs[3].SpeakerOpenID)
	}
}
