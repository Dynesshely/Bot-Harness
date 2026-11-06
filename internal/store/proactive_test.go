package store

import (
	"context"
	"testing"
	"time"
)

// 额度只由「自然交谈」驱动：旁观/未被 @ 的人类发言计入，
// 冲着机器人来的 @ 消息、机器人自己的话、手动发送、别的机器人都排除。
func TestHumanMessageCounting(t *testing.T) {
	s := taskStore(t)
	ctx := context.Background()
	if err := s.EnsureConversation(ctx, &Conversation{
		ID: "group-shared:G1", Scene: SceneGroupShared, GroupOpenID: "G1",
	}); err != nil {
		t.Fatalf("conversation: %v", err)
	}

	add := func(role, kind string, addressed bool, content string) {
		if err := s.AppendMessage(ctx, MessageInput{
			ConversationID: "group-shared:G1", Role: role, Kind: kind, Content: content,
			Addressed: addressed,
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// 计入：自然交谈（all_messages 下会被回答，at_only 下是旁观记录）。
	add("user", KindDialogue, false, "群友闲聊 1")
	add("user", KindDialogue, false, "群友闲聊 2")
	add("user", KindObserved, false, "旁观的群聊")
	// 不计：对机器人说话（@ / 私聊）。
	add("user", KindDialogue, true, "@机器人 在吗")
	add("user", KindObserved, true, "标记异常也按 addressed 排除")
	// 不计：机器人自己的话、手动发送、别的机器人。
	add("assistant", KindDialogue, false, "机器人说话")
	add("assistant", KindManual, false, "手动发送")
	add("user", KindObservedBot, false, "另一个机器人")

	n, err := s.CountHumanMessagesSince(ctx, "G1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Fatalf("应只数到 3 条未被 @ 的人类发言，实际 %d", n)
	}
	if n, _ := s.CountHumanMessagesSince(ctx, "G2", time.Now().Add(-time.Hour)); n != 0 {
		t.Fatalf("别的群应为 0，实际 %d", n)
	}
	if n, _ := s.CountHumanMessagesSince(ctx, "G1", time.Now().Add(time.Hour)); n != 0 {
		t.Fatalf("未来时间点应为 0，实际 %d", n)
	}
}

func TestProactiveStateRoundTrip(t *testing.T) {
	s := taskStore(t)
	ctx := context.Background()

	empty, err := s.GetProactiveState(ctx, "G1")
	if err != nil || empty.LastSentAt != "" || empty.SilentStreak != 0 {
		t.Fatalf("初始状态应为空：%+v err=%v", empty, err)
	}

	want := ProactiveState{GroupOpenID: "G1", LastSentAt: "2026-10-30T14:00:00+08:00",
		SilentStreak: 1, Sends: 4, LastReason: "接了个话"}
	if err := s.SaveProactiveState(ctx, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, _ := s.GetProactiveState(ctx, "G1")
	if got.LastSentAt != want.LastSentAt || got.Sends != 4 || got.LastReason != want.LastReason {
		t.Fatalf("回读不一致：%+v", got)
	}

	// 再存一次是更新而不是插入
	want.Sends = 5
	if err := s.SaveProactiveState(ctx, want); err != nil {
		t.Fatalf("update: %v", err)
	}
	list, _ := s.ListProactiveStates(ctx)
	if len(list) != 1 || list[0].Sends != 5 {
		t.Fatalf("应只有一行且已更新：%+v", list)
	}
}
