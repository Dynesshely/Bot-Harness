package admin

import (
	"net/http"
	"strings"
)

// Group metadata endpoints.
//
// The policies page works with group openids (they are the override keys) but
// humans need names. The store caches a name per group conversation, so the
// panel can ask for a batch of openids in one request instead of one call per
// row.

// groupNamesMaxOpenIDs bounds the batch. It matches the size of the policies
// page (and keeps the request URL well below proxy limits); a larger list is a
// client bug, not something to clamp silently.
const groupNamesMaxOpenIDs = 100

// handleGroupNames resolves a batch of group openids to their display names.
//
//	GET /api/groups/names?openids=a,b
//
// Unknown groups are present in the map with an empty name: the panel shows the
// openid as the fallback, and a missing key would make every caller special-case
// the lookup.
func (s *Server) handleGroupNames(w http.ResponseWriter, r *http.Request) {
	if s.deps.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "存储未初始化")
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("openids"))
	if raw == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "openids 不能为空")
		return
	}
	seen := make(map[string]struct{})
	ids := make([]string, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "openids 不能为空")
		return
	}
	if len(ids) > groupNamesMaxOpenIDs {
		writeError(w, http.StatusBadRequest, "invalid_request", "openids 一次最多 100 个")
		return
	}
	names := make(map[string]string, len(ids))
	for _, id := range ids {
		names[id] = s.deps.Store.GroupName(r.Context(), id)
	}
	writeOK(w, map[string]any{"names": names})
}
