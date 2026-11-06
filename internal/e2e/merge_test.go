package e2e

import (
	"context"
	"testing"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/store"
)

// 建两个成员各自的会话 + 一个共享会话，验证合并只动本群。
func TestMergeGroupConversationsStaysInsideTheGroup(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), groupAtMessage("M1", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)
	h.dis.HandlePayload(context.Background(), groupAtMessage("M2", "G1", "U2", "小红", "在吗"))
	h.waitForSent(t, 2)
	// 另一个群，绝不能被卷进来
	h.dis.HandlePayload(context.Background(), groupAtMessage("M3", "G2", "U3", "小刚", "别的群"))
	h.waitForSent(t, 3)

	ctx := context.Background()
	g1 := core.EncodeConversationID("group:G1:U1")
	g1b := core.EncodeConversationID("group:G1:U2")
	g2 := core.EncodeConversationID("group:G2:U3")
	shared := core.EncodeConversationID("group-shared:G1")
	if err := h.st.EnsureConversation(ctx, &store.Conversation{
		ID: shared, Scene: store.SceneGroupShared, GroupOpenID: "G1",
	}); err != nil {
		t.Fatalf("ensure shared: %v", err)
	}

	before, _ := h.st.Messages(ctx, g2, 20, 0)
	if len(before) == 0 {
		t.Fatal("另一个群应当有自己的消息")
	}

	result, err := h.st.MergeGroupConversations(ctx, "G1", shared, false)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if result.DeletedConversations != 2 {
		t.Fatalf("应合并 2 个成员会话，实际 %d", result.DeletedConversations)
	}
	if result.MovedMessages != 4 {
		t.Fatalf("应搬移 4 条消息，实际 %d", result.MovedMessages)
	}

	// 本群：只剩共享会话，且消息都在
	ids, err := h.st.ConversationsForGroup(ctx, "G1", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 1 || ids[0] != shared {
		t.Fatalf("G1 应只剩共享会话，实际 %v", ids)
	}
	merged, _ := h.st.Messages(ctx, shared, 50, 0)
	if len(merged) != 4 {
		t.Fatalf("共享会话应有 4 条消息，实际 %d", len(merged))
	}

	// 另一个群：原封不动
	after, _ := h.st.Messages(ctx, g2, 20, 0)
	if len(after) != len(before) {
		t.Fatalf("其他群的消息数被改动了：%d → %d", len(before), len(after))
	}
	if others, _ := h.st.ConversationsForGroup(ctx, "G2", 20); len(others) != 1 || others[0] != g2 {
		t.Fatalf("其他群的会话被改动了：%v", others)
	}
	_ = g1
	_ = g1b
}

func TestMergeRefusesAForeignTarget(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), groupAtMessage("F1", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)
	h.dis.HandlePayload(context.Background(), groupAtMessage("F2", "G2", "U2", "小红", "你好"))
	h.waitForSent(t, 2)

	ctx := context.Background()
	foreign := core.EncodeConversationID("group:G2:U2")
	if _, err := h.st.MergeGroupConversations(ctx, "G1", foreign, false); err == nil {
		t.Fatal("把别的群的会话当作目标必须被拒绝")
	}
}

func TestMergeDryRunChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), groupAtMessage("D1", "G1", "U1", "小明", "你好"))
	h.waitForSent(t, 1)
	h.dis.HandlePayload(context.Background(), groupAtMessage("D2", "G1", "U2", "小红", "在吗"))
	h.waitForSent(t, 2)

	ctx := context.Background()
	shared := core.EncodeConversationID("group-shared:G1")
	if err := h.st.EnsureConversation(ctx, &store.Conversation{
		ID: shared, Scene: store.SceneGroupShared, GroupOpenID: "G1",
	}); err != nil {
		t.Fatalf("ensure shared: %v", err)
	}
	preview, err := h.st.MergeGroupConversations(ctx, "G1", shared, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if preview.DeletedConversations != 0 || preview.MovedMessages == 0 || !preview.DryRun {
		t.Fatalf("试运行不应改动数据，实际 %#v", preview)
	}
	if n := len(mustConversations(t, h, "G1")); n != 3 {
		t.Fatalf("试运行后仍应是 3 个会话（2 个成员 + 1 个共享），实际 %d", n)
	}
}

func mustConversations(t *testing.T, h *harness, groupOpenID string) []string {
	t.Helper()
	ids, err := h.st.ConversationsForGroup(context.Background(), groupOpenID, 20)
	if err != nil {
		t.Fatalf("conversations: %v", err)
	}
	return ids
}
