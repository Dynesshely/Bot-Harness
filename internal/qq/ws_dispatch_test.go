package qq

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func newDispatchTestClient(t *testing.T) (*WSClient, chan string) {
	t.Helper()
	got := make(chan string, 16)
	w := &WSClient{
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		dispatch: make([]chan *Payload, wsDispatchShards),
		handler: func(_ context.Context, p *Payload) {
			var d struct {
				Content string `json:"content"`
			}
			_ = json.Unmarshal(p.D, &d)
			got <- d.Content
		},
	}
	for i := range w.dispatch {
		w.dispatch[i] = make(chan *Payload, wsDispatchQueue)
	}
	w.startDispatch(context.Background())
	return w, got
}

func wsTestPayload(id, group, content string) *Payload {
	d, _ := json.Marshal(map[string]string{"group_openid": group, "content": content})
	return &Payload{Op: OpDispatch, T: "GROUP_MESSAGE_CREATE", ID: id, D: d}
}

func TestWSDispatchPreservesGroupOrder(t *testing.T) {
	w, got := newDispatchTestClient(t)
	for _, s := range []string{"一", "二", "三"} {
		w.handleDispatch(context.Background(), wsTestPayload("m"+s, "G1", s), false)
	}
	for _, want := range []string{"一", "二", "三"} {
		select {
		case g := <-got:
			if g != want {
				t.Fatalf("order = %q, want %q", g, want)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for dispatch")
		}
	}
}

func TestWSDispatchDoesNotBlockReadLoop(t *testing.T) {
	w := &WSClient{log: slog.New(slog.NewTextHandler(io.Discard, nil)), dispatch: make([]chan *Payload, wsDispatchShards)}
	block := make(chan struct{})
	var once sync.Once
	w.handler = func(context.Context, *Payload) { <-block; once.Do(func() { close(block) }) }
	for i := range w.dispatch {
		w.dispatch[i] = make(chan *Payload, wsDispatchQueue)
	}
	w.startDispatch(context.Background())
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			w.handleDispatch(context.Background(), wsTestPayload("m", "G1", "x"), false)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handleDispatch blocked on a slow handler")
	}
}

func TestWSDispatchKeySeparatesGroups(t *testing.T) {
	a := dispatchKey(wsTestPayload("m", "G1", "x"))
	b := dispatchKey(wsTestPayload("m", "G2", "x"))
	if a == b || a != "g:G1" || b != "g:G2" {
		t.Fatalf("keys = %q %q", a, b)
	}
}
