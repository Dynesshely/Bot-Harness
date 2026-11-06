package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/store"
)

// seedHumanStatusMessageAt stores one conversation message and rewrites its
// created_at, so the 24h roster window can be pinned at exact instants.
func seedHumanStatusMessageAt(t *testing.T, h *harness, conv, name, openID, content string, at time.Time) {
	t.Helper()
	id, err := h.st.AppendMessageID(context.Background(), store.MessageInput{
		ConversationID: conv, Role: "user", Kind: store.KindDialogue,
		Content: content, Speaker: name, SpeakerOpenID: openID,
	})
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if _, err := h.st.DB().ExecContext(context.Background(),
		"UPDATE messages SET created_at = ? WHERE id = ?", at.Format(time.RFC3339), id); err != nil {
		t.Fatalf("backdate message: %v", err)
	}
}

// aiUserText concatenates the user messages of one recorded model request.
func aiUserText(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	var b strings.Builder
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] != "user" {
			continue
		}
		text, _ := m["content"].(string)
		b.WriteString(text)
	}
	return b.String()
}

// /human-status 2：最近 24h 有 3 人发言，只分析最近发言的 2 人（图片标题/审计
// 的计数也是 2），名单外的成员既不进提示词也不进回复。
func TestHumanStatusCommandCountLimitsRoster(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"admin_openids":["U1"],"markdown_enabled":false}`)
	convID := core.EncodeConversationID("group:G1:U1")
	now := time.Now()
	// 最近发言顺序：U3（1 分钟前）> U2（3 分钟前）> U1（5 分钟前）。
	seedHumanStatusMessageAt(t, h, convID, "小刚", "U3", "在打游戏", now.Add(-time.Minute))
	seedHumanStatusMessageAt(t, h, convID, "小红", "U2", "在赶周报", now.Add(-3*time.Minute))
	seedHumanStatusMessageAt(t, h, convID, "老成员", "U1", "昨天也在", now.Add(-5*time.Minute))
	h.mu.Lock()
	h.humanStatusReply = `[{"member_openid":"U1","display_name":"老成员","status":"在线","confidence":0.7,"evidence":"今天说过话"},{"member_openid":"U2","display_name":"小红","status":"工作","confidence":0.8,"evidence":"在赶周报"},{"member_openid":"U3","display_name":"小刚","status":"打游戏","confidence":0.9,"evidence":"在打游戏"}]`
	h.mu.Unlock()

	h.dis.HandlePayload(ctx, groupAtMessage("M-HS-COUNT", "G1", "U1", "老成员", "/human-status 2"))
	sent := h.waitForSent(t, 1)
	assertMediaMessage(t, sent, "M-HS-COUNT")
	assertImageUpload(t, h, "/v2/groups/G1/files")

	outRec := waitForOutboundRecord(t, h, "M-HS-COUNT")
	if outRec.Content != "[图片] 聊天对象状态推测（2 人）" {
		t.Fatalf("出站审计 Content = %q, want 2 analysed people", outRec.Content)
	}

	h.mu.Lock()
	body := h.lastAIBody
	calls := h.humanStatusCalls
	h.mu.Unlock()
	if calls != 1 {
		t.Fatalf("human status AI calls = %d, want 1", calls)
	}
	user := aiUserText(body)
	for _, want := range []string{"- 小刚 (U3)", "- 小红 (U2)"} {
		if !strings.Contains(user, want) {
			t.Fatalf("roster prompt missing %q", want)
		}
	}
	if strings.Contains(user, "- 老成员 (U1)") {
		t.Fatalf("roster prompt must only contain the 2 newest speakers")
	}
	t.Logf("/human-status 2 出站审计: %s", outRec.Content)
}

// 非法参数：回用法文本、不调模型、不出图。
func TestHumanStatusCommandInvalidCountReturnsUsage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"admin_openids":["U1"],"markdown_enabled":false}`)
	convID := core.EncodeConversationID("group:G1:U1")
	seedHumanStatusMessageAt(t, h, convID, "小明", "U1", "在吗", time.Now().Add(-time.Minute))

	h.dis.HandlePayload(ctx, groupAtMessage("M-HS-BAD", "G1", "U1", "小明", "/human-status abc"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("应只回一条文本，实际 %d 条: %#v", len(sent), sent)
	}
	if sent[0].Body.MsgType == qq.MsgTypeMedia {
		t.Fatalf("非法参数不应发图片消息: %#v", sent[0].Body)
	}
	if !strings.Contains(sent[0].Body.Content, "用法：/human-status [人数]") {
		t.Fatalf("reply = %q, want the usage text", sent[0].Body.Content)
	}
	if uploads := h.sentUploads(); len(uploads) != 0 {
		t.Fatalf("非法参数不应上传图片: %#v", uploads)
	}
	h.mu.Lock()
	calls := h.humanStatusCalls
	h.mu.Unlock()
	if calls != 0 {
		t.Fatalf("非法参数不应调用模型，calls = %d", calls)
	}
	t.Logf("/human-status abc 回复: %s", sent[0].Body.Content)
}
