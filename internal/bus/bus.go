// Package bus provides a tiny in-process fan-out for live admin events (SSE).
package bus

import "sync"

// Event is one broadcast item. Name maps to the SSE event name.
type Event struct {
	Name string
	Data any
}

// Snapshotter lets a publisher hand the bus an immutable copy of a value that
// its owner keeps mutating. Publish calls Snapshot synchronously before the
// event reaches any subscriber, so a slow SSE client never races with the
// publisher's later writes.
type Snapshotter interface {
	Snapshot() any
}

// Bus is a non-blocking publish/subscribe hub. Slow subscribers drop events
// rather than stalling producers.
type Bus struct {
	mu     sync.RWMutex
	nextID int
	subs   map[int]chan Event
}

// New creates an empty bus.
func New() *Bus {
	return &Bus{subs: make(map[int]chan Event)}
}

// Subscribe registers a subscriber. The returned cancel function must be
// called to release resources.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan Event, buffer)

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Publish delivers an event to every subscriber, dropping it when a
// subscriber's buffer is full.
func (b *Bus) Publish(name string, data any) {
	// Take the snapshot before broadcasting. Doing this on the caller's
	// goroutine (and before acquiring the lock) means every subscriber sees
	// the same frozen value and producers never block consumers.
	if s, ok := data.(Snapshotter); ok {
		data = s.Snapshot()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- Event{Name: name, Data: data}:
		default:
		}
	}
}
