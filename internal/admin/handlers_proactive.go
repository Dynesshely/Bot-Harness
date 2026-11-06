package admin

import "net/http"

// handleListProactive returns the spontaneous-message state per group.
func (s *Server) handleListProactive(w http.ResponseWriter, r *http.Request) {
	items, err := s.deps.Store.ListProactiveStates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	cur := s.deps.Settings.Effective()
	writeOK(w, map[string]any{
		"items":                items,
		"count":                len(items),
		"level":                cur.ProactiveLevel,
		"ratio":                cur.ProactiveRatio,
		"max_credits":          cur.ProactiveMaxCredits,
		"quiet_hours":          []int{cur.ProactiveQuietStart, cur.ProactiveQuietEnd},
		"min_interval_minutes": cur.ProactiveMinIntervalMinutes,
	})
}
