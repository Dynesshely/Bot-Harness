// Package admin implements the management HTTP API and serves the embedded SPA.
package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/affect"
	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/bus"
	"github.com/dynesshely/bot-harness/internal/config"
	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/logging"
	"github.com/dynesshely/bot-harness/internal/media"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
	"github.com/dynesshely/bot-harness/internal/webhook"
	"github.com/dynesshely/bot-harness/web"
)

const sessionCookie = "botharness_admin"

// Deps are the collaborators the admin API reads from and drives.
type Deps struct {
	Config     *config.Config
	Settings   *settings.Manager
	Store      *store.Store
	Dispatcher *core.Dispatcher
	AI         *ai.Client
	QQ         *qq.Client
	Tokens     *qq.TokenManager
	WS         *qq.WSClient
	Webhook    *webhook.Receiver
	Bus        *bus.Bus
	Logger     *slog.Logger
	LogRing    *logging.Ring
	Media      *media.Bundle
	StartedAt  time.Time
	// Affect is the mood service behind /api/affect. It is optional: the rest of
	// the panel works without it, and the affect endpoints answer 503 when it is
	// absent (feature built but not wired).
	Affect *affect.Service
}

// Server is the admin HTTP surface.
type Server struct {
	deps   Deps
	mux    *http.ServeMux
	models *modelCache
	// loginLimiter throttles wrong-token attempts per peer address.
	loginLimiter *loginLimiter
}

// New builds the admin server and its routing table.
//
// It deliberately caches no session value: the cookie is derived from the
// current admin token on every login request, so rotating the token takes
// effect immediately instead of handing out the value from process start.
func New(deps Deps) *Server {
	s := &Server{
		deps:         deps,
		mux:          http.NewServeMux(),
		models:       newModelCache(5 * time.Minute),
		loginLimiter: newLoginLimiter(),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	// Public endpoints.
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/auth/me", s.handleMe)

	// Authenticated endpoints.
	s.mux.HandleFunc("GET /api/status", s.protected(s.handleStatus))
	s.mux.HandleFunc("GET /api/groups/mergeable", s.protected(s.handleMergeableGroups))
	s.mux.HandleFunc("POST /api/groups/{group_openid}/conversations/merge", s.protected(s.handleMergeGroupConversations))
	s.mux.HandleFunc("GET /api/config", s.protected(s.handleGetConfig))
	s.mux.HandleFunc("PUT /api/config", s.protected(s.handlePutConfig))
	s.mux.HandleFunc("GET /api/policies", s.protected(s.handleGetPolicies))
	s.mux.HandleFunc("PUT /api/policies", s.protected(s.handlePutPolicies))
	s.mux.HandleFunc("GET /api/conversations", s.protected(s.handleListConversations))
	s.mux.HandleFunc("DELETE /api/conversations", s.protected(s.handleClearConversations))
	s.mux.HandleFunc("GET /api/conversations/{id}", s.protected(s.handleGetConversation))
	s.mux.HandleFunc("DELETE /api/conversations/{id}", s.protected(s.handleDeleteConversation))
	s.mux.HandleFunc("POST /api/conversations/{id}/enabled", s.protected(s.handleSetConversationEnabled))
	s.mux.HandleFunc("POST /api/conversations/{id}/messages", s.protected(s.handleSendConversationMessage))
	s.mux.HandleFunc("POST /api/conversations/{id}/quote-reply", s.protected(s.handleSetQuoteReply))
	s.mux.HandleFunc("GET /api/messages", s.protected(s.handleListMessages))
	s.mux.HandleFunc("GET /api/logs", s.protected(s.handleListLogs))
	s.mux.HandleFunc("GET /api/tool-calls", s.protected(s.handleListToolCalls))
	s.mux.HandleFunc("GET /api/proactive", s.protected(s.handleListProactive))
	s.mux.HandleFunc("GET /api/tasks", s.protected(s.handleListTasks))
	s.mux.HandleFunc("DELETE /api/tasks/{id}", s.protected(s.handleCancelTask))
	s.mux.HandleFunc("GET /api/affect/packs", s.protected(s.handleAffectPacks))
	s.mux.HandleFunc("GET /api/affect/conversations", s.protected(s.handleAffectConversations))
	s.mux.HandleFunc("GET /api/affect/state", s.protected(s.handleAffectState))
	s.mux.HandleFunc("GET /api/affect/history", s.protected(s.handleAffectHistory))
	s.mux.HandleFunc("GET /api/affect/events", s.protected(s.handleAffectEvents))
	s.mux.HandleFunc("POST /api/affect/pause", s.protected(s.handleAffectPause))
	s.mux.HandleFunc("POST /api/affect/resume", s.protected(s.handleAffectResume))
	s.mux.HandleFunc("POST /api/affect/preset", s.protected(s.handleAffectPreset))
	s.mux.HandleFunc("POST /api/affect/reset", s.protected(s.handleAffectReset))
	s.mux.HandleFunc("GET /api/join/decisions", s.protected(s.handleJoinDecisions))
	s.mux.HandleFunc("GET /api/join/topics", s.protected(s.handleJoinTopics))
	s.mux.HandleFunc("GET /api/commitments", s.protected(s.handleListCommitments))
	s.mux.HandleFunc("DELETE /api/commitments/{id}", s.protected(s.handleCancelCommitment))
	s.mux.HandleFunc("GET /api/memories", s.protected(s.handleListMemories))
	s.mux.HandleFunc("POST /api/memories", s.protected(s.handleAddMemory))
	s.mux.HandleFunc("DELETE /api/memories/{id}", s.protected(s.handleDeleteMemory))
	s.mux.HandleFunc("DELETE /api/memories", s.protected(s.handleDeleteMemory))
	s.mux.HandleFunc("GET /api/models", s.protected(s.handleListModels))
	s.mux.HandleFunc("GET /api/media/{id}", s.protected(s.handleGetMedia))
	s.mux.HandleFunc("GET /api/groups/{group_openid}/state", s.protected(s.handleGroupState))
	s.mux.HandleFunc("GET /api/stats", s.protected(s.handleStats))
	s.mux.HandleFunc("GET /api/events/stream", s.protected(s.handleEventStream))
	s.mux.HandleFunc("POST /api/actions/reconnect", s.protected(s.handleReconnect))
	s.mux.HandleFunc("POST /api/actions/reload-token", s.protected(s.handleReloadToken))
	s.mux.HandleFunc("POST /api/actions/sync-commands", s.protected(s.handleSyncCommands))
	s.mux.HandleFunc("POST /api/actions/reset-config", s.protected(s.handleResetConfig))
	s.mux.HandleFunc("POST /api/actions/test-ai", s.protected(s.handleTestAI))
	s.mux.HandleFunc("POST /api/actions/test-send", s.protected(s.handleTestSend))

	// Unknown API routes must not fall through to the SPA.
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "unknown API endpoint")
	})

	// Everything else is the single-page admin app.
	s.mux.Handle("/", webui.Handler())
}

// --- authentication ---

// adminToken reads the current token from settings: changing it in the panel
// takes effect immediately and invalidates every existing session.
//
// It is trimmed because an empty token must behave as "no credential", never as
// "match an empty password": hmac.Equal("", "") is true, so callers go through
// tokenEqual which rejects empties before comparing.
func (s *Server) adminToken() string {
	return strings.TrimSpace(s.deps.Settings.Effective().AdminToken)
}

// deriveSession returns the cookie value, or "" when no token is configured.
//
// An empty key would make the HMAC predictable (and forgeable), so an empty
// result is treated as "no session" by authenticated.
func (s *Server) deriveSession() string {
	token := s.adminToken()
	if token == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("bot-harness-admin-session-v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) authenticated(r *http.Request) bool {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if s.tokenEqual(strings.TrimPrefix(h, "Bearer ")) {
			return true
		}
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	want := s.deriveSession()
	if want == "" {
		return false
	}
	return hmac.Equal([]byte(c.Value), []byte(want))
}

// tokenEqual reports whether the provided credential is the configured token.
//
// Empty values are rejected explicitly: after a settings reset the stored token
// used to become "", and hmac.Equal("", "") then authenticated an empty bearer
// token / empty login form. A trim mismatch is not a permission.
func (s *Server) tokenEqual(token string) bool {
	token = strings.TrimSpace(token)
	want := s.adminToken()
	if token == "" || want == "" {
		return false
	}
	return hmac.Equal([]byte(token), []byte(want))
}

func (s *Server) protected(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "登录状态已失效，请重新登录")
			return
		}
		h(w, r)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := loginClientIP(r)
	if !s.loginLimiter.allow(ip) {
		w.Header().Set("Retry-After", strconv.Itoa(int(loginFailureWindow.Seconds())))
		writeError(w, http.StatusTooManyRequests, "too_many_attempts",
			"登录失败次数过多，请稍后再试")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.tokenEqual(strings.TrimSpace(body.Token)) {
		s.loginLimiter.fail(ip)
		s.deps.Logger.Warn("admin login rejected", "component", "admin", "remote", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "invalid_token", "Admin Token 不正确")
		return
	}
	s.loginLimiter.reset(ip)
	// Derive the cookie from the token in force *now*: tokenEqual above only
	// passed because a non-empty token is configured, so this is non-empty too.
	// The guard protects future callers that change the login flow.
	session := s.deriveSession()
	if session == "" {
		s.deps.Logger.Warn("admin login refused: no admin token configured", "component", "admin", "remote", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "invalid_token", "后台口令尚未配置，无法登录")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    session,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Trust X-Forwarded-Proto from the reverse proxy: the panel is usually
		// served over HTTPS terminated in front of this process.
		Secure: requestIsHTTPS(r),
		MaxAge: int((7 * 24 * time.Hour).Seconds()),
	})
	writeOK(w, map[string]any{"authenticated": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
		MaxAge:   -1,
	})
	writeOK(w, map[string]any{"authenticated": false})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	auth := s.authenticated(r)
	mode := "none"
	if auth {
		mode = "token"
	}
	writeOK(w, map[string]any{
		"authenticated": auth,
		"username":      "admin",
		"auth_mode":     mode,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	problems := []string{}
	if missing := s.deps.Settings.Effective().MissingSecrets(); len(missing) > 0 {
		problems = append(problems, "missing credentials: "+strings.Join(missing, ", "))
	}
	if s.deps.Tokens != nil {
		if st := s.deps.Tokens.Status(); !st.Valid && len(problems) == 0 {
			problems = append(problems, "no valid access token yet")
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if len(problems) > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "degraded", "reason": strings.Join(problems, "; ")})
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

// --- helpers ---

type okEnvelope struct {
	OK   bool `json:"ok"`
	Data any  `json:"data"`
}

type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errEnvelope struct {
	OK    bool    `json:"ok"`
	Error errBody `json:"error"`
}

func writeOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(okEnvelope{OK: true, Data: data})
}

// writeError emits the frozen error envelope.
//
// 5xx codes are historical: handlers use store_error, internal_error,
// upstream_error and unavailable loosely for "the server side failed". They are
// intentionally left as-is — clients branch on the HTTP status and the 4xx
// codes, and renaming them is a contract change, not a cleanup. Do not "fix"
// them piecemeal; a stable taxonomy needs its own decision record.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errEnvelope{OK: false, Error: errBody{Code: code, Message: message}})
}

func readJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func (s *Server) applyLimit() {
	if v := s.deps.Settings.Effective().MaxConcurrentLLM; s.deps.Dispatcher != nil {
		s.deps.Dispatcher.SetLimit(v)
	}
}
