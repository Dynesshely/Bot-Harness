package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/store"
)

// Manual send sentinels. The admin layer maps them onto HTTP status codes, so
// they must not be matched by string.
var (
	ErrManualSendDisabled = errors.New("manual sending is disabled")
	ErrManualSendEmpty    = errors.New("content must not be empty")
	ErrManualSendTooLong  = errors.New("content is too long")
	ErrManualSendLimited  = errors.New("manual send rate limited")
	ErrManualSendRejected = errors.New("the platform rejected the message")
	ErrManualSendNoTarget = errors.New("conversation has no sendable target")
	// ErrManualSendQuoteUnavailable: 想引用的那条消息没有平台引用索引。
	ErrManualSendQuoteUnavailable = errors.New("the quoted message has no platform reference index")
	ErrManualSendQuoteDisabled    = errors.New("quote replies are disabled for this group")
	ErrManualSendProactive        = errors.New("the group does not allow proactive messages")
)

// ManualSendError carries an operator-facing message while still matching one
// of the sentinels above via errors.Is: "%w: 需要 3 段" would leak the English
// sentinel text into the panel.
type ManualSendError struct {
	Kind error
	Msg  string
}

func (e *ManualSendError) Error() string { return e.Msg }
func (e *ManualSendError) Unwrap() error { return e.Kind }

func manualError(kind error, format string, args ...any) error {
	return &ManualSendError{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// ManualSendResult reports one admin-initiated send.
type ManualSendResult struct {
	MessageIDs   []string `json:"message_ids"`
	MessageID    string   `json:"message_id"`
	Segments     int      `json:"segments"`
	LatencyMS    int64    `json:"latency_ms"`
	Active       bool     `json:"active"`
	Quoted       bool     `json:"quoted"`
	MessageRowID int64    `json:"message_row_id,omitempty"`
	RecordID     int64    `json:"record_id,omitempty"`
	// ActiveToday counts every proactive message this group received today
	// (manual ones included), because they share one platform quota.
	ActiveToday int `json:"active_today"`
}

// SendManual posts a text message to the conversation's group or C2C chat as the
// bot, from the admin panel.
//
// Design notes, in the order they matter:
//  1. It is always an ACTIVE message: there is no inbound msg_id to reply to, so
//     it needs the platform's proactive permission and spends the daily quota
//     that the bot's own fallbacks also draw on.
//  2. The platform is called BEFORE anything is persisted, so the panel can never
//     show a message that was not actually sent.
//  3. Over-long content is refused instead of truncated: silently cutting an
//     operator's message is worse than making them split it.
//  4. A successful send lands in the conversation as an assistant turn
//     (kind=manual), so the model knows what the bot's account said.
func (d *Dispatcher) SendManual(ctx context.Context, conversationID, text string, quoteMessageRowID int64) (*ManualSendResult, error) {
	s := d.settings.Effective()
	if !s.ManualSendEnabled {
		return nil, ErrManualSendDisabled
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, ErrManualSendEmpty
	}

	conv, err := d.st.GetConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	opts, err := d.manualSendTarget(conv)
	if err != nil {
		return nil, err
	}

	// 右键引用一条消息：拿它存下来的平台引用索引。
	quoted := false
	if quoteMessageRowID > 0 {
		cfg := d.settings.GroupConfig(conv.GroupOpenID)
		if conv.GroupOpenID != "" && !cfg.QuoteReply {
			return nil, ErrManualSendQuoteDisabled
		}
		row, err := d.st.GetMessage(ctx, quoteMessageRowID)
		if err != nil {
			return nil, err
		}
		if row.ID != 0 && row.QuoteIndex == "" {
			return nil, manualError(ErrManualSendQuoteUnavailable,
				"这条消息没有平台引用索引（可能是升级前写入的，或平台未返回），无法引用")
		}
		// 只允许引用本会话里的消息。
		ok, err := d.st.MessageInConversation(ctx, row.ID, conv.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, manualError(ErrManualSendNoTarget, "只能引用本会话里的消息")
		}
		opts.QuoteMessageID = row.QuoteIndex
		quoted = true
	}

	maxChars := s.ReplyMaxChars
	maxSegments := s.ManualSendMaxSegments
	// Split up front so an oversized message is rejected before anything leaves
	// the process (Sender.Send would otherwise truncate it).
	if need := len(qq.SplitText(text, maxChars)); need > maxSegments {
		return nil, manualError(ErrManualSendTooLong,
			"内容需要 %d 段，超过上限 %d 段（约 %d 字），请拆成多条发送",
			need, maxSegments, maxSegments*maxChars)
	}

	// Quota accounting first: a manual send competes with the bot's own replies
	// for the same per-group budget, and gets its own two ceilings on top.
	if conv.GroupOpenID != "" {
		cfg := d.settings.GroupConfig(conv.GroupOpenID)
		if !d.limiter.Allow("group:"+conv.GroupOpenID, cfg.RateLimitGroup) {
			return nil, manualError(ErrManualSendLimited, "该群每分钟的发送额度已用尽，请稍后再试")
		}
	}
	if !d.limiter.Allow("manual:"+conv.ID, s.ManualSendPerConversationQPM) {
		return nil, manualError(ErrManualSendLimited, "本会话每分钟最多发送 %d 条，请稍后再试", s.ManualSendPerConversationQPM)
	}
	if !d.limiter.Allow("manual", s.ManualSendPerMinute) {
		return nil, manualError(ErrManualSendLimited, "后台每分钟最多发送 %d 条，请稍后再试", s.ManualSendPerMinute)
	}

	// Ask the platform before posting: an unauthorized proactive message would
	// otherwise fail after we had already told the operator "sending…".
	if conv.Scene != store.SceneC2C && conv.GroupOpenID != "" && d.identity != nil {
		if state, ok := d.identity.GroupState(ctx, conv.GroupOpenID); ok && !state.AllowProactiveMsg {
			return nil, ErrManualSendProactive
		}
	}

	opts.Markdown = s.MarkdownEnabled
	opts.MaxChars = maxChars
	opts.MaxSegments = maxSegments
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, sendErr := d.sender.Send(sendCtx, opts, text)

	latency := int64(0)
	if res != nil {
		latency = res.LatencyMS
	}
	if sendErr != nil {
		d.metrics.RepliesFailed.Add(1)
		d.stats.RecordError()
		friendly := manualSendError(sendErr)
		d.log.Warn("manual send failed", "component", "sender",
			"conversation", conv.ID, "error", sendErr.Error())
		// Audit the raw platform error: the operator sees the friendly text, the
		// log and the audit row keep the diagnosable one.
		d.recordManualFailure(ctx, conv, text, sendErr)
		return nil, friendly
	}

	messageIDs := []string{}
	segments := 0
	if res != nil {
		messageIDs = res.MessageIDs
		segments = res.Segments
	}
	messageID := ""
	if len(messageIDs) > 0 {
		messageID = messageIDs[0]
	}

	// Persist now that the platform accepted it.
	activeToday := d.countActiveToday(ctx, conv.GroupOpenID)
	refIdx := ""
	if res != nil {
		refIdx = res.RefIdx
	}
	rowID, _ := d.st.AppendMessageID(ctx, store.MessageInput{
		ConversationID: conv.ID, Role: "assistant", Content: text,
		Kind: store.KindManual, SpeakerOpenID: d.selfOpenID(conv.GroupOpenID),
		QuoteIndex: refIdx, Tokens: EstimateTokens(text),
	})
	if err := d.st.TouchConversation(ctx, conv.ID, EstimateTokens(text)); err != nil {
		d.log.Warn("manual send stored but counters not updated", "component", "store", "error", err.Error())
	}

	rec := &store.Record{
		Direction:      "out",
		Scene:          conv.Scene,
		ConversationID: conv.ID,
		GroupOpenID:    conv.GroupOpenID,
		MemberOpenID:   conv.MemberOpenID,
		MemberName:     conv.MemberName,
		Content:        text,
		MsgID:          messageID,
		Status:         store.StatusManual,
		// Always active: manual sends have no msg_id to ride on.
		Delivery:  store.DeliveryActive,
		LatencyMS: latency,
	}
	recID, err := d.st.AddRecord(ctx, rec)
	if err != nil {
		// The message IS out; failing to audit it must not hide that fact.
		d.log.Error("manual send delivered but the audit row could not be written",
			"component", "store", "conversation", conv.ID, "error", err.Error())
	} else {
		rec.ID = recID
	}
	d.metrics.RepliesSent.Add(1)
	d.stats.RecordOut()
	d.publish("message", rec)

	d.log.Info("manual send delivered", "component", "sender",
		"conversation", conv.ID, "segments", segments, "active_today", activeToday)

	return &ManualSendResult{
		MessageIDs:   messageIDs,
		MessageID:    messageID,
		Segments:     segments,
		LatencyMS:    latency,
		Active:       true,
		Quoted:       quoted,
		MessageRowID: rowID,
		RecordID:     recID,
		ActiveToday:  activeToday,
	}, nil
}

// recordManualFailure keeps a failed attempt visible in the audit trail; the
// operator needs to know whether the group ever saw it.
func (d *Dispatcher) recordManualFailure(ctx context.Context, conv *store.Conversation, text string, cause error) {
	rec := &store.Record{
		Direction:      "out",
		Scene:          conv.Scene,
		ConversationID: conv.ID,
		GroupOpenID:    conv.GroupOpenID,
		MemberOpenID:   conv.MemberOpenID,
		MemberName:     conv.MemberName,
		Content:        text,
		Status:         "failed",
		Error:          cause.Error(),
	}
	if _, err := d.st.AddRecord(ctx, rec); err != nil {
		d.log.Warn("failed to record the manual send failure", "component", "store", "error", err.Error())
	}
}

// manualSendTarget resolves where this conversation lives on the platform side.
func (d *Dispatcher) manualSendTarget(conv *store.Conversation) (qq.SendOptions, error) {
	if conv.Scene == store.SceneC2C {
		openid := strings.TrimSpace(conv.MemberOpenID)
		if openid == "" {
			// Rows written before C2C conversations carried the openid still
			// encode it in the id: "c2c:<user_openid>".
			if key, err := DecodeConversationID(conv.ID); err == nil {
				if rest, ok := strings.CutPrefix(key, "c2c:"); ok {
					openid = strings.TrimSpace(rest)
				}
			}
		}
		if openid == "" {
			return qq.SendOptions{}, ErrManualSendNoTarget
		}
		return qq.SendOptions{UserOpenID: openid}, nil
	}
	groupOpenID := strings.TrimSpace(conv.GroupOpenID)
	if groupOpenID == "" {
		return qq.SendOptions{}, ErrManualSendNoTarget
	}
	return qq.SendOptions{GroupOpenID: groupOpenID}, nil
}

// countActiveToday reports how much of the group's daily proactive quota is
// already spent, counting everything proactive (manual sends and fallbacks).
func (d *Dispatcher) countActiveToday(ctx context.Context, groupOpenID string) int {
	if groupOpenID == "" {
		return 0
	}
	n, err := d.st.CountActiveMessages(ctx, groupOpenID, startOfLocalDay(time.Now()))
	if err != nil {
		d.log.Warn("failed to count today's active messages", "component", "store", "error", err.Error())
		return 0
	}
	return n
}

// ActiveMessagesToday exposes the quota counter for the admin panel.
func (d *Dispatcher) ActiveMessagesToday(ctx context.Context, groupOpenID string) int {
	return d.countActiveToday(ctx, groupOpenID)
}

func startOfLocalDay(now time.Time) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
}

// manualSendError turns platform codes into something an operator can act on.
// The raw platform error still goes into the audit row, so nothing is lost.
func manualSendError(err error) error {
	switch {
	case qq.IsRateLimited(err):
		return manualError(ErrManualSendLimited,
			"平台主动消息频控已达上限（未认证 30 条/分钟、单群 1000 条/天），请稍后再试")
	case qq.CodeIs(err, qq.ErrBotMuted):
		return manualError(ErrManualSendRejected, "机器人在该群被禁言，无法发送")
	case qq.CodeIs(err, qq.ErrNotGroupMember):
		return manualError(ErrManualSendRejected, "机器人不在该群内，无法发送")
	case qq.CodeIs(err, qq.ErrContentBlocked):
		return manualError(ErrManualSendRejected, "内容被平台判定违规，已拒绝")
	case qq.CodeIs(err, qq.ErrContentTooLong):
		return manualError(ErrManualSendTooLong, "单条内容超过平台长度上限")
	case qq.CodeIs(err, qq.ErrURLNotAllowed):
		return manualError(ErrManualSendRejected, "该群不允许发送链接")
	}
	return err
}
