package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/webhook"
)

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type wsStatusJSON struct {
	Enabled             bool    `json:"enabled"`
	Connected           bool    `json:"connected"`
	State               string  `json:"state"`
	SessionID           string  `json:"session_id"`
	Shard               []int   `json:"shard"`
	GatewayURL          string  `json:"gateway_url"`
	HeartbeatIntervalMS int     `json:"heartbeat_interval_ms"`
	LatencyMS           int64   `json:"latency_ms"`
	LastEventAt         *string `json:"last_event_at"`
	Reconnects          int     `json:"reconnects"`
	Resumes             int     `json:"resumes"`
	LastError           *string `json:"last_error"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, s.statusData())
}

func (s *Server) statusData() map[string]any {
	d := s.deps
	eff := d.Settings.Effective()

	ws := wsStatusJSON{Enabled: false, State: "disabled", Shard: []int{0, 1}}
	if d.WS != nil {
		st := d.WS.Status()
		ws = wsStatusJSON{
			Enabled:             st.Enabled,
			Connected:           st.Connected,
			State:               st.State,
			SessionID:           st.SessionID,
			Shard:               st.Shard,
			GatewayURL:          st.GatewayURL,
			HeartbeatIntervalMS: st.HeartbeatIntervalMS,
			LatencyMS:           st.LatencyMS,
			LastEventAt:         st.LastEventAt,
			Reconnects:          st.Reconnects,
			Resumes:             st.Resumes,
			LastError:           st.LastError,
		}
	}

	hook := webhook.Stats{Enabled: false, Path: d.Settings.Effective().QQWebhookPath}
	if d.Webhook != nil {
		hook = d.Webhook.Stats()
	}

	token := qq.TokenStatus{}
	if d.Tokens != nil {
		token = d.Tokens.Status()
	}

	identity := core.IdentitySnapshot{}
	if d.Dispatcher != nil && d.Dispatcher.Identity() != nil {
		identity = d.Dispatcher.Identity().Snapshot()
	}

	conversations, messages := 0, 0
	if d.Store != nil {
		if c, m, err := d.Store.Counts(ctxBg()); err == nil {
			conversations, messages = c, m
		}
	}

	inFlight, limit := 0, 0
	if d.Dispatcher != nil {
		inFlight, limit = d.Dispatcher.Concurrency()
	}

	// 生效中的模型端点（来自数据库设置，是真正会被请求的地址）。
	aiBaseURL := ""
	if d.AI != nil {
		aiBaseURL = d.AI.BaseURL()
	}

	data := map[string]any{
		"version":             d.Config.Version,
		"started_at":          d.StartedAt.Format(time.RFC3339),
		"uptime_seconds":      int(time.Since(d.StartedAt).Seconds()),
		"transport":           eff.QQTransport,
		"trigger_mode":        eff.TriggerMode,
		"context_mode":        eff.ContextMode,
		"full_group_detected": d.Dispatcher.FullGroupDetected(),
		"platform":            s.platformSummary(),
		"ws":                  ws,
		"webhook":             hook,
		"ai": map[string]any{
			// 生效值（来自数据库设置），不是 .env 的默认值。
			"base_url":       aiBaseURL,
			"base_url_set":   aiBaseURL != "",
			"api_key_set":    s.deps.AI != nil && s.deps.AI.APIKeySet(),
			"model":          eff.Model,
			"in_flight":      inFlight,
			"max_concurrent": limit,
			"requests_total": d.Dispatcher.Metrics().AIRequests.Load(),
			"errors_total":   d.Dispatcher.Metrics().AIErrors.Load(),
			"avg_latency_ms": d.Dispatcher.Metrics().AvgLatencyMS(),
			"last_error":     strPtr(d.Dispatcher.LastAIError()),
		},
		"qq": map[string]any{
			"app_id_masked":              settings.MaskSecret(eff.QQAppID),
			"sandbox":                    eff.QQSandbox,
			"token_valid":                token.Valid,
			"token_expires_in_seconds":   token.ExpiresInSeconds,
			"token_refreshes":            token.Refreshes,
			"last_token_error":           strPtr(token.LastError),
			"bot_id":                     identity.BotID,
			"bot_username":               identity.BotUsername,
			"identity_error":             strPtr(identity.MeError),
			"openid_override_configured": identity.FallbackSet,
		},
		"store":    s.storeStatus(conversations, messages),
		"counters": s.statusCounters(),
	}
	return data
}

// statusCounters renders the counters block, merging the affect service's own
// totals (see mergeAffectCounters) into the dispatcher snapshot.
func (s *Server) statusCounters() core.CounterSnapshot {
	snapshot := s.deps.Dispatcher.Metrics().Snapshot()
	s.mergeAffectCounters(&snapshot)
	return snapshot
}

// storeStatus renders the "store" block of GET /api/status.
//
// The block is engine-agnostic: "driver" and "target" describe the backend
// (target is the connection string with the password already masked by the
// store), and size_bytes is whatever the backend can honestly report. The
// panel therefore shows a real size and a real location on both SQLite and
// PostgreSQL instead of the bootstrap DB_PATH and a bogus 0.
//
// "path" is kept only as a compatibility key for older clients that still read
// it. On SQLite it is the file the process opened; on PostgreSQL it is empty,
// because a DSN is not a path and duplicating "target" under a name that means
// "file on disk" would mislead anything that displays it as a path.
func (s *Server) storeStatus(conversations, messages int) map[string]any {
	st := s.deps.Store
	path := ""
	if !st.IsPostgres() {
		path = st.Target()
	}
	return map[string]any{
		"driver":        st.Driver(),
		"target":        st.Target(),
		"path":          path,
		"size_bytes":    st.SizeBytes(),
		"conversations": conversations,
		"messages":      messages,
	}
}

// platformSummary reports what the platform says about the groups we serve.
func (s *Server) platformSummary() map[string]any {
	out := map[string]any{
		"known_groups":       0,
		"full_access_groups": []any{},
		"limited_groups":     []any{},
	}
	if s.deps.Dispatcher == nil || s.deps.Dispatcher.Identity() == nil {
		return out
	}
	identity := s.deps.Dispatcher.Identity()
	full := []any{}
	limited := []any{}
	for _, groupOpenID := range identity.KnownGroups() {
		snapshot := identity.GroupSnapshot(groupOpenID)
		if snapshot == nil || snapshot.RecvMsgSetting == "" {
			continue
		}
		entry := map[string]any{
			"group_openid":        groupOpenID,
			"group_name":          s.deps.Store.GroupName(ctxBg(), groupOpenID),
			"recv_msg_setting":    snapshot.RecvMsgSetting,
			"allow_proactive_msg": snapshot.AllowProactiveMsg,
		}
		if snapshot.RecvMsgSetting == "all" {
			full = append(full, entry)
		} else {
			limited = append(limited, entry)
		}
	}
	out["known_groups"] = len(full) + len(limited)
	out["full_access_groups"] = full
	out["limited_groups"] = limited
	return out
}

// platformWarnings turns the platform's per-group reality into actionable hints
// for the currently selected trigger/context mode. Modes are never overridden:
// the operator's choice is stored as-is and only explained here.
func (s *Server) platformWarnings() []string {
	if s.deps.Dispatcher == nil || s.deps.Dispatcher.Identity() == nil {
		return nil
	}
	cur := s.deps.Settings.Effective()
	summary := s.platformSummary()
	limited, _ := summary["limited_groups"].([]any)
	full, _ := summary["full_access_groups"].([]any)

	var out []string
	if cur.TriggerMode == settings.TriggerAllMessages && len(limited) > 0 {
		out = append(out, "当前触发模式是「群内任意消息」，但以下群在平台侧仍是「仅 @机器人」，"+
			"这些群里未 @ 机器人的消息不会被推送。需要群管理员在 QQ 群设置里打开「接收所有消息」："+
			describeGroups(limited))
	}
	if cur.TriggerMode == settings.TriggerAtOnly && len(full) > 0 {
		out = append(out, "以下群在平台侧已开启「接收所有消息」，但当前触发模式仍是「仅 @机器人」："+
			"机器人看得到这些群的全部消息（会记录进会话时间线）但不会主动接话。"+
			"想让它参与群聊，可把触发模式切换为「群内任意消息」："+describeGroups(full))
	}
	if cur.ContextMode == settings.ContextShared && len(limited) > 0 {
		out = append(out, "当前是「整群共享上下文」：在上述「仅 @机器人」的群里，共享上下文只会包含被 @ 的消息。")
	}
	if len(limited) == 0 && len(full) == 0 {
		out = append(out, "尚未读取到任何群的平台权限：收到该群消息或打开会话详情后会自动获取。")
	}

	// 上下文范围与其余设置的配合
	if cur.ContextScope == settings.ContextIncludeUnaddressed {
		if len(limited) > 0 {
			out = append(out, "上下文范围是「包含群内未 @ 的消息」，但下列群在平台侧仍是「仅 @机器人」："+
				"这些群里机器人收不到未 @ 的消息，也就无从纳入上下文。"+describeGroups(limited))
		}
		// 逐群看「生效」的上下文模式（按群覆盖优先），而不是只看全局值。
		if names := s.perMemberGroups(); len(names) > 0 {
			out = append(out, "下列群生效的上下文模式是「每人独立」：每个成员只会把自己未 @ 的发言纳入上下文，"+
				"机器人无法听懂整个群在聊什么；想让它是「群里的听众」，把这些群改为「整群共享」："+names)
		}
	}
	return out
}

// perMemberGroups lists the known groups whose effective context mode is C1.
func (s *Server) perMemberGroups() string {
	if s.deps.Dispatcher == nil || s.deps.Dispatcher.Identity() == nil {
		return ""
	}
	var parts []string
	for _, groupOpenID := range s.deps.Dispatcher.Identity().KnownGroups() {
		if s.deps.Settings.GroupConfig(groupOpenID).ContextMode != settings.ContextPerMember {
			continue
		}
		name := s.deps.Store.GroupName(ctxBg(), groupOpenID)
		if name == "" {
			name = groupOpenID
			if len(name) > 8 {
				name = name[:8] + "…"
			}
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, "、")
}

// describeGroups renders "群名（receiving 设置）" lists for warnings.
func describeGroups(entries []any) string {
	parts := make([]string, 0, len(entries))
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry == nil {
			continue
		}
		name, _ := entry["group_name"].(string)
		if name == "" {
			id, _ := entry["group_openid"].(string)
			if len(id) > 8 {
				id = id[:8] + "…"
			}
			name = id
		}
		setting, _ := entry["recv_msg_setting"].(string)
		parts = append(parts, name+"（"+setting+"）")
	}
	return strings.Join(parts, "、")
}

// maskSecrets blanks the three write-only values on any settings snapshot
// leaving the process: openai_api_key, qq_app_secret and admin_token are only
// ever written, never read back. Whether one exists is reported separately via
// the matching *_set flags (see handleGetConfig).
func maskSecrets(s settings.Settings) settings.Settings {
	return settings.MaskSecrets(s)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	d := s.deps
	cur := d.Settings.Get()
	// 密钥永不回显：面板只需要知道「有没有、来自哪里」。
	// 只进不出的秘密不会从截图、浏览器缓存或日志里漏出去。
	editable := maskSecrets(cur)
	// 密钥只来自数据库：非空就是配过（来源恒为 db），空就是没配。
	// .env 不再参与，所以不需要再区分「后台设的」和「.env 里的」。
	keySet := strings.TrimSpace(cur.OpenAIAPIKey) != ""
	keySource := "none"
	if keySet {
		keySource = "db"
	}
	writeOK(w, map[string]any{
		"source": map[string]any{
			"file":              ".env",
			"overrides_from_db": len(d.Settings.OverriddenKeys()),
			"overridden_keys":   d.Settings.OverriddenKeys(),
		},
		// 只读块只报「启动期决定、运行期改不了」的三项：存储引擎、已遮蔽密码的
		// 连接描述（store.Target() 在 PG 下把口令换成 redacted）与程序版本。
		// 不再报 db_path：那是 SQLite 引导值，PG 下毫无意义。
		"readonly": map[string]any{
			"driver":  d.Store.Driver(),
			"storage": d.Store.Target(),
			"version": d.Config.Version,
		},
		"qq_app_secret_set":     strings.TrimSpace(cur.QQAppSecret) != "",
		"admin_token_set":       strings.TrimSpace(cur.AdminToken) != "",
		"editable":              editable,
		"openai_api_key_set":    keySet,
		"openai_api_key_source": keySource,
	})
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	patch, err := decodePatch(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	updated, warnings, err := s.deps.Settings.Apply(ctxBg(), patch)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	s.applyLimit()
	s.deps.Logger.Info("runtime settings updated", "component", "admin", "fields", len(patch))
	// 同样不回显密钥：一次只改 temperature 的保存也不能顺手带出
	// openai_api_key / qq_app_secret / admin_token。
	masked := maskSecrets(updated)
	writeOK(w, map[string]any{
		"settings":         masked,
		"warnings":         warnings,
		"restart_required": []string{},
	})
}

func (s *Server) handleGetPolicies(w http.ResponseWriter, _ *http.Request) {
	s.writePolicies(w, nil)
}

func (s *Server) handlePutPolicies(w http.ResponseWriter, r *http.Request) {
	patch, err := decodePatch(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if _, _, err := s.deps.Settings.Apply(ctxBg(), patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	s.applyLimit()
	s.deps.Logger.Info("policies updated", "component", "admin", "fields", len(patch))
	s.writePolicies(w, nil)
}

func (s *Server) writePolicies(w http.ResponseWriter, extra []string) {
	cur := s.deps.Settings.Get()
	eff := s.deps.Settings.Effective()
	warnings := append([]string{}, s.platformWarnings()...)
	warnings = append(warnings, extra...)
	writeOK(w, map[string]any{
		"trigger_mode":  eff.TriggerMode,
		"context_mode":  eff.ContextMode,
		"context_scope": eff.ContextScope,
		"requested": map[string]any{
			"trigger_mode": cur.TriggerMode,
			"context_mode": cur.ContextMode,
		},
		"platform":         s.platformSummary(),
		"group_allowlist":  cur.GroupAllowlist,
		"member_allowlist": cur.MemberAllowlist,
		"admin_openids":    cur.AdminOpenIDs,
		"group_overrides":  cur.GroupOverrides,
		"blocked_keywords": cur.BlockedKeywords,
		"reply_to_bots":    cur.ReplyToBots,
		"quote_reply":      cur.QuoteReply,
		"quote_reply_mode": cur.QuoteReplyMode,
		"warnings":         warnings,
	})
}
