package core

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

func TestCompactOnceSummarizesOldest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"用户们讨论了旧话题，并约定稍后继续。"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	st, err := store.Open(t.TempDir() + "/compact.db")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	conv := &store.Conversation{ID: "group:G1:U1", Scene: "group", GroupOpenID: "G1", MemberOpenID: "U1"}
	if err := st.EnsureConversation(ctx, conv); err != nil {
		t.Fatalf("conversation: %v", err)
	}
	const n = 210
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		id, err := st.AppendMessageID(ctx, store.MessageInput{
			ConversationID: conv.ID, Role: "user", Content: "旧消息", Kind: store.KindDialogue, Speaker: "小明",
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	s := settings.Defaults()
	s.Model = "test-model"
	s.MaxTokens = 100
	s.ContextMaxTokens = 1000
	s.ContextMaxTurns = 600
	d := &Dispatcher{
		st:  st,
		ai:  ai.NewClient(upstream.URL+"/v1", "test-key", upstream.Client()),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := d.compactOnce(ctx, conv.ID, s, false); err != nil {
		t.Fatalf("compact: %v", err)
	}
	sum, err := st.GetConversationSummary(ctx, conv.ID)
	if err != nil || sum.Summary == "" {
		t.Fatalf("summary missing: %+v %v", sum, err)
	}
	if sum.ThroughMessageID != ids[9] {
		t.Fatalf("through id = %d, want %d", sum.ThroughMessageID, ids[9])
	}
	msgs, err := st.MessagesAfter(ctx, conv.ID, 0, 500, false)
	if err != nil || len(msgs) != n {
		t.Fatalf("original messages must stay: len=%d err=%v", len(msgs), err)
	}
}

func TestCompactSummaryBlock(t *testing.T) {
	if compactSummaryBlock("  ") != "" {
		t.Fatal("empty summary should not render")
	}
	got := compactSummaryBlock("旧话题摘要")
	if !containsAll(got, "压缩摘要", "旧话题摘要") {
		t.Fatalf("block = %q", got)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !stringsContains(s, p) {
			return false
		}
	}
	return true
}

func stringsContains(s, sub string) bool { return strings.Contains(s, sub) }

// 首条超长（合并转发）不能让游标永久卡住；即使只能摘要一条，也必须前进。
func TestCompactAdvancesPastOversizedFirstMessage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"超长消息摘要"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	st, err := store.Open(t.TempDir() + "/oversize.db")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	conv := &store.Conversation{ID: "group:G9:U9", Scene: "group", GroupOpenID: "G9"}
	if err := st.EnsureConversation(ctx, conv); err != nil {
		t.Fatal(err)
	}
	first, err := st.AppendMessageID(ctx, store.MessageInput{ConversationID: conv.ID, Role: "user", Content: strings.Repeat("超长", 10000), Kind: store.KindDialogue})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 210; i++ {
		if _, err := st.AppendMessageID(ctx, store.MessageInput{ConversationID: conv.ID, Role: "user", Content: "后面", Kind: store.KindDialogue}); err != nil {
			t.Fatal(err)
		}
	}
	s := settings.Defaults()
	s.Model = "test-model"
	s.MaxTokens = 100
	s.ContextMaxTokens = 1000
	s.ContextMaxTurns = 600
	d := &Dispatcher{st: st, ai: ai.NewClient(upstream.URL+"/v1", "k", upstream.Client()), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := d.compactOnce(ctx, conv.ID, s, false); err != nil {
		t.Fatalf("compact: %v", err)
	}
	sum, err := st.GetConversationSummary(ctx, conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.ThroughMessageID == 0 || sum.ThroughMessageID < first {
		t.Fatalf("cursor did not advance: %+v (first=%d)", sum, first)
	}
}
