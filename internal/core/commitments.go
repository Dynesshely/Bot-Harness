package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
	"github.com/dynesshely/bot-harness/internal/tools"
)

// 承诺（open loop）追问。与定时提醒的区别只有一个：提醒发一次就结束，承诺会
// 一直追问到用户明确说「完成了 / 晚点再说 / 别问了」为止。
//
// 这里刻意没有频率上限、没有自动停止条件、没有代码静默时段：什么时候再问由
// 模型结合事项性质、优先级、人类作息、当前状态与记忆里的「勿扰」声明判断。
// 代码只保留两处技术防呆：两次追问至少隔 60 秒、最长不超过 180 天；以及每人
// 未收口承诺的数量上限（防误操作，不是限频）。

const (
	commitmentLease           = 2 * time.Minute
	commitmentSendBackoff     = 5 * time.Minute
	commitmentFallbackDelay   = 3 * time.Hour
	commitmentDefaultPostpone = 4 * time.Hour
	commitmentMinInterval     = 60 * time.Second
	commitmentMaxInterval     = 180 * 24 * time.Hour
	commitmentDecisionMarker  = "承诺追问决策"
)

// RunCommitmentScheduler ticks every second and nudges due commitments.
func (d *Dispatcher) RunCommitmentScheduler(ctx context.Context) {
	if d.st == nil || d.sender == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			d.RunDueCommitments(ctx, now)
		}
	}
}

// RunDueCommitments claims and delivers everything due. Exported so tests can
// drive one deterministic pass.
func (d *Dispatcher) RunDueCommitments(ctx context.Context, now time.Time) {
	if d.st == nil || d.sender == nil {
		return
	}
	s := d.settings.Effective()
	if !s.CommitmentEnabled {
		return
	}
	items, err := d.st.LeaseDueCommitments(ctx, now, 10, commitmentLease)
	if err != nil {
		d.log.Warn("failed to lease due commitments", "component", "commitments", "error", err.Error())
		return
	}
	for _, c := range items {
		d.deliverCommitment(ctx, s, c, now)
	}
}

// commitmentDecision is the model's verdict for one nudge.
type commitmentDecision struct {
	Message          string
	NextCheckSeconds float64
	Reason           string
}

// deliverCommitment asks the model when/how to nudge, sends that nudge, and
// reschedules. Any model/JSON failure falls back to a template plus a
// conservative 3h interval so a commitment can never get stuck.
func (d *Dispatcher) deliverCommitment(ctx context.Context, s settings.Settings, c store.Commitment, now time.Time) {
	decision, ok := d.decideCommitment(ctx, s, c, now)
	if !ok {
		decision = commitmentDecision{
			Message:          commitmentFallbackText(c),
			NextCheckSeconds: commitmentFallbackDelay.Seconds(),
			Reason:           "模型决策失败，使用模板与保守间隔",
		}
		d.log.Warn("commitment decision fell back to the template", "component", "commitments",
			"id", c.ID, "nudge_count", c.NudgeCount)
	}
	text := strings.TrimSpace(decision.Message)
	if text == "" {
		text = commitmentFallbackText(c)
	}
	if runes := []rune(text); len(runes) > 500 {
		text = string(runes[:500])
	}

	opts := qq.SendOptions{
		Markdown:    s.MarkdownEnabled,
		MaxChars:    s.ReplyMaxChars,
		MaxSegments: 1,
	}
	if c.GroupOpenID != "" {
		opts.GroupOpenID = c.GroupOpenID
	} else {
		opts.UserOpenID = firstNonEmpty(c.MemberOpenID, c.CreatorOpenID)
	}
	if opts.GroupOpenID == "" && opts.UserOpenID == "" {
		d.log.Warn("commitment has no sendable destination", "component", "commitments", "id", c.ID)
		_ = d.st.SetCommitmentNextCheck(ctx, c.ID, now.Add(commitmentSendBackoff).Format(time.RFC3339))
		return
	}

	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := d.sender.Send(sendCtx, opts, text)
	if err != nil {
		d.log.Warn("commitment nudge failed to send", "component", "commitments",
			"id", c.ID, "error", err.Error())
		_ = d.st.SetCommitmentNextCheck(ctx, c.ID, now.Add(commitmentSendBackoff).Format(time.RFC3339))
		return
	}

	interval := commitmentInterval(decision.NextCheckSeconds, commitmentFallbackDelay)
	next := now.Add(interval)
	rescheduled, err := d.st.RescheduleCommitment(ctx, c.ID,
		next.Format(time.RFC3339), now.Format(time.RFC3339), 1)
	if err != nil {
		d.log.Warn("failed to reschedule commitment", "component", "commitments",
			"id", c.ID, "error", err.Error())
	} else if !rescheduled {
		// The user closed this promise while the nudge was in flight. The row is
		// terminal now; do not write the message into the timeline and do not
		// count this as a nudge of a still-open loop.
		d.log.Info("commitment closed while the nudge was in flight; dropped",
			"component", "commitments", "id", c.ID)
		return
	}

	// Keep the timeline honest: the user saw this message, so the next turn must
	// know the bot said it. KindManual is the existing "the bot spoke without a
	// prompt" kind, which is exactly what a nudge is.
	if c.ConversationID != "" {
		if err := d.st.AppendMessage(ctx, store.MessageInput{
			ConversationID: c.ConversationID,
			Role:           "assistant",
			Content:        text,
			Kind:           store.KindManual,
			Tokens:         EstimateTokens(text),
		}); err != nil {
			d.log.Debug("failed to append the commitment nudge to the timeline",
				"component", "store", "error", err.Error())
		}
	}
	d.metrics.CommitmentNudges.Add(1)
	if res != nil {
		d.metrics.RepliesSent.Add(1)
	}
	d.log.Info("commitment nudged", "component", "commitments",
		"id", c.ID, "nudge_count", c.NudgeCount+1,
		"next_check_seconds", int(interval.Seconds()),
		"reason", truncateForLog(decision.Reason, 200))
}

// commitmentInterval clamps a model-chosen interval to the technical fuse.
func commitmentInterval(seconds float64, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	d := time.Duration(seconds * float64(time.Second))
	if d < commitmentMinInterval {
		return commitmentMinInterval
	}
	if d > commitmentMaxInterval {
		return commitmentMaxInterval
	}
	return d
}

func commitmentFallbackText(c store.Commitment) string {
	if c.NudgeCount > 0 {
		return fmt.Sprintf("再问一下「%s」——现在方便说一声进展吗？如果已经处理好了，回我一句就好。", c.Text)
	}
	return fmt.Sprintf("关于「%s」——现在方便说一声进展吗？看到回我一下就好。", c.Text)
}

// commitmentDecisionPrompt asks for a decision, not a conversation.
const commitmentDecisionPrompt = `你是「承诺追问决策器」。下面是用户之前让你提醒的一件事（open loop）。
请结合事项性质、优先级、已经追问的次数、上次追问时间、最近的聊天、对方当前的状态，以及记忆里关于「勿扰 / 作息」的条目，决定：
1) 这一次要发什么提醒或追问文案；
2) 下一次（如果用户仍然没有回应）隔多少秒再问。

规则：
- 文案要贴合原承诺的语气，像同一个人在跟进，不要机械重复上一次。
- 追问要有递进：可以是换个角度提醒、给一个更具体的行动建议，或者轻轻确认是不是已经做完了。
- 间隔参考人类生活常识：刚提醒完可以 10-20 分钟后再看；很久没回应可以隔几小时；对方说过「晚点 / 明天」就隔 1-3 天；睡眠、勿扰、开会、上课等时间要避开。
- 不要因为「已经问过几次」就放弃；只有用户明确说完成 / 延后 / 取消才收口。
- 如果记忆里明确写了某个时段不要打扰，把 next_check_seconds 排到那个时段结束之后。
只输出 JSON，不要输出别的：
{"message":"本次提醒/追问文案","next_check_seconds":N,"reason":"判断依据"}
`

// decideCommitment runs the model call and parses its JSON. ok=false means the
// caller must use the template + fallback interval.
func (d *Dispatcher) decideCommitment(ctx context.Context, s settings.Settings, c store.Commitment, now time.Time) (commitmentDecision, bool) {
	if d.ai == nil {
		return commitmentDecision{}, false
	}
	recent := d.recentCommitmentContext(ctx, c)
	memories := d.commitmentDoNotDisturbMemories(ctx, c)
	statuses := d.humanStatusDecisionLines(ctx, c.ConversationID)
	created, _ := time.Parse(time.RFC3339, c.CreatedAt)
	lastNudge := "从未"
	if c.LastNudgeAt != "" {
		if t, err := time.Parse(time.RFC3339, c.LastNudgeAt); err == nil {
			lastNudge = t.In(cstZone()).Format("2006-01-02 15:04")
		}
	}
	deadline := "未设置"
	if c.DeadlineAt != "" {
		if t, err := time.Parse(time.RFC3339, c.DeadlineAt); err == nil {
			deadline = t.In(cstZone()).Format("2006-01-02 15:04")
		}
	}
	prompt := fmt.Sprintf(commitmentDecisionPrompt+`
当前时间：%s（Asia/Shanghai）
承诺文本：%s
优先级：%d（0 低，3 高）
已追问次数：%d
上次追问时间：%s
截止时间：%s
创建时间：%s

最近 3 条会话消息：
%s

当前聊天对象状态推测：
%s

与勿扰/作息相关的记忆：
%s
`,
		now.In(cstZone()).Format("2006-01-02 15:04 Monday"),
		c.Text, c.Priority, c.NudgeCount, lastNudge, deadline,
		created.In(cstZone()).Format("2006-01-02 15:04"),
		recent, statuses, memories)

	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if d.aiSem != nil {
		if err := d.aiSem.Acquire(callCtx); err != nil {
			return commitmentDecision{}, false
		}
		defer d.aiSem.Release()
	}
	res, err := d.ai.Chat(callCtx, ai.ChatRequest{
		Model:       s.Model,
		Messages:    []ai.Message{{Role: ai.RoleSystem, Content: commitmentDecisionMarker}, {Role: ai.RoleUser, Content: prompt}},
		Temperature: 0.5,
		MaxTokens:   400,
		Stream:      false,
	})
	if err != nil {
		d.log.Warn("commitment decision model call failed", "component", "commitments",
			"id", c.ID, "error", err.Error())
		return commitmentDecision{}, false
	}
	var verdict struct {
		Message          string  `json:"message"`
		NextCheckSeconds float64 `json:"next_check_seconds"`
		Reason           string  `json:"reason"`
	}
	raw := strings.TrimSpace(res.Text)
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start || json.Unmarshal([]byte(raw[start:end+1]), &verdict) != nil {
		d.log.Warn("commitment decision was not valid JSON", "component", "commitments",
			"id", c.ID, "preview", truncateForLog(raw, 200))
		return commitmentDecision{}, false
	}
	if strings.TrimSpace(verdict.Message) == "" {
		d.log.Warn("commitment decision had an empty message", "component", "commitments", "id", c.ID)
		return commitmentDecision{}, false
	}
	return commitmentDecision{
		Message:          verdict.Message,
		NextCheckSeconds: verdict.NextCheckSeconds,
		Reason:           verdict.Reason,
	}, true
}

// recentCommitmentContext renders the last few messages of the commitment's
// conversation for the decision prompt.
func (d *Dispatcher) recentCommitmentContext(ctx context.Context, c store.Commitment) string {
	if c.ConversationID == "" {
		return "（无）"
	}
	items, err := d.st.RecentMessages(ctx, c.ConversationID, 3, true)
	if err != nil || len(items) == 0 {
		return "（无）"
	}
	lines := make([]string, 0, len(items))
	for _, m := range items {
		who := m.Speaker
		if who == "" {
			who = m.Role
		}
		lines = append(lines, fmt.Sprintf("%s：%s", who, truncateForLog(m.Content, 160)))
	}
	return strings.Join(lines, "\n")
}

// commitmentDoNotDisturbMemories returns only the memory entries that look like
// a quiet-hours declaration. The model is told to respect them; code does not
// impose a silent window on its own.
func (d *Dispatcher) commitmentDoNotDisturbMemories(ctx context.Context, c store.Commitment) string {
	scope, groupOpenID, memberOpenID := store.MemoryGroup, c.GroupOpenID, c.MemberOpenID
	if c.GroupOpenID == "" {
		scope, groupOpenID, memberOpenID = store.MemoryMember, "", c.CreatorOpenID
	}
	items, err := d.st.VisibleMemories(ctx, scope, groupOpenID, memberOpenID, "", 60)
	if err != nil {
		return "（无）"
	}
	keywords := []string{"勿扰", "不要打扰", "别打扰", "睡觉", "睡了", "休息", "安静", "静音",
		"免打扰", "晚点", "早上", "半夜", "凌晨", "夜里", "上班", "开会", "上课", "通勤", "忙"}
	matched := make([]string, 0, 8)
	for _, m := range items {
		for _, kw := range keywords {
			if strings.Contains(m.Text, kw) {
				matched = append(matched, "- "+m.Text)
				break
			}
		}
		if len(matched) >= 8 {
			break
		}
	}
	if len(matched) == 0 {
		return "（无）"
	}
	return strings.Join(matched, "\n")
}

// --- prompt injection ------------------------------------------------------

// commitmentBlock renders the open loops of this conversation/person and tells
// the model when to call ack_commitment. Empty when disabled or none.
func (d *Dispatcher) commitmentBlock(ctx context.Context, in *Inbound, conv convTarget, s settings.Settings, cfg settings.GroupConfig) string {
	if d.st == nil || !s.CommitmentEnabled || !s.ToolsEnabled || !cfg.ToolsEnabled {
		return ""
	}
	creator := firstNonEmpty(in.MemberOpenID, in.UserOpenID)
	if creator == "" {
		return ""
	}
	items, err := d.st.ListOpenCommitmentsOfCreator(ctx, creator, 50)
	if err != nil {
		return ""
	}
	open := make([]store.Commitment, 0, len(items))
	for _, c := range items {
		if c.ConversationID == conv.ID {
			open = append(open, c)
		}
	}
	if len(open) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("【未收口的承诺（open loop）】\n")
	for _, c := range open {
		last := "从未"
		if c.LastNudgeAt != "" {
			if t, err := time.Parse(time.RFC3339, c.LastNudgeAt); err == nil {
				last = t.In(cstZone()).Format("01-02 15:04")
			}
		}
		created := c.CreatedAt
		if t, err := time.Parse(time.RFC3339, c.CreatedAt); err == nil {
			created = t.In(cstZone()).Format("01-02 15:04")
		}
		fmt.Fprintf(&b, "- #%d %s（已追问 %d 次；上次追问 %s；创建于 %s）\n",
			c.ID, c.Text, c.NudgeCount, last, created)
	}
	b.WriteString("当用户表达这件事已经完成、要延后、或者要取消时，调用 ack_commitment 收口；" +
		"用户没提到就继续正常聊天，不要每轮都催。")
	return b.String()
}

// --- tools -----------------------------------------------------------------

// registerCommitmentTools wires the open-loop tools into the registry.
func (d *Dispatcher) registerCommitmentTools(reg *tools.Registry) {
	if reg == nil || d.st == nil {
		return
	}
	reg.Register(&createCommitmentTool{st: d.st, settings: d.settings})
	reg.Register(&listCommitmentsTool{st: d.st})
	reg.Register(&cancelCommitmentTool{st: d.st})
	reg.Register(&ackCommitmentTool{st: d.st, metrics: &d.metrics, log: d.log})
}

type createCommitmentTool struct {
	st       *store.Store
	settings *settings.Manager
}

func (t *createCommitmentTool) Name() string     { return "create_commitment" }
func (t *createCommitmentTool) Group() string    { return "commitment" }
func (t *createCommitmentTool) Risk() tools.Risk { return tools.SideEffect }
func (t *createCommitmentTool) Description() string {
	return "建立一个开放承诺（open loop）：到点提醒后，如果对方没回应，机器人会按常识继续追问，" +
		"直到对方明确说完成/延后/取消。只在对方明确要求「提醒我做 X」且需要后续跟进的场景使用。"
}
func (t *createCommitmentTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"text":{"type":"string","description":"承诺内容，例如「晚上八点吃药」"},
		"first_delay_seconds":{"type":"number","description":"第一次提醒在多少秒后（至少 60，最长 180 天）"},
		"at":{"type":"string","description":"可选：RFC3339 的首次提醒时间，与 first_delay_seconds 二选一"},
		"priority":{"type":"number","description":"0-3，0 最低、3 最高（默认 0）"},
		"deadline_at":{"type":"string","description":"可选：这件事的截止时间（RFC3339）"}},
		"required":["text"]}`)
}

func (t *createCommitmentTool) Run(ctx context.Context, raw json.RawMessage, env *tools.Env) (tools.Result, error) {
	var args struct {
		Text              string   `json:"text"`
		FirstDelaySeconds *float64 `json:"first_delay_seconds"`
		At                string   `json:"at"`
		Priority          *int     `json:"priority"`
		DeadlineAt        string   `json:"deadline_at"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return tools.Result{}, fmt.Errorf("参数需要 {\"text\": \"…\", \"first_delay_seconds\": 600}: %w", err)
	}
	s := t.settings.Effective()
	if !s.CommitmentEnabled {
		return tools.Result{}, fmt.Errorf("承诺追问功能已关闭")
	}
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return tools.Result{}, fmt.Errorf("text 不能为空")
	}
	if runes := []rune(text); len(runes) > 200 {
		text = string(runes[:200])
	}
	priority := 0
	if args.Priority != nil {
		priority = *args.Priority
	}
	if priority < 0 || priority > 3 {
		return tools.Result{}, fmt.Errorf("priority 必须在 0-3 之间")
	}

	now := time.Now()
	var next time.Time
	switch {
	case strings.TrimSpace(args.At) != "":
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(args.At))
		if err != nil {
			return tools.Result{}, fmt.Errorf("at 需要 RFC3339 时间：%w", err)
		}
		next = parsed
	case args.FirstDelaySeconds != nil:
		next = now.Add(time.Duration(*args.FirstDelaySeconds * float64(time.Second)))
	default:
		return tools.Result{}, fmt.Errorf("first_delay_seconds 与 at 至少给一个")
	}
	if next.Before(now.Add(commitmentMinInterval)) {
		return tools.Result{}, fmt.Errorf("首次提醒至少要在 60 秒之后")
	}
	if next.After(now.Add(commitmentMaxInterval)) {
		return tools.Result{}, fmt.Errorf("首次提醒太远（上限 180 天）")
	}
	deadline := ""
	if strings.TrimSpace(args.DeadlineAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(args.DeadlineAt))
		if err != nil {
			return tools.Result{}, fmt.Errorf("deadline_at 需要 RFC3339 时间：%w", err)
		}
		if parsed.Before(now) {
			return tools.Result{}, fmt.Errorf("deadline_at 不能在过去")
		}
		deadline = parsed.Format(time.RFC3339)
	}

	creator := commitmentCreator(env)
	if creator == "" {
		return tools.Result{}, fmt.Errorf("无法确定是谁在建立承诺，已取消（缺少 sender openid）")
	}
	open, err := t.st.OpenCommitmentCount(ctx, creator)
	if err != nil {
		return tools.Result{}, err
	}
	if open >= s.CommitmentMaxOpenPerMember {
		return tools.Result{}, fmt.Errorf("你还有 %d 条未收口的承诺（上限 %d 条），先处理几条再建新的",
			open, s.CommitmentMaxOpenPerMember)
	}
	groupOpenID, memberOpenID := taskDestination(env)
	id, err := t.st.AddCommitment(ctx, store.CommitmentInput{
		ConversationID: env.ConversationID,
		GroupOpenID:    groupOpenID,
		MemberOpenID:   memberOpenID,
		CreatorOpenID:  creator,
		Text:           text,
		Priority:       priority,
		DeadlineAt:     deadline,
		NextCheckAt:    next.Format(time.RFC3339),
	})
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Text: fmt.Sprintf("已建立承诺（#%d），第一次提醒在 %s：%s。之后你没回应的话，我会按情况继续跟进。",
		id, next.In(time.Local).Format("01-02 15:04"), text)}, nil
}

func commitmentCreator(env *tools.Env) string {
	if env == nil {
		return ""
	}
	return strings.TrimSpace(env.MemberOpenID)
}

type listCommitmentsTool struct{ st *store.Store }

func (t *listCommitmentsTool) Name() string     { return "list_commitments" }
func (t *listCommitmentsTool) Group() string    { return "commitment" }
func (t *listCommitmentsTool) Risk() tools.Risk { return tools.ReadOnly }
func (t *listCommitmentsTool) Description() string {
	return "列出当前会话里还没收口的承诺（含编号、已追问次数、下次追问时间）。"
}
func (t *listCommitmentsTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *listCommitmentsTool) Run(ctx context.Context, _ json.RawMessage, env *tools.Env) (tools.Result, error) {
	items, err := t.st.ListCommitments(ctx, env.ConversationID, commitmentCreator(env), 50)
	if err != nil {
		return tools.Result{}, err
	}
	var b strings.Builder
	n := 0
	for _, c := range items {
		if c.Status != store.CommitmentWaiting && c.Status != store.CommitmentLeased {
			continue
		}
		next := c.NextCheckAt
		if t, err := time.Parse(time.RFC3339, c.NextCheckAt); err == nil {
			next = t.In(time.Local).Format("01-02 15:04")
		}
		fmt.Fprintf(&b, "#%d %s —— 已追问 %d 次，下次 %s\n", c.ID, c.Text, c.NudgeCount, next)
		n++
	}
	if n == 0 {
		return tools.Result{Text: "当前会话里没有未收口的承诺。"}, nil
	}
	return tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

type cancelCommitmentTool struct{ st *store.Store }

func (t *cancelCommitmentTool) Name() string     { return "cancel_commitment" }
func (t *cancelCommitmentTool) Group() string    { return "commitment" }
func (t *cancelCommitmentTool) Risk() tools.Risk { return tools.SideEffect }
func (t *cancelCommitmentTool) Description() string {
	return "取消一件还没收口的承诺，需要编号。用户说不想再被提醒时使用。"
}
func (t *cancelCommitmentTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"id":{"type":"number","description":"list_commitments 返回的编号"}},"required":["id"]}`)
}
func (t *cancelCommitmentTool) Run(ctx context.Context, raw json.RawMessage, env *tools.Env) (tools.Result, error) {
	var args struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return tools.Result{}, fmt.Errorf("参数需要 {\"id\": 12}: %w", err)
	}
	if args.ID <= 0 {
		return tools.Result{}, fmt.Errorf("id 必须是正整数")
	}
	creator := commitmentCreator(env)
	c, err := findOpenCommitment(ctx, t.st, creator, args.ID)
	if err != nil {
		return tools.Result{}, err
	}
	if c == nil {
		return tools.Result{Text: fmt.Sprintf("你名下没有 #%d 这条未收口的承诺（可能已完成或已取消）。", args.ID)}, nil
	}
	if err := t.st.FinishCommitment(ctx, args.ID, store.CommitmentCancelled, "用户取消"); err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Text: fmt.Sprintf("已取消承诺 #%d：%s", args.ID, c.Text)}, nil
}

type ackCommitmentTool struct {
	st      *store.Store
	metrics *Metrics
	log     *slog.Logger
}

func (t *ackCommitmentTool) Name() string     { return "ack_commitment" }
func (t *ackCommitmentTool) Group() string    { return "commitment" }
func (t *ackCommitmentTool) Risk() tools.Risk { return tools.SideEffect }
func (t *ackCommitmentTool) Description() string {
	return "在用户对某条承诺表态时收口：outcome=done 已完成、cancel 取消、postpone 延后。" +
		"延后可以给 remind_after_seconds；不给时代码按 4 小时兜底。"
}
func (t *ackCommitmentTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"id":{"type":"number","description":"承诺编号"},
		"outcome":{"type":"string","enum":["done","postpone","cancel"],"description":"done=完成，postpone=延后，cancel=取消"},
		"note":{"type":"string","description":"可选：用户的原话或补充说明"},
		"remind_after_seconds":{"type":"number","description":"outcome=postpone 时，多少秒后再提醒（至少 60）"}},
		"required":["id","outcome"]}`)
}

func (t *ackCommitmentTool) Run(ctx context.Context, raw json.RawMessage, env *tools.Env) (tools.Result, error) {
	var args struct {
		ID                 int64    `json:"id"`
		Outcome            string   `json:"outcome"`
		Note               string   `json:"note"`
		RemindAfterSeconds *float64 `json:"remind_after_seconds"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return tools.Result{}, fmt.Errorf("参数需要 {\"id\": 12, \"outcome\": \"done\"}: %w", err)
	}
	if args.ID <= 0 {
		return tools.Result{}, fmt.Errorf("id 必须是正整数")
	}
	outcome := strings.ToLower(strings.TrimSpace(args.Outcome))
	if outcome != "done" && outcome != "postpone" && outcome != "cancel" {
		return tools.Result{}, fmt.Errorf("outcome 必须是 done / postpone / cancel")
	}
	creator := commitmentCreator(env)
	c, err := findOpenCommitment(ctx, t.st, creator, args.ID)
	if err != nil {
		return tools.Result{}, err
	}
	if c == nil {
		return tools.Result{Text: fmt.Sprintf("你名下没有 #%d 这条未收口的承诺（可能已完成或已取消）。", args.ID)}, nil
	}
	note := strings.TrimSpace(args.Note)
	if runes := []rune(note); len(runes) > 200 {
		note = string(runes[:200])
	}
	switch outcome {
	case "done":
		if err := t.st.FinishCommitment(ctx, args.ID, store.CommitmentDone, note); err != nil {
			return tools.Result{}, err
		}
		if t.metrics != nil {
			t.metrics.CommitmentAcked.Add(1)
		}
		return tools.Result{Text: fmt.Sprintf("已收口承诺 #%d（完成）：%s", args.ID, c.Text)}, nil
	case "cancel":
		if err := t.st.FinishCommitment(ctx, args.ID, store.CommitmentCancelled, note); err != nil {
			return tools.Result{}, err
		}
		return tools.Result{Text: fmt.Sprintf("已取消承诺 #%d：%s", args.ID, c.Text)}, nil
	default:
		delay := commitmentDefaultPostpone
		if args.RemindAfterSeconds != nil && *args.RemindAfterSeconds > 0 {
			delay = time.Duration(*args.RemindAfterSeconds * float64(time.Second))
			if delay < commitmentMinInterval {
				delay = commitmentMinInterval
			}
			if delay > commitmentMaxInterval {
				delay = commitmentMaxInterval
			}
		}
		if args.RemindAfterSeconds == nil || *args.RemindAfterSeconds <= 0 {
			if t.log != nil {
				t.log.Info("commitment postpone used the default interval", "component", "commitments",
					"id", args.ID, "default_seconds", int(commitmentDefaultPostpone.Seconds()))
			}
		}
		next := time.Now().Add(delay)
		rescheduled, err := t.st.RescheduleCommitment(ctx, args.ID,
			next.Format(time.RFC3339), c.LastNudgeAt, 0)
		if err != nil {
			return tools.Result{}, err
		}
		if !rescheduled {
			// It was closed between findOpenCommitment and this write (for example
			// the user said done in another message). Report the truth instead of
			// claiming a postpone that never happened.
			return tools.Result{Text: fmt.Sprintf("承诺 #%d 已经收口，未改动。", args.ID)}, nil
		}
		if t.metrics != nil {
			t.metrics.CommitmentPostponed.Add(1)
		}
		return tools.Result{Text: fmt.Sprintf("已把承诺 #%d 延后到 %s：%s",
			args.ID, next.In(time.Local).Format("01-02 15:04"), c.Text)}, nil
	}
}

// findOpenCommitment returns one of the creator's open loops by id, or nil.
func findOpenCommitment(ctx context.Context, st *store.Store, creator string, id int64) (*store.Commitment, error) {
	if creator == "" {
		return nil, nil
	}
	items, err := st.ListOpenCommitmentsOfCreator(ctx, creator, 100)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].ID == id {
			return &items[i], nil
		}
	}
	return nil, nil
}
