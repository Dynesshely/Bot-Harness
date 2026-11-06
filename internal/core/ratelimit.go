package core

import (
	"sync"
	"time"
)

// Limiter is a sliding-window rate limiter keyed by an arbitrary string
// (user, group, ...). A limit of zero or less means "unlimited".
type Limiter struct {
	mu        sync.Mutex
	windows   map[string][]time.Time
	cooldowns map[string]time.Time
}

// NewLimiter creates an empty limiter.
func NewLimiter() *Limiter {
	return &Limiter{
		windows:   map[string][]time.Time{},
		cooldowns: map[string]time.Time{},
	}
}

// Allow reports whether another event is permitted within the last minute.
func (l *Limiter) Allow(key string, perMinute int) bool {
	if perMinute <= 0 {
		return true
	}
	now := time.Now()
	cutoff := now.Add(-time.Minute)

	l.mu.Lock()
	defer l.mu.Unlock()

	times := l.windows[key]
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= perMinute {
		l.windows[key] = kept
		return false
	}
	l.windows[key] = append(kept, now)
	return true
}

// Cooldown reports whether the key has been idle for at least d. A successful
// check starts a new cooldown, so it doubles as the marker.
func (l *Limiter) Cooldown(key string, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, ok := l.cooldowns[key]; ok && now.Sub(last) < d {
		return false
	}
	l.cooldowns[key] = now
	return true
}

// Prune drops idle keys to bound memory usage.
func (l *Limiter) Prune() {
	cutoff := time.Now().Add(-10 * time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, times := range l.windows {
		if len(times) == 0 || times[len(times)-1].Before(cutoff) {
			delete(l.windows, k)
		}
	}
	for k, t := range l.cooldowns {
		if t.Before(cutoff) {
			delete(l.cooldowns, k)
		}
	}
}

// RunPruner periodically calls Prune until the stop channel is closed.
func (l *Limiter) RunPruner(stop <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			l.Prune()
		}
	}
}
