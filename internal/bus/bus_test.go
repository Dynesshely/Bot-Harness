package bus

import (
	"sync"
	"testing"
)

// mutable mimics store.Record: its owner keeps writing after Publish returns.
type mutable struct {
	N int
	S []string
}

func (m *mutable) Snapshot() any {
	cp := *m
	cp.S = append([]string(nil), m.S...)
	return &cp
}

func TestPublishSnapshotsMutableData(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe(1)
	defer cancel()

	original := &mutable{N: 1, S: []string{"before"}}
	b.Publish("message", original)

	event := <-ch
	// The publisher keeps using its object; the subscriber must not see this.
	original.N = 2
	original.S[0] = "after"

	snapshot, ok := event.Data.(*mutable)
	if !ok {
		t.Fatalf("published data type = %T, want *mutable", event.Data)
	}
	if snapshot == original {
		t.Fatal("subscriber received the publisher's mutable pointer")
	}
	if snapshot.N != 1 || snapshot.S[0] != "before" {
		t.Fatalf("snapshot changed with the original: %+v", snapshot)
	}
}

func TestPublishPassesPlainValuesThrough(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe(1)
	defer cancel()

	b.Publish("ping", map[string]any{"ok": true})
	event := <-ch
	got, ok := event.Data.(map[string]any)
	if !ok || got["ok"] != true {
		t.Fatalf("plain value was altered: %#v", event.Data)
	}
}

// TestPublishSnapshotUnderConcurrentMutation is meaningful under -race: every
// subscriber encodes its own frozen copy while the publisher mutates the
// source object in place.
func TestPublishSnapshotUnderConcurrentMutation(t *testing.T) {
	b := New()
	const subscribers = 4
	const published = 100
	channels := make([]<-chan Event, 0, subscribers)
	for i := 0; i < subscribers; i++ {
		// Buffer >= published so Publish never drops an event: the slow
		// subscriber drop policy is tested implicitly elsewhere.
		ch, cancel := b.Subscribe(published)
		defer cancel()
		channels = append(channels, ch)
	}

	record := &mutable{N: 0, S: []string{"a"}}
	var wg sync.WaitGroup
	for _, ch := range channels {
		wg.Add(1)
		go func(ch <-chan Event) {
			defer wg.Done()
			for i := 0; i < published; i++ {
				event, ok := <-ch
				if !ok {
					return
				}
				snapshot := event.Data.(*mutable)
				_ = snapshot.S[0]
				_ = snapshot.N
			}
		}(ch)
	}

	for i := 0; i < published; i++ {
		record.N = i
		record.S[0] = "a"
		b.Publish("message", record)
	}
	wg.Wait()
}
