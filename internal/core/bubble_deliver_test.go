package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
)

// recordingBubbleQQ is a minimal fake platform: it records outbound messages and
// can start failing from a chosen call number, which is how "stop after the
// first bubble" becomes observable at the deliver() level.
type recordingBubbleQQ struct {
	mu       sync.Mutex
	messages []qq.SendMessageRequest
	failFrom int
}

func (r *recordingBubbleQQ) snapshot() []qq.SendMessageRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]qq.SendMessageRequest, len(r.messages))
	copy(out, r.messages)
	return out
}

func newBubbleSender(t *testing.T, failFrom int) (*recordingBubbleQQ, *qq.Sender) {
	t.Helper()
	p := &recordingBubbleQQ{failFrom: failFrom}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/app/getAppAccessToken":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"test-token","expires_in":"7200"}`)
		case strings.Contains(req.URL.Path, "/messages"):
			var body qq.SendMessageRequest
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode body: %v", err)
			}
			p.mu.Lock()
			p.messages = append(p.messages, body)
			n := len(p.messages)
			fail := p.failFrom
			p.mu.Unlock()
			if fail > 0 && n >= fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"code":500,"message":"boom"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"MSG-OUT-1","timestamp":"2026-11-11T10:00:00+08:00","ext_info":{"ref_idx":"REFIDX_OUT"}}`)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	client := qq.NewClient(srv.URL, qq.NewTokenManager("app-id", "app-secret", srv.URL, srv.Client()), srv.Client())
	return p, qq.NewSender(client, nil)
}

// deliverSettings is a humanize-on baseline whose segment behaviour is pinned by
// the split roll seam, so a test does not depend on math/rand.
func deliverSettings(split string) settings.Settings {
	s := settings.Defaults()
	s.HumanizeLevel = HumanizeMedium
	s.HumanizeDelayScale = 1
	s.HumanizeSegmentSplit = split
	s.HumanizeTypingSecondsPerChar = 0.3
	s.HumanizeTypingMaxSeconds = 12
	s.ReplyMaxChars = 1200
	s.ReplyMaxSegments = 3
	s.MarkdownEnabled = false
	return s
}

func newDeliverDispatcher(t *testing.T, sender *qq.Sender) (*Dispatcher, *settings.Manager) {
	t.Helper()
	d, _, sm := newImageTestDispatcher(t, sender)
	return d, sm
}

// humanize off：一条消息、零逐段等待，完全保持旧行为。
func TestDeliverOffSendsOneMessageWithoutTypingWait(t *testing.T) {
	f, sender := newBubbleSender(t, 0)
	d, _ := newDeliverDispatcher(t, sender)
	s := deliverSettings(HumanizeOff)
	s.HumanizeLevel = HumanizeOff
	text := "第一句话讲背景。第二句话讲原因。第三句话给结论。第四句话补充说明。"

	in := &Inbound{Scene: "group", AtBot: true, GroupOpenID: "G1", MsgID: "MSG-IN"}
	start := time.Now()
	res, err := d.deliver(context.Background(), in, s, text, false, "", HumanizeOff, time.Now())
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("off 不该有打字等待，实际 %v", elapsed)
	}
	if res == nil || res.Segments != 1 || len(res.MessageIDs) != 1 {
		t.Fatalf("off 应只发 1 条：%#v", res)
	}
	msgs := f.snapshot()
	if len(msgs) != 1 || msgs[0].Content != text {
		t.Fatalf("off 应原样发整段：%#v", msgs)
	}
	if msgs[0].MessageRef != nil {
		t.Fatal("没有引用索引时不该带 message_reference")
	}
}

// 长文本：拆成 2–3 条，引用只挂在第一条上。
func TestDeliverSplitsLongReplyAndQuotesTheFirstBubble(t *testing.T) {
	f, sender := newBubbleSender(t, 0)
	d, _ := newDeliverDispatcher(t, sender)
	d.SetSplitRoll(func() float64 { return 0 })
	s := deliverSettings("strong")
	s.HumanizeTypingSecondsPerChar = 0.05
	s.HumanizeTypingMaxSeconds = 1
	text := "第一句话讲背景。第二句话讲原因。第三句话给结论。第四句话补充说明。"

	in := &Inbound{Scene: "group", AtBot: true, GroupOpenID: "G1", MsgID: "MSG-IN"}
	res, err := d.deliver(context.Background(), in, s, text, false, "REFIDX_IN", HumanizeMedium, time.Now())
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if res == nil || res.Segments < 2 || res.Segments > 3 {
		t.Fatalf("应拆成 2–3 条：%#v", res)
	}
	msgs := f.snapshot()
	if len(msgs) != res.Segments {
		t.Fatalf("平台收到 %d 条，结果是 %d 段", len(msgs), res.Segments)
	}
	if msgs[0].MessageRef == nil || msgs[0].MessageRef.MessageID != "REFIDX_IN" {
		t.Fatalf("引用只该挂在第一条：%#v", msgs[0].MessageRef)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].MessageRef != nil {
			t.Fatalf("第 %d 条不该再引用：%#v", i+1, msgs[i].MessageRef)
		}
	}
	var joined string
	for _, m := range msgs {
		joined += m.Content
	}
	if joined != text {
		t.Fatalf("拆分丢了内容：%q vs %q", joined, text)
	}
}

// 逐段等待总量不超过上限；已耗时超过预算时归零。
func TestDeliverTypingWaitStaysWithinBudget(t *testing.T) {
	_, sender := newBubbleSender(t, 0)
	d, _ := newDeliverDispatcher(t, sender)
	d.SetSplitRoll(func() float64 { return 0 })
	s := deliverSettings("strong")
	s.HumanizeTypingSecondsPerChar = 0.5
	s.HumanizeTypingMaxSeconds = 1 // 1s 上限，远小于按字数算出来的时间
	text := "第一句话讲背景。第二句话讲原因。第三句话给结论。第四句话补充说明。"

	in := &Inbound{Scene: "group", AtBot: true, GroupOpenID: "G1", MsgID: "MSG-IN"}
	start := time.Now()
	if _, err := d.deliver(context.Background(), in, s, text, false, "", HumanizeMedium, time.Now()); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	// humanize 的读题停顿 + 1s 打字预算；这里只验证不会远远超预算。
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Fatalf("逐段等待超预算：%v", elapsed)
	}

	waits := bubbleWaits([]string{"第一段内容", "第二段内容"}, 0.5, 1,
		bubblePauseBudget(s), 5*time.Second)
	for _, w := range waits {
		if w != 0 {
			t.Fatalf("已耗时超过预算时不该等待：%v", w)
		}
	}
}

// 第二条失败：停下，并把「已发一条」如实带回，调用方据此记 failed。
func TestDeliverStopsWhenALaterBubbleFails(t *testing.T) {
	p, sender := newBubbleSender(t, 2)
	d, _ := newDeliverDispatcher(t, sender)
	d.SetSplitRoll(func() float64 { return 0 })
	s := deliverSettings("strong")
	s.HumanizeTypingSecondsPerChar = 0.05
	text := "第一句话讲背景。第二句话讲原因。第三句话给结论。第四句话补充说明。"

	in := &Inbound{Scene: "group", AtBot: true, GroupOpenID: "G1", MsgID: "MSG-IN"}
	res, err := d.deliver(context.Background(), in, s, text, false, "", HumanizeMedium, time.Now())
	if err == nil {
		t.Fatal("第二条失败时应返回错误")
	}
	if res == nil || len(res.MessageIDs) != 1 {
		t.Fatalf("应报告 1 条已送达：%#v", res)
	}
	if got := len(p.snapshot()); got != 2 {
		t.Fatalf("失败后不该继续发：平台收到 %d 条", got)
	}
}

// ctx 取消：不继续下一条。
func TestDeliverStopsWhenContextCancelled(t *testing.T) {
	_, sender := newBubbleSender(t, 0)
	d, _ := newDeliverDispatcher(t, sender)
	d.SetSplitRoll(func() float64 { return 0 })
	s := deliverSettings("strong")
	s.HumanizeTypingSecondsPerChar = 0.5
	s.HumanizeTypingMaxSeconds = 12
	text := "第一句话讲背景。第二句话讲原因。第三句话给结论。第四句话补充说明。"

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(60 * time.Millisecond)
		cancel()
	}()

	in := &Inbound{Scene: "group", AtBot: true, GroupOpenID: "G1", MsgID: "MSG-IN"}
	_, err := d.deliver(ctx, in, s, text, false, "", HumanizeMedium, time.Now())
	if err == nil {
		t.Fatal("ctx 取消后应返回错误")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("错误应可判定为 Canceled：%v", err)
	}
}

// 半截回复的形状：已送达的 id 必须保留，且能被调用方识别。
func TestSendResultKeepsPartialDelivery(t *testing.T) {
	res := &qq.SendResult{Segments: 3, MessageIDs: []string{"m1"}}
	if !sentAny(res) {
		t.Fatal("已有一条送达时应判定为「已发送部分」")
	}
	if sentAny(&qq.SendResult{Segments: 3}) {
		t.Fatal("一条都没送达时不该判定为部分成功")
	}
	if sentAny(nil) {
		t.Fatal("nil 不算已发送")
	}
}
