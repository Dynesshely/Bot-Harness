package core

import (
	"context"
	"sync"
	"time"

	"github.com/dynesshely/bot-harness/internal/settings"
)

// 群聊消息合并（借鉴 MaiBot 的消息防抖）。
//
// 群里经常把一句想法拆成三条发：如果每条都独立判断/回复，机器人会抢话、重复，
// 看到的上下文也是碎的。这里在「已通过策略门、命令与静默判定、即将
// EnsureConversation 与入队」之前加一个很小的等待窗口：同一群的未 @ 消息先攒
// 一会儿，只让第一条（leader）去跑模型；其余消息照常写时间线，并各留一条审计
// （reason=debounced into <leader_msg_id>）。
//
// 边界：
//   - 只合并 group 且 !AtBot 的消息；@ / 引用机器人 / 命令零延迟；
//   - 模式 off 或等待秒数为 0 时不等待，行为与旧版逐条处理完全一致；
//   - 每批硬上限 20s（从 leader 到达起算），且可被 dispatcher 的 baseCtx 取消；
//   - 一个群同时只有一个 pending 批次，批次结构由 mutex 保护，不泄漏 goroutine。
const (
	// debounceHardCap is the whole-batch ceiling measured from the leader's
	// arrival: no amount of later messages may push the reply past 20 seconds.
	debounceHardCap = 20 * time.Second
	// debounceActiveWindow / debounceActiveThreshold decide whether a group
	// currently feels active: >= 3 human messages in the last 60 seconds.
	debounceActiveWindow    = 60 * time.Second
	debounceActiveThreshold = 3
	// debounceRecentLimit bounds one group's timestamp ring.
	debounceRecentLimit = 64
)

// debounceWindows resolves the two wait windows of one mode.
//
// natural（默认）直接采用后台的两个秒数键，管理员调数字立刻生效；fast / steady
// 是固定档位（1/3 与 4/7），与设置页的括号说明一致；off 不等待。
func debounceWindows(mode string, quietSeconds, activeSeconds int) (time.Duration, time.Duration) {
	switch mode {
	case "off":
		return 0, 0
	case "fast":
		return 1 * time.Second, 3 * time.Second
	case "steady":
		return 4 * time.Second, 7 * time.Second
	}
	// natural（空值/未知值由 normalize 兜底成 natural）：跟随配置数字。
	if quietSeconds < 0 {
		quietSeconds = 0
	}
	if activeSeconds < 0 {
		activeSeconds = 0
	}
	return time.Duration(quietSeconds) * time.Second, time.Duration(activeSeconds) * time.Second
}

// debounceBatch is one group's pending window. The first message owns the turn
// (leader); later messages only move the deadline forward, never past the cap.
type debounceBatch struct {
	groupOpenID string
	leader      *Inbound

	mu    sync.Mutex
	wait  time.Duration
	reset chan struct{}
	// done is closed when the debouncer shuts down, so a leader blocked in
	// waitForBatch cannot outlive the process that owns it.
	done chan struct{}
}

// leaderMsgID names the leader in the folded messages' audit reason.
func (b *debounceBatch) leaderMsgID() string {
	if b == nil || b.leader == nil {
		return ""
	}
	return b.leader.MsgID
}

func (b *debounceBatch) setWait(wait time.Duration) {
	b.mu.Lock()
	b.wait = wait
	b.mu.Unlock()
}

func (b *debounceBatch) currentWait() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wait
}

// debouncer owns every group's pending batch plus the per-group activity ring.
// Both are guarded by mu: route runs concurrently for different messages.
type debouncer struct {
	mu      sync.Mutex
	batches map[string]*debounceBatch
	recent  map[string][]time.Time
	closed  bool
}

func newDebouncer() *debouncer {
	return &debouncer{
		batches: map[string]*debounceBatch{},
		recent:  map[string][]time.Time{},
	}
}

// enqueue adds msg to the group's pending batch.
//
// It returns (batch, true) when this message created the batch — the caller then
// owns the delayed turn — and (batch, false) when the message was folded into an
// existing batch (the caller records it and returns). A nil batch means the
// debouncer is closed (process shutdown): the caller must take the original
// per-message path instead of waiting on a window that will never fire.
func (b *debouncer) enqueue(groupOpenID string, msg *Inbound, wait time.Duration) (*debounceBatch, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, true
	}
	if batch, ok := b.batches[groupOpenID]; ok {
		batch.setWait(wait)
		select {
		case batch.reset <- struct{}{}:
		default:
			// A reset is already queued; waitForBatch re-reads the wait below.
		}
		return batch, false
	}
	batch := &debounceBatch{
		groupOpenID: groupOpenID,
		leader:      msg,
		wait:        wait,
		reset:       make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
	b.batches[groupOpenID] = batch
	return batch, true
}

// pop removes and returns the group's batch (nil when there is none).
func (b *debouncer) pop(groupOpenID string) *debounceBatch {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch := b.batches[groupOpenID]
	delete(b.batches, groupOpenID)
	return batch
}

// noteHuman records one human group message and reports whether the group has
// become active (>= debounceActiveThreshold messages within debounceActiveWindow).
//
// It is called for every human group message, not just for the un-@ ones that
// are eligible for merging: an @-ing or command message is still evidence that
// the group is lively right now.
func (b *debouncer) noteHuman(groupOpenID string, now time.Time) bool {
	if groupOpenID == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.recent == nil {
		b.recent = map[string][]time.Time{}
	}
	kept := pruneTimes(b.recent[groupOpenID], now.Add(-debounceActiveWindow))
	kept = append(kept, now)
	if len(kept) > debounceRecentLimit {
		kept = kept[len(kept)-debounceRecentLimit:]
	}
	b.recent[groupOpenID] = kept
	return len(kept) >= debounceActiveThreshold
}

// groupActive reports whether the group had enough human traffic recently. It
// prunes stale timestamps but records nothing, so long-idle groups do not grow
// the ring.
func (b *debouncer) groupActive(groupOpenID string, now time.Time) bool {
	if groupOpenID == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := pruneTimes(b.recent[groupOpenID], now.Add(-debounceActiveWindow))
	if len(kept) == 0 {
		delete(b.recent, groupOpenID)
	} else {
		b.recent[groupOpenID] = kept
	}
	return len(kept) >= debounceActiveThreshold
}

// pruneTimes drops timestamps at or before cutoff. The slice is filtered in
// place: index i is only ever overwritten by an element at index j <= i, so the
// iteration stays valid and no allocation is needed.
func pruneTimes(stamps []time.Time, cutoff time.Time) []time.Time {
	kept := stamps[:0]
	for _, at := range stamps {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	return kept
}

// close drops every pending batch and disables new ones. It returns how many
// batches were still waiting so shutdown can be recorded in the log.
//
// Each dropped batch has its done channel closed, which wakes the leader's
// waitForBatch immediately: without it the goroutine would sit on its timer for
// up to the 20s cap and then run a turn after the process already stopped.
func (b *debouncer) close() int {
	b.mu.Lock()
	pending := len(b.batches)
	b.closed = true
	batches := b.batches
	b.batches = map[string]*debounceBatch{}
	b.recent = map[string][]time.Time{}
	b.mu.Unlock()

	for _, batch := range batches {
		close(batch.done)
	}
	return pending
}

// debounceWaitForGroup resolves how long this message should wait: the active
// window when the group has been chatty in the last minute, otherwise the quiet
// one. 0 means "do not wait" (mode off, both windows 0, or no debouncer).
func (d *Dispatcher) debounceWaitForGroup(s settings.Settings, groupOpenID string) time.Duration {
	quiet, active := debounceWindows(s.ReplyDebounceMode, s.ReplyDebounceQuietSeconds, s.ReplyDebounceActiveSeconds)
	if quiet <= 0 && active <= 0 {
		return 0
	}
	if d.debounce == nil {
		return 0
	}
	if d.debounce.groupActive(groupOpenID, time.Now()) {
		return active
	}
	return quiet
}

// noteGroupHuman records one human group message for the activity window. It is
// a no-op outside group chats, for C2C traffic and when the feature is absent.
func (d *Dispatcher) noteGroupHuman(in *Inbound) {
	if d.debounce == nil || in == nil || in.Scene != "group" || in.GroupOpenID == "" {
		return
	}
	d.debounce.noteHuman(in.GroupOpenID, time.Now())
}

// debounceCapDuration is the effective whole-batch ceiling. Tests shorten it so
// the cap can be exercised without a 20-second test.
func (d *Dispatcher) debounceCapDuration() time.Duration {
	if d.debounceCap > 0 {
		return d.debounceCap
	}
	return debounceHardCap
}

// waitForBatch blocks until the batch's window closes or the hard cap is hit.
// A reset (a later message) moves the deadline forward, but never past the cap.
// It returns false when ctx is cancelled, i.e. the dispatcher is shutting down.
func (d *Dispatcher) waitForBatch(ctx context.Context, batch *debounceBatch) bool {
	started := time.Now()
	capAt := started.Add(d.debounceCapDuration())
	deadline := started.Add(batch.currentWait())
	if deadline.After(capAt) {
		deadline = capAt
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		deadline = time.Now().Add(batch.currentWait())
		if deadline.After(capAt) {
			deadline = capAt
		}
		timer.Reset(time.Until(deadline))
	}

	for {
		select {
		case <-ctx.Done():
			return false
		case <-batch.done:
			// The debouncer was closed (shutdown): stop waiting, never process.
			return false
		case <-batch.reset:
			resetTimer()
		case <-timer.C:
			return true
		}
	}
}
