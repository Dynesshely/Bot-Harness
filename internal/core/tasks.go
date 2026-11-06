package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
	"github.com/dynesshely/bot-harness/internal/tools"
)

// taskGrace is how late a reminder may still be delivered. Past this window the
// bot stayed down long enough that sending it would be confusing ("提醒你起床"
// arriving in the afternoon), so it is marked failed instead.
const taskGrace = 10 * time.Minute

// taskLease is how long one worker may hold a claimed task before others may
// retry it (crash recovery without a heartbeat).
const taskLease = 30 * time.Second

// RunTaskScheduler ticks every second and delivers due reminders.
func (d *Dispatcher) RunTaskScheduler(ctx context.Context) {
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
			d.RunDueTasks(ctx, now)
		}
	}
}

// RunDueTasks claims and delivers everything that is due. Exported so tests (and
// an admin "flush now" action later) can trigger one pass deterministically.
func (d *Dispatcher) RunDueTasks(ctx context.Context, now time.Time) {
	if d.st == nil || d.sender == nil {
		return
	}
	tasks, err := d.st.LeaseDueTasks(ctx, now, 10, taskLease)
	if err != nil {
		d.log.Warn("failed to lease due tasks", "component", "tasks", "error", err.Error())
		return
	}
	for _, task := range tasks {
		d.deliverTask(ctx, task, now)
	}
}

// deliverTask sends one reminder and records the outcome.
func (d *Dispatcher) deliverTask(ctx context.Context, task store.ScheduledTask, now time.Time) {
	s := d.settings.Effective()

	due, err := time.Parse(time.RFC3339, task.DueAt)
	if err != nil {
		_ = d.st.FinishTask(ctx, task.ID, store.TaskFailed, "due_at 解析失败: "+err.Error())
		return
	}
	if late := now.Sub(due); late > taskGrace {
		// Too late to be useful: say so in the audit trail instead of spamming.
		_ = d.st.FinishTask(ctx, task.ID, store.TaskFailed,
			fmt.Sprintf("错过发送窗口 %s（超过 %s，可能进程停机）", late.Round(time.Second), taskGrace))
		d.log.Warn("scheduled task missed its window", "component", "tasks",
			"id", task.ID, "late", late.Round(time.Second).String())
		return
	}

	opts := qq.SendOptions{
		Markdown:    s.MarkdownEnabled,
		MaxChars:    s.ReplyMaxChars,
		MaxSegments: s.ReplyMaxSegments,
	}
	if task.GroupOpenID != "" {
		opts.GroupOpenID = task.GroupOpenID
	} else {
		opts.UserOpenID = task.MemberOpenID
	}

	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := d.sender.Send(sendCtx, opts, task.Text)
	if err != nil {
		_ = d.st.FinishTask(ctx, task.ID, store.TaskFailed, err.Error())
		d.log.Warn("scheduled task failed to send", "component", "tasks",
			"id", task.ID, "error", err.Error())
		return
	}
	if err := d.st.FinishTask(ctx, task.ID, store.TaskSent, ""); err != nil {
		d.log.Warn("failed to mark task sent", "component", "store", "id", task.ID, "error", err.Error())
	}

	// Keep the timeline honest: the bot must know它已经说过这句话。
	if task.ConversationID != "" {
		if err := d.st.AppendMessage(ctx, store.MessageInput{
			ConversationID: task.ConversationID,
			Role:           "assistant",
			Content:        task.Text,
			Kind:           store.KindManual,
			Tokens:         EstimateTokens(task.Text),
		}); err != nil {
			d.log.Debug("failed to append the reminder to the timeline",
				"component", "store", "error", err.Error())
		}
	}
	if res != nil {
		d.metrics.RepliesSent.Add(1)
		d.log.Info("scheduled task delivered", "component", "tasks",
			"id", task.ID, "segments", res.Segments, "late_ms", now.Sub(due).Milliseconds())
	}
}

// --- tools -----------------------------------------------------------------

// registerTaskTools wires the reminder tools into the registry.
func (d *Dispatcher) registerTaskTools(reg *tools.Registry) {
	if reg == nil || d.st == nil {
		return
	}
	reg.Register(&scheduleTaskTool{st: d.st, settings: d.settings})
	reg.Register(&listTasksTool{st: d.st})
	reg.Register(&cancelTaskTool{st: d.st})
}

// taskDestination is where the reminder is delivered (group or private chat).
func taskDestination(env *tools.Env) (groupOpenID, memberOpenID string) {
	if env.IsGroup {
		return env.GroupOpenID, ""
	}
	return "", env.MemberOpenID
}

// taskCreator is who asked for it. Group reminders are owned by the member who
// asked, so two people in the same group cannot see or cancel each other's.
func taskCreator(env *tools.Env) string {
	if env == nil {
		return ""
	}
	return strings.TrimSpace(env.MemberOpenID)
}

type scheduleTaskTool struct {
	st       *store.Store
	settings *settings.Manager
}

func (t *scheduleTaskTool) Name() string     { return "schedule_task" }
func (t *scheduleTaskTool) Group() string    { return "task" }
func (t *scheduleTaskTool) Risk() tools.Risk { return tools.SideEffect }

func (t *scheduleTaskTool) Description() string {
	return "安排一条定时提醒，到点由机器人主动发出。只在对方明确要求「X 之后提醒我」时使用，" +
		"不要自己创造提醒。文本要写成到点后能独立看懂的一句话。"
}

func (t *scheduleTaskTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"text":{"type":"string","description":"到点要发出的提醒内容，例如「该吃药了」"},
		"delay_seconds":{"type":"number","description":"多少秒后发出（至少 10，最长 7 天）"},
		"at":{"type":"string","description":"可选：RFC3339 时间点，与 delay_seconds 二选一"}},
		"required":["text"]}`)
}

func (t *scheduleTaskTool) Run(ctx context.Context, raw json.RawMessage, env *tools.Env) (tools.Result, error) {
	var args struct {
		Text         string   `json:"text"`
		DelaySeconds *float64 `json:"delay_seconds"`
		At           string   `json:"at"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return tools.Result{}, fmt.Errorf("参数需要 {\"text\": \"…\", \"delay_seconds\": 600}: %w", err)
	}
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return tools.Result{}, fmt.Errorf("text 不能为空")
	}
	if runes := []rune(text); len(runes) > 200 {
		text = string(runes[:200])
	}

	s := t.settings.Effective()
	maxDelay := time.Duration(s.TasksMaxDelayHours) * time.Hour
	now := time.Now()
	var due time.Time
	switch {
	case strings.TrimSpace(args.At) != "":
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(args.At))
		if err != nil {
			return tools.Result{}, fmt.Errorf("at 需要 RFC3339 时间：%w", err)
		}
		due = parsed
	case args.DelaySeconds != nil:
		delay := time.Duration(*args.DelaySeconds * float64(time.Second))
		if delay < 10*time.Second {
			return tools.Result{}, fmt.Errorf("delay_seconds 至少 10 秒")
		}
		due = now.Add(delay)
	default:
		return tools.Result{}, fmt.Errorf("delay_seconds 与 at 至少给一个")
	}
	if due.Before(now.Add(10 * time.Second)) {
		return tools.Result{}, fmt.Errorf("提醒时间必须在 10 秒之后")
	}
	if due.After(now.Add(maxDelay)) {
		return tools.Result{}, fmt.Errorf("提醒时间太远（上限 %d 小时）", s.TasksMaxDelayHours)
	}

	creator := taskCreator(env)
	if creator == "" {
		return tools.Result{}, fmt.Errorf("无法确定是谁在安排提醒，已取消（缺少 sender openid）")
	}
	groupOpenID, memberOpenID := taskDestination(env)
	pending, err := t.st.PendingTasksOfCreator(ctx, creator)
	if err != nil {
		return tools.Result{}, err
	}
	if pending >= s.TasksMaxPerMember {
		return tools.Result{}, fmt.Errorf("你还有 %d 条待发提醒（上限 %d 条），等几条发出去再安排",
			pending, s.TasksMaxPerMember)
	}

	id, err := t.st.AddTask(ctx, store.TaskInput{
		ConversationID: env.ConversationID,
		GroupOpenID:    groupOpenID,
		MemberOpenID:   memberOpenID,
		CreatorOpenID:  creator,
		Kind:           "reminder",
		Text:           text,
		DueAt:          due.Format(time.RFC3339),
	})
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Text: fmt.Sprintf("已安排（#%d），将于 %s 发出：%s",
		id, due.In(time.Local).Format("01-02 15:04:05"), text)}, nil
}

type listTasksTool struct{ st *store.Store }

func (t *listTasksTool) Name() string     { return "list_tasks" }
func (t *listTasksTool) Group() string    { return "task" }
func (t *listTasksTool) Risk() tools.Risk { return tools.ReadOnly }
func (t *listTasksTool) Description() string {
	return "列出当前会话里还没发出的提醒（含编号，便于取消）。"
}

func (t *listTasksTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t *listTasksTool) Run(ctx context.Context, _ json.RawMessage, env *tools.Env) (tools.Result, error) {
	items, err := t.st.ListPendingTasksOfCreator(ctx, taskCreator(env), 20)
	if err != nil {
		return tools.Result{}, err
	}
	if len(items) == 0 {
		return tools.Result{Text: "你名下没有待发的提醒。"}, nil
	}
	var b strings.Builder
	for _, task := range items {
		due, _ := time.Parse(time.RFC3339, task.DueAt)
		fmt.Fprintf(&b, "#%d %s —— %s\n", task.ID, due.In(time.Local).Format("01-02 15:04"), task.Text)
	}
	return tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

type cancelTaskTool struct{ st *store.Store }

func (t *cancelTaskTool) Name() string     { return "cancel_task" }
func (t *cancelTaskTool) Group() string    { return "task" }
func (t *cancelTaskTool) Risk() tools.Risk { return tools.SideEffect }
func (t *cancelTaskTool) Description() string {
	return "取消一条还没发出的提醒，需要编号。"
}

func (t *cancelTaskTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"id":{"type":"number","description":"list_tasks 返回的编号"}},"required":["id"]}`)
}

func (t *cancelTaskTool) Run(ctx context.Context, raw json.RawMessage, env *tools.Env) (tools.Result, error) {
	var args struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return tools.Result{}, fmt.Errorf("参数需要 {\"id\": 12}: %w", err)
	}
	if args.ID <= 0 {
		return tools.Result{}, fmt.Errorf("id 必须是正整数")
	}
	// 只能取消自己名下的提醒：别人的编号在这里根本不出现，
	// 因此也拿不到"这条存在但你没权限"的信息。
	creator := taskCreator(env)
	if creator == "" {
		return tools.Result{Text: "无法确认你的身份，不能取消提醒。"}, nil
	}
	items, err := t.st.ListPendingTasksOfCreator(ctx, creator, 50)
	if err != nil {
		return tools.Result{}, err
	}
	for _, task := range items {
		if task.ID != args.ID {
			continue
		}
		ok, err := t.st.CancelTaskAs(ctx, args.ID, creator)
		if err != nil {
			return tools.Result{}, err
		}
		if !ok {
			return tools.Result{Text: "这条提醒已经发出或已被取消。"}, nil
		}
		return tools.Result{Text: fmt.Sprintf("已取消 #%d", args.ID)}, nil
	}
	return tools.Result{Text: fmt.Sprintf("你名下没有 #%d 这条待发提醒（可能已发出，或不是你的）。", args.ID)}, nil
}
