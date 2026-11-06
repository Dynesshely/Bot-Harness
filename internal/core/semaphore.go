package core

import (
	"context"
	"sync"
	"time"
)

// dynamicSem is a resizable counting semaphore. Waiters wake on release or on
// a limit change, and always honour context cancellation.
type dynamicSem struct {
	mu     sync.Mutex
	limit  int
	inUse  int
	waitCh chan struct{}
}

func newDynamicSem(limit int) *dynamicSem {
	if limit < 1 {
		limit = 1
	}
	return &dynamicSem{limit: limit, waitCh: make(chan struct{})}
}

// SetLimit changes the concurrency ceiling. Growing the limit wakes waiters.
func (s *dynamicSem) SetLimit(n int) {
	if n < 1 {
		n = 1
	}
	s.mu.Lock()
	changed := s.limit != n
	s.limit = n
	if changed {
		s.broadcastLocked()
	}
	s.mu.Unlock()
}

func (s *dynamicSem) Limit() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limit
}

func (s *dynamicSem) InUse() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inUse
}

// Acquire blocks until a permit is available or ctx is done.
func (s *dynamicSem) Acquire(ctx context.Context) error {
	s.mu.Lock()
	for {
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return err
		}
		if s.inUse < s.limit {
			s.inUse++
			s.mu.Unlock()
			return nil
		}
		wait := s.waitCh
		s.mu.Unlock()

		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.mu.Lock()
	}
}

func (s *dynamicSem) Release() {
	s.mu.Lock()
	if s.inUse > 0 {
		s.inUse--
	}
	s.broadcastLocked()
	s.mu.Unlock()
}

func (s *dynamicSem) broadcastLocked() {
	close(s.waitCh)
	s.waitCh = make(chan struct{})
}

// keyedMutex serialises work per key with a bounded number of live entries.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*keyEntry
}

type keyEntry struct {
	ch   chan struct{}
	refs int
}

func newKeyedMutex() *keyedMutex {
	return &keyedMutex{m: map[string]*keyEntry{}}
}

func (k *keyedMutex) acquireEntry(key string) *keyEntry {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.m[key]
	if !ok {
		e = &keyEntry{ch: make(chan struct{}, 1)}
		k.m[key] = e
	}
	e.refs++
	return e
}

func (k *keyedMutex) releaseEntry(key string, e *keyEntry) {
	k.mu.Lock()
	defer k.mu.Unlock()
	e.refs--
	if e.refs <= 0 && len(e.ch) == 0 {
		delete(k.m, key)
	}
}

// Lock blocks until the key is free or ctx expires. The returned function must
// be called to release the key.
func (k *keyedMutex) Lock(ctx context.Context, key string) (func(), error) {
	e := k.acquireEntry(key)
	select {
	case e.ch <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-e.ch
				k.releaseEntry(key, e)
			})
		}, nil
	case <-ctx.Done():
		k.releaseEntry(key, e)
		return nil, ctx.Err()
	}
}

// TryLock is Lock with an explicit timeout.
func (k *keyedMutex) TryLock(key string, wait time.Duration) (func(), bool) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	unlock, err := k.Lock(ctx, key)
	if err != nil {
		return nil, false
	}
	return unlock, true
}
