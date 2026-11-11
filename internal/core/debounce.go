package core

import (
	"context"
	"sync"
	"time"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// 群聊消息合并（借鉴 MaiBot 的消息防抖）。
//
// 群里经常一句想法拆成三条发：如果每条都独立判断/回复，机器人会抢话、重复，
// 看到的上下文也是碎的。这里在「已通过策略门、即将进入 processMessage」之前
// 加一个很小的等待窗口：同一群的未 @ 消息先攒一会儿，只让第一条去跑模型，
// 其余消息照常写时间线并留审计（reason=debounced into ...）。
//
// 边界：
//   - 只合并 group 且 !AtBot 的消息；@ / 引用机器人 / 命令立即处理；
//   - 模式 off 或窗口 0 时不等待，行为与旧版逐条处理完全一致；
//   - 每批硬上限 20s，且可被 dispatcher 的 baseCtx 取消，不泄漏 timer。

const debounceHardCap = 20 * time.Second

// debounceBatch is one group's pending window. The first message owns the turn;
// later messages only extend the timer.
type debounceBatch struct {
	groupOpenID string
	leader      *Inbound
	conv        convTarget
	cfg         settings.GroupConfig
	rec         *store.Record

	mu    sync.Mutex
	wait  time.Duration
	reset chan struct{}
}

type debouncer struct {
	mu      sync.Mutex
	batches map[string]*debounceBatch
	closed  bool
}

func newDebouncer() *debouncer {
	return &debouncer{batches: map[string]*debounceBatch{}}
}

// enqueue adds msg to the group's pending batch. It returns the batch and true
// when this message created it (the caller then owns the delayed turn), or false
// when it was folded into an existing batch (the caller records it and returns).
func (b *debouncer) enqueue(groupOpenID string, msg *Inbound, wait time.Duration) (*debounceBatch, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, false
	}
	if batch, ok := b.batches[groupOpenID]; ok {
		batch.mu.Lock()
		batch.wait = wait
		batch.mu.Unlock()
		select {
		case batch.reset <- struct{}{}:
		default:
		}
		return batch, false
	}
	batch := &debounceBatch{
		groupOpenID: groupOpenID,
		leader:      msg,
		wait:        wait,
		reset:       make(chan struct{}, 1),
	}
	b.batches[groupOpenID] = batch
	return batch, true
}

// pop removes and returns the finished batch.
func (b *debouncer) pop(groupOpenID string) *debounceBatch {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch := b.batches[groupOpenID]
	delete(b.batches, groupOpenID)
	return batch
}

func (b *debouncer) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.batches = map[string]*debounceBatch{}
}

// debounceWindow resolves the wait for one group: quiet groups wait less, busy
// groups a bit longer. Mode presets override the two numeric settings so the
// admin can either pick a preset or tune the numbers directly.
func (d *Dispatcher) debounceWindow(s settings.Settings) time.Duration {
	if s.ReplyDebounceMode == "off" {
		return 0
	}
	// 两个秒数是用户可见的真相：模式只是预设，改了秒数就按秒数走。
	quiet := time.Duration(s.ReplyDebounceQuietSeconds) * time.Second
	active := time.Duration(s.ReplyDebounceActiveSeconds) * time.Second
	if d.groupBusy(HumanizeMedium) {
		return active
	}
	return quiet
}

// waitForBatch blocks until the batch's window closes or the hard cap is hit.
// A reset (a later message) moves the deadline forward, but never past the cap.
func (d *Dispatcher) waitForBatch(ctx context.Context, batch *debounceBatch) bool {
	started := time.Now()
	capAt := started.Add(debounceHardCap)
	deadline := time.Now().Add(batch.currentWait())
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
		case <-batch.reset:
			resetTimer()
		case <-timer.C:
			return true
		}
	}
}

func (b *debounceBatch) currentWait() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wait
}
