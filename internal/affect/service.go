package affect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// State is the persisted state shape. It is an alias so handlers can pass the
// store struct around without copying it.
type State = store.AffectState

// storage is the narrow slice of *store.Store the service needs. Keeping it an
// interface makes the service testable without a database and documents the
// exact store contract it depends on.
type storage interface {
	GetAffectState(ctx context.Context, conversationID string) (*store.AffectState, error)
	SaveAffectState(ctx context.Context, st *store.AffectState) error
	ListAffectConversations(ctx context.Context, limit int) ([]string, error)
	AppendAffectEvent(ctx context.Context, conversationID, eventID, tone, source, at string) error
	ListAffectEvents(ctx context.Context, conversationID string, limit int) ([]store.AffectEventLog, error)
	AppendAffectHistory(ctx context.Context, conversationID, at string, dims map[string]float64) error
	ListAffectHistory(ctx context.Context, conversationID, since string, limit int) ([]store.AffectPoint, error)
	PruneAffect(ctx context.Context, historyBefore, eventsBefore string) error
}

// Metrics holds the affect counters. main owns one instance and the service
// updates it; nil is accepted and replaced by a private instance.
type Metrics struct {
	Ticks       atomic.Int64
	Events      atomic.Int64
	Injections  atomic.Int64
	InjectChars atomic.Int64
	Errors      atomic.Int64
}

// NewMetrics creates an empty counter set.
func NewMetrics() *Metrics { return &Metrics{} }

// AddInjection counts one non-empty injected block and its character size.
func (m *Metrics) AddInjection(chars int) {
	if m == nil {
		return
	}
	m.Injections.Add(1)
	if chars > 0 {
		m.InjectChars.Add(int64(chars))
	}
}

// Service coordinates classification, persistence, injection and pacing. Every
// method is safe to call with a disabled or half-configured service; DB and
// settings failures are logged and degrade to "no state" rather than to an
// error visible in a reply.
type Service struct {
	st      storage
	mgr     *settings.Manager
	log     *slog.Logger
	metrics *Metrics

	ticking atomic.Bool
	pruned  atomic.Int64 // unix seconds of the last prune

	// now is a test seam; nil means time.Now.
	now func() time.Time
}

// NewService wires the service. st and mgr may be nil in tests; production
// always passes both.
func NewService(st *store.Store, mgr *settings.Manager, log *slog.Logger, m *Metrics) *Service {
	var s storage
	if st != nil {
		s = st
	}
	return newService(s, mgr, log, m)
}

func newService(st storage, mgr *settings.Manager, log *slog.Logger, m *Metrics) *Service {
	if log == nil {
		log = slog.Default()
	}
	if m == nil {
		m = NewMetrics()
	}
	// A broken embedded pack must be visible in the log once, at startup, while
	// still letting the bot run without the feature.
	if _, err := BuiltinPacks(); err != nil {
		log.Warn("affect: embedded content packs are invalid", "component", "affect", "error", err.Error())
	}
	return &Service{st: st, mgr: mgr, log: log, metrics: m, now: time.Now}
}

func (s *Service) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Metrics exposes the counters so main can bridge them into the admin status
// snapshot if it wants to.
func (s *Service) Metrics() *Metrics {
	if s == nil {
		return nil
	}
	return s.metrics
}

// settings returns the live document, or Defaults when no manager is wired.
func (s *Service) settings() settings.Settings {
	if s == nil || s.mgr == nil {
		return settings.Defaults()
	}
	return s.mgr.Get()
}

func (s *Service) enabled() bool {
	return s != nil && s.st != nil && s.settings().AffectEnabled
}

// Run drives the decay ticker. It is single-flight by construction (one
// goroutine) and defensively guards re-entry with a CAS in case a future caller
// starts more than one.
func (s *Service) Run(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	for {
		interval := s.tickInterval()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.tick(ctx)
		}
	}
}

func (s *Service) tickInterval() time.Duration {
	secs := s.settings().AffectTickSeconds
	if secs < 60 || secs > 1800 {
		secs = 300
	}
	return time.Duration(secs) * time.Second
}

func (s *Service) tick(ctx context.Context) {
	if !s.enabled() {
		return
	}
	if !s.ticking.CompareAndSwap(false, true) {
		return
	}
	defer s.ticking.Store(false)

	now := s.currentTime()
	// The ticker is the only decay driver, so a low LIMIT would strand states in
	// larger deployments. 5000 is far above a single bot's group count while
	// still bounding one tick's work.
	conversations, err := s.st.ListAffectConversations(ctx, 5000)
	if err != nil {
		s.fail("tick: list conversations", err)
		return
	}
	for _, conversationID := range conversations {
		if ctx.Err() != nil {
			return
		}
		if err := s.decay(ctx, conversationID, now); err != nil {
			s.fail("tick: decay "+conversationID, err)
			continue
		}
	}
	s.metrics.Ticks.Add(1)
	s.maybePrune(ctx, now)
}

func (s *Service) maybePrune(ctx context.Context, now time.Time) {
	last := s.pruned.Load()
	if last != 0 && now.Sub(time.Unix(last, 0)) < time.Hour {
		return
	}
	s.pruned.Store(now.Unix())
	// History powers the 30-day chart; the event log is debug-only and carries
	// user-derived text, so it is kept for 7 days (docs/AFFECT.md §10).
	historyBefore := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	eventsBefore := now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	if err := s.st.PruneAffect(ctx, historyBefore, eventsBefore); err != nil {
		s.fail("prune", err)
	}
}

// decay advances one conversation's state and records a history point.
func (s *Service) decay(ctx context.Context, conversationID string, now time.Time) error {
	p := s.pack()
	if p == nil {
		return nil
	}
	err := s.update(ctx, conversationID, now, p, func(st *State, p *Pack) error {
		if paused(st, now) {
			return nil
		}
		st.Dims = stepFromState(p, st, now)
		st.TickedAt = now.Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return err
	}
	// The ticker is also the history sampler: one point per tick is enough for
	// the chart and keeps the table small.
	if err := s.st.AppendAffectHistory(ctx, conversationID, now.Format(time.RFC3339), s.currentDims(ctx, conversationID)); err != nil {
		s.fail("tick: append history", err)
	}
	return nil
}

// Observe classifies one human message and applies the matched events. It
// returns nil on every recoverable failure: the caller is a reply path and must
// not be able to fail because the mood framework failed.
func (s *Service) Observe(ctx context.Context, conversationID, text string, now time.Time) error {
	if !s.enabled() || strings.TrimSpace(conversationID) == "" {
		return nil
	}
	ss := s.settings()
	if ss.AffectEventClassifier == "off" {
		return nil
	}
	p := PackByID(ss.AffectPack)
	if p == nil {
		return nil
	}
	matched := Classify(p, text)
	if len(matched) == 0 {
		return nil
	}
	if now.IsZero() {
		now = s.currentTime()
	}
	at := now.Format(time.RFC3339)
	var applied []*EventDef
	err := s.update(ctx, conversationID, now, p, func(st *State, p *Pack) error {
		applied = applied[:0]
		st.PackID = p.ID
		st.Dims = stepFromState(p, st, now)
		st.TickedAt = at
		batch := make([]EventApplication, 0, len(matched))
		for _, ev := range matched {
			if dailyCapReached(st, ev, now) {
				continue
			}
			batch = append(batch, EventApplication{Event: ev, Repeats: repeatCount(st.Events, ev.ID, now)})
			st.Events = append(st.Events, store.AffectEventMark{ID: ev.ID, At: at})
			applied = append(applied, ev)
		}
		st.Dims = ApplyEvents(p, st.Dims, batch)
		st.Events = trimMarks(st.Events, now)
		return nil
	})
	if err != nil {
		s.fail("observe "+conversationID, err)
		return nil
	}
	for _, ev := range applied {
		if err := s.st.AppendAffectEvent(ctx, conversationID, ev.ID, ev.Tone, "classifier", at); err != nil {
			s.fail("append event", err)
			break
		}
		s.metrics.Events.Add(1)
	}
	if len(applied) > 0 {
		if err := s.st.AppendAffectHistory(ctx, conversationID, at, s.currentDims(ctx, conversationID)); err != nil {
			s.fail("append history", err)
		}
	}
	return nil
}

// Block renders the injection block for the current turn. It reads the resolved
// per-group settings so a group can turn the feature off or choose its own mode.
func (s *Service) Block(ctx context.Context, conversationID string, ss settings.Settings, cfg settings.GroupConfig, now time.Time) string {
	return s.block(ctx, conversationID, ss, cfg, now, false)
}

// ShortLine is the one-line variant used by the polish stage. It is capped at
// the line budget and returns "" when nothing should be injected.
func (s *Service) ShortLine(ctx context.Context, conversationID string, ss settings.Settings, cfg settings.GroupConfig, now time.Time) string {
	line := s.block(ctx, conversationID, ss, cfg, now, true)
	if line == "" {
		return ""
	}
	return truncateTokens(line, lineMaxTokens)
}

func (s *Service) block(ctx context.Context, conversationID string, ss settings.Settings, cfg settings.GroupConfig, now time.Time, lineOnly bool) string {
	if s == nil || s.st == nil || !ss.AffectEnabled || !cfg.AffectEnabled {
		return ""
	}
	mode := strings.TrimSpace(cfg.AffectInjectMode)
	if mode == "" {
		mode = strings.TrimSpace(ss.AffectInjectMode)
	}
	if mode == "" || mode == ModeOff {
		return ""
	}
	p := PackByID(ss.AffectPack)
	if p == nil {
		return ""
	}
	if now.IsZero() {
		now = s.currentTime()
	}
	// Injection has its own short deadline, independent of the turn's model
	// budget: a slow database must degrade to "no block", never to a slow reply.
	readCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	ctx = readCtx
	st, err := s.st.GetAffectState(ctx, conversationID)
	if err != nil {
		s.fail("block: get state", err)
		return ""
	}
	if st == nil || paused(st, now) {
		return ""
	}
	dims := stepFromState(p, st, now)
	b := BehaviorFor(p, dims)
	out := ""
	if lineOnly {
		out = BuildLine(p, dims, now)
	} else {
		out = BuildBlock(p, dims, marksOf(st.Events), mode, ss.AffectInjectMaxTokens, b, now)
	}
	if out != "" {
		s.metrics.AddInjection(len([]rune(out)))
	}
	return out
}

// Behavior resolves the reply-pacing factors. It never returns an error: a
// missing state or a broken pack means "no adjustment".
func (s *Service) Behavior(ctx context.Context, conversationID string, now time.Time) Behavior {
	if !s.enabled() {
		return DefaultBehavior()
	}
	p := s.pack()
	if p == nil {
		return DefaultBehavior()
	}
	st, err := s.st.GetAffectState(ctx, conversationID)
	if err != nil {
		s.fail("behavior: get state", err)
		return DefaultBehavior()
	}
	if st == nil || paused(st, now) {
		return DefaultBehavior()
	}
	return BehaviorFor(p, stepFromState(p, st, now))
}

// ProactiveSkip reports whether an unprompted message should be skipped because
// the state is too flat or the conversation is paused.
func (s *Service) ProactiveSkip(ctx context.Context, conversationID string, now time.Time) (bool, string) {
	if !s.enabled() {
		return false, ""
	}
	p := s.pack()
	if p == nil {
		return false, ""
	}
	st, err := s.st.GetAffectState(ctx, conversationID)
	if err != nil {
		s.fail("proactive: get state", err)
		return false, ""
	}
	if st == nil {
		return false, ""
	}
	if paused(st, now) {
		return true, "情绪状态已暂停"
	}
	dims := stepFromState(p, st, now)
	if dims["energy"] < 25 {
		return true, "精力太低"
	}
	if dims["valence"] < 20 {
		return true, "心情太低"
	}
	return false, ""
}

// Snapshot is the admin read model.
type Snapshot struct {
	ConversationID string             `json:"conversation_id"`
	PackID         string             `json:"pack_id"`
	PackName       string             `json:"pack_name"`
	Dims           map[string]float64 `json:"dims"`
	Behavior       Behavior           `json:"behavior"`
	Events         []EventMark        `json:"events"`
	PausedUntil    string             `json:"paused_until,omitempty"`
	TickedAt       string             `json:"ticked_at,omitempty"`
	UpdatedAt      string             `json:"updated_at,omitempty"`
	Version        int64              `json:"version"`
}

// Snapshot returns the current state without mutating it. A conversation that
// has never been observed gets the pack baseline.
func (s *Service) Snapshot(ctx context.Context, conversationID string) (Snapshot, error) {
	ss := s.settings()
	p := PackByID(ss.AffectPack)
	out := Snapshot{ConversationID: conversationID, Dims: BaselineDims(p)}
	if p != nil {
		out.PackID, out.PackName = p.ID, p.Name
		out.Behavior = BehaviorFor(p, out.Dims)
	}
	if s == nil || s.st == nil {
		return out, nil
	}
	st, err := s.st.GetAffectState(ctx, conversationID)
	if err != nil {
		return out, err
	}
	if st == nil {
		return out, nil
	}
	if p == nil {
		p = PackByID(st.PackID)
	}
	dims := st.Dims
	if p != nil {
		dims = stepFromState(p, st, s.currentTime())
		out.Behavior = BehaviorFor(p, dims)
	}
	out.PackID = st.PackID
	if p != nil && p.ID == st.PackID {
		out.PackName = p.Name
	}
	out.Dims = NormalizeDims(p, dims)
	out.Events = marksOf(st.Events)
	out.PausedUntil = st.PausedUntil
	out.TickedAt = st.TickedAt
	out.UpdatedAt = st.UpdatedAt
	out.Version = st.Version
	return out, nil
}

// Pause suppresses injection and proactive speech until the given instant.
// A zero instant means "for the next 30 minutes".
func (s *Service) Pause(ctx context.Context, conversationID string, until time.Time) error {
	if s == nil || s.st == nil {
		return errors.New("affect: service is not configured")
	}
	if until.IsZero() {
		until = s.currentTime().Add(30 * time.Minute)
	}
	now := s.currentTime()
	p := s.pack()
	return s.update(ctx, conversationID, now, p, func(st *State, p *Pack) error {
		if p != nil {
			st.PackID = p.ID
			st.Dims = stepFromState(p, st, now)
		}
		st.TickedAt = now.Format(time.RFC3339)
		st.PausedUntil = until.Format(time.RFC3339)
		return nil
	})
}

// Resume clears the pause deadline.
func (s *Service) Resume(ctx context.Context, conversationID string) error {
	if s == nil || s.st == nil {
		return errors.New("affect: service is not configured")
	}
	now := s.currentTime()
	p := s.pack()
	return s.update(ctx, conversationID, now, p, func(st *State, p *Pack) error {
		st.PausedUntil = ""
		if p != nil {
			st.PackID = p.ID
			st.Dims = stepFromState(p, st, now)
		}
		st.TickedAt = now.Format(time.RFC3339)
		return nil
	})
}

// ApplyPreset resets the dimensions to a named preset.
func (s *Service) ApplyPreset(ctx context.Context, conversationID, presetID string, now time.Time) error {
	if s == nil || s.st == nil {
		return errors.New("affect: service is not configured")
	}
	p := s.pack()
	if p == nil {
		return errors.New("affect: no pack available")
	}
	preset, ok := p.PresetByID(presetID)
	if !ok {
		for _, candidate := range p.Presets {
			if candidate.Name == presetID {
				preset, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		return fmt.Errorf("affect: unknown preset %q", presetID)
	}
	if now.IsZero() {
		now = s.currentTime()
	}
	return s.update(ctx, conversationID, now, p, func(st *State, p *Pack) error {
		st.PackID = p.ID
		st.Dims = ApplyPreset(p, preset)
		st.TickedAt = now.Format(time.RFC3339)
		return nil
	})
}

// Reset returns the conversation to the pack baseline and clears its ledger.
func (s *Service) Reset(ctx context.Context, conversationID string, now time.Time) error {
	if s == nil || s.st == nil {
		return errors.New("affect: service is not configured")
	}
	p := s.pack()
	if p == nil {
		return errors.New("affect: no pack available")
	}
	if now.IsZero() {
		now = s.currentTime()
	}
	return s.update(ctx, conversationID, now, p, func(st *State, p *Pack) error {
		st.PackID = p.ID
		st.Dims = BaselineDims(p)
		st.Events = []store.AffectEventMark{}
		st.PausedUntil = ""
		st.TickedAt = now.Format(time.RFC3339)
		return nil
	})
}

// History returns snapshots since the given time, oldest first.
func (s *Service) History(ctx context.Context, conversationID string, since time.Time, limit int) ([]store.AffectPoint, error) {
	if s == nil || s.st == nil {
		return []store.AffectPoint{}, nil
	}
	sinceText := ""
	if !since.IsZero() {
		sinceText = since.Format(time.RFC3339)
	}
	return s.st.ListAffectHistory(ctx, conversationID, sinceText, limit)
}

// Conversations lists conversations with a state, most recently updated first.
func (s *Service) Conversations(ctx context.Context, limit int) ([]string, error) {
	if s == nil || s.st == nil {
		return []string{}, nil
	}
	return s.st.ListAffectConversations(ctx, limit)
}

// Packs lists the embedded content packs.
func (s *Service) Packs() []PackInfo { return PackInfos() }

// --- internals -------------------------------------------------------------

// update is a read-modify-write with the store's optimistic lock. The apply
// callback may run more than once after a conflict, so it must be idempotent
// with respect to the state it receives (it always starts from a fresh row).
func (s *Service) update(ctx context.Context, conversationID string, now time.Time, p *Pack, apply func(*State, *Pack) error) error {
	if s == nil || s.st == nil {
		return errors.New("affect: service is not configured")
	}
	if strings.TrimSpace(conversationID) == "" {
		return errors.New("affect: conversation id is required")
	}
	if now.IsZero() {
		now = s.currentTime()
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		st, err := s.st.GetAffectState(ctx, conversationID)
		if err != nil {
			return err
		}
		if st == nil {
			st = &State{ConversationID: conversationID, Dims: BaselineDims(p), Events: []store.AffectEventMark{}}
		}
		if st.Dims == nil {
			st.Dims = BaselineDims(p)
		}
		if st.PackID == "" && p != nil {
			st.PackID = p.ID
		}
		if err := apply(st, p); err != nil {
			return err
		}
		st.UpdatedAt = now.Format(time.RFC3339)
		if err := s.st.SaveAffectState(ctx, st); err != nil {
			lastErr = err
			if errors.Is(err, store.ErrAffectConflict) {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("affect: save state after retries: %w", lastErr)
}

func (s *Service) currentDims(ctx context.Context, conversationID string) map[string]float64 {
	if s == nil || s.st == nil {
		return map[string]float64{}
	}
	st, err := s.st.GetAffectState(ctx, conversationID)
	if err != nil || st == nil {
		return map[string]float64{}
	}
	return st.Dims
}

func (s *Service) pack() *Pack {
	ss := s.settings()
	p := PackByID(ss.AffectPack)
	if p == nil {
		p = DefaultPack()
	}
	return p
}

func (s *Service) fail(what string, err error) {
	if err == nil {
		return
	}
	if s != nil && s.metrics != nil {
		s.metrics.Errors.Add(1)
	}
	if s != nil && s.log != nil {
		s.log.Debug("affect: "+what, "component", "affect", "error", err.Error())
	}
}

func paused(st *State, now time.Time) bool {
	if st == nil || strings.TrimSpace(st.PausedUntil) == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339, st.PausedUntil)
	if err != nil {
		return false
	}
	return now.Before(until)
}

func stepFromState(p *Pack, st *State, now time.Time) map[string]float64 {
	if p == nil || st == nil {
		return BaselineDims(p)
	}
	dims := st.Dims
	dt := time.Duration(0)
	if st.TickedAt != "" {
		if ticked, err := time.Parse(time.RFC3339, st.TickedAt); err == nil {
			dt = now.Sub(ticked)
		}
	}
	return Step(p, dims, dt)
}

func dailyCapReached(st *State, ev *EventDef, now time.Time) bool {
	if st == nil || ev == nil || ev.DailyCap < 1 {
		return false
	}
	cutoff := now.Add(-24 * time.Hour)
	n := 0
	for _, mark := range st.Events {
		if mark.ID != ev.ID {
			continue
		}
		at, err := time.Parse(time.RFC3339, mark.At)
		if err != nil || at.Before(cutoff) {
			continue
		}
		n++
	}
	return n >= ev.DailyCap
}

func repeatCount(marks []store.AffectEventMark, eventID string, now time.Time) int {
	cutoff := now.Add(-repeatWindow)
	n := 0
	for _, mark := range marks {
		if mark.ID != eventID {
			continue
		}
		at, err := time.Parse(time.RFC3339, mark.At)
		if err != nil || at.Before(cutoff) {
			continue
		}
		n++
	}
	return n
}

func trimMarks(marks []store.AffectEventMark, now time.Time) []store.AffectEventMark {
	cutoff := now.Add(-48 * time.Hour)
	out := make([]store.AffectEventMark, 0, len(marks))
	for _, mark := range marks {
		at, err := time.Parse(time.RFC3339, mark.At)
		if err != nil {
			continue
		}
		if at.Before(cutoff) {
			continue
		}
		out = append(out, mark)
	}
	if len(out) > 200 {
		out = out[len(out)-200:]
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

func marksOf(marks []store.AffectEventMark) []EventMark {
	out := make([]EventMark, 0, len(marks))
	for _, mark := range marks {
		out = append(out, EventMark{ID: mark.ID, At: mark.At})
	}
	return out
}
