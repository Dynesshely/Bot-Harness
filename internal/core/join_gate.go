package core

// 自主加入（join gate）：未 @ 机器人的群消息先由一次轻量 LLM 调用判断要不要加入，
// 同一次调用顺带把最近消息切分成话题、判断这句话在对谁说。默认关闭，关闭时回复管线
// 与 v0.2.0 完全一致。
//
// 设计要点（见 docs/JOIN-GATE.md 与 AGENTS §21）：
//   - 只处理群聊的未 @ 文本消息；私聊、命令、空消息、其他机器人不在这里评估。
//     引用机器人的消息在 dispatcher 里已被提升成 AtBot，因此同样跳过本闸门。
//   - 模型只输出 JSON（action / addressed_to / topics），不生成回复；判定加入后仍走原管线。
//   - action 是三分类 join / wait / skip，不是布尔：wait 表示「话题还在发展，稍后再看」，
//     只能由该群的下一条新消息在 join_gate_wait_retry_seconds 之后触发提前重评估。
//   - 任何失败（并发额度、超时、非法 JSON、非法 action）都 fail-closed：不加入，但写一条 decision。
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

// Join-gate actions. They are the v2 contract: the boolean is gone and joined is
// derived from the action, so "wait a little" is finally expressible.
const (
	joinGateActionJoin = "join"
	joinGateActionWait = "wait"
	joinGateActionSkip = "skip"
)

// joinGateAddressed* is who the model thinks the last message is aimed at.
const (
	joinGateAddressedBot     = "bot"
	joinGateAddressedGroup   = "group"
	joinGateAddressedSomeone = "someone_else"
	joinGateAddressedUnknown = "unknown"
)

// Topic lifecycle states. The model proposes them, the code can only *downgrade*
// them by age: a topic whose newest message is older than the dormant window
// becomes dormant, older than the closed window becomes closed, and a topic the
// model already called closed never comes back because time passed.
const (
	joinGateTopicStatusActive  = "active"
	joinGateTopicStatusDormant = "dormant"
	joinGateTopicStatusClosed  = "closed"
)

// Topic types are a human-facing label only: they never decide the action, so an
// unknown value is normalised to "other" instead of failing the verdict.
const (
	joinGateTopicTypeOther    = "other"
	joinGateTopicTypeHelp     = "help"
	joinGateTopicTypeChat     = "chat"
	joinGateTopicTypeQuestion = "question"
	joinGateTopicTypePlan     = "plan"
	joinGateTopicTypeConflict = "conflict"
	joinGateTopicTypeJoke     = "joke"
)

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

// 引用识别的读库预算：识别是回复的前置优化，绝不允许拖慢一条本来能发的消息。
const joinGateQuoteLookupTimeout = 1 * time.Second

// 话题背景块只用于「别把旧话题生硬转回来」：只认 15 分钟内的分析，最多 400 字。
const (
	joinGateBackgroundWindow = 15 * time.Minute
	joinGateBackgroundRunes  = 400
)

// 上一次分析的话题最多回灌 8 条给闸门模型，整块不超过 500 字：
// 它只是「别旧事重提」的参考，不能把闸门的输入预算吃掉。
const (
	joinGatePrevTopicsMax     = 8
	joinGatePrevTopicsRunes   = 500
	joinGatePrevTopicSumRunes = 40
)

const joinGateSystemPrompt = joinGateMarker + `你在一个 QQ 群里，要判断「最后那条刚出现的消息」值不值得你主动加入，并顺便把最近的群消息切分成话题。

只输出一个 JSON 对象，不要输出解释、Markdown 代码块或回复正文。

先看谁在跟谁说话，再判断这条消息在对谁说（addressed_to）：
- bot：点名机器人、引用机器人、明确在等机器人回应；
- group：公开抛给群里的问题或话题（公开求助）；
- someone_else：在回复某个具体的人、两个人正在对话；
- unknown：判断不出来。
公开求助才可能是对群里说；两个人之间的对话、其他 bot 之间的对话、广告与争吵都不要插。

再在 join / wait / skip 里选一个（action）：
- join：addressed_to=bot（被点名 / 被引用），或 addressed_to=group 的公开求助 / 明确问群里，且你能补充有用信息；
- wait：话题还在发展（有人正在打字、问题还没定论、争论未结束），现在插话会打断，但过一会儿可能适合加入；
- skip：不该插话——闲聊寒暄、两人私聊、其他机器人之间的对话、争吵、广告 / 引流、你没有增量信息、内容不适合参与。

引用 / 点名 / 公开求助是强信号；两人私聊、其他 bot、广告、争吵中不插话；话题还在发展时选 wait。
不要为了显得活跃而加入；不确定时选 skip。
confidence 是 0 到 1 的小数；reason 用一句中文说明依据，最多 200 字。

话题规则：
- 输出 1 到 8 个话题；message_ids 只能使用输入里出现过的 [#id]（当前消息的 id 是 0）。
- title 最多 30 字，summary 最多 80 字；current_topic_id 可以留空。
- 每个话题可给 type：help / chat / question / plan / conflict / joke / other（缺省按 other）。
- 每个话题给 status：active（还在聊）/ dormant（暂时停了，新消息可以自然接回）/ closed（已经结束），
  以及 resumable（这个话题还值不值得被新消息接回）。
- 输入里的「上次话题」是上一次分析的缓存（含它自己的 status）。沿用它的话题 id 与状态：
  closed 的话题不要再接、不要旧事重提；dormant 的话题只有当前消息明显接回它时才可 wait / join；
  active 的话题按 addressed_to 判断。
只输出 JSON，不要解释、不要 markdown：
{"topics":[{"id":"t1","title":"…","summary":"…","message_ids":[12,13],"type":"question","status":"active","resumable":true}],"current_topic_id":"t1","addressed_to":"group","action":"join","confidence":0.82,"reason":"…"}`

// joinGateTopic / joinGateOutput mirror the JSON contract exactly.
type joinGateTopic struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Summary    string  `json:"summary"`
	MessageIDs []int64 `json:"message_ids"`
	// Type is a human-facing label (help / chat / question / plan / conflict /
	// joke / other). Unknown or empty values are normalised to "other": the label
	// must never be the reason a whole verdict is rejected.
	Type string `json:"type,omitempty"`
	// Status is the topic lifecycle (active / dormant / closed). An illegal value
	// is a parse error (fail-closed); empty means "active" so an older model that
	// does not know the field still works. The code only ever downgrades it.
	Status string `json:"status,omitempty"`
	// Resumable says whether a dormant topic may be picked up again by new
	// messages. closed topics are always non-resumable, enforced in parsing.
	Resumable bool `json:"resumable,omitempty"`
}

type joinGateOutput struct {
	Topics         []joinGateTopic `json:"topics"`
	CurrentTopicID string          `json:"current_topic_id"`
	AddressedTo    string          `json:"addressed_to"`
	Action         string          `json:"action"`
	Confidence     float64         `json:"confidence"`
	Reason         string          `json:"reason"`
}

// joinGateSnapshot is the cheap state the rate limits are made of.
type joinGateSnapshot struct {
	// LastDecisionAt is the newest model evaluation, used as the cooldown clock.
	LastDecisionAt string
	// LastAction is that evaluation's action; "wait" is what allows an early
	// re-evaluation once join_gate_wait_retry_seconds has passed.
	LastAction    string
	JoinsLastHour int
	JoinsLastDay  int
	// JoinsSinceMinInterval counts joins inside the minimum-interval window.
	JoinsSinceMinInterval int
}

// joinGateWaitRetryReady reports whether the previous verdict was "wait" and
// enough time has passed to look again. It only unlocks the evaluation cooldown:
// the hourly / daily join caps and the minimum join interval still apply.
func joinGateWaitRetryReady(now, last time.Time, snap joinGateSnapshot, s settings.Settings) bool {
	if snap.LastAction != joinGateActionWait || s.JoinGateWaitRetrySeconds <= 0 {
		return false
	}
	return !now.Before(last.Add(time.Duration(s.JoinGateWaitRetrySeconds) * time.Second))
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
			if cooldown > 0 && now.Sub(last) < cooldown && !joinGateWaitRetryReady(now, last, snap, s) {
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

// markReplyToBot 把「引用了机器人」提升成「被寻址」。
//
// 平台事件只给被引用消息的 ref_msg_idx（一个 REFIDX_… 令牌），要判断被引用的是不是
// 机器人的发言，只能拿它比对本群 assistant 消息的 quote_index。命中即 AtBot=true，
// 与 @ 走同一条快路径（跳过 join gate、跳过 trigger gate 与人味随机沉默）。
//
// 这是一次前置优化，方向与 join gate 相反：识别失败 **fail-open**（1s 超时、只记
// DEBUG、按未命中处理），绝不拖慢或中断一条本来能发的消息。
func (d *Dispatcher) markReplyToBot(ctx context.Context, in *Inbound) {
	if d.st == nil || in == nil || in.Scene != "group" {
		return
	}
	refIndex := strings.TrimSpace(in.RefMsgIndex)
	if refIndex == "" {
		return
	}
	lookupCtx, cancel := context.WithTimeout(ctx, joinGateQuoteLookupTimeout)
	defer cancel()
	hit, err := d.st.IsBotQuote(lookupCtx, in.GroupOpenID, refIndex)
	if err != nil {
		d.log.Debug("reply-to-bot lookup failed; treating the message as unaddressed",
			"component", "dispatch", "group_openid", in.GroupOpenID, "error", err.Error())
		return
	}
	if !hit {
		return
	}
	in.ReplyToBot = true
	in.AtBot = true
	// ReplyToBot 不改既有 status（审计语义不变），只留一条可解释的日志。
	d.log.Info("message quotes the bot; treating it as addressed",
		"component", "dispatch", "group_openid", in.GroupOpenID,
		"member_openid", in.MemberOpenID, "ref_msg_idx", refIndex)
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
			d.recordJoinDecision(ctx, in, conv.ID, joinGateActionSkip, false, 0, "skip: 同群评估进行中", "", "", 0)
			return false
		}
		defer unlock()
	}
	start := time.Now()

	// Cheap gates first: they must never spend a model call. The cooldown reads
	// the last *model* decision because a "wait" is allowed to unlock an early
	// re-evaluation after join_gate_wait_retry_seconds.
	lastDecision, err := d.st.LastJoinDecision(ctx, in.GroupOpenID)
	if err != nil {
		d.metrics.JoinGateErrors.Add(1)
		d.log.Warn("join gate: failed to read the last decision", "component", "join_gate",
			"group_openid", in.GroupOpenID, "error", err.Error())
		return false
	}
	snap := joinGateSnapshot{}
	if lastDecision != nil {
		snap.LastDecisionAt = lastDecision.CreatedAt
		snap.LastAction = lastDecision.Action
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
	snap.JoinsLastHour = joinsLastHour
	snap.JoinsLastDay = joinsLastDay
	snap.JoinsSinceMinInterval = joinsInMinInterval
	if reason := joinGateSkipReason(time.Now(), in.Content, snap, s); reason != "" {
		d.metrics.JoinGateSkips.Add(1)
		d.recordJoinDecision(ctx, in, conv.ID, joinGateActionSkip, false, 0, reason, "", "", time.Since(start))
		d.log.Debug("join gate skipped", "component", "join_gate",
			"group_openid", in.GroupOpenID, "reason", reason)
		return false
	}

	// Build the context window. A store failure is fail-closed, like any other
	// error: a gate that cannot see the room must not guess.
	recent, err := d.st.RecentGroupMessages(ctx, in.GroupOpenID, s.JoinGateWindow)
	if err != nil {
		d.metrics.JoinGateErrors.Add(1)
		d.recordJoinDecision(ctx, in, conv.ID, joinGateActionSkip, false, 0, "gate error: 读取群消息失败", "", "", time.Since(start))
		d.log.Warn("join gate: failed to read recent messages", "component", "join_gate",
			"group_openid", in.GroupOpenID, "error", err.Error())
		return false
	}
	contextText, allowedIDs := buildJoinGateContext(recent, conv.ID, in)
	// 上一次分析的话题状态是判断「是不是在旧事重提」的唯一依据：closed 不接、
	// dormant 只有明显接回才可 wait/join。缓存读失败只是少一块参考，不影响判定。
	analyzedAt := time.Now()
	createdAtByID := joinGateCreatedAtByID(recent)
	if prev := d.joinGatePreviousTopicsBlock(ctx, in.GroupOpenID, createdAtByID, analyzedAt, s); prev != "" {
		contextText = prev + "\n\n" + contextText
	}

	model := strings.TrimSpace(s.JoinGateModel)
	if model == "" {
		model = s.Model
	}
	if model == "" {
		d.metrics.JoinGateErrors.Add(1)
		d.recordJoinDecision(ctx, in, conv.ID, joinGateActionSkip, false, 0, "gate error: 未配置模型", "", "", time.Since(start))
		return false
	}

	semCtx, semCancel := context.WithTimeout(ctx, joinGateSemAcquireTimeout)
	err = d.aiSem.Acquire(semCtx)
	semCancel()
	if err != nil {
		d.metrics.JoinGateErrors.Add(1)
		d.recordJoinDecision(ctx, in, conv.ID, joinGateActionSkip, false, 0, "gate error: 等待模型并发额度超时", "", model, time.Since(start))
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
		d.recordJoinDecision(ctx, in, conv.ID, joinGateActionSkip, false, 0, "gate error: "+joinGateErrText(err), "", model, time.Since(start))
		d.log.Debug("join gate model call failed", "component", "join_gate",
			"group_openid", in.GroupOpenID, "error", err.Error())
		return false
	}
	verdict, err := parseJoinGateOutput(res.Text, allowedIDs)
	if err != nil {
		d.metrics.JoinGateErrors.Add(1)
		d.recordJoinDecision(ctx, in, conv.ID, joinGateActionSkip, false, 0, "gate error: "+err.Error(), "", model, time.Since(start))
		d.log.Debug("join gate: unparseable verdict", "component", "join_gate",
			"group_openid", in.GroupOpenID, "error", err.Error())
		return false
	}

	topicsJSON := ""
	// 保存前先跑一遍年龄兜底：缓存里必须是**已经降级过**的状态，否则下一次
	// 回灌给模型的「上次话题」会把一个早就结束的话题继续标成进行中。
	topics := applyJoinGateTopicLifecycle(verdict.Topics, createdAtByID, analyzedAt, analyzedAt, s)
	if len(topics) > 0 {
		if raw, err := json.Marshal(topics); err == nil {
			topicsJSON = string(raw)
			if err := d.st.SaveJoinTopics(ctx, in.GroupOpenID, topicsJSON, analyzedAt.Format(time.RFC3339)); err != nil {
				d.log.Warn("join gate: failed to save topics", "component", "join_gate",
					"group_openid", in.GroupOpenID, "error", err.Error())
			} else {
				d.metrics.JoinGateTopics.Add(1)
			}
		}
	}

	// joined 由 action 派生：低置信度的 join 降级成 skip（不插话），wait 也只是
	// 「再等等」，两者都不进入回复管线。
	action := verdict.Action
	joined := action == joinGateActionJoin && verdict.Confidence >= s.JoinGateConfidence
	if !joined && action == joinGateActionJoin {
		action = joinGateActionSkip
	}
	latency := time.Since(start)
	d.recordJoinDecision(ctx, in, conv.ID, action, joined, verdict.Confidence, verdict.Reason, topicsJSON, model, latency)
	if action == joinGateActionWait {
		d.metrics.JoinGateWaits.Add(1)
	}
	if !joined {
		d.metrics.JoinGateSkips.Add(1)
		d.log.Debug("join gate chose silence", "component", "join_gate",
			"group_openid", in.GroupOpenID, "action", action, "confidence", verdict.Confidence)
		return false
	}
	d.metrics.JoinGateJoins.Add(1)
	d.log.Info("join gate joined the conversation", "component", "join_gate",
		"group_openid", in.GroupOpenID, "confidence", verdict.Confidence,
		"addressed_to", verdict.AddressedTo, "topics", len(verdict.Topics),
		"model", model, "latency_ms", latency.Milliseconds())
	return true
}

// recordJoinDecision writes one audit row. It must never make a reply fail, so
// errors are only logged.
func (d *Dispatcher) recordJoinDecision(ctx context.Context, in *Inbound, conversationID, action string,
	joined bool, confidence float64, reason, topicsJSON, model string, latency time.Duration) {
	if d.st == nil || in == nil {
		return
	}
	if action == "" {
		action = joinGateActionSkip
	}
	decision := store.JoinDecision{
		GroupOpenID:    in.GroupOpenID,
		ConversationID: conversationID,
		MessageID:      in.MsgID,
		Speaker:        in.MemberName,
		Preview:        truncateRunes(strings.TrimSpace(in.Content), joinGatePreviewRunes),
		Joined:         joined,
		Action:         action,
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
//
// Quote relations are annotated from the only edge the store can prove: the
// current message's ref_msg_idx matched against a window message's quote_index.
// The platform never tells us which message an *older* message quoted, so no
// other edge is invented.
func buildJoinGateContext(recent []store.GroupMessage, convID string, current *Inbound) (string, map[int64]bool) {
	lines := make([]string, 0, len(recent)+1)
	allowed := make(map[int64]bool, len(recent)+1)
	botLines := make([]string, 0, 2)
	refIndex := ""
	if current != nil {
		refIndex = strings.TrimSpace(current.RefMsgIndex)
	}
	// quote_index → the id that owns it, so the current message's reference can be
	// resolved inside the window instead of being guessed.
	userByQuote := make(map[string]int64, len(recent))
	botByQuote := make(map[string]bool, 2)
	botQuoted := false
	for _, m := range recent {
		content := truncateRunes(strings.TrimSpace(m.Content), joinGateMessageRunes)
		if content == "" {
			continue
		}
		speaker := strings.TrimSpace(m.Speaker)
		if speaker == "" {
			speaker = "群成员"
		}
		quoted := refIndex != "" && strings.TrimSpace(m.QuoteIndex) == refIndex
		if m.Role == "assistant" {
			// 只参考同一个会话里机器人自己最近的两句，避免跨成员拼接。
			if m.ConversationID == convID {
				line := fmt.Sprintf("[bot] %s: %s", speaker, content)
				if quoted {
					line += "（当前消息引用了这条）"
					botQuoted = true
				}
				botLines = append(botLines, line)
				if len(botLines) > 2 {
					botLines = botLines[1:]
				}
				if q := strings.TrimSpace(m.QuoteIndex); q != "" {
					botByQuote[q] = true
				}
			}
			continue
		}
		line := fmt.Sprintf("[#%d] %s: %s", m.ID, speaker, content)
		if quoted {
			line += "（当前消息引用了这条）"
		}
		lines = append(lines, line)
		allowed[m.ID] = true
		if q := strings.TrimSpace(m.QuoteIndex); q != "" {
			userByQuote[q] = m.ID
		}
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
		line := fmt.Sprintf("[#0] %s: %s", speaker, content)
		if refIndex != "" {
			if id, ok := userByQuote[refIndex]; ok {
				line += fmt.Sprintf("（回复 [#%d]）", id)
			} else if botQuoted || botByQuote[refIndex] || current.ReplyToBot {
				line += "（回复 你）"
			} else {
				line += "（引用了窗口外的一条消息）"
			}
		}
		lines = append(lines, line)
		allowed[0] = true
	}
	return "最近群消息（按时间顺序，最后一条是当前消息）：\n" + strings.Join(lines, "\n"), allowed
}

// normalizeJoinGateAction maps the model's action onto the three-way contract.
// Anything else is an error: the gate fails closed, it never guesses.
func normalizeJoinGateAction(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case joinGateActionJoin:
		return joinGateActionJoin, nil
	case joinGateActionWait:
		return joinGateActionWait, nil
	case joinGateActionSkip:
		return joinGateActionSkip, nil
	}
	return "", fmt.Errorf("action 必须是 join/wait/skip，实际 %q", truncateRunes(strings.TrimSpace(raw), 16))
}

// normalizeAddressedTo maps who the message is aimed at. Unknown values are a
// normal "unknown" instead of an error: the field only tunes the reasoning
// narrative, while the action already carries the decision.
func normalizeAddressedTo(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case joinGateAddressedBot:
		return joinGateAddressedBot
	case joinGateAddressedGroup:
		return joinGateAddressedGroup
	case joinGateAddressedSomeone:
		return joinGateAddressedSomeone
	default:
		return joinGateAddressedUnknown
	}
}

// normalizeJoinGateTopicStatus maps the lifecycle state. Empty means "active" so
// a model that does not know the field still produces a usable verdict; an
// unknown value is a parse error because a typo must not silently pin a topic
// open forever (fail-closed).
func normalizeJoinGateTopicStatus(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return joinGateTopicStatusActive, nil
	case joinGateTopicStatusActive:
		return joinGateTopicStatusActive, nil
	case joinGateTopicStatusDormant:
		return joinGateTopicStatusDormant, nil
	case joinGateTopicStatusClosed:
		return joinGateTopicStatusClosed, nil
	}
	return "", fmt.Errorf("topic.status 必须是 active/dormant/closed，实际 %q", truncateRunes(strings.TrimSpace(raw), 16))
}

// joinGateTopicStatusLabel is the prompt-facing wording of one state.
func joinGateTopicStatusLabel(status string) string {
	switch status {
	case joinGateTopicStatusDormant:
		return "暂歇，可自然接回"
	case joinGateTopicStatusClosed:
		return "已结束，不要提"
	default:
		return "进行中"
	}
}

// resolveJoinGateTopicStatus applies the age backstop. It is a one-way ratchet:
// a topic with no new message for the dormant/closed window is downgraded, and
// nothing time passes can ever raise a status the model itself set.
func resolveJoinGateTopicStatus(status string, latest, now time.Time, s settings.Settings) string {
	if status != joinGateTopicStatusDormant && status != joinGateTopicStatusClosed {
		status = joinGateTopicStatusActive
	}
	if latest.IsZero() || now.Before(latest) {
		// 没有可用的消息时间（例如全在窗口外）或时钟偏差：保持模型给的判断。
		return status
	}
	age := now.Sub(latest)
	if s.JoinGateTopicClosedMinutes > 0 && age >= time.Duration(s.JoinGateTopicClosedMinutes)*time.Minute {
		return joinGateTopicStatusClosed
	}
	if s.JoinGateTopicDormantMinutes > 0 && age >= time.Duration(s.JoinGateTopicDormantMinutes)*time.Minute {
		if status == joinGateTopicStatusActive {
			return joinGateTopicStatusDormant
		}
	}
	return status
}

// joinGateCreatedAtByID indexes the window so a topic's newest message can be
// found by the ids the model returned. Unparseable timestamps are skipped: they
// must not be read as "the beginning of time" and close a live topic.
func joinGateCreatedAtByID(recent []store.GroupMessage) map[int64]time.Time {
	out := make(map[int64]time.Time, len(recent))
	for _, m := range recent {
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(m.CreatedAt))
		if err != nil {
			continue
		}
		out[m.ID] = at
	}
	return out
}

// joinGateTopicLatest is the newest evidence the code has for one topic: the
// newest window message it lists, falling back to the analysis instant (the
// moment the model last looked at the room). Zero means "unknown".
func joinGateTopicLatest(t joinGateTopic, createdAtByID map[int64]time.Time, analyzedAt time.Time) time.Time {
	var newest time.Time
	for _, id := range t.MessageIDs {
		if at, ok := createdAtByID[id]; ok && at.After(newest) {
			newest = at
		}
	}
	if newest.IsZero() {
		return analyzedAt
	}
	return newest
}

// applyJoinGateTopicLifecycle normalises status/resumable and downgrades by age.
// It runs on every saving path, so the cache always carries the effective state.
func applyJoinGateTopicLifecycle(topics []joinGateTopic, createdAtByID map[int64]time.Time, analyzedAt, now time.Time, s settings.Settings) []joinGateTopic {
	for i := range topics {
		t := &topics[i]
		if t.Status == "" {
			t.Status = joinGateTopicStatusActive
		}
		latest := joinGateTopicLatest(*t, createdAtByID, analyzedAt)
		t.Status = resolveJoinGateTopicStatus(t.Status, latest, now, s)
		switch t.Status {
		case joinGateTopicStatusClosed:
			// closed 绝不「可接回」，不论模型写了什么。
			t.Resumable = false
		case joinGateTopicStatusActive:
			t.Resumable = true
		}
	}
	return topics
}

// renderJoinGatePreviousTopics is the 「上次话题」input for the gate model: at
// most 8 topics, each with its cached status and the analysis instant, capped at
// 500 runes. It is deliberately separate from the reply-time background block:
// the gate needs the *statuses* to avoid reviving closed threads, while the
// reply model only needs a light hint.
func renderJoinGatePreviousTopics(topicsJSON, analyzedAt string, createdAtByID map[int64]time.Time, now time.Time, s settings.Settings) string {
	var topics []joinGateTopic
	if err := json.Unmarshal([]byte(strings.TrimSpace(topicsJSON)), &topics); err != nil || len(topics) == 0 {
		return ""
	}
	if len(topics) > joinGatePrevTopicsMax {
		topics = topics[:joinGatePrevTopicsMax]
	}
	at, atErr := time.Parse(time.RFC3339, strings.TrimSpace(analyzedAt))
	topics = applyJoinGateTopicLifecycle(topics, createdAtByID, at, now, s)
	lines := make([]string, 0, len(topics))
	for _, t := range topics {
		title := strings.TrimSpace(t.Title)
		if title == "" {
			continue
		}
		line := "- " + title + " [" + joinGateTopicStatusLabel(t.Status) + "]"
		if t.Status == joinGateTopicStatusDormant && t.Resumable {
			line += "（可接回）"
		}
		if summary := strings.TrimSpace(t.Summary); summary != "" {
			line += "：" + truncateRunes(summary, joinGatePrevTopicSumRunes)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	header := "上次话题（仅用于判断是否旧事重提）"
	if atErr == nil {
		header = "上次话题（分析于 " + at.Local().Format("01-02 15:04") + "，仅用于判断是否旧事重提）"
	}
	return truncateRunes(header+"：\n"+strings.Join(lines, "\n"), joinGatePrevTopicsRunes)
}

// normalizeJoinGateTopicType maps the topic label. Unknown/empty → other.
func normalizeJoinGateTopicType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case joinGateTopicTypeHelp:
		return joinGateTopicTypeHelp
	case joinGateTopicTypeChat:
		return joinGateTopicTypeChat
	case joinGateTopicTypeQuestion:
		return joinGateTopicTypeQuestion
	case joinGateTopicTypePlan:
		return joinGateTopicTypePlan
	case joinGateTopicTypeConflict:
		return joinGateTopicTypeConflict
	case joinGateTopicTypeJoke:
		return joinGateTopicTypeJoke
	default:
		return joinGateTopicTypeOther
	}
}

// parseJoinGateOutput enforces the JSON contract. It deliberately accepts no
// repair: a malformed verdict, an illegal action or a bogus message id is an
// error, and the caller turns that into a fail-closed skip decision.
func parseJoinGateOutput(raw string, allowedIDs map[int64]bool) (joinGateOutput, error) {
	var out joinGateOutput
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return out, errors.New("响应不是 JSON 对象")
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &out); err != nil {
		return out, fmt.Errorf("响应 JSON 非法: %w", err)
	}
	action, err := normalizeJoinGateAction(out.Action)
	if err != nil {
		return out, err
	}
	out.Action = action
	out.AddressedTo = normalizeAddressedTo(out.AddressedTo)
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
		t.Type = normalizeJoinGateTopicType(t.Type)
		status, err := normalizeJoinGateTopicStatus(t.Status)
		if err != nil {
			return out, err
		}
		t.Status = status
		if status == joinGateTopicStatusClosed {
			// 已结束的话题不可能「可接回」：模型写反了也按结束处理，
			// 否则下游会用 resumable 把 closed 的话术放回来。
			t.Resumable = false
		}
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

// joinGatePreviousTopicsBlock reads the cached topic split and renders it as the
// 「上次话题」input for the gate model. Unlike the reply-time block it has no
// freshness window: the older a closed topic is, the more important it is that
// the gate knows not to drag it back up.
func (d *Dispatcher) joinGatePreviousTopicsBlock(ctx context.Context, groupOpenID string, createdAtByID map[int64]time.Time, now time.Time, s settings.Settings) string {
	if d.st == nil || strings.TrimSpace(groupOpenID) == "" {
		return ""
	}
	// A slow database must not delay the gate; the block is optional background.
	readCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	topics, err := d.st.GetJoinTopics(readCtx, groupOpenID)
	if err != nil || topics == nil {
		return ""
	}
	return renderJoinGatePreviousTopics(topics.TopicsJSON, topics.AnalyzedAt, createdAtByID, now, s)
}

// renderJoinTopics is the pure part of the reply-time background block: parse,
// check the freshness window, and cap the result at 400 runes. Each line carries
// its lifecycle label (进行中 / 暂歇，可自然接回 / 已结束，不要提) so the reply
// model does not resurrect a thread that is over. When every cached topic is
// closed the block is not injected at all: there is nothing worth saying.
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
	allClosed := true
	for _, t := range topics {
		title := strings.TrimSpace(t.Title)
		if title == "" {
			continue
		}
		status := t.Status
		if status == "" {
			status = joinGateTopicStatusActive
		}
		if status != joinGateTopicStatusClosed {
			allClosed = false
		}
		label := joinGateTopicStatusLabel(status)
		if kind := normalizeJoinGateTopicType(t.Type); kind != joinGateTopicTypeOther {
			label += " · " + kind
		}
		line := "- " + title + "（" + label + "）"
		if summary := strings.TrimSpace(t.Summary); summary != "" {
			line += "：" + summary
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 || allClosed {
		return ""
	}
	return truncateRunes("群里最近的话题（仅背景，不要强行转回）：\n"+strings.Join(lines, "\n"), joinGateBackgroundRunes)
}
