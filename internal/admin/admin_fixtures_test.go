package admin

// Shared fixtures for the conversation/data-plane tests: a shared group
// conversation, a per-member one and a C2C one, plus timeline rows, audit rows,
// tool calls, memories, tasks and proactive state.

import (
	"context"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/store"
)

type adminTestFixture struct {
	sharedID       string
	memberID       string
	c2cID          string
	quoteMessageID int64
	plainMessageID int64
	groupOpenID    string
}

func seedAdminFixture(t *testing.T, h *adminTestHarness) adminTestFixture {
	t.Helper()
	ctx := context.Background()
	f := adminTestFixture{
		sharedID:    "group-shared:" + adminTestGroup,
		memberID:    "group:" + adminTestGroup + ":U1",
		c2cID:       "c2c:U2",
		groupOpenID: adminTestGroup,
	}

	h.seedConversation(t, store.Conversation{
		ID: f.sharedID, Scene: store.SceneGroupShared, GroupOpenID: adminTestGroup,
		GroupName: "测试群",
	})
	h.seedConversation(t, store.Conversation{
		ID: f.memberID, Scene: store.SceneGroupMember, GroupOpenID: adminTestGroup,
		MemberOpenID: "U1", MemberName: "小明",
	})
	h.seedConversation(t, store.Conversation{
		ID: f.c2cID, Scene: store.SceneC2C, MemberOpenID: "U2", MemberName: "小红",
	})

	f.plainMessageID = h.seedMessage(t, store.MessageInput{
		ConversationID: f.sharedID, Role: "user", Content: "你好", Speaker: "小明",
		SpeakerOpenID: "U1", Tokens: 3,
	})
	f.quoteMessageID = h.seedMessage(t, store.MessageInput{
		ConversationID: f.sharedID, Role: "assistant", Content: "你好呀", Speaker: "机器人",
		SpeakerOpenID: "BOT-MEMBER-1", QuoteIndex: "REFIDX-SEED", Tokens: 4,
	})
	h.seedMessage(t, store.MessageInput{
		ConversationID: f.sharedID, Role: "user", Content: "旁听一句", Kind: store.KindObserved,
		Speaker: "小红", SpeakerOpenID: "U2", Tokens: 2,
	})
	h.seedMessage(t, store.MessageInput{
		ConversationID: f.c2cID, Role: "user", Content: "私聊消息", Speaker: "小红",
		SpeakerOpenID: "U2", Tokens: 3,
	})
	// A C2C row that carries a platform reference index, so the cross-conversation
	// quote guard is reachable independently of the "no reference" guard.
	h.seedMessage(t, store.MessageInput{
		ConversationID: f.c2cID, Role: "assistant", Content: "私聊回复", Speaker: "机器人",
		QuoteIndex: "REFIDX-C2C", Tokens: 3,
	})
	if err := h.store.TouchConversation(ctx, f.sharedID, 9); err != nil {
		t.Fatalf("touch shared: %v", err)
	}
	if err := h.store.TouchConversation(ctx, f.c2cID, 3); err != nil {
		t.Fatalf("touch c2c: %v", err)
	}

	h.seedRecord(t, store.Record{
		Direction: "in", Scene: store.SceneGroupShared, ConversationID: f.sharedID,
		GroupOpenID: adminTestGroup, MemberOpenID: "U1", MemberName: "小明",
		Content: "你好", MsgID: "MSG-IN-1", Status: "received",
	})
	h.seedRecord(t, store.Record{
		Direction: "out", Scene: store.SceneGroupShared, ConversationID: f.sharedID,
		GroupOpenID: adminTestGroup, Content: "你好呀", MsgID: "MSG-OUT-1",
		Status: "replied", Delivery: store.DeliveryPassive, LatencyMS: 42,
	})
	h.seedRecord(t, store.Record{
		Direction: "out", Scene: store.SceneGroupShared, ConversationID: f.sharedID,
		GroupOpenID: adminTestGroup, Content: "发送失败的内容", Status: "failed",
		Error: "platform refused",
	})

	for _, in := range []store.ToolCallInput{
		{
			ConversationID: f.sharedID, GroupOpenID: adminTestGroup, MemberOpenID: "U1",
			Tool: "now", Args: "{}", Status: store.ToolCallOK, LatencyMS: 1, ResultChars: 20,
		},
		{
			ConversationID: f.sharedID, GroupOpenID: adminTestGroup, MemberOpenID: "U1",
			Tool: "web_fetch", Args: "url=https://example.invalid",
			Status: store.ToolCallError, Error: "blocked",
		},
	} {
		if _, err := h.store.AddToolCall(ctx, in); err != nil {
			t.Fatalf("add tool call: %v", err)
		}
	}

	if err := h.store.SaveProactiveState(ctx, store.ProactiveState{
		GroupOpenID: adminTestGroup, LastSentAt: "2026-01-01T10:00:00+08:00",
		SilentStreak: 1, Sends: 4, LastReason: "有人卡在链接上",
	}); err != nil {
		t.Fatalf("save proactive state: %v", err)
	}

	due := time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	if _, err := h.store.AddTask(ctx, store.TaskInput{
		ConversationID: f.sharedID, GroupOpenID: adminTestGroup, CreatorOpenID: "U1",
		MemberOpenID: "U1", Text: "提醒我喝水", DueAt: due,
	}); err != nil {
		t.Fatalf("add task: %v", err)
	}

	for _, in := range []store.MemoryInput{
		{Scope: store.MemoryGroup, GroupOpenID: adminTestGroup, Kind: "fact", Text: "这个群的公告是周五发版", Source: "admin"},
		{Scope: store.MemoryGlobal, Kind: "fact", Text: "用户喜欢简短回答", Source: "admin"},
		{Scope: store.MemoryMember, MemberOpenID: "U2", Kind: "fact", Text: "小红在准备考试", Source: "admin"},
	} {
		if _, err := h.store.AddMemory(ctx, in); err != nil {
			t.Fatalf("add memory: %v", err)
		}
	}

	return f
}

func adminTestString(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, _ := m[key].(string)
	return v
}

func adminTestItems(t *testing.T, data map[string]any) []any {
	t.Helper()
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("missing items array: %#v", data)
	}
	return items
}

func adminTestNumber(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("%s is not a number: %#v", key, m[key])
	}
	return v
}
