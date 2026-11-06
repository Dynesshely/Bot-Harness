package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleGetMedia serves a cached attachment by its content-addressed id.
//
// Ids are the sha256 of the bytes, so the response is immutable and can be
// cached aggressively by the browser.
func (s *Server) handleGetMedia(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || len(id) != 64 {
		writeError(w, http.StatusBadRequest, "invalid_request", "无效的媒体 ID")
		return
	}
	if s.deps.Media == nil || s.deps.Media.Store == nil {
		writeError(w, http.StatusNotFound, "not_found", "媒体存储未启用")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	contentType, data, err := s.deps.Media.Store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "媒体不存在或已被清理")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
