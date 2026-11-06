package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/dynesshely/bot-harness/internal/store"
)

// handleListTasks pages the reminder queue.
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	items, err := s.deps.Store.ListTasks(r.Context(),
		strings.TrimSpace(r.URL.Query().Get("status")),
		queryInt(r, "limit", 50),
		queryInt(r, "offset", 0),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"items": items, "count": len(items)})
}

// handleListCommitments pages the open-loop queue.
//
// Unlike /api/tasks it has no status filter: a commitment is a single row whose
// status changes over its lifetime, and the admin view wants the full history.
// Optional conversation_id / creator narrow the result.
func (s *Server) handleListCommitments(w http.ResponseWriter, r *http.Request) {
	items, err := s.deps.Store.ListCommitments(r.Context(),
		strings.TrimSpace(r.URL.Query().Get("conversation_id")),
		strings.TrimSpace(r.URL.Query().Get("creator")),
		queryInt(r, "limit", 50),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"items": items, "count": len(items)})
}

// handleCancelCommitment cancels an open loop from the admin panel.
//
// Only waiting/leased rows can be cancelled. done/cancelled are terminal and
// answer 409: a second cancel must not overwrite the real acknowledgement
// (ack_note / ack_at) recorded when the user closed it.
func (s *Server) handleCancelCommitment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "id 不是合法数字")
		return
	}
	outcome, err := s.deps.Store.CancelCommitment(r.Context(), id, "后台取消")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	switch outcome {
	case store.CancelOutcomeCanceled:
		writeOK(w, map[string]any{"canceled": true})
	case store.CancelOutcomeNotFound:
		writeError(w, http.StatusNotFound, "not_found", "承诺不存在")
	case store.CancelOutcomeNotOpen:
		writeError(w, http.StatusConflict, "conflict", "承诺已收口（done / cancelled），不能取消")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "未知的取消失败")
	}
}

// handleCancelTask cancels one pending reminder.
func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "id 不是合法数字")
		return
	}
	ok, err := s.deps.Store.CancelTask(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "任务不存在或已发出")
		return
	}
	writeOK(w, map[string]any{"canceled": true})
}
