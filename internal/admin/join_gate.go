package admin

import (
	"net/http"
	"strconv"
	"strings"
)

// Join-gate panel endpoints.
//
// Both routes are read-only: the gate itself writes the rows from the core
// pipeline, and the panel only renders what happened. Parameters are validated
// here rather than clamped silently, because a typo in the panel (limit=0)
// should surface as an error instead of a surprising default.

const (
	joinMaxLimit       = 200
	joinDefaultLimit   = 50
	joinMaxGroupOpenID = 256
)

// handleJoinDecisions returns the newest gate evaluations, optionally narrowed
// to one group.
//
//	GET /api/join/decisions?group_openid=&limit=1..200
func (s *Server) handleJoinDecisions(w http.ResponseWriter, r *http.Request) {
	if s.deps.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "存储未初始化")
		return
	}
	groupOpenID := strings.TrimSpace(r.URL.Query().Get("group_openid"))
	if !validJoinGroupOpenID(groupOpenID, false) {
		writeError(w, http.StatusBadRequest, "invalid_request", "group_openid 不能超过 256 字符")
		return
	}
	limit := joinDefaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > joinMaxLimit {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit 需要在 1–200 之间")
			return
		}
		limit = n
	}
	decisions, err := s.deps.Store.ListJoinDecisions(r.Context(), groupOpenID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"decisions": decisions})
}

// handleJoinTopics returns the latest cached topic split of one group.
//
//	GET /api/join/topics?group_openid=
func (s *Server) handleJoinTopics(w http.ResponseWriter, r *http.Request) {
	if s.deps.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "存储未初始化")
		return
	}
	groupOpenID := strings.TrimSpace(r.URL.Query().Get("group_openid"))
	if !validJoinGroupOpenID(groupOpenID, true) {
		writeError(w, http.StatusBadRequest, "invalid_request", "group_openid 不能为空且不超过 256 字符")
		return
	}
	topics, err := s.deps.Store.GetJoinTopics(r.Context(), groupOpenID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	// No analysis yet is a normal empty result, not a 404: the panel shows an
	// empty state and the next gate evaluation fills it in.
	writeOK(w, map[string]any{"topics": topics})
}

// validJoinGroupOpenID enforces the common shape check. required distinguishes
// the topics endpoint (a group is the whole key) from the decisions endpoint
// (empty means "all groups").
func validJoinGroupOpenID(groupOpenID string, required bool) bool {
	if groupOpenID == "" {
		return !required
	}
	return len([]rune(groupOpenID)) <= joinMaxGroupOpenID
}
