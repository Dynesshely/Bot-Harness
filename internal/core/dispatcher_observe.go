package core

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/store"
)

// observeBot records a message written by another bot in the group.
//
// A human's unaddressed message stays inside that member's own conversation
// (C1 isolation), but another bot is not a member whose privacy matters: its
// messages are part of the group's ambient conversation, so they are mirrored
// into every conversation of that group. They are never sent to the model and
// never trigger a reply.
func (d *Dispatcher) observeBot(ctx context.Context, p *qq.Payload, ev *qq.MessageEvent) {
	content := ev.Text()
	if strings.TrimSpace(content) == "" {
		return
	}
	groupOpenID := ev.GroupOpenID
	author := ev.Author.DisplayName()
	authorOpenID := ev.Author.MemberOpenID
	if authorOpenID == "" {
		authorOpenID = ev.Author.UserOpenID
	}

	// Idempotency: the platform may push the same event more than once.
	if seen, err := d.st.MarkSeen(ctx, ev.ID); err == nil && !seen {
		return
	}

	targets, err := d.st.ConversationsForGroup(ctx, groupOpenID, 50)
	if err != nil {
		d.log.Warn("failed to list the group's conversations", "component", "store", "error", err.Error())
		return
	}
	if len(targets) == 0 {
		// Nobody has a conversation in this group yet; there is no timeline to
		// show it on. The message is still visible in the message audit log.
		d.log.Debug("another bot spoke before any conversation existed in this group",
			"component", "dispatch", "group_openid", groupOpenID, "bot", author)
		return
	}

	tokens := EstimateTokens(content)
	stored := 0
	for _, convID := range targets {
		if err := d.st.AppendMessage(ctx, store.MessageInput{
			ConversationID: convID, Role: "user", Content: content,
			Speaker: author, SpeakerOpenID: authorOpenID,
			QuoteIndex: ev.MessageScene.Get("msg_idx"),
			Kind:       store.KindObservedBot, Tokens: tokens,
		}); err != nil {
			d.log.Warn("failed to store another bot's message", "component", "store",
				"conversation", convID, "error", err.Error())
			continue
		}
		if err := d.st.TouchObservedMessage(ctx, convID, tokens); err != nil {
			d.log.Warn("failed to update conversation counters", "component", "store", "error", err.Error())
		}
		stored++
	}
	if stored > 0 {
		d.metrics.MessagesObserved.Add(int64(stored))
		d.log.Debug("recorded another bot's message in the group timelines",
			"component", "dispatch", "group_openid", groupOpenID, "bot", author, "conversations", stored)
	}
	_ = p
}

// observe stores an unaddressed group message in the sender's conversation so
// the admin timeline shows the whole group, without it becoming model context.
func (d *Dispatcher) observe(in *Inbound, conv convTarget) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := d.st.EnsureConversation(ctx, &store.Conversation{
		ID:           conv.ID,
		Scene:        conv.Scene,
		GroupOpenID:  conv.GroupOpenID,
		MemberOpenID: conv.MemberOpenID,
		MemberName:   in.MemberName,
	}); err != nil {
		d.log.Warn("failed to persist conversation", "component", "store", "error", err.Error())
		return
	}
	tokens := EstimateTokens(in.Content)
	if err := d.st.AppendMessage(ctx, store.MessageInput{
		ConversationID: conv.ID, Role: "user", Content: in.Content,
		Speaker: in.MemberName, SpeakerOpenID: in.MemberOpenID,
		QuoteIndex: in.QuoteIndex,
		Kind:       store.KindObserved, Tokens: tokens,
		// 旁观发言正是主动额度要统计的那类人类消息。
		Addressed:   false,
		Attachments: in.Attachments,
	}); err != nil {
		d.log.Warn("failed to store observed message", "component", "store", "error", err.Error())
		return
	}
	if err := d.st.TouchObservedMessage(ctx, conv.ID, tokens); err != nil {
		d.log.Warn("failed to update conversation counters", "component", "store", "error", err.Error())
	}
	d.metrics.MessagesObserved.Add(1)
}

// logIgnored records why a message was dropped. Group drops are logged at INFO
// because they are the usual explanation for "the bot did not answer"; the
// reason is rate-limited so a busy E2 group cannot flood the log.
func (d *Dispatcher) logIgnored(in *Inbound, reason string) {
	level := slog.LevelDebug
	key := reason
	if in.Scene == "group" {
		level = slog.LevelInfo
	}
	if !d.ignoreLogAllow(key) {
		level = slog.LevelDebug
	}
	d.log.Log(context.Background(), level, "message ignored",
		"component", "dispatch",
		"reason", reason,
		"request_id", in.RequestID,
		"event", in.EventType,
		"scene", in.Scene,
		"group_openid", in.GroupOpenID,
		"member", in.MemberName,
	)
}

// ignoreLogAllow limits one log line per reason per interval.
func (d *Dispatcher) ignoreLogAllow(reason string) bool {
	d.ignoreLogMu.Lock()
	defer d.ignoreLogMu.Unlock()
	if d.ignoreLogAt == nil {
		d.ignoreLogAt = map[string]time.Time{}
	}
	now := time.Now()
	if last, ok := d.ignoreLogAt[reason]; ok && now.Sub(last) < 30*time.Second {
		return false
	}
	d.ignoreLogAt[reason] = now
	return true
}
