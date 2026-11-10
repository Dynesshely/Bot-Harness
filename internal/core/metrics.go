package core

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dynesshely/bot-harness/internal/store"
)

// Metrics holds process-lifetime counters exposed on /api/status.
type Metrics struct {
	EventsReceived       atomic.Int64
	EventsDeduped        atomic.Int64
	EventsIgnored        atomic.Int64
	RepliesSent          atomic.Int64
	RepliesFailed        atomic.Int64
	EarlyRepliesSent     atomic.Int64
	MessagesObserved     atomic.Int64
	EarlyRepliesRecalled atomic.Int64
	AIRequests           atomic.Int64
	AIErrors             atomic.Int64
	AIInFlight           atomic.Int64
	AILatencySumMS       atomic.Int64
	AILatencyCount       atomic.Int64
	TokensUsed           atomic.Int64
	ToolCalls            atomic.Int64
	ProactiveSent        atomic.Int64
	// PolishRequests / PolishFallbacks count the two-stage pipeline: one stage-2
	// attempt per polished (or skipped-before-call) turn, and how often the
	// draft had to be sent instead.
	PolishRequests  atomic.Int64
	PolishFallbacks atomic.Int64

	// CommitmentNudges counts delivered open-loop nudges; CommitmentAcked counts
	// the loops the user closed as done; CommitmentPostponed counts explicit
	// postponements.
	CommitmentNudges    atomic.Int64
	CommitmentAcked     atomic.Int64
	CommitmentPostponed atomic.Int64
	HumanStatusRuns     atomic.Int64
	HumanStatusFailures atomic.Int64
	// HumanStatusImageCalls counts analyze_image tool invocations during state
	// inference (successful or not). Vision calls are extra model requests, so
	// this is the cost signal for the image capability.
	HumanStatusImageCalls atomic.Int64

	// Affect counters (internal/affect). All five are monotonic process-lifetime
	// counters: ticks are completed background ticks, events are classified
	// messages, injections count non-empty prompt blocks, inject chars is their
	// total size, and errors counts failed observe/tick/block attempts.
	//
	// The affect package keeps its own totals (affect.Metrics) so it stays
	// independent of core; the admin /api/status handler merges them into this
	// snapshot with mergeAffectCounters. These fields remain the JSON contract
	// and a place for a future bridge that does not go through admin.
	AffectTicks       atomic.Int64
	AffectEvents      atomic.Int64
	AffectInjections  atomic.Int64
	AffectInjectChars atomic.Int64
	AffectErrors      atomic.Int64
}

// CounterSnapshot is the JSON shape of Metrics.
type CounterSnapshot struct {
	EventsReceived        int64 `json:"events_received"`
	EventsDeduped         int64 `json:"events_deduped"`
	EventsIgnored         int64 `json:"events_ignored"`
	RepliesSent           int64 `json:"replies_sent"`
	RepliesFailed         int64 `json:"replies_failed"`
	EarlyRepliesSent      int64 `json:"early_replies_sent"`
	MessagesObserved      int64 `json:"messages_observed"`
	EarlyRepliesRecalled  int64 `json:"early_replies_recalled"`
	AIRequests            int64 `json:"ai_requests"`
	AIErrors              int64 `json:"ai_errors"`
	TokensUsed            int64 `json:"tokens_used"`
	PolishRequests        int64 `json:"polish_requests"`
	PolishFallbacks       int64 `json:"polish_fallbacks"`
	CommitmentNudges      int64 `json:"commitment_nudges"`
	CommitmentAcked       int64 `json:"commitment_acked"`
	CommitmentPostponed   int64 `json:"commitment_postponed"`
	HumanStatusRuns       int64 `json:"human_status_runs"`
	HumanStatusFailures   int64 `json:"human_status_failures"`
	HumanStatusImageCalls int64 `json:"human_status_image_calls"`
	AffectTicks           int64 `json:"affect_ticks"`
	AffectEvents          int64 `json:"affect_events"`
	AffectInjections      int64 `json:"affect_injections"`
	AffectInjectChars     int64 `json:"affect_inject_chars"`
	AffectErrors          int64 `json:"affect_errors"`
}

// Snapshot returns the current counter values.
func (m *Metrics) Snapshot() CounterSnapshot {
	return CounterSnapshot{
		EventsReceived:        m.EventsReceived.Load(),
		EventsDeduped:         m.EventsDeduped.Load(),
		EventsIgnored:         m.EventsIgnored.Load(),
		RepliesSent:           m.RepliesSent.Load(),
		RepliesFailed:         m.RepliesFailed.Load(),
		EarlyRepliesSent:      m.EarlyRepliesSent.Load(),
		MessagesObserved:      m.MessagesObserved.Load(),
		EarlyRepliesRecalled:  m.EarlyRepliesRecalled.Load(),
		AIRequests:            m.AIRequests.Load(),
		AIErrors:              m.AIErrors.Load(),
		TokensUsed:            m.TokensUsed.Load(),
		PolishRequests:        m.PolishRequests.Load(),
		PolishFallbacks:       m.PolishFallbacks.Load(),
		CommitmentNudges:      m.CommitmentNudges.Load(),
		CommitmentAcked:       m.CommitmentAcked.Load(),
		CommitmentPostponed:   m.CommitmentPostponed.Load(),
		HumanStatusRuns:       m.HumanStatusRuns.Load(),
		HumanStatusFailures:   m.HumanStatusFailures.Load(),
		HumanStatusImageCalls: m.HumanStatusImageCalls.Load(),
		AffectTicks:           m.AffectTicks.Load(),
		AffectEvents:          m.AffectEvents.Load(),
		AffectInjections:      m.AffectInjections.Load(),
		AffectInjectChars:     m.AffectInjectChars.Load(),
		AffectErrors:          m.AffectErrors.Load(),
	}
}

// AddAffectInjection counts one non-empty prompt block and its character size.
//
// It is the core-side helper for any caller that feeds this Metrics object
// directly; the shipped affect.Service instead owns affect.Metrics and the
// admin status handler merges that. Either way the rule is the same: only a
// non-empty Block counts as an injection, so mode off / paused / failure never
// looks like a successful injection in the panel.
func (m *Metrics) AddAffectInjection(chars int) {
	m.AffectInjections.Add(1)
	if chars > 0 {
		m.AffectInjectChars.Add(int64(chars))
	}
}

// AvgLatencyMS reports the mean model latency.
func (m *Metrics) AvgLatencyMS() int64 {
	n := m.AILatencyCount.Load()
	if n == 0 {
		return 0
	}
	return m.AILatencySumMS.Load() / n
}

// Stats aggregates per-minute counters and periodically persists them.
type Stats struct {
	mu      sync.Mutex
	buckets map[string]*store.StatBucket
	st      *store.Store
	dirty   bool
}

// NewStats creates an empty aggregator.
func NewStats(st *store.Store) *Stats {
	return &Stats{buckets: map[string]*store.StatBucket{}, st: st}
}

const statsBucketLayout = "2006-01-02T15:04"

func bucketKey(t time.Time) string { return t.Format(statsBucketLayout) }

// Load seeds the in-memory buckets from the database.
func (s *Stats) Load(ctx context.Context, since time.Time) error {
	if s.st == nil {
		return nil
	}
	rows, err := s.st.LoadStats(ctx, since.Format(time.RFC3339))
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range rows {
		b := rows[i]
		key := bucketKey(timeFromBucket(b.Bucket))
		s.buckets[key] = &store.StatBucket{
			Bucket:       b.Bucket,
			In:           b.In,
			Out:          b.Out,
			Errors:       b.Errors,
			LatencySumMS: b.LatencySumMS,
			LatencyCount: b.LatencyCount,
			Tokens:       b.Tokens,
		}
	}
	return nil
}

func timeFromBucket(v string) time.Time {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	if t, err := time.Parse(statsBucketLayout, strings.TrimSpace(v)); err == nil {
		return t
	}
	return time.Now()
}

func (s *Stats) bump(fn func(*store.StatBucket)) {
	key := bucketKey(time.Now())
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.buckets[key]
	if !ok {
		b = &store.StatBucket{Bucket: time.Now().Format(time.RFC3339)}
		s.buckets[key] = b
	}
	fn(b)
	s.dirty = true
}

// RecordIn counts one inbound message.
func (s *Stats) RecordIn() { s.bump(func(b *store.StatBucket) { b.In++ }) }

// RecordOut counts one outbound message.
func (s *Stats) RecordOut() { s.bump(func(b *store.StatBucket) { b.Out++ }) }

// RecordError counts one failure.
func (s *Stats) RecordError() { s.bump(func(b *store.StatBucket) { b.Errors++ }) }

// RecordLatency records one model round-trip.
func (s *Stats) RecordLatency(ms int64) {
	s.bump(func(b *store.StatBucket) {
		b.LatencySumMS += ms
		b.LatencyCount++
	})
}

// RecordTokens records token consumption.
func (s *Stats) RecordTokens(n int) {
	if n <= 0 {
		return
	}
	s.bump(func(b *store.StatBucket) { b.Tokens += int64(n) })
}

// Flush writes dirty buckets to the database.
func (s *Stats) Flush(ctx context.Context) error {
	if s.st == nil {
		return nil
	}
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	buckets := make([]store.StatBucket, 0, len(s.buckets))
	for _, b := range s.buckets {
		buckets = append(buckets, *b)
	}
	s.dirty = false
	s.mu.Unlock()

	if err := s.st.SaveStats(ctx, buckets); err != nil {
		return err
	}
	// Retention: keep 7 days of minute buckets.
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	return s.st.PruneStats(ctx, cutoff.Format(time.RFC3339))
}

// SeriesPoint is one aggregated point of /api/stats.
type SeriesPoint struct {
	TS           string `json:"ts"`
	In           int    `json:"in"`
	Out          int    `json:"out"`
	Errors       int    `json:"errors"`
	AvgLatencyMS int64  `json:"avg_latency_ms"`
	Tokens       int64  `json:"tokens"`
}

// RangeSpec describes the aggregation window for a stats query.
type RangeSpec struct {
	Range  string
	Window time.Duration
	Bucket time.Duration
}

// ParseRange maps the API's range parameter onto a window and bucket size.
func ParseRange(r string) RangeSpec {
	switch r {
	case "1h":
		return RangeSpec{Range: "1h", Window: time.Hour, Bucket: 5 * time.Minute}
	case "6h":
		return RangeSpec{Range: "6h", Window: 6 * time.Hour, Bucket: 30 * time.Minute}
	case "7d":
		return RangeSpec{Range: "7d", Window: 7 * 24 * time.Hour, Bucket: 6 * time.Hour}
	default:
		return RangeSpec{Range: "24h", Window: 24 * time.Hour, Bucket: time.Hour}
	}
}

// Series aggregates the in-memory buckets into the requested resolution.
func (s *Stats) Series(spec RangeSpec) []SeriesPoint {
	now := time.Now()
	start := now.Add(-spec.Window)

	agg := map[time.Time]*SeriesPoint{}
	s.mu.Lock()
	for _, b := range s.buckets {
		ts := timeFromBucket(b.Bucket)
		if ts.Before(start) {
			continue
		}
		slot := ts.Truncate(spec.Bucket)
		p, ok := agg[slot]
		if !ok {
			p = &SeriesPoint{TS: slot.Format(time.RFC3339)}
			agg[slot] = p
		}
		p.In += b.In
		p.Out += b.Out
		p.Errors += b.Errors
		p.Tokens += b.Tokens
		if b.LatencyCount > 0 {
			p.AvgLatencyMS += b.LatencySumMS / max64(b.LatencyCount, 1)
		}
	}
	s.mu.Unlock()

	slots := make([]time.Time, 0, len(agg))
	for slot := range agg {
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].Before(slots[j]) })

	out := make([]SeriesPoint, 0, len(slots))
	for _, slot := range slots {
		out = append(out, *agg[slot])
	}
	return out
}

// Totals sums the buckets inside the window.
func (s *Stats) Totals(spec RangeSpec) SeriesPoint {
	series := s.Series(spec)
	var total SeriesPoint
	var latCount int
	for _, p := range series {
		total.In += p.In
		total.Out += p.Out
		total.Errors += p.Errors
		total.Tokens += p.Tokens
		if p.AvgLatencyMS > 0 {
			total.AvgLatencyMS += p.AvgLatencyMS
			latCount++
		}
	}
	if latCount > 0 {
		total.AvgLatencyMS /= int64(latCount)
	}
	return total
}

// Prune drops in-memory buckets outside the retention window.
func (s *Stats) Prune(window time.Duration) {
	cutoff := time.Now().Add(-window)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, b := range s.buckets {
		if timeFromBucket(b.Bucket).Before(cutoff) {
			delete(s.buckets, k)
		}
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
