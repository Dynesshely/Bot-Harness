package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

func TestResolveConversationFollowsDecisions(t *testing.T) {
	member := &Inbound{
		Scene:        "group",
		GroupOpenID:  "G1",
		MemberOpenID: "M1",
	}
	c1 := resolveConversation(member, settings.ContextPerMember)
	if c1.Scene != SceneGroupMember {
		t.Fatalf("C1 scene = %q", c1.Scene)
	}
	if c1.ID == resolveConversation(&Inbound{Scene: "group", GroupOpenID: "G1", MemberOpenID: "M2"}, settings.ContextPerMember).ID {
		t.Fatal("C1 must keep different members apart")
	}

	c2 := resolveConversation(member, settings.ContextShared)
	if c2.Scene != SceneGroupShared {
		t.Fatalf("C2 scene = %q", c2.Scene)
	}
	other := resolveConversation(&Inbound{Scene: "group", GroupOpenID: "G1", MemberOpenID: "M2"}, settings.ContextShared)
	if c2.ID != other.ID {
		t.Fatal("C2 must share one context per group")
	}

	c2c := resolveConversation(&Inbound{Scene: "c2c", UserOpenID: "U1"}, settings.ContextPerMember)
	if c2c.Scene != SceneC2C {
		t.Fatalf("c2c scene = %q", c2c.Scene)
	}

	// The id must round-trip so the admin panel can pass it back verbatim.
	if key, err := DecodeConversationID(c1.ID); err != nil || key != "group:G1:M1" {
		t.Fatalf("decode(%q) = %q, %v", c1.ID, key, err)
	}
}

func TestBuildChatMessagesTrimsToBudget(t *testing.T) {
	history := []store.Message{
		{Role: "user", Content: strings.Repeat("很长的历史内容", 40)},
		{Role: "assistant", Content: "旧回复"},
		{Role: "user", Content: "最近的问题"},
		{Role: "assistant", Content: "最近的回答"},
	}

	msgs := BuildChatMessages("系统提示", history, "新的问题", 60)
	if len(msgs) == 0 {
		t.Fatal("expected messages")
	}
	if msgs[0].Role != "system" {
		t.Fatalf("first message should be the system prompt, got %q", msgs[0].Role)
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.Content != "新的问题" {
		t.Fatalf("last message should be the new question, got %#v", last)
	}
	// The huge first turn must have been dropped to respect the budget.
	for _, m := range msgs {
		if strings.Contains(m.Content, "很长的历史内容") {
			t.Fatal("expected the oversized history turn to be trimmed")
		}
	}
}

func TestBuildChatMessagesKeepsChronologicalOrder(t *testing.T) {
	history := []store.Message{
		{Role: "user", Content: "U1"},
		{Role: "assistant", Content: "A1"},
		{Role: "user", Content: "U2"},
		{Role: "assistant", Content: "A2"},
	}
	msgs := BuildChatMessages("sys", history, "U3", 10000)
	got := make([]string, 0, len(msgs))
	for _, m := range msgs {
		got = append(got, m.Content)
	}
	want := "sys,U1,A1,U2,A2,U3"
	if strings.Join(got, ",") != want {
		t.Fatalf("order = %v, want %s", got, want)
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("empty string should be 0 tokens, got %d", got)
	}
	if got := EstimateTokens("你好世界"); got < 4 {
		t.Fatalf("CJK should cost about one token per rune, got %d", got)
	}
	if got := EstimateTokens(strings.Repeat("a", 40)); got > 15 {
		t.Fatalf("ASCII should cost about one token per four chars, got %d", got)
	}
}

func TestLimiterWindow(t *testing.T) {
	l := NewLimiter()
	for i := 0; i < 3; i++ {
		if !l.Allow("user:1", 3) {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	if l.Allow("user:1", 3) {
		t.Fatal("the fourth request should be rejected")
	}
	if !l.Allow("user:2", 3) {
		t.Fatal("limits are per key")
	}
	if !l.Allow("user:1", 0) {
		t.Fatal("a zero limit means unlimited")
	}
}

func TestLimiterCooldown(t *testing.T) {
	l := NewLimiter()
	if !l.Cooldown("group:1", 50*time.Millisecond) {
		t.Fatal("the first call should pass")
	}
	if l.Cooldown("group:1", 50*time.Millisecond) {
		t.Fatal("a second immediate call should be blocked")
	}
	time.Sleep(60 * time.Millisecond)
	if !l.Cooldown("group:1", 50*time.Millisecond) {
		t.Fatal("after the cooldown the call should pass again")
	}
}

func TestDynamicSemaphoreRespectsLimitChanges(t *testing.T) {
	s := newDynamicSem(1)
	ctx := context.Background()

	if err := s.Acquire(ctx); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if s.InUse() != 1 {
		t.Fatalf("in use = %d", s.InUse())
	}

	blocked, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer cancel()
	if err := s.Acquire(blocked); err == nil {
		t.Fatal("expected the second acquire to block on a limit of 1")
	}

	// Raising the limit must wake a waiter immediately.
	done := make(chan error, 1)
	go func() { done <- s.Acquire(ctx) }()
	time.Sleep(20 * time.Millisecond)
	s.SetLimit(2)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("acquire after raising the limit: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("raising the limit did not wake the waiter")
	}
}

func TestDynamicSemaphoreReleasesOnContextCancel(t *testing.T) {
	s := newDynamicSem(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Acquire(ctx); err == nil {
		t.Fatal("expected a cancelled context to fail the acquire")
	}
	if s.InUse() != 0 {
		t.Fatalf("a failed acquire must not leak a permit, in use = %d", s.InUse())
	}
}

func TestKeyedMutexSerialisesSameKey(t *testing.T) {
	k := newKeyedMutex()
	unlock, err := k.Lock(context.Background(), "conv")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	if _, ok := k.TryLock("conv", 30*time.Millisecond); ok {
		t.Fatal("the same key must not be acquired twice")
	}
	otherUnlock, ok := k.TryLock("other", 30*time.Millisecond)
	if !ok {
		t.Fatal("a different key must be independent")
	}
	unlock()
	otherUnlock()

	finalUnlock, ok := k.TryLock("conv", time.Second)
	if !ok {
		t.Fatal("the key should be free after unlocking")
	}

	k.mu.Lock()
	held := len(k.m)
	k.mu.Unlock()
	if held != 1 {
		t.Fatalf("only the live entry should remain, %d present", held)
	}

	finalUnlock()
	k.mu.Lock()
	remaining := len(k.m)
	k.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("entries should be cleaned up, %d remain", remaining)
	}
}
