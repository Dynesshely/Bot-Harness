package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/store"
)

// handleSendConversationMessage posts a text message to the conversation's group
// or C2C chat as the bot. It is the only endpoint that writes to QQ on an
// operator's behalf, so every failure mode gets its own status code and a
// message that says what to do about it.
func (s *Server) handleSendConversationMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Content string `json:"content"`
		// QuoteMessageID is our own message row id to quote (the panel gets it
		// from the timeline). The server resolves it to the platform's reference
		// index, so a client can never inject a raw index.
		QuoteMessageID int64 `json:"quote_message_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if s.deps.Dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "调度器尚未就绪")
		return
	}

	// Manual sends are always active messages; give the platform call room.
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()

	res, err := s.deps.Dispatcher.SendManual(ctx, id, body.Content, body.QuoteMessageID)
	if err != nil {
		writeManualSendError(w, err)
		return
	}
	s.deps.Logger.Info("admin sent a message",
		"component", "admin", "conversation", id, "segments", res.Segments, "quoted", res.Quoted)
	writeOK(w, res)
}

// handleSetQuoteReply flips the per-group 引用回复 switch shown in the
// conversation detail page. The override lives in the policies document, but the
// detail page owns the control, so it gets a dedicated endpoint.
func (s *Server) handleSetQuoteReply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	conv, err := s.deps.Store.GetConversation(ctxBg(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "会话不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if conv.GroupOpenID == "" {
		writeError(w, http.StatusConflict, "no_group", "只有群会话能设置引用回复（私聊跟随全局默认）")
		return
	}
	// enabled 为空表示清掉覆盖、回到「跟随全局」。
	if err := s.deps.Settings.SetGroupQuoteReply(ctxBg(), conv.GroupOpenID, body.Enabled); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	cfg := s.deps.Settings.GroupConfig(conv.GroupOpenID)
	writeOK(w, map[string]any{
		"group_openid": conv.GroupOpenID,
		"quote_reply":  cfg.QuoteReply,
		"override":     s.deps.Settings.GroupQuoteReplyOverride(conv.GroupOpenID),
		"global":       s.deps.Settings.Get().QuoteReply,
	})
}

func writeManualSendError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
	case errors.Is(err, core.ErrManualSendDisabled):
		writeError(w, http.StatusForbidden, "manual_send_disabled",
			"后台已关闭手动发送（系统设置 → 手动发送）")
	case errors.Is(err, core.ErrManualSendEmpty):
		writeError(w, http.StatusBadRequest, "empty_content", "消息内容不能为空")
	case errors.Is(err, core.ErrManualSendTooLong):
		writeError(w, http.StatusBadRequest, "content_too_long", err.Error())
	case errors.Is(err, core.ErrManualSendLimited):
		writeError(w, http.StatusTooManyRequests, "rate_limited", err.Error())
	case errors.Is(err, core.ErrManualSendRejected):
		writeError(w, http.StatusConflict, "rejected_by_platform", err.Error())
	case errors.Is(err, core.ErrManualSendProactive):
		writeError(w, http.StatusConflict, "no_proactive_permission",
			"该群未开启主动消息权限，无法发送（QQ 群设置 → 机器人 → 主动消息）")
	case errors.Is(err, core.ErrManualSendNoTarget):
		writeError(w, http.StatusConflict, "no_target", err.Error())
	case errors.Is(err, core.ErrManualSendQuoteDisabled):
		writeError(w, http.StatusConflict, "quote_disabled", "该群已关闭引用回复（会话详情里可打开）")
	case errors.Is(err, core.ErrManualSendQuoteUnavailable):
		writeError(w, http.StatusConflict, "quote_unavailable", err.Error())
	default:
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
	}
}
