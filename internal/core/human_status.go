package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/render"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// 人类状态感知：从聊天内容里推测对方在做什么（吃饭/睡觉/工作/打游戏/通勤/上课/
// 在线……），作为回复语气与承诺追问时机的参考。推测结果永远只是"可能不准"的
// 提示，不写进用户可见消息，除非用户主动发 /human-status。

var cstFixed = time.FixedZone("CST", 8*3600)

func cstZone() *time.Location { return cstFixed }

// humanStatusMarker lets tests and the fake provider recognise this call. It is
// part of the system prompt, which also carries the JSON contract.
const humanStatusMarker = "【人类状态推测】"

const humanStatusSystemPrompt = humanStatusMarker + `你是一个聊天状态观察器。根据最近的聊天内容，推测每个参与对象此刻可能的活动状态。
你会看到按成员分组的时间线（每条消息带时间），以及一小段按时间排序的全局最近消息。
可以结合每个成员的发言节奏、用词习惯、活跃时段，以及消息里附带的图片内容综合判断。
只有图片内容确实对判断有帮助时才调用 analyze_image 查看；不要为每张图片都调用。
工具返回“图片不可用”或调用失败时，继续按已有文字证据判断，不要因此停止推测。
只推断活动状态（例如：吃饭、睡觉、工作、学习、打游戏、通勤、上课、运动、休息、在线、忙碌、离线），
不要推断年龄、性别、健康、政治倾向、宗教、性取向等敏感属性，也不要编造没有证据的状态。
输出 JSON 数组（最多 20 项），不要输出任何别的文字：
[{"member_openid":"…","display_name":"昵称","status":"状态","confidence":0.0-1.0,"evidence":"一句依据（≤40 字）"}]
如果证据不足，可以少输出几项，但不要编造；evidence 必须 ≤40 字。`

// 图片工具的预算：一次推测最多 4 次调用、2 轮模型循环；时间线里图片的展示
// 上限是每人 4 张、总计 8 张，避免提示词膨胀。这些是技术防呆，不是产品策略。
const (
	humanStatusMaxToolCalls            = 4
	humanStatusMaxToolRounds           = 2
	humanStatusTimelineImagesPerMember = 4
	humanStatusTimelineImagesTotal     = 8
	humanStatusGlobalTimelineMessages  = 20
	humanStatusTimelineContentRunes    = 160
	humanStatusVisionResultRunes       = 800
	humanStatusAnalyzeImageToolName    = "analyze_image"
	humanStatusVisionDefaultQuestion   = "请描述这张聊天图片的内容，并指出任何能帮助判断发送者当前活动状态的线索（场景、时间、屏幕、物品等）。不要推断敏感属性。"
)

// 推测的输出预算。网关的默认 MaxTokens 可能小到装不下 20 个对象的 JSON，
// 截断后整段解析失败；这里给一个下限，并保留用户配置的更大值。
const humanStatusOutputMinTokens = 2000

// /human-status 的可选人数参数，以及「过去 24 小时发言者」硬限制。
const (
	// humanStatusWindow is the hard limit of the command: only members who spoke
	// inside this window are analysed, even when the operator asks for more.
	humanStatusWindow = 24 * time.Hour
	// humanStatusDefaultCount is how many members /human-status analyses when the
	// argument is omitted (the historical behaviour).
	humanStatusDefaultCount = 10
	// humanStatusMaxCount is the largest accepted argument. It is also the
	// ceiling for one inference, so a group cannot ask for an unbounded roster.
	humanStatusMaxCount = 200
)

// humanStatusRosterRule is appended to the system prompt when the command
// supplies a roster. The roster is the hard limit of the analysis: the model
// must not invent states for people who did not speak in the window, even if
// they appear in the trailing global transcript.
const humanStatusRosterRule = `
本次推测有固定的成员范围：用户消息里「本次分析范围」列出的成员（过去 24 小时内在本会话发过言的人）。
只分析这些成员，不要分析列表之外的任何人，也不要为列表外的成员输出对象；列表外的发言只能作为语境参考。`

// humanStatusRetryInstruction is appended as a user turn when the first reply
// could not be parsed at all. It must only restate the output contract.
const humanStatusRetryInstruction = "上次输出不是合法 JSON 数组或为空。只输出 JSON 数组，不要任何解释、不要 markdown；每个 evidence ≤ 40 字。"

func humanStatusMaxTokens(s settings.Settings) int {
	if s.MaxTokens > humanStatusOutputMinTokens {
		return s.MaxTokens
	}
	return humanStatusOutputMinTokens
}

// humanStatusAnalyzeImageTool declares the only tool the inference may call.
func humanStatusAnalyzeImageTool() ai.ToolDef {
	params := json.RawMessage(`{
  "type": "object",
  "properties": {
    "media_id": {"type": "string", "description": "时间线里 [图片 media_id=…] 给出的图片 id"},
    "question": {"type": "string", "description": "可选：想从图片里确认的具体问题"}
  },
  "required": ["media_id"]
}`)
	return ai.NewToolDef(humanStatusAnalyzeImageToolName,
		"分析聊天图片内容，返回与成员状态相关的线索。仅在图片对判断有帮助时调用。", params)
}

// buildHumanStatusTimeline renders the recent messages grouped by speaker, in
// chronological order, followed by a short global transcript. It returns the
// text and how many analyzable image ids it listed.
func buildHumanStatusTimeline(msgs []store.Message, max int) (string, int) {
	return buildHumanStatusTimelineWithin(msgs, max, nil)
}

// buildHumanStatusTimelineWithin is buildHumanStatusTimeline with an optional
// member allow-list. When allow is non-nil only those speakers get a member
// section: that is the "/human-status 只分析过去 24 小时发言者" hard limit.
//
// The trailing global transcript still comes from the full message window on
// purpose: it is the context the model needs to understand the conversation
// (≤20 messages), and the prompt separately tells it not to analyse people
// outside the roster.
func buildHumanStatusTimelineWithin(msgs []store.Message, max int, allow map[string]bool) (string, int) {
	if max <= 0 {
		max = 120
	}
	if len(msgs) > max {
		msgs = msgs[len(msgs)-max:]
	}

	type member struct {
		openID string
		name   string
		msgs   []store.Message
	}
	order := make([]string, 0, 8)
	members := map[string]*member{}
	for _, m := range msgs {
		id := strings.TrimSpace(m.SpeakerOpenID)
		if id == "" {
			continue
		}
		if allow != nil && !allow[id] {
			continue
		}
		mb, ok := members[id]
		if !ok {
			name := strings.TrimSpace(m.Speaker)
			if name == "" {
				name = "未知"
			}
			mb = &member{openID: id, name: name}
			members[id] = mb
			order = append(order, id)
		} else if name := strings.TrimSpace(m.Speaker); name != "" {
			mb.name = name
		}
		mb.msgs = append(mb.msgs, m)
	}

	var b strings.Builder
	imageCount := 0
	for _, id := range order {
		mb := members[id]
		fmt.Fprintf(&b, "## 成员：%s (%s)\n", mb.name, mb.openID)
		perMemberImages := 0
		for _, m := range mb.msgs {
			line := fmt.Sprintf("[%s] %s", humanStatusClock(m.CreatedAt),
				truncateForLog(m.Content, humanStatusTimelineContentRunes))
			for _, a := range m.Attachments {
				if a.Kind != "image" && !strings.HasPrefix(strings.ToLower(a.ContentType), "image/") {
					continue
				}
				mediaID := strings.TrimSpace(a.MediaID)
				if mediaID == "" {
					line += " [图片]"
					continue
				}
				if perMemberImages >= humanStatusTimelineImagesPerMember ||
					imageCount >= humanStatusTimelineImagesTotal {
					continue
				}
				line += " [图片 media_id=" + mediaID + "]"
				perMemberImages++
				imageCount++
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## 全局最近消息（按时间顺序）\n")
	recent := msgs
	if len(recent) > humanStatusGlobalTimelineMessages {
		recent = recent[len(recent)-humanStatusGlobalTimelineMessages:]
	}
	for _, m := range recent {
		who := strings.TrimSpace(m.Speaker)
		if who == "" {
			who = "未知"
		}
		fmt.Fprintf(&b, "[%s] %s：%s\n", humanStatusClock(m.CreatedAt), who,
			truncateForLog(m.Content, humanStatusTimelineContentRunes))
	}
	return b.String(), imageCount
}

// humanStatusClock renders one message timestamp in QQ-facing local time.
func humanStatusClock(createdAt string) string {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(createdAt)); err == nil {
		return t.In(cstZone()).Format("15:04")
	}
	return "--:--"
}

// runHumanStatusTool executes one analyze_image call and returns the role=tool
// message. Failures are text for the model, never errors that abort inference:
// a bad image must not cost the whole state analysis.
func (d *Dispatcher) runHumanStatusTool(ctx context.Context, s settings.Settings, call ai.ToolCall, calls *int) ai.Message {
	if strings.TrimSpace(call.Function.Name) != humanStatusAnalyzeImageToolName {
		return ai.ToolMessage(call.ID, "图片不可用：未知工具，只有 analyze_image 可用。")
	}
	if calls == nil || *calls >= humanStatusMaxToolCalls {
		return ai.ToolMessage(call.ID, "图片不可用：本次推测的图片分析调用次数已达上限。")
	}
	*calls++
	d.metrics.HumanStatusImageCalls.Add(1)

	var args struct {
		MediaID  string `json:"media_id"`
		Question string `json:"question"`
	}
	if raw := strings.TrimSpace(call.Function.Arguments); raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return ai.ToolMessage(call.ID, "图片不可用：工具参数无法解析。")
		}
	}
	mediaID := strings.TrimSpace(args.MediaID)
	if mediaID == "" {
		return ai.ToolMessage(call.ID, "图片不可用：缺少 media_id。")
	}
	if d.media == nil {
		return ai.ToolMessage(call.ID, "图片不可用：媒体存储不可用。")
	}
	contentType, data, err := d.media.Load(ctx, mediaID)
	if err != nil || len(data) == 0 {
		return ai.ToolMessage(call.ID, "图片不可用：图片未缓存或已过期。")
	}

	model := strings.TrimSpace(s.HumanStatusVisionModel)
	if model == "" {
		model = s.Model
	}
	question := truncateForLog(strings.TrimSpace(args.Question), 200)
	if question == "" {
		question = humanStatusVisionDefaultQuestion
	}
	vision, err := d.ai.Chat(ctx, ai.ChatRequest{
		Model: model,
		Messages: []ai.Message{{
			Role: ai.RoleUser,
			Parts: []ai.ContentPart{
				ai.TextPart(question),
				ai.ImagePart(ai.DataURI(contentType, data)),
			},
		}},
		Temperature: 0.2,
		MaxTokens:   300,
		Stream:      false,
	})
	if err != nil {
		return ai.ToolMessage(call.ID, "图片不可用：视觉模型调用失败。")
	}
	description := strings.TrimSpace(vision.Text)
	if description == "" {
		return ai.ToolMessage(call.ID, "图片不可用：视觉模型没有返回描述。")
	}
	return ai.ToolMessage(call.ID, truncateForLog(description, humanStatusVisionResultRunes))
}

// 触发与去重状态。pending 按 conversation 累计新消息，running 保证同一会话
// 同时只有一次推测在跑（单飞）。
func (d *Dispatcher) noteHumanStatusMessage(conv convTarget, in *Inbound) {
	s := d.settings.Effective()
	if !s.HumanStatusEnabled || d.st == nil || d.ai == nil {
		return
	}
	d.humanMu.Lock()
	if d.humanPending == nil {
		d.humanPending = map[string]int{}
	}
	d.humanPending[conv.ID]++
	pending := d.humanPending[conv.ID]
	d.humanMu.Unlock()
	if pending >= s.HumanStatusTriggerMessages {
		d.startHumanStatusRefresh(conv, in)
	}
}

// refreshHumanStatusIfStale is the "before prompt building" check: if the cache
// has expired and enough new messages accumulated, kick off a background
// refresh. The current reply uses whatever cache exists; it never blocks.
func (d *Dispatcher) refreshHumanStatusIfStale(ctx context.Context, conv convTarget, in *Inbound) {
	s := d.settings.Effective()
	if !s.HumanStatusEnabled || d.st == nil || d.ai == nil {
		return
	}
	items, err := d.st.ListHumanStatuses(ctx, conv.ID)
	if err != nil {
		return
	}
	now := time.Now()
	for _, item := range items {
		if expires, err := time.Parse(time.RFC3339, item.ExpiresAt); err == nil && expires.After(now) {
			return
		}
	}
	d.humanMu.Lock()
	pending := 0
	if d.humanPending != nil {
		pending = d.humanPending[conv.ID]
	}
	d.humanMu.Unlock()
	if pending >= s.HumanStatusTriggerMessages {
		d.startHumanStatusRefresh(conv, in)
	}
}

// humanStatusCommandTimeout bounds one synchronous inference triggered by the
// /human-status command. It is deliberately shorter than a full reply budget:
// the operator is waiting for the answer, not for a turn.
const humanStatusCommandTimeout = 45 * time.Second

// Sentinel reasons for RefreshHumanStatus. They keep the command reply free of
// implementation detail (and of anything that could look like a DSN or key).
var (
	errHumanStatusNoAI    = errors.New("human status: ai client is not configured")
	errHumanStatusNoStore = errors.New("human status: store is not configured")
)

// humanStatusRefreshOptions carries the optional inputs of one inference. The
// zero value is the automatic (background) path: analyse every member found in
// the timeline.
type humanStatusRefreshOptions struct {
	// Members, when Restrict is true, is the roster the analysis is limited to
	// (the command path's "past 24h speakers" list, newest first). The timeline
	// only shows these members, the prompt tells the model not to analyse anyone
	// else, and states for out-of-roster openids are dropped instead of stored.
	Members []store.SpeakerActivity
	// Restrict enables the roster limit. It is separate from len(Members) so an
	// empty roster is a valid, explicit request (nothing may be analysed) and
	// cannot be confused with the background path.
	Restrict bool
}

// RefreshHumanStatus runs exactly one inference synchronously and stores the
// result, for the admin-triggered /human-status command. It keeps analysing the
// whole timeline, which is what the automatic trigger relies on.
//
// The command path does not hold aiSem (commands never enter the reply
// pipeline), so acquiring it here is safe and does not nest. The background
// path keeps its own counters; inferHumanStatus owns HumanStatusRuns so a
// synchronous call cannot double-count an attempt, and this function adds the
// failure counter on every error path.
func (d *Dispatcher) RefreshHumanStatus(ctx context.Context, conv convTarget, in *Inbound, s settings.Settings) error {
	return d.RefreshHumanStatusFor(ctx, conv, in, s, humanStatusRefreshOptions{})
}

// RefreshHumanStatusFor is RefreshHumanStatus with an explicit roster. The
// command passes the past-24h speakers; every other caller keeps the original
// signature and therefore the original behaviour.
func (d *Dispatcher) RefreshHumanStatusFor(ctx context.Context, conv convTarget, in *Inbound, s settings.Settings, opts humanStatusRefreshOptions) error {
	if d == nil {
		return errHumanStatusNoAI
	}
	if d.ai == nil {
		d.metrics.HumanStatusFailures.Add(1)
		return errHumanStatusNoAI
	}
	if d.st == nil {
		d.metrics.HumanStatusFailures.Add(1)
		return errHumanStatusNoStore
	}
	ctx, cancel := context.WithTimeout(ctx, humanStatusCommandTimeout)
	defer cancel()
	if d.aiSem != nil {
		if err := d.aiSem.Acquire(ctx); err != nil {
			d.metrics.HumanStatusFailures.Add(1)
			return fmt.Errorf("human status: acquire ai semaphore: %w", err)
		}
		defer d.aiSem.Release()
	}
	if err := d.inferHumanStatus(ctx, conv, in, s, opts); err != nil {
		d.metrics.HumanStatusFailures.Add(1)
		return err
	}
	return nil
}

func (d *Dispatcher) startHumanStatusRefresh(conv convTarget, in *Inbound) {
	s := d.settings.Effective()
	if !s.HumanStatusEnabled || d.st == nil || d.ai == nil {
		return
	}
	d.humanMu.Lock()
	if d.humanRunning == nil {
		d.humanRunning = map[string]bool{}
	}
	if d.humanRunning[conv.ID] {
		d.humanMu.Unlock()
		return
	}
	d.humanRunning[conv.ID] = true
	if d.humanPending == nil {
		d.humanPending = map[string]int{}
	}
	d.humanPending[conv.ID] = 0
	d.humanMu.Unlock()

	go func() {
		defer func() {
			d.humanMu.Lock()
			delete(d.humanRunning, conv.ID)
			d.humanMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(d.base(), 90*time.Second)
		defer cancel()
		if d.aiSem != nil {
			if err := d.aiSem.Acquire(ctx); err != nil {
				d.metrics.HumanStatusFailures.Add(1)
				d.log.Warn("human status inference skipped: ai semaphore", "component", "human-status",
					"conversation", conv.ID, "error", err.Error())
				return
			}
			defer d.aiSem.Release()
		}
		if err := d.inferHumanStatus(ctx, conv, in, s, humanStatusRefreshOptions{}); err != nil {
			d.metrics.HumanStatusFailures.Add(1)
			d.log.Warn("human status inference failed", "component", "human-status",
				"conversation", conv.ID, "error", err.Error())
		}
	}()
}

// humanStatusInferred is one parsed member state.
type humanStatusInferred struct {
	MemberOpenID string  `json:"member_openid"`
	DisplayName  string  `json:"display_name"`
	Status       string  `json:"status"`
	Confidence   float64 `json:"confidence"`
	Evidence     string  `json:"evidence"`
}

// stripHumanStatusFence removes a markdown code fence around the reply. Models
// routinely wrap the JSON in ```json even when asked not to; a fence must never
// be the reason the analysis fails.
func stripHumanStatusFence(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	} else {
		s = strings.TrimPrefix(s, "```")
	}
	if j := strings.LastIndex(s, "```"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

// parseHumanStatusReply parses the reply as a JSON array of objects without
// requiring the whole reply to be valid JSON. It starts at the first '[' and
// decodes objects one at a time, so explanatory text before/after the array is
// tolerated. When the model was cut off by max_tokens, the objects decoded
// before the error are kept and partial=true: a truncated reply is still worth
// more than no analysis at all. Zero decoded objects is a failure.
func parseHumanStatusReply(raw string) (items []humanStatusInferred, partial bool, err error) {
	s := stripHumanStatusFence(raw)
	start := strings.Index(s, "[")
	if start < 0 {
		return nil, false, fmt.Errorf("human status reply was not a JSON array: %s",
			truncateForLog(strings.TrimSpace(raw), 160))
	}
	dec := json.NewDecoder(strings.NewReader(s[start:]))
	tok, tokErr := dec.Token()
	if tokErr != nil || tok != json.Delim('[') {
		return nil, false, fmt.Errorf("human status reply was not a JSON array: %s",
			truncateForLog(strings.TrimSpace(raw), 160))
	}
	for dec.More() {
		var item humanStatusInferred
		if decodeErr := dec.Decode(&item); decodeErr != nil {
			if len(items) == 0 {
				return nil, false, fmt.Errorf("parse human status JSON: %w", decodeErr)
			}
			return items, true, nil
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, false, fmt.Errorf("human status reply was an empty JSON array: %s",
			truncateForLog(strings.TrimSpace(raw), 160))
	}
	return items, false, nil
}

// inferHumanStatus runs one inference and stores the parsed states.
//
// The prompt is a per-member timeline rather than a flat transcript: it keeps
// speaker attribution and timestamps, which is what rhythm/active-hour hints
// need. With vision enabled the model may call analyze_image (bounded: 4 calls,
// 2 rounds) and the tool results are fed back as role=tool messages; the tool is
// advertised even when no image is listed so a stale/mistaken model call simply
// gets an "图片不可用" answer. The final answer contract is unchanged: one JSON
// array.
func (d *Dispatcher) inferHumanStatus(ctx context.Context, conv convTarget, in *Inbound, s settings.Settings, opts humanStatusRefreshOptions) error {
	limit := s.HumanStatusTimelineMessages
	if limit <= 0 {
		limit = 120
	}
	msgs, err := d.st.RecentMessages(ctx, conv.ID, limit, true)
	if err != nil {
		return err
	}
	// 触发发生在入库之前，所以本轮消息要显式补进转录里。名单模式不补：
	// /human-status 本身是命令而不是发言（命令也不写会话上下文），名单由
	// store.RecentSpeakers 决定；把命令文本塞进时间线只会多出一个名单外人。
	if !opts.Restrict && in != nil && (strings.TrimSpace(in.Content) != "" || len(in.Attachments) > 0) {
		already := false
		for _, m := range msgs {
			if m.Role == "user" && m.Content == in.Content &&
				m.SpeakerOpenID == firstNonEmpty(in.MemberOpenID, in.UserOpenID) {
				already = true
				break
			}
		}
		if !already {
			msgs = append(msgs, store.Message{
				Role: "user", Content: in.Content, Attachments: in.Attachments,
				Speaker: in.MemberName, SpeakerOpenID: firstNonEmpty(in.MemberOpenID, in.UserOpenID),
			})
		}
	}
	if len(msgs) == 0 {
		return fmt.Errorf("no messages to infer from")
	}

	// roster maps openid → display name; order is the member order the analysis
	// is limited to (the command roster) or the order members appear in the
	// timeline (the background path).
	roster := map[string]string{}
	var order []string
	if opts.Restrict {
		order = make([]string, 0, len(opts.Members))
		for _, m := range opts.Members {
			id := strings.TrimSpace(m.OpenID)
			if id == "" {
				continue
			}
			if _, seen := roster[id]; !seen {
				order = append(order, id)
				roster[id] = ""
			}
			if name := strings.TrimSpace(m.Name); name != "" {
				roster[id] = name
			}
		}
		if len(order) == 0 {
			return fmt.Errorf("no identifiable members")
		}
	} else {
		order = make([]string, 0, 20)
		addMember := func(openID, name string) {
			openID = strings.TrimSpace(openID)
			if openID == "" || len(order) >= 20 {
				return
			}
			if _, ok := roster[openID]; !ok {
				order = append(order, openID)
			}
			if strings.TrimSpace(name) != "" {
				roster[openID] = strings.TrimSpace(name)
			} else if _, ok := roster[openID]; !ok {
				roster[openID] = ""
			}
		}
		for _, m := range msgs {
			addMember(m.SpeakerOpenID, m.Speaker)
		}
		if in != nil {
			addMember(firstNonEmpty(in.MemberOpenID, in.UserOpenID), in.MemberName)
		}
		if len(order) == 0 {
			return fmt.Errorf("no identifiable members")
		}
	}

	// In roster mode only those speakers get a member timeline section, and the
	// prompt states the restriction; the global transcript still keeps its ≤20
	// messages of context.
	var allow map[string]bool
	systemPrompt := humanStatusSystemPrompt
	rosterSection := ""
	if opts.Restrict {
		allow = make(map[string]bool, len(order))
		for _, id := range order {
			allow[id] = true
		}
		systemPrompt += humanStatusRosterRule
		rosterSection = "\n本次只分析下列成员（过去 24 小时在本会话发过言的人，按最近发言时间倒序），" +
			"不要分析列表外的人：\n" + humanStatusRosterLines(order, roster) +
			"列表外成员的发言只能作为语境参考，不要为它们输出状态。\n"
	}

	timeline, imageCount := buildHumanStatusTimelineWithin(msgs, limit, allow)
	user := fmt.Sprintf(`当前时间：%s（Asia/Shanghai）
可用线索：
- 结合每个成员的发言节奏、用词、活跃时段，以及消息里附带的图片内容判断状态。
- 图片只在确实对判断有帮助时调用 analyze_image 获取内容；不要为每张图片都调用。
- 本段共有 %d 张可分析的图片（时间线里最多列出前 4 张/人、总计 8 张）。

%s
%s
请按系统提示的 JSON 数组格式输出推测。`,
		time.Now().In(cstZone()).Format("2006-01-02 15:04 Monday"),
		imageCount, timeline, rosterSection)

	var tools []ai.ToolDef
	if s.HumanStatusVisionEnabled {
		tools = []ai.ToolDef{humanStatusAnalyzeImageTool()}
	}

	d.metrics.HumanStatusRuns.Add(1)
	chatMessages := []ai.Message{
		{Role: ai.RoleSystem, Content: systemPrompt},
		{Role: ai.RoleUser, Content: user},
	}
	var res *ai.ChatResult
	calls := 0
	maxTokens := humanStatusMaxTokens(s)
	for round := 0; round < humanStatusMaxToolRounds; round++ {
		req := ai.ChatRequest{
			Model:       s.Model,
			Messages:    chatMessages,
			Temperature: 0.2,
			MaxTokens:   maxTokens,
			Stream:      false,
			Tools:       tools,
		}
		out, err := d.ai.Chat(ctx, req)
		if err != nil {
			return err
		}
		res = out
		if len(out.ToolCalls) == 0 {
			break
		}
		// 最后一轮不再执行工具：宁可按现有失败路径报错，也不超预算。
		if round == humanStatusMaxToolRounds-1 {
			break
		}
		chatMessages = append(chatMessages, ai.Message{
			Role: ai.RoleAssistant, Content: out.Text, ToolCalls: out.ToolCalls,
		})
		for _, call := range out.ToolCalls {
			chatMessages = append(chatMessages, d.runHumanStatusTool(ctx, s, call, &calls))
		}
	}
	if res == nil {
		return fmt.Errorf("human status: no completion")
	}

	parsed, partial, parseErr := parseHumanStatusReply(res.Text)
	if parseErr != nil {
		// One retry, with the same output budget: either the model obeyed the
		// contract this time or the command falls back to its failure path.
		d.log.Warn("human status reply was not usable JSON; retrying once",
			"component", "human-status", "conversation", conv.ID,
			"error", parseErr.Error(),
			"prompt_tokens", res.Usage.PromptTokens,
			"completion_tokens", res.Usage.CompletionTokens,
			"tokens", res.Usage.TotalTokens)
		retryMessages := append(append([]ai.Message(nil), chatMessages...),
			ai.Message{Role: ai.RoleAssistant, Content: res.Text},
			ai.Message{Role: ai.RoleUser, Content: humanStatusRetryInstruction})
		// No tools on the retry: the model already had its chance to look at
		// images, and a second tool loop would only make truncation more likely.
		retry, retryErr := d.ai.Chat(ctx, ai.ChatRequest{
			Model:       s.Model,
			Messages:    retryMessages,
			Temperature: 0.2,
			MaxTokens:   maxTokens,
			Stream:      false,
		})
		if retryErr != nil {
			return retryErr
		}
		// The retry is an extra billable call, so its usage goes to the cost log.
		// It deliberately does not add a counter: human_status_runs stays "one
		// attempt per inference" (D-045). Operators see retries in this line.
		d.log.Info("human status JSON retry",
			"component", "human-status", "conversation", conv.ID,
			"prompt_tokens", retry.Usage.PromptTokens,
			"completion_tokens", retry.Usage.CompletionTokens,
			"tokens", retry.Usage.TotalTokens)
		res = retry
		parsed, partial, parseErr = parseHumanStatusReply(retry.Text)
		if parseErr != nil {
			return parseErr
		}
	}
	if partial {
		d.log.Warn("human status JSON was truncated; keeping the parsed objects",
			"component", "human-status", "conversation", conv.ID,
			"partial", true, "parsed", len(parsed))
	}
	// The output contract is one object per analysed member: the background path
	// keeps the historical 20-item cap, the roster path allows as many as the
	// operator asked for (bounded by humanStatusMaxCount when the roster was
	// built). The cap is applied to *stored* items below, after the roster
	// filter, so a model that lists an out-of-roster member first cannot push a
	// valid one out of the result.
	maxItems := 20
	if opts.Restrict {
		maxItems = len(order)
	}
	now := time.Now()
	ttl := time.Duration(s.HumanStatusTTLMinutes) * time.Minute
	scope := store.HumanStatusGroup
	if conv.Scene == SceneC2C {
		scope = store.HumanStatusC2C
	}
	stored := 0
	ignored := 0
	for _, item := range parsed {
		openID := strings.TrimSpace(item.MemberOpenID)
		status := strings.TrimSpace(item.Status)
		if openID == "" || status == "" {
			continue
		}
		// The roster is a hard limit, not a hint: a disobedient model must not be
		// able to write states for people who did not speak in the window.
		if opts.Restrict && !allow[openID] {
			ignored++
			continue
		}
		if stored >= maxItems {
			break
		}
		confidence := item.Confidence
		if confidence < 0 {
			confidence = 0
		}
		if confidence > 1 {
			confidence = 1
		}
		name := strings.TrimSpace(item.DisplayName)
		if name == "" {
			name = roster[openID]
		}
		evidence := strings.TrimSpace(item.Evidence)
		if runes := []rune(evidence); len(runes) > 200 {
			evidence = string(runes[:200])
		}
		if runes := []rune(status); len(runes) > 20 {
			status = string(runes[:20])
		}
		if err := d.st.UpsertHumanStatus(ctx, store.HumanStatusInput{
			Scope:          scope,
			ConversationID: conv.ID,
			GroupOpenID:    conv.GroupOpenID,
			MemberOpenID:   openID,
			DisplayName:    name,
			Status:         status,
			Confidence:     confidence,
			Evidence:       evidence,
			UpdatedAt:      now.Format(time.RFC3339),
			ExpiresAt:      now.Add(ttl).Format(time.RFC3339),
		}); err != nil {
			return err
		}
		stored++
	}
	if ignored > 0 {
		d.log.Debug("human status ignored members outside the roster", "component", "human-status",
			"conversation", conv.ID, "ignored", ignored)
	}
	d.log.Info("human status updated", "component", "human-status",
		"conversation", conv.ID, "stored", stored)
	return nil
}

// unexpiredHumanStatuses returns the live rows of one conversation, newest
// first and capped at max.
func (d *Dispatcher) unexpiredHumanStatuses(ctx context.Context, conversationID string, max int) []store.HumanStatus {
	if d.st == nil || conversationID == "" {
		return nil
	}
	items, err := d.st.ListHumanStatuses(ctx, conversationID)
	if err != nil {
		return nil
	}
	now := time.Now()
	out := make([]store.HumanStatus, 0, len(items))
	for _, item := range items {
		expires, err := time.Parse(time.RFC3339, item.ExpiresAt)
		if err != nil || !expires.After(now) {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// humanStatusBlock renders the "current chat partner state" block for the reply
// system prompt. It is advisory only; the model must not mention it.
func (d *Dispatcher) humanStatusBlock(ctx context.Context, conversationID string) string {
	s := d.settings.Effective()
	if !s.HumanStatusEnabled {
		return ""
	}
	items := d.unexpiredHumanStatuses(ctx, conversationID, 10)
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("【当前聊天对象状态推测（可能不准，仅用于调整语气/时机）】")
	for _, item := range items {
		name := humanStatusDisplayName(item)
		fmt.Fprintf(&b, "\n- %s：%s（置信度 %.2f）", name, item.Status, item.Confidence)
	}
	b.WriteString("\n这些只是推测，不要直接说出来，也不要拿它当事实质问对方。")
	return b.String()
}

// humanStatusDecisionLines is the same data for the commitment decision prompt.
func (d *Dispatcher) humanStatusDecisionLines(ctx context.Context, conversationID string) string {
	items := d.unexpiredHumanStatuses(ctx, conversationID, 10)
	if len(items) == 0 {
		return "（无）"
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		updated := item.UpdatedAt
		if t, err := time.Parse(time.RFC3339, item.UpdatedAt); err == nil {
			updated = t.In(cstZone()).Format("01-02 15:04")
		}
		lines = append(lines, fmt.Sprintf("- %s：%s（置信度 %.2f，更新于 %s）",
			humanStatusDisplayName(item), item.Status, item.Confidence, updated))
	}
	return strings.Join(lines, "\n")
}

func humanStatusDisplayName(item store.HumanStatus) string {
	if name := strings.TrimSpace(item.DisplayName); name != "" {
		return name
	}
	if id := strings.TrimSpace(item.MemberOpenID); id != "" {
		return id
	}
	return "未知对象"
}

// parseHumanStatusCount parses the optional 人数 argument of /human-status.
// Only a single integer in 1..humanStatusMaxCount is accepted; anything else
// (a non-number, an out-of-range number, extra arguments) is a usage error, not
// a silent fallback to the default.
func parseHumanStatusCount(args []string) (int, bool) {
	if len(args) == 0 {
		return humanStatusDefaultCount, true
	}
	if len(args) > 1 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || n < 1 || n > humanStatusMaxCount {
		return 0, false
	}
	return n, true
}

// humanStatusUsageText is the reply for a missing/invalid argument. It repeats
// the usage line so a typo in the chat immediately shows the right shape, and
// states the 24h rule because it explains why fewer people may come back.
func humanStatusUsageText() string {
	return "用法：" + humanStatusUsage + "\n人数范围 1–" + strconv.Itoa(humanStatusMaxCount) +
		"，缺省 " + strconv.Itoa(humanStatusDefaultCount) +
		"；只分析过去 24 小时在本会话发过言的人。"
}

// humanStatusRosterLines renders the analysed roster for the user prompt, one
// line per member, newest speaker first, so the model can see the allowed set
// even when a member's messages fall outside the timeline window.
func humanStatusRosterLines(order []string, names map[string]string) string {
	var b strings.Builder
	for _, id := range order {
		name := strings.TrimSpace(names[id])
		if name == "" {
			name = "未知"
		}
		fmt.Fprintf(&b, "- %s (%s)\n", name, id)
	}
	return b.String()
}

// humanStatusRosterItems keeps only the live states of roster members, in
// roster order (the roster is newest speaker first). Cached states of members
// outside the roster stay in the database for the prompt-injection path, but
// the command never renders them.
func humanStatusRosterItems(items []store.HumanStatus, roster []store.SpeakerActivity) []store.HumanStatus {
	byID := make(map[string]store.HumanStatus, len(items))
	for _, item := range items {
		if id := strings.TrimSpace(item.MemberOpenID); id != "" {
			byID[id] = item
		}
	}
	out := make([]store.HumanStatus, 0, len(roster))
	for _, speaker := range roster {
		id := strings.TrimSpace(speaker.OpenID)
		if id == "" {
			continue
		}
		item, ok := byID[id]
		if !ok {
			continue
		}
		if strings.TrimSpace(item.DisplayName) == "" {
			item.DisplayName = strings.TrimSpace(speaker.Name)
		}
		out = append(out, item)
	}
	return out
}

// runHumanStatus runs /human-status [人数]: it first resolves the roster of
// members who spoke in the last 24 hours (newest speaker first, at most 人数),
// then forces one synchronous inference restricted to that roster and renders
// the result. When the model call fails it falls back to the live cache, as
// before.
//
// The admin command is deliberate and therefore ignores human_status_enabled:
// the switch controls automatic speculation and prompt injection, not an
// operator asking for an analysis right now.
func runHumanStatus(ctx context.Context, d *Dispatcher, in *Inbound, s settings.Settings, args []string) (commandOutput, error) {
	if d == nil {
		return textOutput("分析失败，请稍后再试（服务不可用）。"), nil
	}
	count, ok := parseHumanStatusCount(args)
	if !ok {
		return textOutput(humanStatusUsageText()), nil
	}
	cfg := d.settings.GroupConfig(in.GroupOpenID)
	conv := resolveConversation(in, cfg.ContextMode)

	// The roster is the command's hard limit: it is computed from the messages
	// that actually exist in this conversation, so asking for 100 in a quiet
	// room analyses the few people who did speak, never a hundred.
	if d.st == nil {
		return textOutput("分析失败，请稍后再试（" + humanStatusFailureReason(errHumanStatusNoStore) + "）。"), nil
	}
	roster, err := d.st.RecentSpeakers(ctx, conv.ID, time.Now().Add(-humanStatusWindow), count)
	if err != nil {
		if d.log != nil {
			d.log.Warn("human status roster query failed", "component", "human-status",
				"conversation", conv.ID, "error", err.Error())
		}
		return textOutput("分析失败，请稍后再试（读取最近发言失败）。"), nil
	}
	if len(roster) == 0 {
		return textOutput("过去 24 小时没有可分析的对象。"), nil
	}

	refreshErr := d.RefreshHumanStatusFor(ctx, conv, in, s, humanStatusRefreshOptions{
		Members:  roster,
		Restrict: true,
	})
	if refreshErr != nil && d.log != nil {
		d.log.Warn("human status command inference failed", "component", "human-status",
			"conversation", conv.ID, "error", refreshErr.Error())
	}

	items := humanStatusRosterItems(d.unexpiredHumanStatuses(ctx, conv.ID, 0), roster)
	if in.Scene == "c2c" && len(items) > 1 {
		items = items[:1]
	}
	if len(items) == 0 {
		if refreshErr != nil {
			return textOutput("分析失败，请稍后再试（" + humanStatusFailureReason(refreshErr) + "）。"), nil
		}
		return textOutput("已分析，但暂时没有可推断的对象。"), nil
	}
	rows := make([]humanStatusRow, 0, len(items))
	for _, item := range items {
		evidence := strings.TrimSpace(strings.ReplaceAll(item.Evidence, "\n", " "))
		if runes := []rune(evidence); len(runes) > 40 {
			evidence = string(runes[:40]) + "…"
		}
		if evidence == "" {
			evidence = "—"
		}
		// The image has real columns and wraps long cells, so its evidence may be
		// longer than the one-line text list allows: 80 runes instead of 40.
		evidenceTable := strings.TrimSpace(strings.ReplaceAll(item.Evidence, "\n", " "))
		if runes := []rune(evidenceTable); len(runes) > 80 {
			evidenceTable = string(runes[:80]) + "…"
		}
		if evidenceTable == "" {
			evidenceTable = "—"
		}
		// 只显示时分：状态 TTL 最多两小时，日期在这里没有信息量，还占宽度。
		updated := item.UpdatedAt
		if t, err := time.Parse(time.RFC3339, item.UpdatedAt); err == nil {
			updated = t.In(time.Local).Format("15:04")
		}
		rows = append(rows, humanStatusRow{
			Object:        humanStatusDisplayName(item),
			Status:        item.Status,
			Confidence:    fmt.Sprintf("%.0f%%", item.Confidence*100),
			Evidence:      evidence,
			EvidenceTable: evidenceTable,
			Updated:       updated,
		})
	}

	// The text rendering stays the fallback: an image that cannot be rendered,
	// uploaded or delivered must never cost the operator the answer.
	var text string
	if s.MarkdownEnabled {
		text = humanStatusMarkdown(rows)
	} else {
		text = humanStatusList(rows)
	}

	png, err := renderTablePNG(humanStatusTable(rows))
	if err != nil {
		if d.log != nil {
			d.log.Warn("human status image render failed; using text",
				"component", "human-status", "conversation", conv.ID, "error", err.Error())
		}
		return textOutput(text), nil
	}
	return commandOutput{
		Text:     text,
		Image:    png,
		ImageAlt: fmt.Sprintf("[图片] 聊天对象状态推测（%d 人）", len(rows)),
	}, nil
}

// renderTablePNG is a seam for tests: the failure path (text fallback) is part
// of the contract and cannot be triggered by normal inputs.
var renderTablePNG = render.RenderPNG

// humanStatusTable projects the rows onto the generic PNG renderer. Object names
// are not truncated (the renderer has real columns and wraps), and evidence uses
// the longer 80-rune cut.
func humanStatusTable(rows []humanStatusRow) render.Table {
	t := render.Table{
		Title:   fmt.Sprintf("聊天对象状态推测（%d 人）", len(rows)),
		Headers: []string{"对象", "状态", "置信度", "依据", "更新"},
		Rows:    make([][]string, 0, len(rows)),
	}
	for _, row := range rows {
		evidence := row.EvidenceTable
		if evidence == "" {
			evidence = row.Evidence
		}
		t.Rows = append(t.Rows, []string{row.Object, row.Status, row.Confidence, evidence, row.Updated})
	}
	return t
}

// humanStatusFailureReason turns an inference error into a short, safe user
// facing reason. Raw errors can carry endpoints or database details, so the
// reply never echoes err.Error() itself.
func humanStatusFailureReason(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, errHumanStatusNoAI):
		return "模型未配置"
	case errors.Is(err, errHumanStatusNoStore):
		return "存储不可用"
	case errors.Is(err, context.DeadlineExceeded):
		return "超时"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "timeout"), strings.Contains(msg, "超时"):
		return "超时"
	case strings.Contains(msg, "not configured"), strings.Contains(msg, "未配置"):
		return "模型未配置"
	case strings.Contains(msg, "semaphore"):
		return "当前并发已满"
	case strings.Contains(msg, "no messages"), strings.Contains(msg, "no identifiable"):
		return "没有可分析的消息"
	case strings.Contains(msg, "401"), strings.Contains(msg, "403"),
		strings.Contains(msg, "unauthorized"), strings.Contains(msg, "forbidden"):
		return "模型鉴权失败"
	case strings.Contains(msg, "429"):
		return "模型限流，请稍后再试"
	case strings.Contains(msg, "http 5"):
		return "模型服务不可用"
	default:
		return "模型调用失败"
	}
}

type humanStatusRow struct {
	Object     string
	Status     string
	Confidence string
	Evidence   string
	// EvidenceTable is the longer (80 rune) evidence used by the image table;
	// the one-line text renderings keep the shorter Evidence.
	EvidenceTable string
	Updated       string
}

// humanStatusTitle is the shared first line of both renderings.
func humanStatusTitle(n int) string {
	return fmt.Sprintf("最近聊天对象状态（%d 人）：", n)
}

// humanStatusList renders the plain-text reply as one line per object.
// QQ group chat uses a proportional font: a space-padded table never lines up
// there, so this rendering must not depend on column alignment at all. Fields
// are separated by the full-width bar (｜), and object names are never
// truncated because a list has no fixed column width.
func humanStatusList(rows []humanStatusRow) string {
	var b strings.Builder
	b.WriteString(humanStatusTitle(len(rows)))
	for _, row := range rows {
		fmt.Fprintf(&b, "\n- %s｜%s｜置信度 %s｜依据 %s｜更新 %s",
			horizontalCell(row.Object), horizontalCell(row.Status),
			horizontalCell(row.Confidence), horizontalCell(row.Evidence),
			horizontalCell(row.Updated))
	}
	return b.String()
}

// humanStatusMarkdown renders the same columns as a monospace table wrapped in
// a fenced code block. QQ markdown does not document table syntax as
// supported (its format list is headings, emphasis, links, images, lists,
// quotes and rules), while fenced code blocks render with a monospace font on
// desktop QQNT, iOS and newer Android clients. Inside the fence the table is
// aligned with spaces and a dashed rule; the columns and their order are
// identical to the plain-text list.
func humanStatusMarkdown(rows []humanStatusRow) string {
	header := []string{"对象", "状态", "置信度", "依据", "更新"}
	all := make([][]string, 0, len(rows)+1)
	all = append(all, header)
	for _, row := range rows {
		all = append(all, []string{row.Object, row.Status, row.Confidence, row.Evidence, row.Updated})
	}
	widths := make([]int, len(header))
	for _, cells := range all {
		for i, cell := range cells {
			if w := displayWidth(horizontalCell(cell)); w > widths[i] {
				widths[i] = w
			}
		}
	}
	var b strings.Builder
	writeRow := func(cells []string) {
		for i, cell := range cells {
			cell = horizontalCell(cell)
			b.WriteString(cell)
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-displayWidth(cell)+2))
			}
		}
		b.WriteString("\n")
	}
	b.WriteString(humanStatusTitle(len(rows)))
	b.WriteString("\n\n" + fenceOpen + "\n")
	writeRow(header)
	// Dashed rule under the header, padded to the same visual widths.
	for i, w := range widths {
		b.WriteString(strings.Repeat("-", w))
		if i < len(widths)-1 {
			b.WriteString("  ")
		}
	}
	b.WriteString("\n")
	for _, cells := range all[1:] {
		writeRow(cells)
	}
	b.WriteString(fenceClose)
	return b.String()
}

const (
	fenceOpen  = "```text"
	fenceClose = "```"
)

// horizontalCell keeps one cell on one line and neutralises characters that
// would break the surrounding fence or the full-width field separator.
func horizontalCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, fenceClose, "'''")
	s = strings.ReplaceAll(s, "｜", "/")
	return strings.TrimSpace(s)
}

// displayWidth approximates terminal width: CJK / full-width runes take two
// columns. It is only used inside the monospace markdown table.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r >= 0x1100 && (r <= 0x115F || r == 0x2329 || r == 0x232A ||
			(r >= 0x2E80 && r <= 0xA4CF) || (r >= 0xAC00 && r <= 0xD7A3) ||
			(r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFE10 && r <= 0xFE19) ||
			(r >= 0xFE30 && r <= 0xFE6F) || (r >= 0xFF00 && r <= 0xFF60) ||
			(r >= 0xFFE0 && r <= 0xFFE6)):
			w += 2
		default:
			w++
		}
	}
	return w
}
