package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// 把回答送到群里：被动回复/主动兜底、提前回复与撤回、错误话术、回复页脚。

// --- 提前回复与正式答复 -----------------------------------------------------

// modelOutcome is the result of a model call, plus whether the user already
// received the "let me think" notice while it was running.
type modelOutcome struct {
	result    *ai.ChatResult
	err       error
	earlySent bool
	// earlyMessageID / earlySentAt let us retract the notice once the real
	// answer has been delivered.
	earlyMessageID string
	earlySentAt    time.Time
	// usedVision marks a request that carried image parts.
	usedVision bool
	// systemPrompt is the system message of the step that produced the answer.
	// It already carries the persona and this turn's memory block, which is
	// exactly what the polish stage needs — and nothing more.
	systemPrompt string
	// usage sums every model call of the turn, tool steps included, so the
	// reply footer and the token counters report what the turn really cost.
	usage ai.Usage
}

// deliver sends the reply. forceActive is set when the passive reply slot has
// already been spent on the "let me think" notice, in which case the answer is
// pushed as an active message; otherwise it is a passive reply that downgrades
// to an active one if the 5-minute window has closed.
// deliver sends the answer. `level` is the effective humanize level for this
// group and `since` is when the inbound message arrived: the pause is measured
// against the whole turn, so a slow model is not delayed a second time.
func (d *Dispatcher) deliver(ctx context.Context, in *Inbound, s settings.Settings, text string,
	forceActive bool, quoteIndex, level string, since time.Time) (*qq.SendResult, error) {

	if pause := humanizeDelayForTurn(level, len([]rune(text)), s.HumanizeDelayScale, time.Since(since)); pause > 0 {
		d.log.Debug("humanize pause", "component", "humanize", "level", level,
			"pause_ms", pause.Milliseconds(), "conversation", in.GroupOpenID)
		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return &qq.SendResult{}, ctx.Err()
		}
	}

	opts := qq.SendOptions{
		MsgID:       in.MsgID,
		Markdown:    s.MarkdownEnabled,
		MaxChars:    s.ReplyMaxChars,
		MaxSegments: s.ReplyMaxSegments,
		// Empty for the "let me think" notice and the error fallbacks: those are
		// meta messages, quoting the question under them only adds noise.
		QuoteMessageID: quoteIndex,
	}
	// 分段气泡 + 逐段打字：只有 humanize 开启时才拆，off 保持单条 + 零等待。
	if level != HumanizeOff && level != "" {
		bubbles := splitBubbles(text, s.HumanizeSegmentSplit, s.ReplyMaxChars, s.ReplyMaxSegments, nil)
		if len(bubbles) > 1 {
			opts.Bubbles = bubbles
			remaining := bubblePauseBudget(s) - time.Since(since)
			if remaining < 0 {
				remaining = 0
			}
			spent := time.Duration(0)
			opts.BubblePause = func(_ int, seg string) time.Duration {
				pause := typingPause(seg, s.HumanizeTypingSecondsPerChar, s.HumanizeDelayScale)
				if pause <= 0 || remaining <= 0 {
					return 0
				}
				if pause > remaining-spent {
					pause = remaining - spent
				}
				if pause < 0 {
					pause = 0
				}
				spent += pause
				return pause
			}
		} else {
			// 单气泡也按字数等一次，但总量受同一份预算约束。
			opts.SegmentPause = 0
		}
	} else {
		opts.SegmentPause = 0
	}
	if forceActive {
		opts.MsgID = ""
	}
	if in.Scene == "c2c" {
		opts.UserOpenID = in.UserOpenID
	} else {
		opts.GroupOpenID = in.GroupOpenID
	}

	res, err := d.sender.Send(ctx, opts, text)
	if err == nil {
		return res, nil
	}
	if !errors.Is(err, qq.ErrPassiveWindowClosed) || forceActive {
		return res, err
	}

	// Passive window closed: retry as an active message (subject to platform
	// active-message quotas).
	d.log.Warn("passive window closed, falling back to active message", "component", "sender")
	activeOpts := opts
	activeOpts.MsgID = ""
	activeRes, activeErr := d.sender.Send(ctx, activeOpts, text)
	if activeErr != nil {
		return activeRes, fmt.Errorf("passive reply expired and active fallback failed: %w", activeErr)
	}
	return activeRes, nil
}

func (d *Dispatcher) replyBusy(ctx context.Context, in *Inbound) {
	if !in.AtBot {
		return
	}
	s := d.settings.Effective()
	opts := qq.SendOptions{MsgID: in.MsgID, MaxChars: s.ReplyMaxChars, MaxSegments: 1}
	if in.Scene == "c2c" {
		opts.UserOpenID = in.UserOpenID
	} else {
		opts.GroupOpenID = in.GroupOpenID
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := d.sender.Send(ctx, opts, "我还在回上一条消息，等我一下下～"); err != nil {
		d.log.Debug("busy notice failed", "component", "sender", "error", err.Error())
	}
}

func (d *Dispatcher) replyFallback(ctx context.Context, in *Inbound, text string, forceActive bool) {
	if !in.AtBot {
		return
	}
	s := d.settings.Effective()
	opts := qq.SendOptions{MsgID: in.MsgID, MaxChars: s.ReplyMaxChars, MaxSegments: 1}
	if forceActive {
		opts.MsgID = ""
	}
	if in.Scene == "c2c" {
		opts.UserOpenID = in.UserOpenID
	} else {
		opts.GroupOpenID = in.GroupOpenID
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := d.sender.Send(ctx, opts, text); err != nil {
		d.log.Debug("fallback notice failed", "component", "sender", "error", err.Error())
	}
	d.stats.RecordOut()
}

// sendEarlyNotice delivers the short acknowledgement as a passive reply.
func (d *Dispatcher) sendEarlyNotice(ctx context.Context, in *Inbound, s settings.Settings, convID string) (bool, string) {
	text := strings.TrimSpace(s.EarlyReplyText)
	if text == "" {
		return false, ""
	}
	opts := qq.SendOptions{MsgID: in.MsgID, MaxChars: s.ReplyMaxChars, MaxSegments: 1}
	if in.Scene == "c2c" {
		opts.UserOpenID = in.UserOpenID
	} else {
		opts.GroupOpenID = in.GroupOpenID
	}

	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	res, err := d.sender.Send(sendCtx, opts, text)
	if err != nil {
		d.log.Warn("early acknowledgement failed", "component", "sender", "error", err.Error())
		return false, ""
	}
	messageID := firstOrEmpty(res.MessageIDs)
	d.metrics.EarlyRepliesSent.Add(1)
	d.stats.RecordOut()

	rec := &store.Record{
		Direction:      "out",
		GroupOpenID:    in.GroupOpenID,
		MemberOpenID:   in.MemberOpenID,
		MemberName:     in.MemberName,
		Content:        text,
		MsgID:          in.MsgID,
		Status:         "early_reply",
		Scene:          in.Scene,
		ConversationID: convID,
		// The acknowledgement is always an active message.
		Delivery: store.DeliveryActive,
	}
	if id, err := d.st.AddRecord(sendCtx, rec); err == nil {
		rec.ID = id
	}
	d.publish("message", rec)

	d.log.Info("sent early acknowledgement while the model is still thinking",
		"component", "dispatch", "delay_seconds", s.EarlyReplyDelaySecs, "segments", res.Segments)
	return true, messageID
}

// recallEarlyNotice retracts the "let me think" notice now that the real answer
// has been delivered, so the chat does not keep a stale placeholder.
func (d *Dispatcher) recallEarlyNotice(ctx context.Context, in *Inbound, outcome modelOutcome) {
	if outcome.earlyMessageID == "" {
		return
	}
	if age := time.Since(outcome.earlySentAt); age >= earlyRecallWindow {
		d.log.Info("early acknowledgement is past the recall window, leaving it in place",
			"component", "sender", "age_seconds", int(age.Seconds()))
		return
	}
	if d.qq == nil {
		return
	}

	recallCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var err error
	if in.Scene == "c2c" {
		err = d.qq.RecallC2CMessage(recallCtx, in.UserOpenID, outcome.earlyMessageID)
	} else {
		err = d.qq.RecallGroupMessage(recallCtx, in.GroupOpenID, outcome.earlyMessageID)
	}
	switch {
	case err == nil:
		d.metrics.EarlyRepliesRecalled.Add(1)
		d.log.Info("recalled the early acknowledgement after delivering the real answer",
			"component", "sender", "message_id", outcome.earlyMessageID)
	case qq.IsRecallExpired(err):
		d.log.Info("the platform refused the recall (older than 2 minutes)",
			"component", "sender", "message_id", outcome.earlyMessageID)
	default:
		d.log.Warn("failed to recall the early acknowledgement", "component", "sender",
			"message_id", outcome.earlyMessageID, "error", err.Error())
	}
}

// appendReplyFooter adds the elapsed time and token estimate to a model answer.
func appendReplyFooter(text string, tokens int, elapsed time.Duration) string {
	return fmt.Sprintf("%s\n\n—— 用时 %s · 约 %d tokens", strings.TrimRight(text, "\n"), formatElapsed(elapsed), tokens)
}

func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// estimatePromptTokens approximates the input size when the provider does not
// report usage.
func estimatePromptTokens(messages []ai.Message) int {
	total := 0
	for _, m := range messages {
		total += EstimateTokens(m.Content) + 4
	}
	return total
}
