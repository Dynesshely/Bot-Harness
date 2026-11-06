package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/dynesshely/bot-harness/internal/store"
)

// handleListMemories pages the long-term memory store.
func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := s.deps.Store.ListMemories(r.Context(),
		strings.TrimSpace(q.Get("scope")),
		strings.TrimSpace(q.Get("group_openid")),
		strings.TrimSpace(q.Get("member_openid")),
		queryInt(r, "limit", 50),
		queryInt(r, "offset", 0),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"items": items, "count": len(items)})
}

// handleAddMemory lets an operator write a fact by hand.
func (s *Server) handleAddMemory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scope        string `json:"scope"`
		GroupOpenID  string `json:"group_openid"`
		MemberOpenID string `json:"member_openid"`
		Text         string `json:"text"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "text 不能为空")
		return
	}
	switch body.Scope {
	case store.MemoryGlobal, store.MemoryGroup, store.MemoryMember:
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "scope 只能是 global / group / member")
		return
	}
	if body.Scope == store.MemoryGroup && body.GroupOpenID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "群记忆需要 group_openid")
		return
	}
	if body.Scope == store.MemoryMember && body.MemberOpenID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "个人记忆需要 member_openid")
		return
	}
	id, err := s.deps.Store.AddMemory(r.Context(), store.MemoryInput{
		Scope: body.Scope, GroupOpenID: body.GroupOpenID, MemberOpenID: body.MemberOpenID,
		Kind: "fact", Text: body.Text, Source: "admin",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"id": id})
}

// handleDeleteMemory removes one fact, or every fact of a group when called with
// ?group_openid=… and no id.
func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "id 不是数字")
			return
		}
		ok, err := s.deps.Store.DeleteMemory(r.Context(), n)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "记忆不存在")
			return
		}
		writeOK(w, map[string]any{"deleted": 1})
		return
	}
	groupOpenID := strings.TrimSpace(r.URL.Query().Get("group_openid"))
	if groupOpenID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "缺少 id 或 group_openid")
		return
	}
	n, err := s.deps.Store.DeleteMemoriesForGroup(r.Context(), groupOpenID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"deleted": n})
}
