package core

import (
	"context"

	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
)

// 群里出现另一个机器人时的策略：只回应 @ 我们的，并限制连续互回次数。

// BotChainLimit caps how many bot-initiated replies may happen back to back
// before we stop and wait for a human. Two bots answering each other's
// @-mentions would otherwise loop forever.
const BotChainLimit = 5

// dispatchBotMessage decides what to do with a message authored by a bot.
//
//   - our own echo            -> dropped (otherwise we would answer ourselves)
//   - another bot @-ing us    -> answered, subject to reply_to_bots and a
//     consecutive-reply cap that breaks bot ping-pong
//   - any other bot message   -> recorded in the timeline only
func (d *Dispatcher) dispatchBotMessage(ctx context.Context, p *qq.Payload, ev *qq.MessageEvent, in *Inbound, s settings.Settings, addressedToBot bool) {
	if d.identity != nil && d.identity.IsSelfAuthor(ctx, ev.GroupOpenID, ev.Author) {
		d.log.Debug("ignoring our own message echoed back by the platform",
			"component", "dispatch", "group_openid", ev.GroupOpenID)
		return
	}

	// 按群覆盖优先于全局设置
	if !d.settings.GroupConfig(in.GroupOpenID).ReplyToBots {
		d.observeBot(ctx, p, ev)
		return
	}
	if !addressedToBot {
		// Deliberately stricter than for humans: a bot that merely chats must
		// never be answered, not even with trigger_mode=all_messages, otherwise
		// two bots would talk to each other forever.
		d.observeBot(ctx, p, ev)
		return
	}

	if d.botChainExceeded(chainKey(in)) {
		d.log.Warn("another bot has @-ed us repeatedly; stopping the chain to avoid a bot loop",
			"component", "dispatch",
			"group_openid", in.GroupOpenID, "bot", in.MemberName,
			"max_consecutive", BotChainLimit)
		d.observeBot(ctx, p, ev)
		return
	}
	d.bumpBotChain(chainKey(in))
	d.log.Info("answering another bot that mentioned us",
		"component", "dispatch", "group_openid", in.GroupOpenID, "bot", in.MemberName)
	d.route(in)
}

func (d *Dispatcher) bumpBotChain(conversationID string) {
	d.botChainMu.Lock()
	defer d.botChainMu.Unlock()
	if d.botChain == nil {
		d.botChain = map[string]int{}
	}
	d.botChain[conversationID]++
}

func (d *Dispatcher) botChainExceeded(conversationID string) bool {
	d.botChainMu.Lock()
	defer d.botChainMu.Unlock()
	return d.botChain[conversationID] >= BotChainLimit
}

func (d *Dispatcher) resetBotChain(in *Inbound) {
	d.botChainMu.Lock()
	defer d.botChainMu.Unlock()
	delete(d.botChain, chainKey(in))
}

// chainKey scopes the bot-loop breaker to the *group*, not to a conversation.
//
// In C1 a bot's message lands in that bot's own conversation while a human's
// message lands in the human's, so a per-conversation counter could never be
// reset by a human stepping in — which is exactly the escape hatch we need.
func chainKey(in *Inbound) string {
	if in.Scene == "c2c" {
		return "c2c:" + in.UserOpenID
	}
	return "group:" + in.GroupOpenID
}
