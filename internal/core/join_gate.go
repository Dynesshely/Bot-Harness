package core

// 自主加入（join gate）：未 @ 机器人的群消息先由一次轻量 LLM 调用判断要不要加入，
// 同一次调用顺带把最近消息切分成话题。默认关闭，关闭时回复管线与 v0.2.0 完全一致。
//
// 设计要点（见 docs/JOIN-GATE.md 与 AGENTS §21）：
//   - 只处理群聊的未 @ 文本消息；私聊、命令、空消息、其他机器人不在这里评估。
//   - 模型只输出 JSON，不生成回复；判定加入后仍走原管线（过滤、限流、上下文、润色）。
//   - 任何失败（并发额度、超时、非法 JSON）都 fail-closed：不加入，但写一条 decision。
//   - 频率是硬闸门（评估冷却、每小时/每天上限、最小加入间隔），超限不调模型。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// joinGateMarker lets tests and the fake provider recognise the gate call. It is
// part of the system prompt, which also carries the JSON contract.
const joinGateMarker = "【自主加入判断】"

// JSON 输出的硬约束。超限的 title/summary 会被截断，其余结构错误一律 fail-closed。
const (
	joinGateMinTopics       = 1
	joinGateMaxTopics       = 8
	joinGateTopicTitleRunes = 30
	joinGateTopicSumRunes   = 80
	joinGateReasonRunes     = 200
	joinGateMessageRunes    = 200
	joinGatePreviewRunes    = 80
	joinGateMaxTokens       = 600
)

// 调用的时间预算：拿不到并发额度就不评估（2s），单次调用 10s。
const (
	joinGateSemAcquireTimeout = 2 * time.Second
	joinGateCallTimeout       = 10 * time.Second
)

// 话题背景块只用于「别把旧话题生硬转回来」：只认 15 分钟内的分析，最多 400 字。
const (
	joinGateBackgroundWindow = 15 * time.Minute
	joinGateBackgroundRunes  = 400
)

const joinGateSystemPrompt = joinGateMarker + `你在一个 QQ 群里，要判断「最后那条刚出现的消息」值不值得你主动加入，并顺便把最近的群消息切分成话题。
加入规则（默认倾向不插话）：
- 只有这些情况才 join=true：被点名或有人明确求助；有人在延续你之前参与过的话题，而且你能补充；消息明显在等你回应；情绪需要被接住。
- 闲聊、两个人之间的私聊、争吵、广告、刷屏、其他机器人的发言、没有信息量的消息，一律 join=false。
- 不要为了显得活跃而加入；不确定时选 false。
- confidence 是 0 到 1 的小数；reason 用一句中文说明依据，最多 200 字。
话题规则：
- 输出 1 到 8 个话题；message_ids 只能使用输入里出现过的 [#id]（当前消息的 id 是 0）。
- title 最多 30 字，summary 最多 80 字；current_topic_id 可以留空。
只输出 JSON，不要解释、不要 markdown：
{"topics":[{"id":"t1","title":"…","summary":"…","message_ids":[12,13]}],"current_topic_id":"t1","join":true,"confidence":0.82,"reason":"…"}`

// joinGateTopic / joinGateOutput mirror the JSON contract exactly.
type joinGateTopic struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Summary    string  `json:"summary"`
	MessageIDs []int64 `json:"message_ids"`
}

type joinGateOutput struct {
	Topics         []joinGateTopic `json:"topics"`
	CurrentTopicID string          `json:"current_topic_id"`
	Join           bool            `json:"join"`
	Confidence     float64         `json:"confidence"`
	Reason         string          `json:"reason"`
}

// joinGateSnapshot is the cheap state the rate limits are made of.
type joinGateSnapshot struct {
	// LastDecisionAt is the newest evaluation, used as the cooldown clock.
	LastDecisionAt string
	JoinsLastHour  int
	JoinsLastDay   int
	// JoinsSinceMinInterval counts joins inside the minimum-interval window.
	JoinsSinceMinInterval int
}

// joinGateSkipReason returns a non-empty reason when a cheap gate blocks the
// evaluation before any model call. It is pure so the ordering is testable
// without a store.
func joinGateSkipReason(now time.Time, content string, snap joinGateSnapshot, s settings.Settings) string {
	trimmed := strings.TrimSpace(content)
	if len([]rune(trimmed)) < 2 {
		return "skip: 消息太短"
	}
	if strings.HasPrefix(trimmed, "/") {
		return "skip: 命令消息不评估"
	}
	if strings.TrimSpace(snap.LastDecisionAt) != "" {
		if last, err := time.Parse(time.RFC3339, snap.LastDecisionAt); err == nil {
			cooldown := time.Duration(s.JoinGateEvalCooldownSeconds) * time.Second
			if cooldown > 0 && now.Sub(last) < cooldown {
				return fmt.Sprintf("cooldown: 距上次评估不足 %d 秒", s.JoinGateEvalCooldownSeconds)
			}
		}
	}
	if s.JoinGateMaxPerHour > 0 && snap.JoinsLastHour >= s.JoinGateMaxPerHour {
		return fmt.Sprintf("rate: 每小时加入已达上限 %d", s.JoinGateMaxPerHour)
	}
	if s.JoinGateMaxPerDay > 0 && snap.JoinsLastDay >= s.JoinGateMaxPerDay {
		return fmt.Sprintf("rate: 每天加入已达上限 %d", s.JoinGateMaxPerDay)
	}
	if s.JoinGateMinIntervalMinutes > 0 && snap.JoinsSinceMinInterval > 0 {
		return fmt.Sprintf("rate: 距上次加入不足 %d 分钟", s.JoinGateMinIntervalMinutes)
	}
	return ""
}

// maybeJoinGroupChat runs the gate. It returns true only when the model decided
// to join and all cheap gates passed; every other outcome is false and, when
// possible, leaves a decision row explaining why.
func (d *Dispatcher) maybeJoinGroupChat(ctx context.Context, in *Inbound, conv convTarget, cfg settings.GroupConfig, s settings.Settings) bool {
	if d.st == nil || d.ai == nil {
		return false
	}
	if in.Scene != "group" || in.AtBot || !cfg.JoinGateEnabled || s.JoinGateMode == "off" {
		return false
	}
	// One gate evaluation per group at a time: two members can speak almost
	// simultaneously, and without this both could pass the cooldown check and
	// spend two model calls (and write two decisions) for the same moment.
	if d.locks != nil {
		unlock, ok := d.locks.TryLock("join_gate:"+in.GroupOpenID, 2*time.Second)
		if !ok {
			d.metrics.JoinGateSkips.Add(1)
			d.recordJoinDecision(ctx, in, conv.ID, false, 0, "skip: 同群评估进行中", "", "", 0)
			return false
		}
		defer unlock()
	}
	start := time.Now()

	// Cheap gates first: they must never spend a model call.
	lastAt, err := d.st.LastJoinDecisionAt(ctx, in.GroupOpenID)
	if err != nil {
		d.metrics.JoinGateErrors.Add(1)
		d.log.Warn("join gate: failed to read the last decision", "component", "join_gate",
			"group_openid", in.GroupOpenID, "error", err.Error())
		return false
	}
	joinsLastHour, err := d.st.CountJoinDecisionsSince(ctx, in.GroupOpenID,
		time.Now().Add(-time.Hour).Format(time.RFC3339), true)
	if err != nil {
		d.metrics.JoinGateErrors.Add(1)
		d.log.Warn("join gate: failed to count hourly joins", "component", "join_gate",
			"group_openid", in.GroupOpenID, "error", err.Error())
		return false
	}
	joinsLastDay, err := d.st.CountJoinDecisionsSince(ctx, in.GroupOpenID,
		time.Now().Add(-24*time.Hour).Format(time.RFC3339), true)
	if err != nil {
		d.metrics.JoinGateErrors.Add(1)
		return false
	}
	joinsInMinInterval := 0
	if s.JoinGateMinIntervalMinutes > 0 {
		joinsInMinInterval, err = d.st.CountJoinDecisionsSince(ctx, in.GroupOpenID,
			time.Now().Add(-time.Duration(s.JoinGateMinIntervalMinutes)*time.Minute).Format(time.RFC3339), true)
		if err != nil {
			d.metrics.JoinGateErrors.Add(1)
			return false
		}
	}
	snap := joinGateSnapshot{
		LastDecisionAt:        lastAt,
		JoinsLastHour:         joinsLastHour,
		JoinsLastDay:          joinsLastDay,
		JoinsSinceMinInterval: joinsInMinInterval,
	}
	if reason := joinGateSkipReason(time.Now(), in.Content, snap, s); reason != "" {
		d.metrics.JoinGateSkips.Add(1)
		d.recordJoinDecision(ctx, in, conv.ID, false, 0, reason, "", "", time.Since(start))
		d.log.Debug("join gate skipped", "component", "join_gate",
			"group_openid", in.GroupOpenID, "reason", reason)
		return false
	}

	// Build the context window. A store failure is fail-closed, like any other
	// error: a gate that cannot see the room must not guess.
	recent, err := d.st.RecentGroupMessages(ctx, in.GroupOpenID, s.JoinGateWindow)
	if err != nil {
		d.metrics.JoinGateErrors.Add(1)
		d.recordJoinDecision(ctx, in, conv.ID, false, 0, "gate error: 读取群消息失败", "", "", time.Since(start))
		d.log.Warn("join gate: failed to read recent messages", "component", "join_gate",
			"group_openid", in.GroupOpenID, "error", err.Error())
		return false
	}
	contextText, allowedIDs := buildJoinGateContext(recent, conv.ID, in)

	model := strings.TrimSpace(s.JoinGateModel)
	if model == "" {
		model = s.Model
	}
	if model == "" {
		d.metrics.JoinGateErrors.Add(1)
		d.recordJoinDecision(ctx, in, conv.ID, false, 0, "gate error: 未配置模型", "", "", time.Since(start))
		return false
	}

	semCtx, semCancel := context.WithTimeout(ctx, joinGateSemAcquireTimeout)
	err = d.aiSem.Acquire(semCtx)
	semCancel()
	if err != nil {
		d.metrics.JoinGateErrors.Add(1)
		d.recordJoinDecision(ctx, in, conv.ID, false, 0, "gate error: 等待模型并发额度超时", "", model, time.Since(start))
		d.log.Debug("join gate: no model permit within the budget", "component", "join_gate",
			"group_openid", in.GroupOpenID)
		return false
	}
	defer d.aiSem.Release()

	d.metrics.JoinGateEvals.Add(1)
	callCtx, cancel := context.WithTimeout(ctx, joinGateCallTimeout)
	defer cancel()
	res, err := d.ai.Chat(callCtx, ai.ChatRequest{
		Model:       model,
		Messages:    []ai.Message{{Role: ai.RoleSystem, Content: joinGateSystemPrompt}, {Role: ai.RoleUser, Content: contextText}},
		Temperature: 0.1,
		MaxTokens:   joinGateMaxTokens,
		Stream:      false,
	})
	if err != nil {
		d.metrics.JoinGateErrors.Add(1)
		d.recordJoinDecision(ctx, in, conv.ID, false, 0, "gate error: "+joinGateErrText(err), "", model, time.Since(start))
		d.log.Debug("join gate model call failed", "component", "join_gate",
			"group_openid", in.GroupOpenID, "error", err.Error())
		return false
	}
	verdict, err := parseJoinGateOutput(res.Text, allowedIDs)
	if err != nil {
		d.metrics.JoinGateErrors.Add(1)
		d.recordJoinDecision(ctx, in, conv.ID, false, 0, "gate error: "+err.Error(), "", model, time.Since(start))
		d.log.Debug("join gate: unparseable verdict", "component", "join_gate",
			"group_openid", in.GroupOpenID, "error", err.Error())
		return false
	}

	topicsJSON := ""
	if len(verdict.Topics) > 0 {
		if raw, err := json.Marshal(verdict.Topics); err == nil {
			topicsJSON = string(raw)
			if err := d.st.SaveJoinTopics(ctx, in.GroupOpenID, topicsJSON, time.Now().Format(time.RFC3339)); err != nil {
				d.log.Warn("join gate: failed to save topics", "component", "join_gate",
					"group_openid", in.GroupOpenID, "error", err.Error())
			} else {
				d.metrics.JoinGateTopics.Add(1)
			}
		}
	}

	joined := verdict.Join && verdict.Confidence >= s.JoinGateConfidence
	latency := time.Since(start)
	d.recordJoinDecision(ctx, in, conv.ID, joined, verdict.Confidence, verdict.Reason, topicsJSON, model, latency)
	if !joined {
		d.metrics.JoinGateSkips.Add(1)
		d.log.Debug("join gate chose silence", "component", "join_gate",
			"group_openid", in.GroupOpenID, "confidence", verdict.Confidence)
		return false
	}
	d.metrics.JoinGateJoins.Add(1)
	d.log.Info("join gate joined the conversation", "component", "join_gate",
		"group_openid", in.GroupOpenID, "confidence", verdict.Confidence,
		"topics", len(verdict.Topics), "model", model, "latency_ms", latency.Milliseconds())
	return true
}

// recordJoinDecision writes one audit row. It must never make a reply fail, so
// errors are only logged.
func (d *Dispatcher) recordJoinDecision(ctx context.Context, in *Inbound, conversationID string, joined bool,
	confidence float64, reason, topicsJSON, model string, latency time.Duration) {
	if d.st == nil || in == nil {
		return
	}
	decision := store.JoinDecision{
		GroupOpenID:    in.GroupOpenID,
		ConversationID: conversationID,
		MessageID:      in.MsgID,
		Speaker:        in.MemberName,
		Preview:        truncateRunes(strings.TrimSpace(in.Content), joinGatePreviewRunes),
		Joined:         joined,
		Confidence:     confidence,
		Reason:         truncateRunes(strings.TrimSpace(reason), joinGateReasonRunes),
		TopicsJSON:     topicsJSON,
		Model:          model,
		LatencyMS:      int(latency.Milliseconds()),
		CreatedAt:      time.Now().Format(time.RFC3339),
	}
	if err := d.st.AppendJoinDecision(ctx, decision); err != nil {
		d.log.Warn("join gate: failed to record a decision", "component", "join_gate",
			"group_openid", in.GroupOpenID, "error", err.Error())
	}
}

// joinGateErrText keeps stored failure reasons short and free of user content.
func joinGateErrText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "模型调用超时"
	}
	return truncateRunes(strings.TrimSpace(err.Error()), 120)
}

// buildJoinGateContext renders the chronological window and returns the set of
// local message ids the model is allowed to reference. Bot replies are shown as
// [bot] references instead of ordinary speakers; the current message is always
// the last line with id 0.
func buildJoinGateContext(recent []store.GroupMessage, convID string, current *Inbound) (string, map[int64]bool) {
	lines := make([]string, 0, len(recent)+1)
	allowed := make(map[int64]bool, len(recent)+1)
	botLines := make([]string, 0, 2)
	for _, m := range recent {
		content := truncateRunes(strings.TrimSpace(m.Content), joinGateMessageRunes)
		if content == "" {
			continue
		}
		speaker := strings.TrimSpace(m.Speaker)
		if speaker == "" {
			speaker = "群成员"
		}
		if m.Role == "assistant" {
			// 只参考同一个会话里机器人自己最近的两句，避免跨成员拼接。
			if m.ConversationID == convID {
				botLines = append(botLines, fmt.Sprintf("[bot] %s: %s", speaker, content))
				if len(botLines) > 2 {
					botLines = botLines[1:]
				}
			}
			continue
		}
		lines = append(lines, fmt.Sprintf("[#%d] %s: %s", m.ID, speaker, content))
		allowed[m.ID] = true
	}
	if len(botLines) > 0 {
		lines = append(lines, "（机器人自己刚才的发言，仅供参考）")
		lines = append(lines, botLines...)
	}
	// 当前消息永远是最后一行，id=0：模型判断的是「这条新消息」，不是历史。
	if current != nil {
		speaker := strings.TrimSpace(current.MemberName)
		if speaker == "" {
			speaker = "群成员"
		}
		content := truncateRunes(strings.TrimSpace(current.Content), joinGateMessageRunes)
		lines = append(lines, fmt.Sprintf("[#0] %s: %s", speaker, content))
		allowed[0] = true
	}
	return "最近群消息（按时间顺序，最后一条是当前消息）：\n" + strings.Join(lines, "\n"), allowed
}

// parseJoinGateOutput enforces the JSON contract. It deliberately accepts no
// repair: a malformed or out-of-range verdict is an error, and the caller turns
// that into a fail-closed decision.
func parseJoinGateOutput(raw string, allowedIDs map[int64]bool) (joinGateOutput, error) {
	var out joinGateOutput
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return out, errors.New("响应不是 JSON 对象")
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &out); err != nil {
		return out, fmt.Errorf("响应 JSON 非法: %w", err)
	}
	if len(out.Topics) < joinGateMinTopics || len(out.Topics) > joinGateMaxTopics {
		return out, fmt.Errorf("topics 数量必须在 %d-%d 之间", joinGateMinTopics, joinGateMaxTopics)
	}
	seen := map[string]bool{}
	for i := range out.Topics {
		t := &out.Topics[i]
		t.ID = strings.TrimSpace(t.ID)
		if t.ID == "" {
			return out, errors.New("topic.id 不能为空")
		}
		if seen[t.ID] {
			return out, fmt.Errorf("topic.id 重复: %s", t.ID)
		}
		seen[t.ID] = true
		t.Title = truncateRunes(strings.TrimSpace(t.Title), joinGateTopicTitleRunes)
		t.Summary = truncateRunes(strings.TrimSpace(t.Summary), joinGateTopicSumRunes)
		for _, id := range t.MessageIDs {
			if !allowedIDs[id] {
				return out, fmt.Errorf("topic.message_ids 含未提供的 id: %d", id)
			}
		}
	}
	if out.Confidence < 0 || out.Confidence > 1 {
		return out, errors.New("confidence 必须在 0-1 之间")
	}
	out.Reason = strings.TrimSpace(out.Reason)
	if len([]rune(out.Reason)) > joinGateReasonRunes {
		return out, errors.New("reason 超过 200 字")
	}
	out.CurrentTopicID = strings.TrimSpace(out.CurrentTopicID)
	return out, nil
}

// joinTopicsBlock renders the cached topic split as background. It is empty
// unless the cache is fresh: stale topics would drag the model back to a
// conversation that is already over.
func (d *Dispatcher) joinTopicsBlock(ctx context.Context, conv convTarget, cfg settings.GroupConfig) string {
	if d.st == nil || !cfg.JoinGateEnabled || conv.GroupOpenID == "" {
		return ""
	}
	// A slow database must not delay a reply; the block is optional background.
	readCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	topics, err := d.st.GetJoinTopics(readCtx, conv.GroupOpenID)
	if err != nil || topics == nil {
		return ""
	}
	return renderJoinTopics(topics.TopicsJSON, topics.AnalyzedAt, time.Now())
}

// renderJoinTopics is the pure part of the background block: parse, check the
// freshness window, and cap the result at 400 runes.
func renderJoinTopics(topicsJSON, analyzedAt string, now time.Time) string {
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(analyzedAt))
	if err != nil || now.Sub(at) > joinGateBackgroundWindow || now.Before(at) {
		return ""
	}
	var topics []joinGateTopic
	if err := json.Unmarshal([]byte(topicsJSON), &topics); err != nil || len(topics) == 0 {
		return ""
	}
	lines := make([]string, 0, len(topics))
	for _, t := range topics {
		title := strings.TrimSpace(t.Title)
		if title == "" {
			continue
		}
		summary := strings.TrimSpace(t.Summary)
		if summary == "" {
			lines = append(lines, "- "+title)
			continue
		}
		lines = append(lines, "- "+title+"："+summary)
	}
	if len(lines) == 0 {
		return ""
	}
	return truncateRunes("群里最近的话题（仅背景，不要强行转回）：\n"+strings.Join(lines, "\n"), joinGateBackgroundRunes)
}
