package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleEventStream streams live events to the admin UI over SSE.
//
// Event names match the contract: status, message, log, ws, ping.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unsupported", "当前服务器不支持流式响应")
		return
	}
	if s.deps.Bus == nil {
		writeError(w, http.StatusInternalServerError, "not_configured", "事件总线不可用")
		return
	}

	headers := w.Header()
	headers.Set("Content-Type", "text/event-stream; charset=utf-8")
	headers.Set("Cache-Control", "no-cache, no-transform")
	headers.Set("Connection", "keep-alive")
	headers.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, cancel := s.deps.Bus.Subscribe(256)
	defer cancel()

	statusTicker := time.NewTicker(5 * time.Second)
	defer statusTicker.Stop()
	pingTicker := time.NewTicker(15 * time.Second)
	defer pingTicker.Stop()

	// Send an initial snapshot so the UI has data without waiting for a poll.
	if !sseWrite(w, flusher, "status", s.statusData()) {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-events:
			if !open {
				return
			}
			// Event names are forwarded verbatim; the contract currently uses
			// status / message / log / tool / ping.
			if !sseWrite(w, flusher, ev.Name, ev.Data) {
				return
			}
		case <-statusTicker.C:
			if !sseWrite(w, flusher, "status", s.statusData()) {
				return
			}
		case <-pingTicker.C:
			if !sseWrite(w, flusher, "ping", map[string]any{}) {
				return
			}
		}
	}
}

func sseWrite(w http.ResponseWriter, flusher http.Flusher, name string, data any) bool {
	payload, err := json.Marshal(data)
	if err != nil {
		payload = []byte(`{"error":"encode failed"}`)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload); err != nil {
		return false
	}
	flusher.Flush()
	return true
}
