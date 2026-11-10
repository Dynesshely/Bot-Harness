package admin

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/affect"
	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/store"
)

// Affect REST surface (design: docs/AFFECT.md).
//
// All endpoints are authenticated by the same admin session as the rest of the
// panel. The service is optional: when it is not wired the reads that only need
// the pack catalogue keep working and the rest answer 503 rather than panicking,
// so a partially configured deployment still shows a usable panel.

const (
	affectMaxConversationLen = 256
	affectMaxLimit           = 200
	affectMaxHours           = 720
	affectMaxPresetLen       = 64
	affectMaxPauseMinutes    = 10080 // 7 days, matching docs/ADMIN-API.md
)

// handleAffectPacks lists the embedded content packs. It reads the catalogue
// directly: packs exist even when the service (or its database) does not.
func (s *Server) handleAffectPacks(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, map[string]any{"packs": affect.PackInfos()})
}

// handleAffectConversations lists the conversations that have a state row.
func (s *Server) handleAffectConversations(w http.ResponseWriter, r *http.Request) {
	if s.deps.Affect == nil || s.deps.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "情绪状态服务未启用")
		return
	}
	limit := queryInt(r, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > affectMaxLimit {
		limit = affectMaxLimit
	}
	ids, err := s.deps.Affect.Conversations(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		st, err := s.deps.Store.GetAffectState(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		if st == nil {
			continue
		}
		items = append(items, map[string]any{
			"id":         id,
			"pack":       st.PackID,
			"updated_at": st.UpdatedAt,
			"dims":       st.Dims,
		})
	}
	writeOK(w, map[string]any{"conversations": items})
}

// handleAffectState returns the derived view plus both injection previews.
//
// The previews deliberately ignore the master switch and the current inject
// mode: they exist so an operator can see what line/full would render before
// enabling the feature. Pause and an unknown conversation still yield empty
// blocks, because that is what the reply path would actually inject.
func (s *Server) handleAffectState(w http.ResponseWriter, r *http.Request) {
	if s.deps.Affect == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "情绪状态服务未启用")
		return
	}
	conversationID, ok := affectConversationParam(w, r)
	if !ok {
		return
	}
	conv, ok := s.affectConversation(w, r, conversationID)
	if !ok {
		return
	}
	ctx := r.Context()
	now := time.Now()
	snapshot, err := s.deps.Affect.Snapshot(ctx, conversationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	preview := s.deps.Settings.Effective()
	preview.AffectEnabled = true
	cfg := s.deps.Settings.GroupConfig(conv.GroupOpenID)
	cfg.AffectEnabled = true
	cfg.AffectInjectMode = ""

	lineSettings := preview
	lineSettings.AffectInjectMode = affect.ModeLine
	fullSettings := preview
	fullSettings.AffectInjectMode = affect.ModeFull

	writeOK(w, map[string]any{
		"state":    snapshot,
		"behavior": snapshot.Behavior,
		"blocks": map[string]string{
			"line": s.deps.Affect.Block(ctx, conversationID, lineSettings, cfg, now),
			"full": s.deps.Affect.Block(ctx, conversationID, fullSettings, cfg, now),
		},
	})
}

// handleAffectHistory returns the chart window.
func (s *Server) handleAffectHistory(w http.ResponseWriter, r *http.Request) {
	if s.deps.Affect == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "情绪状态服务未启用")
		return
	}
	conversationID, ok := affectConversationParam(w, r)
	if !ok {
		return
	}
	if _, ok := s.affectConversation(w, r, conversationID); !ok {
		return
	}
	hours := queryInt(r, "hours", 24)
	if hours <= 0 {
		hours = 24
	}
	if hours > affectMaxHours {
		hours = affectMaxHours
	}
	limit := queryInt(r, "limit", 1000)
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	points, err := s.deps.Affect.History(r.Context(), conversationID,
		time.Now().Add(-time.Duration(hours)*time.Hour), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"points": points})
}

// handleAffectEvents returns the newest classified events.
func (s *Server) handleAffectEvents(w http.ResponseWriter, r *http.Request) {
	if s.deps.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "存储未初始化")
		return
	}
	conversationID, ok := affectConversationParam(w, r)
	if !ok {
		return
	}
	if _, ok := s.affectConversation(w, r, conversationID); !ok {
		return
	}
	limit := queryInt(r, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > affectMaxLimit {
		limit = affectMaxLimit
	}
	events, err := s.deps.Store.ListAffectEvents(r.Context(), conversationID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"events": events})
}

// handleAffectPause suppresses injection for 1–10080 minutes.
func (s *Server) handleAffectPause(w http.ResponseWriter, r *http.Request) {
	if s.deps.Affect == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "情绪状态服务未启用")
		return
	}
	var body struct {
		Conversation string `json:"conversation"`
		Minutes      int    `json:"minutes"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	conversationID := strings.TrimSpace(body.Conversation)
	if !validAffectConversation(conversationID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "conversation 不能为空且不超过 256 字符")
		return
	}
	if _, ok := s.affectConversation(w, r, conversationID); !ok {
		return
	}
	if body.Minutes < 1 || body.Minutes > affectMaxPauseMinutes {
		writeError(w, http.StatusBadRequest, "invalid_request", "minutes 需要在 1–10080 之间")
		return
	}
	until := time.Now().Add(time.Duration(body.Minutes) * time.Minute)
	if err := s.deps.Affect.Pause(r.Context(), conversationID, until); err != nil {
		writeAffectServiceError(w, err)
		return
	}
	s.writeAffectStateEnvelope(w, r, conversationID)
}

// handleAffectResume clears a pause. Resuming a non-paused conversation is a
// no-op, not an error: the panel's button is idempotent.
func (s *Server) handleAffectResume(w http.ResponseWriter, r *http.Request) {
	if s.deps.Affect == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "情绪状态服务未启用")
		return
	}
	conversationID, ok := affectConversationBody(w, r)
	if !ok {
		return
	}
	if _, ok := s.affectConversation(w, r, conversationID); !ok {
		return
	}
	if err := s.deps.Affect.Resume(r.Context(), conversationID); err != nil {
		writeAffectServiceError(w, err)
		return
	}
	s.writeAffectStateEnvelope(w, r, conversationID)
}

// handleAffectPreset applies one named preset of the effective pack.
func (s *Server) handleAffectPreset(w http.ResponseWriter, r *http.Request) {
	if s.deps.Affect == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "情绪状态服务未启用")
		return
	}
	var body struct {
		Conversation string `json:"conversation"`
		Preset       string `json:"preset"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	conversationID := strings.TrimSpace(body.Conversation)
	if !validAffectConversation(conversationID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "conversation 不能为空且不超过 256 字符")
		return
	}
	if _, ok := s.affectConversation(w, r, conversationID); !ok {
		return
	}
	preset := strings.TrimSpace(body.Preset)
	if preset == "" || len([]rune(preset)) > affectMaxPresetLen {
		writeError(w, http.StatusBadRequest, "invalid_request", "preset 不能为空且不超过 64 字符")
		return
	}
	// Validate before touching the row: an unknown preset is a client mistake,
	// not a server failure (docs/ADMIN-API.md maps it to 404 not_found).
	pack := affect.PackByID(s.deps.Settings.Effective().AffectPack)
	if pack == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "当前内容包不可用")
		return
	}
	if _, ok := pack.PresetByID(preset); !ok {
		writeError(w, http.StatusNotFound, "not_found", "未知预设："+preset)
		return
	}
	if err := s.deps.Affect.ApplyPreset(r.Context(), conversationID, preset, time.Now()); err != nil {
		writeAffectServiceError(w, err)
		return
	}
	s.writeAffectStateEnvelope(w, r, conversationID)
}

// handleAffectReset returns the conversation to the pack baseline.
func (s *Server) handleAffectReset(w http.ResponseWriter, r *http.Request) {
	if s.deps.Affect == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "情绪状态服务未启用")
		return
	}
	conversationID, ok := affectConversationBody(w, r)
	if !ok {
		return
	}
	if _, ok := s.affectConversation(w, r, conversationID); !ok {
		return
	}
	if err := s.deps.Affect.Reset(r.Context(), conversationID, time.Now()); err != nil {
		writeAffectServiceError(w, err)
		return
	}
	s.writeAffectStateEnvelope(w, r, conversationID)
}

// writeAffectStateEnvelope answers a control POST the way docs/ADMIN-API.md
// describes: the fresh state, identical to the "state" member of GET /state.
// The control endpoints therefore never need a second round trip from the panel.
func (s *Server) writeAffectStateEnvelope(w http.ResponseWriter, r *http.Request, conversationID string) {
	snapshot, err := s.deps.Affect.Snapshot(r.Context(), conversationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"state": snapshot})
}

// writeAffectServiceError maps a transition failure onto the documented codes.
func writeAffectServiceError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrAffectConflict) {
		writeError(w, http.StatusConflict, "conflict", "状态已被其他写入更新，请重试")
		return
	}
	writeError(w, http.StatusInternalServerError, "store_error", err.Error())
}

// --- helpers ---

// affectConversationParam validates the ?conversation= query parameter.
func affectConversationParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	conversationID := strings.TrimSpace(r.URL.Query().Get("conversation"))
	if !validAffectConversation(conversationID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "conversation 参数不能为空且不超过 256 字符")
		return "", false
	}
	return conversationID, true
}

// affectConversationBody validates the conversation field of a JSON body.
func affectConversationBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Conversation string `json:"conversation"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return "", false
	}
	conversationID := strings.TrimSpace(body.Conversation)
	if !validAffectConversation(conversationID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "conversation 不能为空且不超过 256 字符")
		return "", false
	}
	return conversationID, true
}

func validAffectConversation(id string) bool {
	return id != "" && len([]rune(id)) <= affectMaxConversationLen
}

// affectConversation resolves the conversation and writes 404 when it does not
// exist. Without this, a typo in the panel would silently read a baseline state
// that no reply path will ever see.
func (s *Server) affectConversation(w http.ResponseWriter, r *http.Request, conversationID string) (*store.Conversation, bool) {
	if s.deps.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "存储未初始化")
		return nil, false
	}
	conv, err := s.deps.Store.GetConversation(r.Context(), conversationID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return nil, false
	}
	if conv == nil {
		writeError(w, http.StatusNotFound, "not_found", "会话不存在")
		return nil, false
	}
	return conv, true
}

// mergeAffectCounters copies the service's monotonic totals into the status
// snapshot.
//
// Why bridge here instead of having affect write into core.Metrics: the affect
// package owns its counters (created before this integration landed) and must
// stay usable without importing core. The admin status endpoint is the one
// place that needs both, so it merges them at render time. Values are totals,
// so overwriting is safe even if core.Metrics was also fed by a future bridge.
func (s *Server) mergeAffectCounters(snapshot *core.CounterSnapshot) {
	if s.deps.Affect == nil {
		return
	}
	m := s.deps.Affect.Metrics()
	if m == nil {
		return
	}
	snapshot.AffectTicks = m.Ticks.Load()
	snapshot.AffectEvents = m.Events.Load()
	snapshot.AffectInjections = m.Injections.Load()
	snapshot.AffectInjectChars = m.InjectChars.Load()
	snapshot.AffectErrors = m.Errors.Load()
}
