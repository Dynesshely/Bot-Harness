package affect

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// fakeStore is an in-memory storage with the same semantics the service relies
// on: two-value Get, optimistic locking and append-only history/events.
type fakeStore struct {
	mu       sync.Mutex
	states   map[string]*store.AffectState
	events   []store.AffectEventLog
	history  []store.AffectPoint
	getErr   error
	saveErr  error
	conflict int
	pruned   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{states: map[string]*store.AffectState{}}
}

func (f *fakeStore) GetAffectState(_ context.Context, conversationID string) (*store.AffectState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	st := f.states[conversationID]
	if st == nil {
		return nil, nil
	}
	return cloneState(st), nil
}

func cloneState(st *store.AffectState) *store.AffectState {
	out := *st
	out.Dims = map[string]float64{}
	for k, v := range st.Dims {
		out.Dims[k] = v
	}
	out.Events = append([]store.AffectEventMark{}, st.Events...)
	return &out
}

func (f *fakeStore) SaveAffectState(_ context.Context, st *store.AffectState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	if f.conflict > 0 {
		f.conflict--
		return store.ErrAffectConflict
	}
	cur := f.states[st.ConversationID]
	if st.Version <= 0 {
		if cur != nil {
			return store.ErrAffectConflict
		}
		saved := cloneState(st)
		saved.Version = 1
		f.states[st.ConversationID] = saved
		st.Version = 1
		return nil
	}
	if cur == nil || cur.Version != st.Version {
		return store.ErrAffectConflict
	}
	saved := cloneState(st)
	saved.Version = st.Version + 1
	f.states[st.ConversationID] = saved
	st.Version = saved.Version
	return nil
}

func (f *fakeStore) ListAffectConversations(_ context.Context, limit int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	out := make([]string, 0, len(f.states))
	for id := range f.states {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		return f.states[out[i]].UpdatedAt > f.states[out[j]].UpdatedAt
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) AppendAffectEvent(_ context.Context, conversationID, eventID, tone, source, at string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, store.AffectEventLog{
		ID: int64(len(f.events) + 1), ConversationID: conversationID,
		EventID: eventID, Tone: tone, Source: source, CreatedAt: at,
	})
	return nil
}

func (f *fakeStore) ListAffectEvents(_ context.Context, conversationID string, limit int) ([]store.AffectEventLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.AffectEventLog{}
	for i := len(f.events) - 1; i >= 0; i-- {
		if f.events[i].ConversationID != conversationID {
			continue
		}
		out = append(out, f.events[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) AppendAffectHistory(_ context.Context, conversationID, at string, dims map[string]float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := map[string]float64{}
	for k, v := range dims {
		copied[k] = v
	}
	f.history = append(f.history, store.AffectPoint{At: at, Dims: copied})
	return nil
}

func (f *fakeStore) ListAffectHistory(_ context.Context, conversationID, since string, limit int) ([]store.AffectPoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.AffectPoint{}
	for _, p := range f.history {
		if since != "" && p.At < since {
			continue
		}
		out = append(out, p)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) PruneAffect(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruned++
	return nil
}

func testManager(t *testing.T, tweak func(*settings.Settings)) *settings.Manager {
	t.Helper()
	base := settings.Defaults()
	base.AffectEnabled = true
	base.AffectInjectMode = "full"
	if tweak != nil {
		tweak(&base)
	}
	m, err := settings.New(context.Background(), nil, base)
	if err != nil {
		t.Fatalf("settings manager: %v", err)
	}
	return m
}

func testService(t *testing.T, st storage, mgr *settings.Manager, m *Metrics) *Service {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := newService(st, mgr, log, m)
	s.now = func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local) }
	return s
}

func TestServiceDisabledIsInert(t *testing.T) {
	f := newFakeStore()
	mgr := testManager(t, func(s *settings.Settings) { s.AffectEnabled = false })
	svc := testService(t, f, mgr, NewMetrics())

	if err := svc.Observe(context.Background(), "c1", "这也太厉害了吧", time.Now()); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(f.states) != 0 || len(f.events) != 0 {
		t.Fatal("a disabled service must not write state")
	}
	cfg := mgr.GroupConfig("g1")
	if got := svc.Block(context.Background(), "c1", mgr.Get(), cfg, time.Now()); got != "" {
		t.Fatalf("disabled Block = %q, want empty", got)
	}
	if got := svc.Behavior(context.Background(), "c1", time.Now()); got != DefaultBehavior() {
		t.Fatalf("disabled Behavior = %+v, want default", got)
	}
	if skip, _ := svc.ProactiveSkip(context.Background(), "c1", time.Now()); skip {
		t.Fatal("disabled proactive must not skip")
	}
}

func TestObserveAppliesEventsAndPersists(t *testing.T) {
	f := newFakeStore()
	mgr := testManager(t, nil)
	metrics := NewMetrics()
	svc := testService(t, f, mgr, metrics)
	now := svc.currentTime()

	if err := svc.Observe(context.Background(), "c1", "这也太厉害了吧", now); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(f.states) != 1 {
		t.Fatalf("state rows = %d, want 1", len(f.states))
	}
	st := f.states["c1"]
	if st.Dims["valence"] <= 55 {
		t.Fatalf("praise should raise valence, got %.2f", st.Dims["valence"])
	}
	if len(st.Events) != 1 || st.Events[0].ID != "praise" {
		t.Fatalf("event ledger = %+v", st.Events)
	}
	if len(f.events) != 1 || f.events[0].EventID != "praise" {
		t.Fatalf("event log = %+v", f.events)
	}
	if len(f.history) != 1 {
		t.Fatalf("history rows = %d, want 1", len(f.history))
	}
	if metrics.Events.Load() != 1 {
		t.Fatalf("event metric = %d, want 1", metrics.Events.Load())
	}

	// A repeated praise is damped but still raises the value, and lands in the
	// ledger as a second mark.
	before := st.Dims["valence"]
	if err := svc.Observe(context.Background(), "c1", "真棒", now.Add(time.Minute)); err != nil {
		t.Fatalf("second Observe: %v", err)
	}
	if f.states["c1"].Dims["valence"] <= before {
		t.Fatalf("second praise should still raise valence: %.2f -> %.2f", before, f.states["c1"].Dims["valence"])
	}
}

func TestObserveClassifierOff(t *testing.T) {
	f := newFakeStore()
	mgr := testManager(t, func(s *settings.Settings) { s.AffectEventClassifier = "off" })
	svc := testService(t, f, mgr, NewMetrics())
	_ = svc.Observe(context.Background(), "c1", "厉害", svc.currentTime())
	if len(f.states) != 0 {
		t.Fatal("classifier=off must not persist a state")
	}
}

func TestBlockAndPauseResume(t *testing.T) {
	f := newFakeStore()
	mgr := testManager(t, nil)
	svc := testService(t, f, mgr, NewMetrics())
	ctx := context.Background()
	now := svc.currentTime()

	if err := svc.Observe(ctx, "c1", "好累啊", now); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	cfg := mgr.GroupConfig("g1")
	line := svc.Block(ctx, "c1", mgr.Get(), cfg, now)
	if line == "" {
		t.Fatal("full/line mode should render a block")
	}
	st := f.states["c1"]
	if st.Dims["stress"] <= 30 {
		t.Fatalf("complaint should raise stress, got %.2f", st.Dims["stress"])
	}
	short := svc.ShortLine(ctx, "c1", mgr.Get(), cfg, now)
	if approxTokens(short) > lineMaxTokens {
		t.Fatalf("ShortLine = %d tokens, want <= %d", approxTokens(short), lineMaxTokens)
	}

	if err := svc.Pause(ctx, "c1", now.Add(time.Hour)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if got := svc.Block(ctx, "c1", mgr.Get(), cfg, now); got != "" {
		t.Fatalf("paused Block = %q, want empty", got)
	}
	if skip, _ := svc.ProactiveSkip(ctx, "c1", now); !skip {
		t.Fatal("paused conversation must skip proactive")
	}
	if err := svc.Resume(ctx, "c1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := svc.Block(ctx, "c1", mgr.Get(), cfg, now); got == "" {
		t.Fatal("resumed Block should render again")
	}
}

func TestPresetResetHistoryConversations(t *testing.T) {
	f := newFakeStore()
	mgr := testManager(t, nil)
	svc := testService(t, f, mgr, NewMetrics())
	ctx := context.Background()
	now := svc.currentTime()

	if err := svc.Observe(ctx, "c1", "厉害", now); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := svc.ApplyPreset(ctx, "c1", "low_battery", now); err != nil {
		t.Fatalf("ApplyPreset: %v", err)
	}
	if got := f.states["c1"].Dims["energy"]; got != 20 {
		t.Fatalf("preset energy = %.2f, want 20", got)
	}
	if err := svc.ApplyPreset(ctx, "c1", "nope", now); err == nil {
		t.Fatal("unknown preset should error")
	}

	if err := svc.Reset(ctx, "c1", now); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	st := f.states["c1"]
	if len(st.Events) != 0 || st.Dims["energy"] != 55 {
		t.Fatalf("reset state = %+v", st)
	}

	convs, err := svc.Conversations(ctx, 10)
	if err != nil || len(convs) != 1 || convs[0] != "c1" {
		t.Fatalf("Conversations = %v, %v", convs, err)
	}
	points, err := svc.History(ctx, "c1", now.Add(-time.Hour), 10)
	if err != nil || len(points) == 0 {
		t.Fatalf("History = %v, %v", points, err)
	}
	snapshot, err := svc.Snapshot(ctx, "c1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.ConversationID != "c1" || snapshot.PackID != "daily" {
		t.Fatalf("Snapshot = %+v", snapshot)
	}
	if snapshot.Behavior == (Behavior{}) {
		t.Fatal("Snapshot should include a behavior")
	}
}

func TestServiceDegradesOnStoreFailures(t *testing.T) {
	f := newFakeStore()
	f.getErr = errors.New("db down")
	mgr := testManager(t, nil)
	metrics := NewMetrics()
	svc := testService(t, f, mgr, metrics)
	ctx := context.Background()
	now := svc.currentTime()

	if err := svc.Observe(ctx, "c1", "厉害", now); err != nil {
		t.Fatalf("Observe must not surface store errors: %v", err)
	}
	if got := svc.Block(ctx, "c1", mgr.Get(), mgr.GroupConfig("g1"), now); got != "" {
		t.Fatalf("Block on a broken store = %q, want empty", got)
	}
	if got := svc.Behavior(ctx, "c1", now); got != DefaultBehavior() {
		t.Fatalf("Behavior on a broken store = %+v, want default", got)
	}
	if skip, _ := svc.ProactiveSkip(ctx, "c1", now); skip {
		t.Fatal("broken store must not skip proactive")
	}
	if _, err := svc.Snapshot(ctx, "c1"); err == nil {
		t.Fatal("Snapshot should expose the store error to the admin handler")
	}
	if metrics.Errors.Load() == 0 {
		t.Fatal("failures should be counted")
	}
}

func TestTickDecaysAndRecordsHistory(t *testing.T) {
	f := newFakeStore()
	mgr := testManager(t, nil)
	metrics := NewMetrics()
	svc := testService(t, f, mgr, metrics)
	ctx := context.Background()
	now := svc.currentTime()

	if err := svc.Observe(ctx, "c1", "厉害", now); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	before := f.states["c1"].Dims["valence"]
	svc.now = func() time.Time { return now.Add(2 * time.Hour) }
	svc.tick(ctx)
	after := f.states["c1"].Dims["valence"]
	if after >= before {
		t.Fatalf("tick should decay valence towards the baseline: %.2f -> %.2f", before, after)
	}
	if metrics.Ticks.Load() != 1 {
		t.Fatalf("tick metric = %d, want 1", metrics.Ticks.Load())
	}
	if len(f.history) < 2 {
		t.Fatalf("tick should append history: %d rows", len(f.history))
	}
}

func TestUpdateRetriesOnConflict(t *testing.T) {
	f := newFakeStore()
	f.conflict = 1
	mgr := testManager(t, nil)
	svc := testService(t, f, mgr, NewMetrics())
	if err := svc.Observe(context.Background(), "c1", "厉害", svc.currentTime()); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(f.states) != 1 {
		t.Fatalf("the retry should create the state, rows = %d", len(f.states))
	}
}

func TestPackAndSnapshotDefaultForUnknownConversation(t *testing.T) {
	f := newFakeStore()
	mgr := testManager(t, nil)
	svc := testService(t, f, mgr, NewMetrics())
	snapshot, err := svc.Snapshot(context.Background(), "never-seen")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Dims["valence"] != 55 {
		t.Fatalf("baseline valence = %.2f, want 55", snapshot.Dims["valence"])
	}
	if len(svc.Packs()) == 0 {
		t.Fatal("Packs should list the embedded pack")
	}
}
