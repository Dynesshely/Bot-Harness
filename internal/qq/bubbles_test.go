package qq

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// bubblePlatform records one outbound message per platform call and can fail a
// chosen call, which is what makes "stop after the first bubble" observable.
type bubblePlatform struct {
	mu       sync.Mutex
	messages []SendMessageRequest
	paths    []string
	// failFrom is the 1-based call number at which the platform starts answering
	// 500. 0 = never fail.
	failFrom int
	// onSend, when set, runs inside the handler (used to cancel a context).
	onSend func()
}

func (p *bubblePlatform) snapshot() []SendMessageRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]SendMessageRequest, len(p.messages))
	copy(out, p.messages)
	return out
}

func (p *bubblePlatform) setFailFrom(n int) {
	p.mu.Lock()
	p.failFrom = n
	p.mu.Unlock()
}

func newBubblePlatform(t *testing.T) (*bubblePlatform, *Sender) {
	t.Helper()
	p := &bubblePlatform{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/getAppAccessToken":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"test-token","expires_in":"7200"}`)
		case strings.Contains(r.URL.Path, "/messages"):
			var body SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode message body: %v", err)
			}
			p.mu.Lock()
			p.messages = append(p.messages, body)
			p.paths = append(p.paths, r.URL.Path)
			n := len(p.messages)
			fail := p.failFrom
			hook := p.onSend
			p.mu.Unlock()
			if hook != nil {
				hook()
			}
			if fail > 0 && n >= fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"code":500,"message":"boom"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"MSG-OUT-1","timestamp":"2026-11-11T10:00:00+08:00","ext_info":{"ref_idx":"REFIDX_OUT"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL, NewTokenManager("app-id", "app-secret", srv.URL, srv.Client()), srv.Client())
	return p, NewSender(client, nil)
}

// 预拆分的气泡各发一条消息，引用只挂在第一条上。
func TestSendBubblesQuotesOnlyTheFirstSegment(t *testing.T) {
	p, sender := newBubblePlatform(t)
	res, err := sender.Send(context.Background(), SendOptions{
		GroupOpenID:    "G1",
		MsgID:          "MSG-IN",
		QuoteMessageID: "REFIDX_IN",
		MaxChars:       200,
		Bubbles:        []string{"第一段。", "第二段。", "第三段。"},
	}, "第一段。第二段。第三段。")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res.Segments != 3 || len(res.MessageIDs) != 3 {
		t.Fatalf("应发出 3 条气泡：segments=%d ids=%d", res.Segments, len(res.MessageIDs))
	}
	msgs := p.snapshot()
	if len(msgs) != 3 {
		t.Fatalf("平台收到 %d 条消息", len(msgs))
	}
	if msgs[0].MessageRef == nil || msgs[0].MessageRef.MessageID != "REFIDX_IN" {
		t.Fatalf("引用应挂在第一条：%#v", msgs[0].MessageRef)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].MessageRef != nil {
			t.Fatalf("第 %d 条不该再引用：%#v", i+1, msgs[i].MessageRef)
		}
	}
	for i, m := range msgs {
		if m.MsgSeq != i+1 {
			t.Fatalf("msg_seq 应递增：第 %d 条是 %d", i+1, m.MsgSeq)
		}
		if m.MsgID != "MSG-IN" {
			t.Fatalf("被动回复应带 msg_id：%#v", m)
		}
	}
}

// 单个气泡超过 MaxChars 时仍会被 SplitText 再切，继承既有行为。
func TestSendBubblesStillRespectsMaxChars(t *testing.T) {
	p, sender := newBubblePlatform(t)
	long := strings.Repeat("很长的内容。", 20)
	res, err := sender.Send(context.Background(), SendOptions{
		GroupOpenID: "G1",
		MsgID:       "MSG-IN",
		MaxChars:    60,
		MaxSegments: 5,
		Bubbles:     []string{long},
	}, long)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	msgs := p.snapshot()
	if len(msgs) < 2 {
		t.Fatalf("超长气泡应被再切：%d 条", len(msgs))
	}
	if len(msgs) > 5 {
		t.Fatalf("被动回复仍受 5 条上限约束：%d 条", len(msgs))
	}
	for _, m := range msgs {
		if n := runeLen(m.Content); n > 60 {
			t.Fatalf("单条超过 MaxChars：%d", n)
		}
	}
	if res.Truncated {
		t.Fatal("内容装得下时不该标记截断")
	}
}

// 后续气泡发送失败时立刻停下，并把「已经发出去的那部分」如实返回。
func TestSendBubblesStopsOnPlatformError(t *testing.T) {
	p, sender := newBubblePlatform(t)
	p.setFailFrom(2)
	res, err := sender.Send(context.Background(), SendOptions{
		GroupOpenID: "G1",
		MsgID:       "MSG-IN",
		MaxChars:    200,
		Bubbles:     []string{"第一段。", "第二段。", "第三段。"},
	}, "第一段。第二段。第三段。")
	if err == nil {
		t.Fatal("第二条失败时应返回错误")
	}
	if res == nil || len(res.MessageIDs) != 1 || res.Segments != 3 {
		t.Fatalf("应报告 1 条已送达（总段数仍是 3）：%#v", res)
	}
	if got := len(p.snapshot()); got != 2 {
		t.Fatalf("失败后不该继续发第 3 条，平台收到 %d 条", got)
	}
}

// ctx 取消：停在气泡之间，不把剩下的发出去。
func TestSendBubblesStopsWhenContextCancelled(t *testing.T) {
	p, sender := newBubblePlatform(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 第一条已送达，第二条前的等待被取消：这正是「半截回复」要停下的位置。
	res, err := sender.Send(ctx, SendOptions{
		GroupOpenID: "G1",
		MsgID:       "MSG-IN",
		MaxChars:    200,
		Bubbles:     []string{"第一段。", "第二段。", "第三段。"},
		BubblePause: func(index int, _ string) time.Duration {
			cancel()
			return 20 * time.Millisecond
		},
	}, "第一段。第二段。第三段。")
	if err == nil {
		t.Fatal("ctx 取消后应返回错误")
	}
	if res == nil || len(res.MessageIDs) != 1 {
		t.Fatalf("取消后应只报告已送达的那一条：%#v", res)
	}
	if got := len(p.snapshot()); got != 1 {
		t.Fatalf("取消后不该继续发送，平台收到 %d 条", got)
	}
}

// BubblePause 的等待确实发生在气泡之间，而不是全部开头一次等完。
func TestSendBubblesWaitsBetweenSegments(t *testing.T) {
	_, sender := newBubblePlatform(t)
	start := time.Now()
	_, err := sender.Send(context.Background(), SendOptions{
		GroupOpenID: "G1",
		MsgID:       "MSG-IN",
		MaxChars:    200,
		Bubbles:     []string{"第一段。", "第二段。", "第三段。"},
		BubblePause: func(index int, text string) time.Duration {
			if index == 0 {
				return 50 * time.Millisecond
			}
			return 20 * time.Millisecond
		},
	}, "第一段。第二段。第三段。")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("两段等待合计约 70ms，实际只用 %v", elapsed)
	}
}
