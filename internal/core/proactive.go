package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// Proactive levels. "auto" lets the bot speak on its own; anything else keeps it
// reaction-only.
const (
	ProactiveOff  = "off"
	ProactiveAuto = "auto"
)

// proactiveTick is how often each group is considered. Given the credit model
// this is a cheap check most of the time.
const proactiveTick = 2 * time.Minute

// proactiveActivityWindow is how recently humans must have spoken for the bot to
// consider joining: talking into a dead group is exactly the rude behaviour the
// fuses exist to prevent.
const proactiveActivityWindow = 30 * time.Minute

// creditsFor turns "how many human messages happened" into spendable credits.
//
// This is the ratio budget from D-034: every `ratio` human messages buy one
// unprompted message, and at most `max` are banked so a long silence cannot
// suddenly produce a burst.
//
// "Human messages" means ambient chatter only (store.CountHumanMessagesSince):
// questions asked of the bot do not buy the right to speak unprompted. An at_only
// group only ever receives addressed messages, so it earns zero credits and the
// feature stays dormant there by construction.
func creditsFor(humanMessages, ratio, max int) int {
	if ratio < 1 || humanMessages <= 0 {
		return 0
	}
	n := humanMessages / ratio
	if n > max {
		n = max
	}
	return n
}

// inQuietHours reports whether the clock is inside the quiet window, which may
// wrap around midnight (23 → 8).
func inQuietHours(now time.Time, start, end int) bool {
	if start == end {
		return false
	}
	hour := now.Hour()
	if start < end {
		return hour >= start && hour < end
	}
	return hour >= start || hour < end
}

// proactiveGate is the set of cheap reasons not to speak. Returned as a string
// so the audit trail can explain every decision.
//
// SilentStreak note: it counts "we spoke and nobody answered", but in the normal
// flow it can never be >= 2 while credits exist — no human reply means no
// credits, and the first human message resets the streak before credits are
// computed. The real fuse against talking to a dead group is therefore the credit
// gate; this check is defence in depth for a state only a manual/imported row can
// produce.
func proactiveGate(s settings.Settings, st store.ProactiveState, now time.Time, humanMessages int) (int, string) {
	if s.ProactiveLevel != ProactiveAuto {
		return 0, "全局未开启"
	}
	credits := creditsFor(humanMessages, s.ProactiveRatio, s.ProactiveMaxCredits)
	if credits < 1 {
		return 0, fmt.Sprintf("额度不足（%d 条人类消息，比例 %d）", humanMessages, s.ProactiveRatio)
	}
	if st.SilentStreak >= 2 {
		return 0, "连续两次没人理，已降级为只做委托"
	}
	if inQuietHours(now, s.ProactiveQuietStart, s.ProactiveQuietEnd) {
		return 0, "静默时段"
	}
	if st.LastSentAt != "" {
		last, err := time.Parse(time.RFC3339, st.LastSentAt)
		if err == nil && now.Sub(last) < time.Duration(s.ProactiveMinIntervalMinutes)*time.Minute {
			return 0, fmt.Sprintf("距上次主动发言不到 %d 分钟", s.ProactiveMinIntervalMinutes)
		}
	}
	return credits, ""
}

// RunProactiveLoop considers every eligible group on a slow tick.
func (d *Dispatcher) RunProactiveLoop(ctx context.Context) {
	if d.st == nil || d.sender == nil {
		return
	}
	ticker := time.NewTicker(proactiveTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			d.RunProactiveCheck(ctx, now)
		}
	}
}

// RunProactiveCheck runs one pass. Exported so tests (and a future "try now"
// admin action) can drive it deterministically.
func (d *Dispatcher) RunProactiveCheck(ctx context.Context, now time.Time) {
	s := d.settings.Effective()
	if s.ProactiveLevel != ProactiveAuto {
		return
	}
	groups, err := d.st.DistinctGroupOpenIDs(ctx)
	if err != nil {
		d.log.Warn("failed to list groups for proactive check", "component", "proactive", "error", err.Error())
		return
	}
	for _, groupOpenID := range groups {
		if ctx.Err() != nil {
			return
		}
		d.checkGroup(ctx, s, groupOpenID, now)
	}
}

// checkGroup decides about one group and, when the model wants to speak, sends.
func (d *Dispatcher) checkGroup(ctx context.Context, s settings.Settings, groupOpenID string, now time.Time) {
	cfg := d.settings.GroupConfig(groupOpenID)
	if cfg.ProactiveLevel != ProactiveAuto {
		return
	}

	state, err := d.st.GetProactiveState(ctx, groupOpenID)
	if err != nil {
		return
	}
	since := now.Add(-24 * time.Hour)
	if state.LastSentAt != "" {
		if last, err := time.Parse(time.RFC3339, state.LastSentAt); err == nil {
			since = last
		}
	}
	humanMessages, err := d.st.CountHumanMessagesSince(ctx, groupOpenID, since)
	if err != nil {
		return
	}

	// 上一次主动发言之后有没有人说话？没有就累计「自说自话」次数。
	if state.LastSentAt != "" {
		if humanMessages > 0 {
			state.SilentStreak = 0
		} else if state.SilentStreak < 99 {
			state.SilentStreak++
		}
	}

	credits, reason := proactiveGate(s, state, now, humanMessages)
	if credits < 1 {
		d.log.Debug("proactive skipped", "component", "proactive", "group", groupOpenID, "reason", reason)
		d.saveProactiveState(ctx, state)
		return
	}
	// 心情/精力低的时候少主动开口：只按概率降频，不动额度与其它门。
	if d.affectInitiativeSkip(ctx, groupOpenID, now) {
		d.log.Debug("proactive skipped by affect initiative", "component", "proactive",
			"group", groupOpenID)
		return
	}

	// 最近半小时没人说话就不插嘴：不打断冷场。
	busySince, err := d.st.CountHumanMessagesSince(ctx, groupOpenID, now.Add(-proactiveActivityWindow))
	if err != nil || busySince == 0 {
		d.saveProactiveState(ctx, state)
		return
	}

	text, why, err := d.reflect(ctx, s, groupOpenID)
	if err != nil {
		d.log.Debug("proactive reflection failed", "component", "proactive", "error", err.Error())
		return
	}
	if strings.TrimSpace(text) == "" {
		d.log.Debug("proactive reflection chose silence", "component", "proactive",
			"group", groupOpenID, "why", why)
		return
	}
	// 发送前的最后一道心情闸：能量/心情太低或暂停中就不打扰。
	if skip, why := d.affectProactiveSkip(ctx, groupOpenID, now); skip {
		d.log.Debug("proactive skipped by affect", "component", "proactive",
			"group", groupOpenID, "reason", why)
		return
	}

	if err := d.sendProactive(ctx, s, groupOpenID, text); err != nil {
		d.log.Warn("proactive send failed", "component", "proactive",
			"group", groupOpenID, "error", err.Error())
		return
	}

	state.LastSentAt = now.Format(time.RFC3339)
	state.Sends++
	state.LastReason = strings.TrimSpace(why)
	d.saveProactiveState(ctx, state)
	d.log.Info("proactive message sent", "component", "proactive", "group", groupOpenID,
		"why", why, "credits_before", credits)
}

func (d *Dispatcher) saveProactiveState(ctx context.Context, st store.ProactiveState) {
	if err := d.st.SaveProactiveState(ctx, st); err != nil {
		d.log.Debug("failed to save proactive state", "component", "proactive", "error", err.Error())
	}
}

// reflectPrompt asks for a decision, not a conversation: JSON only, and silence
// must be an easy answer.
const reflectPrompt = `你在一个 QQ 群里。下面是最近的对话。你有机会主动说一句话，也可以什么都不说。
规则：
- 大多数情况下应该沉默：没人问问题、话题已经结束、或者你只是被顺便提到，就不要说。
- 只有在确实能帮上忙、或者接话明显自然时才开口。
- 开口就一句话（不超过 40 字），口语化，不要复述上下文，不要用列表。
- 不要提「我主动」「我注意到」这类元信息。
只输出 JSON：{"speak": true/false, "text": "要说的话", "reason": "为什么说 / 为什么不说"}
最近对话：
%s`

// reflect runs the decision call and parses the verdict.
func (d *Dispatcher) reflect(ctx context.Context, s settings.Settings, groupOpenID string) (string, string, error) {
	convs, err := d.st.ConversationsForGroup(ctx, groupOpenID, 5)
	if err != nil || len(convs) == 0 {
		return "", "", fmt.Errorf("no conversations to reflect on")
	}
	var lines []string
	for _, convID := range convs {
		recent, err := d.st.RecentMessages(ctx, convID, 12, true)
		if err != nil {
			continue
		}
		for _, m := range recent {
			who := m.Speaker
			if who == "" {
				who = m.Role
			}
			lines = append(lines, fmt.Sprintf("%s：%s", who, truncateForLog(m.Content, 120)))
		}
	}
	if len(lines) == 0 {
		return "", "", fmt.Errorf("no recent messages")
	}
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}

	callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	res, err := d.ai.Chat(callCtx, ai.ChatRequest{
		Model:       s.Model,
		Messages:    []ai.Message{{Role: ai.RoleUser, Content: fmt.Sprintf(reflectPrompt, strings.Join(lines, "\n"))}},
		Temperature: 0.7,
		MaxTokens:   300,
		Stream:      false,
	})
	if err != nil {
		return "", "", err
	}

	var verdict struct {
		Speak  bool   `json:"speak"`
		Text   string `json:"text"`
		Reason string `json:"reason"`
	}
	raw := res.Text
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start || json.Unmarshal([]byte(raw[start:end+1]), &verdict) != nil {
		return "", "", fmt.Errorf("无法解析反思结果")
	}
	if !verdict.Speak {
		return "", verdict.Reason, nil
	}
	text := strings.TrimSpace(verdict.Text)
	if runes := []rune(text); len(runes) > 200 {
		text = string(runes[:200])
	}
	return text, verdict.Reason, nil
}

// sendProactive pushes one unprompted message as an active message.
func (d *Dispatcher) sendProactive(ctx context.Context, s settings.Settings, groupOpenID, text string) error {
	opts := qq.SendOptions{
		GroupOpenID: groupOpenID,
		Markdown:    s.MarkdownEnabled,
		MaxChars:    s.ReplyMaxChars,
		MaxSegments: 1, // one bubble: an unprompted multi-part answer feels like spam
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := d.sender.Send(sendCtx, opts, text)
	if err != nil {
		return err
	}
	d.metrics.RepliesSent.Add(1)
	if res != nil && len(res.MessageIDs) > 0 {
		d.metrics.ProactiveSent.Add(1)
	}
	return nil
}
