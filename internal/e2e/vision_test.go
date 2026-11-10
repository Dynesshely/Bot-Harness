package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/store"
)

// quotedImageMessage builds a message whose only image sits inside msg_elements
// (the shape QQ uses for a quoted picture).
func quotedImageMessage(msgID, groupOpenID, memberOpenID, name, content, imageURL string) *qq.Payload {
	author := map[string]any{
		"id": memberOpenID, "member_openid": memberOpenID, "username": name, "bot": false,
	}
	d, _ := json.Marshal(map[string]any{
		"id":           msgID,
		"content":      content,
		"group_openid": groupOpenID,
		"timestamp":    "2026-07-21T10:00:00+08:00",
		"message_type": 0,
		"author":       author,
		"attachments":  []map[string]any{},
		"msg_elements": []map[string]any{{
			"author":       author,
			"message_type": 0,
			"content":      "引用里的图片",
			"attachments": []map[string]any{{
				"url": imageURL, "content_type": "image/png", "filename": "quoted.png",
				"width": 1, "height": 1, "size": 70,
			}},
		}},
	})
	return &qq.Payload{Op: qq.OpDispatch, T: qq.EventGroupAtMessageCreate, ID: "EV-" + msgID, D: d}
}

func lastUserContent(t *testing.T, h *harness) string {
	t.Helper()
	msgs := lastAIMessages(t, h)
	if len(msgs) == 0 {
		t.Fatal("模型请求里没有消息")
	}
	last := msgs[len(msgs)-1]
	content, _ := last["content"].(string)
	return content
}

func lastToolContent(t *testing.T, h *harness) string {
	t.Helper()
	for _, m := range lastAIMessages(t, h) {
		if m["role"] == "tool" {
			content, _ := m["content"].(string)
			return content
		}
	}
	t.Fatal("模型请求里没有 role=tool 的消息")
	return ""
}

func (h *harness) setToolScript(calls ...string) {
	h.mu.Lock()
	h.aiToolScript = append([]string(nil), calls...)
	h.mu.Unlock()
}

func (h *harness) visionCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.humanStatusVisionCalls
}

// 当前消息的图片 + 模型主动调用 view_image：工具能拿到视觉描述，回复仍正常。
func TestCurrentImageCanBeViewedByTool(t *testing.T) {
	h := newHarness(t)
	mediaID, err := h.media.Store.Put(context.Background(), "image/png", tinyPNG())
	if err != nil {
		t.Fatalf("seed media: %v", err)
	}
	h.setToolScript(fmt.Sprintf(`view_image|{"media_id":%q,"question":"图里有什么？"}`, mediaID))

	h.dis.HandlePayload(context.Background(), imageMessageWithURL("VIMG1", "G1", "U1", "小明", h.imageURL))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 || !strings.Contains(sent[0].Body.Content, h.aiReply) {
		t.Fatalf("带图消息应正常回复，实际 %#v", sent)
	}
	if got := lastToolContent(t, h); !strings.Contains(got, "游戏界面") {
		t.Fatalf("view_image 的结果应回给模型，实际 %q", got)
	}
	if got := h.visionCalls(); got != 1 {
		t.Fatalf("应恰好发生一次视觉请求，实际 %d", got)
	}
}

// 引用消息的图片没进顶层 attachments：必须展开 msg_elements 落缓存，并在
// 下一轮通过 view_image 查看。
func TestQuotedImageIsIngestedAndViewableNextTurn(t *testing.T) {
	h := newHarness(t)
	h.dis.HandlePayload(context.Background(), quotedImageMessage("QIMG1", "G1", "U1", "小明", "看看这张引用图", h.imageURL))
	h.waitForSent(t, 1)

	convID := core.EncodeConversationID("group:G1:U1")
	msgs, err := h.st.Messages(context.Background(), convID, 20, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	var attachment store.Attachment
	for _, m := range msgs {
		for _, a := range m.Attachments {
			if a.Kind == "image" && a.MediaID != "" {
				attachment = a
			}
		}
	}
	if attachment.MediaID == "" {
		t.Fatalf("引用消息里的图片应被缓存，实际消息 %#v", msgs)
	}
	first := lastUserContent(t, h)
	want := "[引用图片 #" + attachment.MediaID + "]"
	if !strings.Contains(first, want) {
		t.Fatalf("首轮模型输入应带引用图片标注 %q，实际 %q", want, first)
	}
	if strings.Contains(first, "data:image") {
		t.Fatalf("引用图不应作为本轮多模态输入：%q", first)
	}

	h.setToolScript(fmt.Sprintf(`view_image|{"media_id":%q}`, attachment.MediaID))
	h.dis.HandlePayload(context.Background(), groupAtMessage("QIMG2", "G1", "U1", "小明", "刚才引用那张图里是什么？"))
	if sent := h.waitForSent(t, 2); len(sent) != 2 {
		t.Fatalf("第二轮也应回复，实际 %d 条", len(sent))
	}
	if got := lastToolContent(t, h); !strings.Contains(got, "游戏界面") {
		t.Fatalf("引用图应能通过 view_image 查看，实际 %q", got)
	}
	// 历史消息里也应带上图片 id，模型才知道可以查。
	msgsNow := lastAIMessages(t, h)
	foundHistory := false
	for _, m := range msgsNow {
		if text, _ := m["content"].(string); strings.Contains(text, "[图片 #"+attachment.MediaID+"]") {
			foundHistory = true
		}
	}
	if !foundHistory {
		t.Fatal("历史消息应带图片 id 标注")
	}
}

// 关闭视觉时工具只返回错误文本，本轮回复仍然成功。
func TestViewImageWithoutVisionKeepsTheReply(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"vision_enabled":false}`)
	mediaID, err := h.media.Store.Put(context.Background(), "image/png", tinyPNG())
	if err != nil {
		t.Fatalf("seed media: %v", err)
	}
	h.setToolScript(fmt.Sprintf(`view_image|{"media_id":%q}`, mediaID))

	h.dis.HandlePayload(context.Background(), groupAtMessage("NOVIS1", "G1", "U1", "小明", "看看刚才那张图"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 || !strings.Contains(sent[0].Body.Content, h.aiReply) {
		t.Fatalf("无视觉时回复应正常，实际 %#v", sent)
	}
	if got := lastToolContent(t, h); !strings.Contains(got, "视觉能力未启用") {
		t.Fatalf("工具应返回不可用文本，实际 %q", got)
	}
	if got := h.visionCalls(); got != 0 {
		t.Fatalf("关闭视觉时不应发起视觉请求，实际 %d", got)
	}
}
