package store

import (
	"context"
	"testing"
)

func TestMessagesAfterReturnsNewestWindow(t *testing.T) {
	s, err := Open(t.TempDir() + "/sum.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	conv := &Conversation{ID: "c1", Scene: "group", GroupOpenID: "G1"}
	if err := s.EnsureConversation(ctx, conv); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := s.AppendMessageID(ctx, MessageInput{ConversationID: conv.ID, Role: "user", Content: "消息", Kind: KindDialogue})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// 旁观消息属于另一个隐私边界，默认不能进入 addressed_only 窗口。
	if _, err := s.AppendMessageID(ctx, MessageInput{ConversationID: conv.ID, Role: "user", Content: "旁观", Kind: KindObserved}); err != nil {
		t.Fatal(err)
	}
	got, err := s.MessagesAfter(ctx, conv.ID, 0, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != ids[3] || got[1].ID != ids[4] {
		t.Fatalf("want newest two %v, got %+v", ids[3:], got)
	}
}

func TestClearConversationRemovesSummary(t *testing.T) {
	s, err := Open(t.TempDir() + "/sum.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	conv := &Conversation{ID: "c2", Scene: "group", GroupOpenID: "G2"}
	if err := s.EnsureConversation(ctx, conv); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertConversationSummary(ctx, ConversationSummary{ConversationID: conv.ID, Summary: "旧摘要", ThroughMessageID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearConversationMessages(ctx, conv.ID); err != nil {
		t.Fatal(err)
	}
	sum, err := s.GetConversationSummary(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Summary != "" {
		t.Fatalf("summary survived clear: %+v", sum)
	}
}

func TestClearAllConversationsRemovesSummaries(t *testing.T) {
	s, err := Open(t.TempDir() + "/sum.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.UpsertConversationSummary(ctx, ConversationSummary{ConversationID: "c3", Summary: "旧摘要"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearAllConversations(ctx); err != nil {
		t.Fatal(err)
	}
	sum, err := s.GetConversationSummary(ctx, "c3")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Summary != "" {
		t.Fatalf("summary survived clear all: %+v", sum)
	}
}
