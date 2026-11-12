package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/store"
)

// 有滚动摘要时，下一轮 systemPrompt 必须带上摘要；原消息仍在时间线里。
func TestConversationSummaryInjected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	convID := core.EncodeConversationID("group:G1:U1")
	if err := h.st.EnsureConversation(ctx, &store.Conversation{
		ID: convID, Scene: "group", GroupOpenID: "G1", MemberOpenID: "U1",
	}); err != nil {
		t.Fatalf("conversation: %v", err)
	}
	oldID, err := h.st.AppendMessageID(ctx, store.MessageInput{
		ConversationID: convID, Role: "user", Content: "很久以前聊过的事", Kind: store.KindDialogue, Speaker: "小明",
	})
	if err != nil {
		t.Fatalf("append old: %v", err)
	}
	if err := h.st.UpsertConversationSummary(ctx, store.ConversationSummary{
		ConversationID: convID, Summary: "用户们以前讨论过旧话题，并约定稍后继续。",
		ThroughMessageID: oldID, Tokens: 20,
	}); err != nil {
		t.Fatalf("summary: %v", err)
	}

	h.dis.HandlePayload(ctx, groupAtMessage("COMPACT1", "G1", "U1", "小明", "继续刚才的话题"))
	h.waitForSent(t, 1)
	if got := h.systemPrompt(t).text; !strings.Contains(got, "压缩摘要") || !strings.Contains(got, "旧话题") {
		t.Fatalf("summary missing from prompt: %q", got)
	}
}
