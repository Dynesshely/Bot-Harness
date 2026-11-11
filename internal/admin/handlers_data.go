package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/logging"
	"github.com/dynesshely/bot-harness/internal/store"
)

func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	f := store.ConversationFilter{
		Query:    strings.TrimSpace(r.URL.Query().Get("q")),
		Scene:    strings.TrimSpace(r.URL.Query().Get("scene")),
		Enabled:  queryBoolPtr(r, "enabled"),
		Page:     clampPage(queryInt(r, "page", 1)),
		PageSize: clampPageSize(queryInt(r, "page_size", defaultConversationPageSize), defaultConversationPageSize),
		Sort:     strings.TrimSpace(r.URL.Query().Get("sort")),
	}
	items, total, err := s.deps.Store.ListConversations(ctxBg(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if items == nil {
		items = []store.Conversation{}
	}
	writeOK(w, map[string]any{
		"total":     total,
		"page":      f.Page,
		"page_size": f.PageSize,
		"items":     items,
	})
}

func (s *Server) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conv, err := s.deps.Store.GetConversation(ctxBg(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "会话不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	limit := queryInt(r, "limit", 200)
	before := queryInt64(r, "before_id", 0)
	msgs, err := s.deps.Store.Messages(ctxBg(), id, limit, before)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if msgs == nil {
		msgs = []store.Message{}
	}
	// 群会话附带平台侧的真实状态（是否全量接收、能否主动推送、我们的群内 openid），
	// 这样后台显示的是平台事实，而不是我们自己的假设。
	var platform *core.GroupSnapshot
	if conv.GroupOpenID != "" && s.deps.Dispatcher != nil && s.deps.Dispatcher.Identity() != nil {
		identity := s.deps.Dispatcher.Identity()
		// 主动确保一次（内部按群缓存 30 分钟、并发去重），
		// 这样后台展示的是平台事实而不是「还没取到」。
		probeCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		identity.GroupState(probeCtx, conv.GroupOpenID)
		cancel()
		platform = identity.GroupSnapshot(conv.GroupOpenID)
	}

	// 引用回复的生效值 / 是否有按群覆盖 / 全局默认：会话详情里的开关要用。
	// quote_reply 是兼容用的布尔视图，quote_reply_mode 是新的概率分档。
	groupCfg := s.deps.Settings.GroupConfig(conv.GroupOpenID)
	quoteReply := groupCfg.QuoteReply
	quoteMode := groupCfg.QuoteReplyMode
	quoteGlobal := s.deps.Settings.Get().QuoteReply
	quoteGlobalMode := s.deps.Settings.Get().QuoteReplyMode
	quoteOverride := s.deps.Settings.GroupQuoteReplyOverride(conv.GroupOpenID)

	// 今日主动消息条数：手动发送与机器人自己的主动兜底共用同一份平台配额，
	// 后台要能看到已经用掉多少（私聊没有这个按群的额度，固定为 0）。
	activeToday := 0
	if conv.GroupOpenID != "" && s.deps.Dispatcher != nil {
		activeToday = s.deps.Dispatcher.ActiveMessagesToday(ctxBg(), conv.GroupOpenID)
	}

	writeOK(w, map[string]any{
		"platform":                platform,
		"id":                      conv.ID,
		"scene":                   conv.Scene,
		"group_openid":            conv.GroupOpenID,
		"group_name":              conv.GroupName,
		"member_openid":           conv.MemberOpenID,
		"member_name":             conv.MemberName,
		"enabled":                 conv.Enabled,
		"message_count":           conv.MessageCount,
		"turn_count":              conv.TurnCount,
		"token_estimate":          conv.TokenEstimate,
		"created_at":              conv.CreatedAt,
		"last_active_at":          conv.LastActiveAt,
		"active_today":            activeToday,
		"quote_reply":             quoteReply,
		"quote_reply_mode":        quoteMode,
		"quote_reply_override":    quoteOverride,
		"quote_reply_global":      quoteGlobal,
		"quote_reply_global_mode": quoteGlobalMode,
		"messages":                msgs,
	})
}

func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.deps.Store.GetConversation(ctxBg(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "会话不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if err := s.deps.Store.ClearConversationMessages(ctxBg(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.deps.Logger.Info("conversation cleared", "component", "admin", "id", id)
	writeOK(w, map[string]any{"cleared": true})
}

// handleGroupState returns the platform-side state of one group.
// `?refresh=1` bypasses the cache (the admin panel's manual refresh button).
func (s *Server) handleGroupState(w http.ResponseWriter, r *http.Request) {
	groupOpenID := r.PathValue("group_openid")
	if groupOpenID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "缺少 group_openid")
		return
	}
	if s.deps.Dispatcher == nil || s.deps.Dispatcher.Identity() == nil {
		writeError(w, http.StatusBadRequest, "not_configured", "QQ 客户端未配置")
		return
	}
	identity := s.deps.Dispatcher.Identity()

	probeCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if r.URL.Query().Get("refresh") == "1" || r.URL.Query().Get("refresh") == "true" {
		identity.Refresh(probeCtx, groupOpenID)
	} else {
		identity.GroupState(probeCtx, groupOpenID)
	}

	snapshot := identity.GroupSnapshot(groupOpenID)
	if snapshot == nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "无法从平台读取该群的机器人状态（可能是接口未授权或群不存在）")
		return
	}
	writeOK(w, snapshot)
}

func (s *Server) handleSetConversationEnabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "缺少 enabled 字段")
		return
	}
	if err := s.deps.Store.SetConversationEnabled(ctxBg(), id, *body.Enabled); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "会话不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"id": id, "enabled": *body.Enabled})
}

func (s *Server) handleClearConversations(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("confirm") != "CLEAR_ALL" {
		writeError(w, http.StatusBadRequest, "invalid_request", "缺少 confirm=CLEAR_ALL 确认参数")
		return
	}
	if err := s.deps.Store.ClearAllConversations(ctxBg()); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.deps.Logger.Warn("all conversations cleared", "component", "admin")
	writeOK(w, map[string]any{"cleared": true})
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	f := store.RecordFilter{
		Direction: strings.TrimSpace(r.URL.Query().Get("direction")),
		Status:    strings.TrimSpace(r.URL.Query().Get("status")),
		Query:     strings.TrimSpace(r.URL.Query().Get("q")),
		Since:     strings.TrimSpace(r.URL.Query().Get("since")),
		Until:     strings.TrimSpace(r.URL.Query().Get("until")),
		Page:      clampPage(queryInt(r, "page", 1)),
		PageSize:  clampPageSize(queryInt(r, "page_size", defaultMessagePageSize), defaultMessagePageSize),
	}
	items, total, err := s.deps.Store.ListRecords(ctxBg(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if items == nil {
		items = []store.Record{}
	}
	writeOK(w, map[string]any{
		"total":     total,
		"page":      f.Page,
		"page_size": f.PageSize,
		"items":     items,
	})
}

func (s *Server) handleListLogs(w http.ResponseWriter, r *http.Request) {
	level := logging.ParseLevel(r.URL.Query().Get("level"))
	if r.URL.Query().Get("level") == "" {
		level = logging.ParseLevel("info")
	}
	limit := queryInt(r, "limit", 200)
	before := queryInt64(r, "before_id", 0)
	items := s.deps.LogRing.List(level, r.URL.Query().Get("q"), limit, before)
	if items == nil {
		items = []logging.Entry{}
	}
	writeOK(w, map[string]any{"items": items})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	spec := core.ParseRange(r.URL.Query().Get("range"))
	series := s.deps.Dispatcher.Stats().Series(spec)
	totals := s.deps.Dispatcher.Stats().Totals(spec)
	top, err := s.deps.Store.TopGroups(ctxBg(), timeCutoff(spec), 10)
	if err != nil {
		top = nil
	}
	if top == nil {
		top = []store.TopGroup{}
	}
	writeOK(w, map[string]any{
		"range":      spec.Range,
		"bucket":     bucketLabel(spec),
		"series":     series,
		"totals":     totals,
		"top_groups": top,
	})
}

func bucketLabel(spec core.RangeSpec) string {
	switch spec.Bucket {
	case 5 * time.Minute:
		return "5m"
	case 30 * time.Minute:
		return "30m"
	case time.Hour:
		return "1h"
	case 6 * time.Hour:
		return "6h"
	default:
		return spec.Bucket.String()
	}
}

func timeCutoff(spec core.RangeSpec) string {
	return time.Now().Add(-spec.Window).Format(time.RFC3339)
}

// handleListToolCalls returns the agent's tool audit trail, newest first.
func (s *Server) handleListToolCalls(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	before := int64(queryInt(r, "before_id", 0))
	rows, err := s.deps.Store.ListToolCalls(r.Context(), limit, before)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	next := 0
	if len(rows) == limit && len(rows) > 0 {
		next = int(rows[len(rows)-1].ID)
	}
	writeOK(w, map[string]any{"items": rows, "next_before_id": next})
}
