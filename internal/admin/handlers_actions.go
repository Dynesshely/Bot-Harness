package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/qq"
)

func (s *Server) handleReconnect(w http.ResponseWriter, _ *http.Request) {
	if s.deps.WS == nil {
		writeError(w, http.StatusBadRequest, "not_configured", "当前传输模式未启用 WebSocket（qq_transport="+s.deps.Settings.Effective().QQTransport+"）")
		return
	}
	s.deps.WS.ForceReconnect()
	s.deps.Logger.Info("manual reconnect requested", "component", "admin")
	writeOK(w, map[string]any{"requested": true})
}

func (s *Server) handleReloadToken(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Tokens == nil {
		writeError(w, http.StatusBadRequest, "not_configured", "未配置 QQ 凭证")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := s.deps.Tokens.Token(ctx, true); err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	st := s.deps.Tokens.Status()
	writeOK(w, map[string]any{
		"token_valid":              st.Valid,
		"token_expires_in_seconds": st.ExpiresInSeconds,
		"token_refreshes":          st.Refreshes,
	})
}

// handleSyncCommands reconciles the QQ command panel with the command registry.
//
// It is the manual escape hatch: the automatic startup sync is skipped when
// command_panel_enabled is false, but an admin can always force a run here.
// Platform refusals (no permission, content risk) are returned as
// upstream_error with the original platform text, so the operator can see why.
func (s *Server) handleSyncCommands(w http.ResponseWriter, r *http.Request) {
	if s.deps.Dispatcher == nil {
		writeError(w, http.StatusBadRequest, "not_configured", "调度器尚未初始化，无法同步指令面板")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	results, err := s.deps.Dispatcher.SyncCommandPanel(ctx)
	if err != nil {
		s.deps.Logger.Warn("command panel sync failed", "component", "admin", "error", err.Error())
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	s.deps.Logger.Info("command panel synced manually", "component", "admin", "scopes", len(results))
	writeOK(w, map[string]any{"results": results})
}

func (s *Server) handleResetConfig(w http.ResponseWriter, _ *http.Request) {
	// 口令是凭据不是覆盖项：Reset 会保留它，只有在「从未生成过」时才会补一个。
	// 记下重置前的状态，才能准确告诉运维这次到底发生了什么。
	beforeToken := strings.TrimSpace(s.deps.Settings.Get().AdminToken)
	cur, err := s.deps.Settings.Reset(ctxBg())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	s.applyLimit()
	s.deps.Logger.Warn("runtime overrides reset to built-in defaults", "component", "admin")
	warnings := []string{}
	// 端点 / Key 不再是「.env 兜底」，重置后就是未配置；密钥若被保留则只提醒缺的那一项。
	var missing []string
	if strings.TrimSpace(cur.OpenAIBaseURL) == "" {
		missing = append(missing, "模型端点")
	}
	if strings.TrimSpace(cur.OpenAIAPIKey) == "" {
		missing = append(missing, "API Key")
	}
	if len(missing) > 0 {
		warnings = append(warnings,
			strings.Join(missing, " / ")+" 只存在数据库里，重置后已变为未配置：请立即到「系统设置」重新填写，否则所有模型调用都会失败")
	}
	if beforeToken == "" && strings.TrimSpace(cur.AdminToken) != "" {
		warnings = append(warnings,
			"后台口令此前为空，已重新生成并持久化：请用 `bot-harness token` 读取（旧登录会话已失效）")
	}
	// 重置后的文档同样只进不出：凭据即使被保留，也绝不能出现在响应里。
	writeOK(w, map[string]any{"settings": maskSecrets(cur), "warnings": warnings})
}

func (s *Server) handleTestAI(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
		// Tools advertises one throwaway tool so the operator can check whether
		// the endpoint accepts the tools parameter at all.
		Tools bool `json:"tools"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	body.Prompt = strings.TrimSpace(body.Prompt)
	if body.Prompt == "" {
		body.Prompt = "你好，请用一句话自我介绍。"
	}
	if s.deps.AI == nil {
		writeError(w, http.StatusBadRequest, "not_configured", "未配置 AI 端点")
		return
	}

	cur := s.deps.Settings.Effective()
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cur.RequestTimeoutSeconds+15)*time.Second)
	defer cancel()

	req := ai.ChatRequest{
		Model:       cur.Model,
		Messages:    []ai.Message{{Role: ai.RoleUser, Content: body.Prompt}},
		Temperature: cur.Temperature,
		MaxTokens:   min(cur.MaxTokens, 512),
		Stream:      true,
	}
	if body.Tools {
		req.Tools = []ai.ToolDef{ai.NewToolDef("echo_probe",
			"把收到的 text 原样返回。只在需要确认工具调用链路时使用。",
			json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`))}
		req.ToolChoice = "auto"
	}

	start := time.Now()
	res, err := s.deps.AI.Chat(ctx, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	writeOK(w, map[string]any{
		"text":       res.Text,
		"model":      res.Model,
		"latency_ms": time.Since(start).Milliseconds(),
		"usage": map[string]any{
			"prompt_tokens":     res.Usage.PromptTokens,
			"completion_tokens": res.Usage.CompletionTokens,
			"total_tokens":      res.Usage.TotalTokens,
		},
		"tools_advertised": body.Tools,
		"tool_calls":       toolCallNames(res.ToolCalls),
		"finish_reason":    res.FinishReason,
	})
}

func (s *Server) handleTestSend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		GroupOpenID string `json:"group_openid"`
		Content     string `json:"content"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	body.GroupOpenID = strings.TrimSpace(body.GroupOpenID)
	body.Content = strings.TrimSpace(body.Content)
	if body.GroupOpenID == "" || body.Content == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "group_openid 与 content 均为必填")
		return
	}
	if !s.deps.Dispatcher.Limiter().Allow("admin:test-send", 10) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "测试发送过于频繁（每分钟最多 10 次）")
		return
	}

	cur := s.deps.Settings.Effective()
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	res, err := s.deps.Dispatcher.Sender().Send(ctx, qq.SendOptions{
		GroupOpenID: body.GroupOpenID,
		MaxChars:    cur.ReplyMaxChars,
		MaxSegments: 1,
	}, body.Content)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	s.deps.Logger.Warn("admin test message sent", "component", "admin", "group", body.GroupOpenID)
	writeOK(w, map[string]any{
		"message_ids": res.MessageIDs,
		"message_id":  firstOrEmpty(res.MessageIDs),
		"segments":    res.Segments,
		"active":      true,
	})
}

func firstOrEmpty(v []string) string {
	if len(v) > 0 {
		return v[0]
	}
	return ""
}

func toolCallNames(calls []ai.ToolCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Function.Name)
	}
	return out
}
