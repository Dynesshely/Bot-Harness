package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
	"github.com/dynesshely/bot-harness/internal/tools"
)

// caps the calls executed from a single model turn; the rest are answered with
// a short "skipped" note so the model can still finish.
const maxToolCallsPerStep = 4

// toolStep is one executed call, kept for the audit trail and logs.
type toolStep struct {
	Name   string
	Args   string
	Status string
	Err    string
	Output string
	MS     int64
	Chars  int
}

// agentTools returns the tool schemas advertised to the model for this turn.
func (d *Dispatcher) agentTools(s settings.Settings, cfg settings.GroupConfig, in *Inbound) []ai.ToolDef {
	if !s.ToolsEnabled || !cfg.ToolsEnabled || d.tools == nil {
		return nil
	}
	admin := d.isAdmin(s, in)
	return d.tools.Definitions(func(t tools.Tool) bool {
		switch t.Group() {
		case "memory":
			return s.MemoryEnabled && cfg.MemoryEnabled
		case "task":
			return cfg.TasksEnabled && cfg.ToolsEnabled
		case "commitment":
			return s.CommitmentEnabled && cfg.TasksEnabled && cfg.ToolsEnabled
		case "web":
			if !s.WebEnabled || !cfg.WebEnabled {
				return false
			}
			// web_search 只有在真的配了搜索后端时才下发。否则模型会看到一个
			// 每次调用都必然报「未配置搜索后端」的工具，等于对它说谎；
			// web_fetch 不依赖搜索后端，继续下发。
			if t.Name() == "web_search" {
				return webSearchConfigured(s)
			}
			return true
		case "admin":
			// 改 harness 配置只给管理员；没有管理员名单时 admin 恒为 false。
			return admin
		case "exec":
			// 开关 + 身份：执行命令只给管理员，非管理员连工具定义都不下发。
			return s.ExecEnabled && cfg.ExecEnabled && admin
		case "vision":
			// 关闭视觉时连 view_image 都不声明；工具内部还会再检查一次，
			// 防止模型被旧上下文诱导后自行调用。
			return s.VisionEnabled
		default:
			return true
		}
	})
}

// webSearchConfigured reports whether web_search can actually run: it needs both
// the searxng provider and its base URL. Advertising the tool without them would
// promise the model a capability the deployment does not have.
func webSearchConfigured(s settings.Settings) bool {
	return s.WebSearchProvider == "searxng" && strings.TrimSpace(s.WebSearchBaseURL) != ""
}

// converse drives the model, running tools in between until it produces an
// answer. It also owns the multimodal downgrade retry and makes sure the
// "let me think" notice is sent at most once per turn (first call only).
func (d *Dispatcher) converse(ctx context.Context, in *Inbound, s settings.Settings, cfg settings.GroupConfig,
	conv convTarget, messages []ai.Message, userText string, usedVision bool) (modelOutcome, []toolStep) {

	defs := d.agentTools(s, cfg, in)
	deadline := time.Now().Add(time.Duration(s.ToolsTotalBudgetSeconds) * time.Second)

	var (
		outcome modelOutcome
		steps   []toolStep
	)
	for step := 0; ; step++ {
		req := ai.ChatRequest{
			Model:       s.Model,
			Messages:    messages,
			Temperature: s.Temperature,
			MaxTokens:   s.MaxTokens,
			Stream:      true,
			Tools:       defs,
		}
		out := d.callModel(ctx, req, in, s, conv.ID, step == 0 && usedVision, step == 0)
		// 每一轮（含工具步）的 usage 都要累计：页脚报的是这一轮对话的真实开销。
		if out.result != nil {
			outcome.usage.PromptTokens += out.result.Usage.PromptTokens
			outcome.usage.CompletionTokens += out.result.Usage.CompletionTokens
			outcome.usage.TotalTokens += out.result.Usage.TotalTokens
		}
		// 系统提示词取**最终那一步**实际发出去的那份（记忆块可能在循环中途变化）。
		for _, m := range req.Messages {
			if m.Role == ai.RoleSystem {
				outcome.systemPrompt = m.Content
				break
			}
		}
		if step == 0 {
			outcome.result, outcome.err = out.result, out.err
			outcome.earlySent, outcome.earlyMessageID, outcome.earlySentAt = out.earlySent, out.earlyMessageID, out.earlySentAt
			outcome.usedVision = out.usedVision
		} else {
			outcome.result, outcome.err = out.result, out.err
		}

		if out.err != nil && step == 0 && usedVision {
			// The endpoint or model may not accept images. Retry once as text so
			// one unsupported modality does not swallow the whole answer.
			d.log.Warn("multimodal request failed, retrying as text-only",
				"component", "ai", "error", out.err.Error())
			textMessages := make([]ai.Message, len(messages))
			copy(textMessages, messages)
			textMessages[len(textMessages)-1] = ai.Message{Role: ai.RoleUser, Content: userText}
			out = d.callModel(ctx, ai.ChatRequest{
				Model: s.Model, Messages: textMessages,
				Temperature: s.Temperature, MaxTokens: s.MaxTokens, Stream: true, Tools: defs,
			}, in, s, conv.ID, false, false)
			if out.result != nil {
				outcome.usage.PromptTokens += out.result.Usage.PromptTokens
				outcome.usage.CompletionTokens += out.result.Usage.CompletionTokens
				outcome.usage.TotalTokens += out.result.Usage.TotalTokens
			}
			outcome.result, outcome.err = out.result, out.err
		}
		if out.err != nil && step == 0 && len(defs) > 0 && ctx.Err() == nil {
			// 首次请求带了工具却失败：先怀疑网关/模型不认识 tools 参数
			// （各家报错措辞不一），去掉工具再试一次。最坏情况只是多一次调用，
			// 好过「默认开启工具」把每条回复都打死。
			d.log.Warn("provider rejected the tools parameter, retrying without them",
				"component", "tools", "error", out.err.Error())
			defs = nil
			step--
			continue
		}
		if out.err != nil || out.result == nil {
			return outcome, steps
		}
		if len(out.result.ToolCalls) == 0 {
			// 推理模型可能把预算花在 reasoning 上，导致 content 在句子中间停止。
			// 只重试一次，且用更大的输出预算；不改用户配置。
			if needsCompletion(out.result) {
				d.log.Warn("reply looks truncated, retrying with a larger budget",
					"component", "ai", "chars", len([]rune(out.result.Text)),
					"finish_reason", out.result.FinishReason,
					"max_tokens", completionRetryTokens(s.MaxTokens),
					"conversation", conv.ID)
				retry := d.callModel(ctx, ai.ChatRequest{
					Model: s.Model, Messages: messages, Temperature: s.Temperature,
					MaxTokens: completionRetryTokens(s.MaxTokens), Stream: true, Tools: nil,
				}, in, s, conv.ID, false, false)
				if retry.result != nil {
					outcome.usage.PromptTokens += retry.result.Usage.PromptTokens
					outcome.usage.CompletionTokens += retry.result.Usage.CompletionTokens
					outcome.usage.TotalTokens += retry.result.Usage.TotalTokens
				}
				if retry.err == nil && retry.result != nil && !needsCompletion(retry.result) {
					out = retry
				} else if retry.result != nil && len([]rune(retry.result.Text)) > len([]rune(out.result.Text)) {
					// 仍然不完整，但至少更长：保留它，交给润色/发送层继续兜底。
					out = retry
				} else {
					if retry.err != nil {
						d.log.Warn("truncation retry failed; keeping the original draft",
							"component", "ai", "error", retry.err.Error(), "conversation", conv.ID)
					}
					return outcome, steps
				}
				outcome.result, outcome.err = out.result, out.err
			}
			return outcome, steps
		}

		// Out of budget: hand the model what it already has and stop offering tools.
		if len(defs) == 0 || step+1 >= s.ToolsMaxSteps || time.Now().After(deadline) {
			d.log.Warn("tool budget exhausted, answering with what is available",
				"component", "tools", "steps", step+1, "conversation", conv.ID)
			defs = nil
			messages = append(messages,
				ai.Message{Role: ai.RoleAssistant, Content: out.result.Text, ToolCalls: out.result.ToolCalls},
				ai.Message{Role: ai.RoleUser, Content: "（工具调用次数已用完，请直接用现有信息回答，不要再调用工具。）"})
			continue
		}

		messages = append(messages, ai.Message{
			Role: ai.RoleAssistant, Content: out.result.Text, ToolCalls: out.result.ToolCalls,
		})
		for i, call := range out.result.ToolCalls {
			if i >= maxToolCallsPerStep {
				messages = append(messages, ai.ToolMessage(call.ID, "（本轮调用过多，已跳过）"))
				continue
			}
			record := d.runTool(ctx, in, s, conv, call)
			steps = append(steps, record)
			messages = append(messages, ai.ToolMessage(call.ID, record.Output))
		}
	}
}

// toolEnv builds the per-call tool environment.
//
// AdminCount rides next to IsAdmin on purpose: run_shell must be able to reject
// the "exec is on but nobody is an administrator" configuration explicitly,
// without trusting the identity computation to keep voting false for an empty
// list (T28). Other tools ignore the field.
func (d *Dispatcher) toolEnv(s settings.Settings, in *Inbound, conv convTarget) *tools.Env {
	return &tools.Env{
		ConversationID: conv.ID,
		GroupOpenID:    in.GroupOpenID,
		MemberOpenID:   firstNonEmpty(in.MemberOpenID, in.UserOpenID),
		MemberName:     in.MemberName,
		IsGroup:        in.GroupOpenID != "",
		IsAdmin:        d.isAdmin(s, in),
		AdminCount:     len(s.AdminOpenIDs),
		Log:            d.log,
	}
}

// runTool executes one call, writes the audit row and returns what the model
// should see. A tool error is reported to the model as text, never as a crash:
// the model can apologise or try another route.
func (d *Dispatcher) runTool(ctx context.Context, in *Inbound, s settings.Settings, conv convTarget, call ai.ToolCall) toolStep {
	start := time.Now()
	record := toolStep{Name: call.Function.Name, Args: call.Function.Arguments, Status: store.ToolCallOK}

	env := d.toolEnv(s, in, conv)

	args := json.RawMessage(call.Function.Arguments)
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	} else if !json.Valid(args) {
		record.Status, record.Err = store.ToolCallError, "arguments are not valid JSON"
		record.Output = "工具参数不是合法 JSON，请重新生成。"
		record.MS = time.Since(start).Milliseconds()
		d.auditToolCall(ctx, in, conv, record, env)
		return record
	}

	res, err := d.tools.Call(ctx, call.Function.Name, args, env, s.ToolsResultMaxChars)
	record.MS = time.Since(start).Milliseconds()
	switch {
	case errors.Is(err, tools.ErrAdminOnly), errors.Is(err, tools.ErrNoAdminsConfigured):
		// Not a malfunction: the caller is not allowed, or the deployment has no
		// administrators configured at all (T28). Both are policy rejections, not
		// execution errors, so the audit trail shows "rejected".
		record.Status, record.Err = store.ToolCallRejected, err.Error()
		record.Output = "这个工具只对管理员开放，你无法使用。请直接回答用户。"
	case err != nil:
		record.Status, record.Err = store.ToolCallError, err.Error()
		record.Output = "工具执行失败：" + err.Error()
	default:
		record.Output, record.Chars = res.Text, len([]rune(res.Text))
	}
	d.auditToolCall(ctx, in, conv, record, env)
	return record
}

// auditToolCall persists the invocation and emits one log line. Failure to audit
// must never break the reply, so problems are only logged.
func (d *Dispatcher) auditToolCall(ctx context.Context, in *Inbound, conv convTarget, rec toolStep, env *tools.Env) {
	d.log.Info("tool call", "component", "tools", "tool", rec.Name, "status", rec.Status,
		"latency_ms", rec.MS, "chars", rec.Chars, "conversation", conv.ID, "error", rec.Err)
	d.metrics.ToolCalls.Add(1)
	d.publish("tool", map[string]any{
		"tool": rec.Name, "status": rec.Status, "latency_ms": rec.MS,
		"conversation_id": conv.ID, "chars": rec.Chars,
	})

	if _, err := d.st.AddToolCall(ctx, store.ToolCallInput{
		ConversationID: conv.ID,
		GroupOpenID:    env.GroupOpenID,
		MemberOpenID:   env.MemberOpenID,
		Tool:           rec.Name,
		Args:           truncateForLog(rec.Args, 500),
		Status:         rec.Status,
		Error:          rec.Err,
		LatencyMS:      rec.MS,
		ResultChars:    rec.Chars,
	}); err != nil {
		d.log.Warn("failed to record tool call", "component", "store", "error", err.Error())
	}
}

// isAdmin reports whether the sender is one of admin_openids.
func (d *Dispatcher) isAdmin(s settings.Settings, in *Inbound) bool {
	for _, id := range s.AdminOpenIDs {
		if id == in.MemberOpenID || id == in.UserOpenID {
			return true
		}
	}
	return false
}
