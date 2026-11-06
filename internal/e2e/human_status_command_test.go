package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/store"
)

var pngHeader = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// assertImageUpload pins the /files wire shape: one image, base64 PNG bytes,
// srv_send_msg=false (the media message is sent separately).
func assertImageUpload(t *testing.T, h *harness, wantPath string) {
	t.Helper()
	uploads := h.sentUploads()
	if len(uploads) != 1 {
		t.Fatalf("uploads = %d, want 1: %#v", len(uploads), uploads)
	}
	if uploads[0].Path != wantPath {
		t.Fatalf("upload path = %q, want %q", uploads[0].Path, wantPath)
	}
	body := uploads[0].Body
	if body["file_type"] != float64(1) {
		t.Fatalf("file_type = %#v, want 1", body["file_type"])
	}
	if body["file_name"] != "status.png" {
		t.Fatalf("file_name = %#v, want status.png", body["file_name"])
	}
	if body["srv_send_msg"] != false {
		t.Fatalf("srv_send_msg = %#v, want false", body["srv_send_msg"])
	}
	data, _ := body["file_data"].(string)
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatalf("file_data is not base64: %v", err)
	}
	if !bytes.HasPrefix(raw, pngHeader) {
		t.Fatalf("file_data does not decode to a PNG: % x", raw[:min(8, len(raw))])
	}
}

// assertMediaMessage pins the msg_type=7 message: media carries file_info, the
// reply is passive and bound to the original msg_id with msg_seq=1.
func assertMediaMessage(t *testing.T, sent []sentMessage, msgID string) {
	t.Helper()
	if len(sent) != 1 {
		t.Fatalf("应只有一条图片消息，实际 %d 条: %#v", len(sent), sent)
	}
	msg := sent[0]
	if msg.Body.MsgType != qq.MsgTypeMedia {
		t.Fatalf("msg_type = %d, want 7", msg.Body.MsgType)
	}
	if msg.Body.Media == nil || msg.Body.Media.FileInfo != "FILEINFO-E2E" {
		t.Fatalf("media = %+v, want file_info FILEINFO-E2E", msg.Body.Media)
	}
	if msg.Body.Content != "" {
		t.Fatalf("图片消息不应带文本 content：%q", msg.Body.Content)
	}
	if msg.Body.MsgID != msgID || msg.Body.MsgSeq != 1 {
		t.Fatalf("msg_id/msg_seq = %q/%d, want %s/1", msg.Body.MsgID, msg.Body.MsgSeq, msgID)
	}
	if msg.Body.Markdown != nil {
		t.Fatalf("图片消息不应带 markdown：%+v", msg.Body.Markdown)
	}
}

// 管理员发 /human-status 时必须立刻跑一次推测并把新结果渲染成图片，而不是
// 只读缓存、回「暂无足够消息用于推测」。文本列表仍随输出返回作为回退，但
// 成功路径上只发图片消息。
func TestHumanStatusCommandRunsFreshInference(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"admin_openids":["U1"],"markdown_enabled":false}`)
	convID := core.EncodeConversationID("group:G1:U1")
	if err := h.st.AppendMessage(ctx, store.MessageInput{
		ConversationID: convID, Role: "user", Kind: store.KindDialogue,
		Content: "今晚继续开黑", Speaker: "小明", SpeakerOpenID: "U1",
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	h.mu.Lock()
	h.humanStatusReply = `[{"member_openid":"U1","display_name":"小明","status":"打游戏","confidence":0.82,"evidence":"刚说在开黑"}]`
	h.humanStatusCalls = 0
	h.mu.Unlock()

	h.dis.HandlePayload(ctx, groupAtMessage("M-HS-NOW", "G1", "U1", "小明", "/human-status"))
	sent := h.waitForSent(t, 1)
	assertMediaMessage(t, sent, "M-HS-NOW")
	assertImageUpload(t, h, "/v2/groups/G1/files")

	// 出站审计记录图片描述，而不是二进制或文本列表。
	outRec := waitForOutboundRecord(t, h, "M-HS-NOW")
	if outRec.Status != "replied" {
		t.Fatalf("出站状态 = %q, want replied", outRec.Status)
	}
	if outRec.Content != "[图片] 聊天对象状态推测（1 人）" {
		t.Fatalf("出站审计 Content = %q", outRec.Content)
	}
	if strings.Contains(outRec.Content, "｜") {
		t.Fatalf("成功出图时不应把文本列表写进审计：%q", outRec.Content)
	}

	h.mu.Lock()
	calls := h.humanStatusCalls
	h.mu.Unlock()
	if calls != 1 {
		t.Fatalf("human status AI calls = %d, want 1", calls)
	}
	if got := h.dis.Metrics().Snapshot().HumanStatusRuns; got != 1 {
		t.Fatalf("human_status_runs = %d, want 1", got)
	}
}

// 缓存为空且模型失败时，管理员必须看到「分析失败」，而且不能把底层错误
// （DSN / key / 上游原文）带进回复；没有数据就没有图片。
func TestHumanStatusCommandFailureWithoutCache(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"admin_openids":["U1"],"markdown_enabled":false}`)
	convID := core.EncodeConversationID("group:G1:U1")
	if err := h.st.AppendMessage(ctx, store.MessageInput{
		ConversationID: convID, Role: "user", Kind: store.KindDialogue,
		Content: "随便聊聊", Speaker: "小明", SpeakerOpenID: "U1",
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	h.mu.Lock()
	h.humanStatusReply = "这不是 JSON postgres://user:secret@db/bot"
	h.mu.Unlock()

	h.dis.HandlePayload(ctx, groupAtMessage("M-HS-FAIL", "G1", "U1", "小明", "/human-status"))
	sent := h.waitForSent(t, 1)
	if len(sent) < 1 {
		t.Fatal("command reply not delivered")
	}
	reply := sent[0].Body.Content
	if !strings.Contains(reply, "分析失败") {
		t.Fatalf("reply = %q, want 分析失败", reply)
	}
	for _, leak := range []string{"postgres", "secret", "这不是 JSON"} {
		if strings.Contains(reply, leak) {
			t.Fatalf("reply leaked %q: %q", leak, reply)
		}
	}
	if strings.Contains(reply, "暂无足够消息") {
		t.Fatalf("failure must not reuse the old no-data copy: %q", reply)
	}
	// 非 JSON 的回复会追加一条合约消息重试一次；两次都失败才回「分析失败」。
	h.mu.Lock()
	calls := h.humanStatusCalls
	h.mu.Unlock()
	if calls != 2 {
		t.Fatalf("human status AI calls = %d, want 2 (one retry)", calls)
	}
	if uploads := h.sentUploads(); len(uploads) != 0 {
		t.Fatalf("没有数据时不应上传图片: %#v", uploads)
	}
}

// 有未过期缓存时，模型失败退化为缓存表格（图片 + 文本回退）：用户仍能看到
// 状态，不被错误打扰。
func TestHumanStatusCommandFailureFallsBackToCache(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"admin_openids":["U1"],"markdown_enabled":false}`)
	convID := core.EncodeConversationID("group:G1:U1")
	if err := h.st.AppendMessage(ctx, store.MessageInput{
		ConversationID: convID, Role: "user", Kind: store.KindDialogue,
		Content: "在赶周报", Speaker: "小明", SpeakerOpenID: "U1",
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := h.st.UpsertHumanStatus(ctx, store.HumanStatusInput{
		Scope: store.HumanStatusGroup, ConversationID: convID, GroupOpenID: "G1",
		MemberOpenID: "U1", DisplayName: "小明", Status: "工作",
		Confidence: 0.65, Evidence: "在赶周报",
		UpdatedAt: time.Now().Format(time.RFC3339),
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	h.mu.Lock()
	h.humanStatusReply = "这不是 JSON"
	h.mu.Unlock()

	h.dis.HandlePayload(ctx, groupAtMessage("M-HS-CACHE", "G1", "U1", "小明", "/human-status"))
	sent := h.waitForSent(t, 1)
	assertMediaMessage(t, sent, "M-HS-CACHE")
	assertImageUpload(t, h, "/v2/groups/G1/files")

	outRec := waitForOutboundRecord(t, h, "M-HS-CACHE")
	if outRec.Content != "[图片] 聊天对象状态推测（1 人）" {
		t.Fatalf("克隆缓存出图时审计 Content = %q", outRec.Content)
	}
	if strings.Contains(outRec.Content, "分析失败") || strings.Contains(outRec.Content, "暂无足够消息") {
		t.Fatalf("failure with a live cache must stay quiet: %q", outRec.Content)
	}
	if got := h.dis.Metrics().Snapshot().HumanStatusFailures; got < 1 {
		t.Fatalf("human_status_failures = %d, want at least 1", got)
	}
}

// 图片上传失败时，/human-status 的文本列表仍必须送达（图片只是增强，
// 不能因为平台拒绝上传就把答案丢掉）。
func TestHumanStatusImageUploadFailureFallsBackToText(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"admin_openids":["U1"],"markdown_enabled":false}`)
	convID := core.EncodeConversationID("group:G1:U1")
	if err := h.st.AppendMessage(ctx, store.MessageInput{
		ConversationID: convID, Role: "user", Kind: store.KindDialogue,
		Content: "今晚继续开黑", Speaker: "小明", SpeakerOpenID: "U1",
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	h.mu.Lock()
	h.humanStatusReply = `[{"member_openid":"U1","display_name":"小明","status":"打游戏","confidence":0.82,"evidence":"刚说在开黑"}]`
	h.mu.Unlock()
	h.failUploads(true)

	h.dis.HandlePayload(ctx, groupAtMessage("M-HS-UPLOADFAIL", "G1", "U1", "小明", "/human-status"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("应只发一条文本回退，实际 %d 条: %#v", len(sent), sent)
	}
	if sent[0].Body.MsgType == qq.MsgTypeMedia {
		t.Fatalf("上传失败后不应再发图片消息: %#v", sent[0].Body)
	}
	reply := sent[0].Body.Content
	for _, want := range []string{"最近聊天对象状态（1 人）：", "- ", "｜", "小明", "打游戏", "82%"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("text fallback missing %q: %q", want, reply)
		}
	}
	if uploads := h.sentUploads(); len(uploads) != 1 {
		t.Fatalf("应尝试过一次上传，实际 %d 次", len(uploads))
	}
	outRec := waitForOutboundRecord(t, h, "M-HS-UPLOADFAIL")
	if !strings.Contains(outRec.Content, "- 小明｜打游戏") {
		t.Fatalf("回退出站审计应记录文本列表：%q", outRec.Content)
	}
}

// /human-status 的会话里带图片时必须走通工具链：模型第一轮要求 analyze_image，
// 视觉模型拿到图片后返回描述，第二轮模型才输出 JSON；命令仍按原样发图片/文本。
func TestHumanStatusCommandUsesImageTool(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, `{"admin_openids":["U1"],"markdown_enabled":false}`)
	convID := core.EncodeConversationID("group:G1:U1")

	mediaID, err := h.media.Store.Put(ctx, "image/png", pngHeader)
	if err != nil {
		t.Fatalf("seed media: %v", err)
	}
	if err := h.st.AppendMessage(ctx, store.MessageInput{
		ConversationID: convID, Role: "user", Kind: store.KindDialogue,
		Content: "看这个界面", Speaker: "小明", SpeakerOpenID: "U1",
		Attachments: []store.Attachment{{Kind: "image", MediaID: mediaID, ContentType: "image/png"}},
	}); err != nil {
		t.Fatalf("seed image message: %v", err)
	}
	h.mu.Lock()
	h.humanStatusReply = `[{"member_openid":"U1","display_name":"小明","status":"打游戏","confidence":0.82,"evidence":"画面是游戏界面"}]`
	h.humanStatusImageTool = true
	h.humanStatusImageID = mediaID
	h.humanStatusVision = "画面是电脑上的游戏界面。"
	h.humanStatusCalls = 0
	h.humanStatusVisionCalls = 0
	h.mu.Unlock()

	h.dis.HandlePayload(ctx, groupAtMessage("M-HS-IMAGE", "G1", "U1", "小明", "/human-status"))
	sent := h.waitForSent(t, 1)
	assertMediaMessage(t, sent, "M-HS-IMAGE")
	assertImageUpload(t, h, "/v2/groups/G1/files")

	outRec := waitForOutboundRecord(t, h, "M-HS-IMAGE")
	if outRec.Content != "[图片] 聊天对象状态推测（1 人）" {
		t.Fatalf("出站审计 Content = %q", outRec.Content)
	}
	h.mu.Lock()
	calls := h.humanStatusCalls
	visionCalls := h.humanStatusVisionCalls
	h.mu.Unlock()
	if calls != 2 {
		t.Fatalf("human status AI calls = %d, want 2 (tool round + final)", calls)
	}
	if visionCalls != 1 {
		t.Fatalf("vision calls = %d, want 1", visionCalls)
	}
	if got := h.dis.Metrics().Snapshot().HumanStatusImageCalls; got != 1 {
		t.Fatalf("human_status_image_calls = %d, want 1", got)
	}
}
