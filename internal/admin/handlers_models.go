package admin

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
)

// modelCache keeps the catalogue fetched from the compatible endpoint so the
// admin UI can populate a selector without hammering the upstream.
type modelCache struct {
	mu      sync.Mutex
	models  []ai.ModelInfo
	fetched time.Time
	lastErr string
	ttl     time.Duration
}

func newModelCache(ttl time.Duration) *modelCache {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &modelCache{ttl: ttl}
}

func (c *modelCache) get() ([]ai.ModelInfo, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.models == nil || time.Since(c.fetched) > c.ttl {
		return nil, c.fetched, false
	}
	out := make([]ai.ModelInfo, len(c.models))
	copy(out, c.models)
	return out, c.fetched, true
}

func (c *modelCache) set(models []ai.ModelInfo, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.lastErr = err.Error()
		return
	}
	c.models = models
	c.fetched = time.Now()
	c.lastErr = ""
}

// handleListModels proxies GET /models from the configured OpenAI-compatible
// endpoint, so the admin panel can offer a real model list.
//
// Query parameters: refresh=1 bypasses the cache.
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	if s.deps.AI == nil {
		writeError(w, http.StatusBadRequest, "not_configured", "未配置 AI 端点")
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1" || r.URL.Query().Get("refresh") == "true"

	if !refresh {
		if models, fetched, ok := s.models.get(); ok {
			s.writeModels(w, models, fetched, true)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	models, err := s.deps.AI.ListModels(ctx)
	s.models.set(models, err)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	s.deps.Logger.Info("model catalogue refreshed", "component", "admin", "count", len(models))
	if cachedModels, fetched, ok := s.models.get(); ok {
		s.writeModels(w, cachedModels, fetched, false)
		return
	}
	s.writeModels(w, models, time.Now(), false)
}

func (s *Server) writeModels(w http.ResponseWriter, models []ai.ModelInfo, fetched time.Time, cached bool) {
	current := s.deps.Settings.Effective().Model
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	known := false
	for _, id := range ids {
		if id == current {
			known = true
			break
		}
	}
	// 必须是**当前实际生效**的端点（数据库设置），不是 .env 里的默认值 —— 否则
	// 后台会显示一个和真实调用地址无关的 URL。
	baseURL := ""
	if s.deps.AI != nil {
		baseURL = s.deps.AI.BaseURL()
	}
	writeOK(w, map[string]any{
		"base_url":        baseURL,
		"count":           len(models),
		"models":          models,
		"ids":             ids,
		"current":         current,
		"current_in_list": known,
		"cached":          cached,
		"fetched_at":      fetched.Format(time.RFC3339),
	})
}
