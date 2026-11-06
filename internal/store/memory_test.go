package store

import (
	"context"
	"testing"
)

func newMemoryStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/mem.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMemoriesAreScopedAndNeverCrossGroups(t *testing.T) {
	s := newMemoryStore(t)
	ctx := context.Background()

	if _, err := s.AddMemory(ctx, MemoryInput{Scope: MemoryGroup, GroupOpenID: "G1", Text: "G1 的人养猫"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := s.AddMemory(ctx, MemoryInput{Scope: MemoryGroup, GroupOpenID: "G2", Text: "G2 的人养狗"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := s.AddMemory(ctx, MemoryInput{Scope: MemoryMember, MemberOpenID: "U1", Text: "U1 喜欢喝美式"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := s.AddMemory(ctx, MemoryInput{Scope: MemoryGlobal, Text: "全局：这个机器人叫小助手"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	for _, tc := range []struct {
		name  string
		scope string
		group string
		mem   string
		want  []string
	}{
		{"群 G1 看不到 G2", MemoryGroup, "G1", "", []string{"G1 的人养猫", "全局"}},
		{"群 G2 看不到 G1", MemoryGroup, "G2", "", []string{"G2 的人养狗", "全局"}},
		{"私聊只看到自己的", MemoryMember, "", "U1", []string{"U1 喜欢喝美式", "全局"}},
		{"别人的私聊看不到 U1", MemoryMember, "", "U2", []string{"全局"}},
	} {
		got, err := s.VisibleMemories(ctx, tc.scope, tc.group, tc.mem, "", 50)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		texts := make([]string, 0, len(got))
		for _, m := range got {
			texts = append(texts, m.Text)
		}
		if len(texts) != len(tc.want) {
			t.Fatalf("%s: 得到 %v，期望 %v", tc.name, texts, tc.want)
		}
		for i := range tc.want {
			if !contains(texts, tc.want[i]) {
				t.Fatalf("%s: %v 里缺少 %q", tc.name, texts, tc.want[i])
			}
		}
	}
}

func contains(list []string, want string) bool {
	for _, x := range list {
		if x == want || (len(x) > len(want) && x[:len(want)] == want) ||
			(len(want) > len(x) && want[:len(x)] == x) {
			return true
		}
	}
	return false
}

func TestMemoryRepeatRaisesSalienceInsteadOfDuplicating(t *testing.T) {
	s := newMemoryStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.AddMemory(ctx, MemoryInput{Scope: MemoryGroup, GroupOpenID: "G1", Text: "周五发版"}); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	got, err := s.VisibleMemories(ctx, MemoryGroup, "G1", "", "", 10)
	if err != nil {
		t.Fatalf("visible: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("重复记忆应合并成一条，实际 %d", len(got))
	}
	if got[0].Salience < 1.9 {
		t.Fatalf("重复应提升 salience，实际 %v", got[0].Salience)
	}
}

func TestForgetGroupRemovesOnlyGroupMemories(t *testing.T) {
	s := newMemoryStore(t)
	ctx := context.Background()
	_, _ = s.AddMemory(ctx, MemoryInput{Scope: MemoryGroup, GroupOpenID: "G1", Text: "群里的约定"})
	_, _ = s.AddMemory(ctx, MemoryInput{Scope: MemoryGlobal, Text: "全局事实"})

	n, err := s.DeleteMemoriesForGroup(ctx, "G1")
	if err != nil || n != 1 {
		t.Fatalf("删除群记忆：n=%d err=%v", n, err)
	}
	got, _ := s.VisibleMemories(ctx, MemoryGroup, "G1", "", "", 10)
	if len(got) != 1 || got[0].Scope != MemoryGlobal {
		t.Fatalf("应只剩全局记忆：%#v", got)
	}
}

func TestWeakestMemoryIsEvicted(t *testing.T) {
	s := newMemoryStore(t)
	ctx := context.Background()
	_, _ = s.AddMemory(ctx, MemoryInput{Scope: MemoryGroup, GroupOpenID: "G1", Text: "重要的事", Salience: 5})
	_, _ = s.AddMemory(ctx, MemoryInput{Scope: MemoryGroup, GroupOpenID: "G1", Text: "随口一提", Salience: 1})
	if err := s.DropWeakestMemory(ctx, MemoryGroup, "G1", ""); err != nil {
		t.Fatalf("evict: %v", err)
	}
	got, _ := s.VisibleMemories(ctx, MemoryGroup, "G1", "", "", 10)
	if len(got) != 1 || got[0].Text != "重要的事" {
		t.Fatalf("应淘汰最弱的一条：%#v", got)
	}
}
