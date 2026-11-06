package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/render"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// fakeImageQQ records uploads and messages for the command image path. Only the
// routes SendImage touches are implemented.
type fakeImageQQ struct {
	mu          sync.Mutex
	uploadPaths []string
	uploads     []map[string]any
	msgPaths    []string
	messages    []qq.SendMessageRequest
	uploadFail  bool
}

func (f *fakeImageQQ) failUploads(fail bool) {
	f.mu.Lock()
	f.uploadFail = fail
	f.mu.Unlock()
}

func (f *fakeImageQQ) snapshot() ([]string, []map[string]any, []string, []qq.SendMessageRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	paths := append([]string(nil), f.uploadPaths...)
	uploads := append([]map[string]any(nil), f.uploads...)
	msgPaths := append([]string(nil), f.msgPaths...)
	messages := append([]qq.SendMessageRequest(nil), f.messages...)
	return paths, uploads, msgPaths, messages
}

func newFakeImageQQ(t *testing.T) (*fakeImageQQ, *qq.Sender) {
	t.Helper()
	f := &fakeImageQQ{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/getAppAccessToken":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"test-token","expires_in":"7200"}`)
		case strings.HasSuffix(r.URL.Path, "/files"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.uploadPaths = append(f.uploadPaths, r.URL.Path)
			f.uploads = append(f.uploads, body)
			fail := f.uploadFail
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"code":40034001,"message":"upload rejected"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"file_info":"FILEINFO-CORE","ttl":3600}`)
		case strings.Contains(r.URL.Path, "/messages"):
			var body qq.SendMessageRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.msgPaths = append(f.msgPaths, r.URL.Path)
			f.messages = append(f.messages, body)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"MSG-OUT-1","timestamp":"2026-11-04T10:00:00+08:00","ext_info":{"ref_idx":"REFIDX_1"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	client := qq.NewClient(srv.URL, qq.NewTokenManager("app-id", "app-secret", srv.URL, srv.Client()), srv.Client())
	return f, qq.NewSender(client, nil)
}

func newImageTestDispatcher(t *testing.T, sender *qq.Sender) (*Dispatcher, *store.Store, *settings.Manager) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/image.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sm, err := settings.New(context.Background(), st, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	d := New(Options{
		Settings: sm,
		Store:    st,
		Sender:   sender,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	d.Start(context.Background())
	return d, st, sm
}

func addInboundRecord(t *testing.T, st *store.Store, msgID string) *store.Record {
	t.Helper()
	rec := &store.Record{
		Direction: "in", Scene: "group", ConversationID: "conv-1", GroupOpenID: "G1",
		MemberOpenID: "U1", MemberName: "管理员", Content: "/imgtest", MsgID: msgID, Status: "received",
	}
	id, err := st.AddRecord(context.Background(), rec)
	if err != nil {
		t.Fatalf("add record: %v", err)
	}
	rec.ID = id
	return rec
}

func findOutbound(t *testing.T, st *store.Store, msgID string) store.Record {
	t.Helper()
	recs, _, err := st.ListRecords(context.Background(), store.RecordFilter{Direction: "out", Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	for _, rec := range recs {
		if rec.MsgID == msgID {
			return rec
		}
	}
	t.Fatalf("没有 msg_id=%s 的出站记录", msgID)
	return store.Record{}
}

func imagePNG() []byte {
	return []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
}

func imageCommand(out commandOutput) *command {
	return &command{
		name: "imgtest",
		run: func(context.Context, *Dispatcher, *Inbound, settings.Settings, []string) (commandOutput, error) {
			return out, nil
		},
	}
}

func runImageCommand(t *testing.T, d *Dispatcher, st *store.Store, sm *settings.Manager, cmd *command, msgID string) store.Record {
	t.Helper()
	in := &Inbound{
		Scene: "group", AtBot: true, GroupOpenID: "G1", MemberOpenID: "U1",
		MemberName: "管理员", MsgID: msgID,
	}
	conv := convTarget{ID: "conv-1", Scene: "group", GroupOpenID: "G1"}
	d.handleCommand(in, conv, addInboundRecord(t, st, msgID), cmd, nil, sm.Effective())
	return findOutbound(t, st, msgID)
}

// 命令输出图片时走 SendImage（上传 + msg_type=7），出站审计记录的是图片描述。
func TestHandleCommandSendsImageAndAuditsAlt(t *testing.T) {
	f, sender := newFakeImageQQ(t)
	d, st, sm := newImageTestDispatcher(t, sender)
	cmd := imageCommand(commandOutput{
		Text:     "文本回退列表",
		Image:    imagePNG(),
		ImageAlt: "[图片] 聊天对象状态推测（1 人）",
	})

	rec := runImageCommand(t, d, st, sm, cmd, "M-IMG-OK")

	uploadPaths, uploads, msgPaths, messages := f.snapshot()
	if len(uploadPaths) != 1 || uploadPaths[0] != "/v2/groups/G1/files" {
		t.Fatalf("upload paths = %v", uploadPaths)
	}
	if uploads[0]["file_type"].(float64) != 1 || uploads[0]["file_name"] != "status.png" ||
		uploads[0]["srv_send_msg"] != false {
		t.Fatalf("upload body = %#v", uploads[0])
	}
	if len(msgPaths) != 1 || msgPaths[0] != "/v2/groups/G1/messages" {
		t.Fatalf("message paths = %v", msgPaths)
	}
	msg := messages[0]
	if msg.MsgType != qq.MsgTypeMedia || msg.Media == nil || msg.Media.FileInfo != "FILEINFO-CORE" {
		t.Fatalf("media message = %+v", msg)
	}
	if msg.MsgID != "M-IMG-OK" || msg.MsgSeq != 1 {
		t.Fatalf("msg_id/msg_seq = %q/%d", msg.MsgID, msg.MsgSeq)
	}
	if msg.Content != "" {
		t.Fatalf("图片消息不应带文本 content：%q", msg.Content)
	}
	if rec.Status != "replied" || rec.Content != "[图片] 聊天对象状态推测（1 人）" {
		t.Fatalf("出站审计 = %+v", rec)
	}
	if rec.Delivery != store.DeliveryPassive {
		t.Fatalf("delivery = %q, want passive", rec.Delivery)
	}
}

// 图片上传失败必须回退到命令给出的文本，而不是丢消息。
func TestHandleCommandFallsBackToTextWhenImageFails(t *testing.T) {
	f, sender := newFakeImageQQ(t)
	f.failUploads(true)
	d, st, sm := newImageTestDispatcher(t, sender)
	cmd := imageCommand(commandOutput{
		Text:     "最近聊天对象状态（1 人）：\n- 小明｜打游戏",
		Image:    imagePNG(),
		ImageAlt: "[图片] 聊天对象状态推测（1 人）",
	})

	rec := runImageCommand(t, d, st, sm, cmd, "M-IMG-FAIL")

	uploadPaths, _, _, messages := f.snapshot()
	if len(uploadPaths) != 1 {
		t.Fatalf("应尝试过一次上传，实际 %v", uploadPaths)
	}
	if len(messages) != 1 {
		t.Fatalf("回退只应发一条文本消息，实际 %d", len(messages))
	}
	if messages[0].MsgType != qq.MsgTypeText {
		t.Fatalf("回退消息类型 = %d, want text", messages[0].MsgType)
	}
	if !strings.Contains(messages[0].Content, "- 小明｜打游戏") {
		t.Fatalf("回退文本 = %q", messages[0].Content)
	}
	if rec.Status != "replied" || !strings.Contains(rec.Content, "- 小明｜打游戏") {
		t.Fatalf("出站审计应记录回退文本：%+v", rec)
	}
}

// 图片失败且命令没有文本时，发一句可理解的失败提示。
func TestHandleCommandImageFailureWithoutTextSendsNotice(t *testing.T) {
	f, sender := newFakeImageQQ(t)
	f.failUploads(true)
	d, st, sm := newImageTestDispatcher(t, sender)
	cmd := imageCommand(commandOutput{Image: imagePNG(), ImageAlt: "[图片] 空"})

	runImageCommand(t, d, st, sm, cmd, "M-IMG-EMPTY")

	_, _, _, messages := f.snapshot()
	if len(messages) != 1 || messages[0].Content != "图片发送失败" {
		t.Fatalf("fallback messages = %+v", messages)
	}
}

// fakeHumanStatusAI returns one fixed JSON array for the inference call.
func fakeHumanStatusAI(t *testing.T, verdict string) *ai.Client {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"message":       map[string]string{"content": verdict},
			"finish_reason": "stop",
		}},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	return ai.NewClient(srv.URL, "sk-test", srv.Client())
}

func newHumanStatusImageDispatcher(t *testing.T, verdict string) (*Dispatcher, settings.Settings) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/hs-image.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sm, err := settings.New(context.Background(), st, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	d := &Dispatcher{
		settings: sm,
		st:       st,
		ai:       fakeHumanStatusAI(t, verdict),
		aiSem:    newDynamicSem(4),
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	in := &Inbound{Scene: "group", GroupOpenID: "G1", MemberOpenID: "U1", MemberName: "小明"}
	conv := resolveConversation(in, sm.Effective().ContextMode)
	if err := st.AppendMessage(context.Background(), store.MessageInput{
		ConversationID: conv.ID, Role: "user", Kind: store.KindDialogue,
		Content: "今晚继续开黑", Speaker: "小明", SpeakerOpenID: "U1",
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	// 线上实际值：markdown 关闭 → 纯文本列表。图片与文本的断言都按这条路径。
	s := sm.Effective()
	s.MarkdownEnabled = false
	return d, s
}

// /human-status 成功时必须同时返回图片与文本回退，并把表格投影交给渲染器。
func TestRunHumanStatusProducesImageAndText(t *testing.T) {
	verdict := `[{"member_openid":"U1","display_name":"小明","status":"打游戏","confidence":0.82,"evidence":"刚说在开黑，今晚继续"}]`
	d, s := newHumanStatusImageDispatcher(t, verdict)

	var got render.Table
	orig := renderTablePNG
	renderTablePNG = func(tbl render.Table) ([]byte, error) {
		got = tbl
		return orig(tbl)
	}
	t.Cleanup(func() { renderTablePNG = orig })

	out, err := runHumanStatus(context.Background(), d, &Inbound{
		Scene: "group", GroupOpenID: "G1", MemberOpenID: "U1", MemberName: "小明",
	}, s, nil)
	if err != nil {
		t.Fatalf("runHumanStatus: %v", err)
	}
	if out.Image == nil {
		t.Fatal("成功的 /human-status 应返回图片")
	}
	if _, format, err := image.Decode(bytes.NewReader(out.Image)); err != nil || format != "png" {
		t.Fatalf("图片不是 PNG: format=%q err=%v", format, err)
	}
	if out.ImageAlt != "[图片] 聊天对象状态推测（1 人）" {
		t.Fatalf("ImageAlt = %q", out.ImageAlt)
	}
	if !strings.Contains(out.Text, "- 小明｜打游戏") || !strings.Contains(out.Text, "最近聊天对象状态（1 人）：") {
		t.Fatalf("文本回退 = %q", out.Text)
	}

	if got.Title != "聊天对象状态推测（1 人）" {
		t.Fatalf("表格标题 = %q", got.Title)
	}
	wantHeaders := []string{"对象", "状态", "置信度", "依据", "更新"}
	if len(got.Headers) != len(wantHeaders) {
		t.Fatalf("headers = %v", got.Headers)
	}
	for i := range wantHeaders {
		if got.Headers[i] != wantHeaders[i] {
			t.Fatalf("headers = %v, want %v", got.Headers, wantHeaders)
		}
	}
	if len(got.Rows) != 1 || got.Rows[0][0] != "小明" || got.Rows[0][1] != "打游戏" || got.Rows[0][2] != "82%" {
		t.Fatalf("rows = %v", got.Rows)
	}
}

// 渲染失败只降到文本，命令仍然成功。
func TestRunHumanStatusRenderFailureFallsBackToText(t *testing.T) {
	verdict := `[{"member_openid":"U1","display_name":"小明","status":"工作","confidence":0.65,"evidence":"在赶周报"}]`
	d, s := newHumanStatusImageDispatcher(t, verdict)

	orig := renderTablePNG
	renderTablePNG = func(render.Table) ([]byte, error) { return nil, errors.New("render exploded") }
	t.Cleanup(func() { renderTablePNG = orig })

	out, err := runHumanStatus(context.Background(), d, &Inbound{
		Scene: "group", GroupOpenID: "G1", MemberOpenID: "U1", MemberName: "小明",
	}, s, nil)
	if err != nil {
		t.Fatalf("渲染失败不应让命令失败: %v", err)
	}
	if out.Image != nil {
		t.Fatal("渲染失败时不应返回图片")
	}
	if !strings.Contains(out.Text, "- 小明｜工作") || !strings.Contains(out.Text, "65%") {
		t.Fatalf("回退文本 = %q", out.Text)
	}
}
