package core

import (
	"context"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/settings"
)

// 两阶段生成（stage 1 = 主模型草稿，stage 2 = 无上下文润色）。
//
// 第二阶段**只**拿到两样东西：草稿文本，以及已经解析好的系统提示词
// （人设/格式规则都在里面，来自 d.promptFor）。它看不到历史消息、用户原文、
// 工具结果 —— 这既是隐私边界，也保证润色模型只能改写措辞，不能凭空补事实。
//
// 任何失败（HTTP 错误、超时、空结果、开关关闭、草稿太短）都退回草稿：
// 一轮对话不能因为润色而失败。

// defaultPolishInstruction 是追加在系统提示词后面的硬约束。
//
// 刻意写得像「编译期约束」而不是「风格建议」：模型最常犯的毛病是加事实、
// 加链接、加承诺、加解释段落，所以逐条禁止。
const defaultPolishInstruction = `你是同一角色的润色器。只重写下面这段草稿，使其符合角色口吻与格式；` +
	`不得新增事实、链接、数字、承诺；不得解释；保持 Markdown 与长度（±30%）；只输出正文。`

// polishMinMaxTokens is the floor of the "auto" output budget.
const polishMinMaxTokens = 128

// polishResult is the outcome of stage 2.
type polishResult struct {
	// Text is what must be delivered: the polished draft on success, the draft
	// itself on any fallback.
	Text string
	// Attempted reports whether a stage-2 request actually went out. A skipped
	// turn (switch off, empty draft, below polish_min_chars) did not.
	Attempted bool
	// Polished reports whether stage 2 actually produced this text.
	Polished bool
	// Usage is the polish call's token accounting (zero when it never ran).
	Usage ai.Usage
	// LatencyMS is the stage-2 wall clock.
	LatencyMS int64
	// Model is the model that was asked to polish.
	Model string
	// Fallback is the logged reason stage 2 did not win (empty on success).
	Fallback string
	// Prompt is what stage 2 was actually sent (empty when it never ran), kept
	// only so the token estimate of a usage-less provider stays honest.
	Prompt []ai.Message
}

// polishUsable reports whether stage 2 may run at all for this turn.
func polishUsable(s settings.Settings, cfg settings.GroupConfig, draft string) (bool, string) {
	// cfg.PolishEnabled is already the resolved value: GroupConfig seeds it from
	// polish_enabled and lets a per-group override flip it either way. Checking
	// the global again here would make "global off, this group on" impossible.
	if !cfg.PolishEnabled {
		return false, "disabled"
	}
	if strings.TrimSpace(draft) == "" {
		return false, "empty draft"
	}
	if n := len([]rune(draft)); n < s.PolishMinChars {
		return false, "draft below polish_min_chars"
	}
	return true, ""
}

// polishMaxTokensFor resolves the stage-2 output budget.
//
// 0 means "auto": the draft's estimated tokens ×1.5, floored at 128 and capped
// by the main MaxTokens. The cap matters because a polish call that runs away
// is worse than no polish at all — the draft is already a complete answer.
func polishMaxTokensFor(s settings.Settings, draft string) int {
	if s.PolishMaxTokens > 0 {
		return s.PolishMaxTokens
	}
	est := EstimateTokens(draft)
	auto := (est*3 + 1) / 2 // ×1.5, rounded up
	if auto < polishMinMaxTokens {
		auto = polishMinMaxTokens
	}
	if s.MaxTokens > 0 && auto > s.MaxTokens {
		auto = s.MaxTokens
	}
	return auto
}

// polishDeadline picks the shorter of the configured polish timeout and whatever
// is left of the turn's request deadline.
func polishDeadline(ctx context.Context, timeoutSeconds int) time.Duration {
	d := time.Duration(timeoutSeconds) * time.Second
	if d <= 0 {
		d = 20 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if left := time.Until(deadline); left < d {
			if left <= 0 {
				return 0
			}
			d = left
		}
	}
	return d
}

// newPolish runs stage 2 and always returns text that can be delivered.
//
// The caller must already hold aiSem: this function deliberately does **not**
// acquire it, because processMessage takes it around the whole turn and
// MaxConcurrentLLM defaults to 4 (1 in tests) — a nested Acquire would deadlock.
func (d *Dispatcher) newPolish(ctx context.Context, s settings.Settings, cfg settings.GroupConfig,
	systemPrompt, draft, stateLine string) polishResult {

	res := polishResult{Text: draft}
	res.Model = strings.TrimSpace(s.PolishModel)
	if res.Model == "" {
		res.Model = s.Model
	}

	// A scheduled task may run with a prompt of its own; processMessage always
	// passes d.promptFor(s, cfg), which already carries the persona.
	if ok, why := polishUsable(s, cfg, draft); !ok {
		res.Fallback = why
		return res
	}

	timeout := polishDeadline(ctx, s.PolishTimeoutSeconds)
	if timeout <= 0 {
		res.Fallback = "no time left in the request budget"
		return res
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Only two messages, ever: the persona/format rules and the draft. No
	// history, no user text, no tool output.
	// The state line is optional and already capped at ~25 tokens. It is
	// appended to the persona so stage 2 can keep the voice, not to add facts.
	system := strings.TrimSpace(systemPrompt)
	if line := strings.TrimSpace(stateLine); line != "" {
		system += "\n\n" + line
	}
	system += "\n\n" + defaultPolishInstruction
	messages := []ai.Message{
		{Role: ai.RoleSystem, Content: system},
		{Role: ai.RoleUser, Content: draft},
	}
	res.Prompt = messages

	if d.ai == nil {
		res.Fallback = "no model client"
		return res
	}
	res.Attempted = true

	start := time.Now()
	out, err := d.ai.Chat(pctx, ai.ChatRequest{
		Model:       res.Model,
		Messages:    messages,
		Temperature: s.PolishTemperature,
		MaxTokens:   polishMaxTokensFor(s, draft),
		// Stream: true 与主路径一致：有些网关只实现了流式，非流式请求直接 400。
		Stream: true,
		// Tools 必须为 nil：润色器不许再调工具，否则它会去查证而不是改写。
		Tools: nil,
	})
	res.LatencyMS = time.Since(start).Milliseconds()

	switch {
	case err != nil:
		res.Fallback = "error: " + err.Error()
	case out == nil || strings.TrimSpace(out.Text) == "":
		res.Fallback = "empty result"
	default:
		res.Text = out.Text
		res.Polished = true
		res.Fallback = ""
		res.Usage = out.Usage
	}
	return res
}
