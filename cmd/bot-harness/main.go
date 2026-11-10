// Command bot-harness runs the QQ group AI chat bot and its admin panel from a
// single binary.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	// Embed the IANA timezone database so TZ=... works without OS tzdata.
	_ "time/tzdata"

	"github.com/dynesshely/bot-harness/internal/admin"
	"github.com/dynesshely/bot-harness/internal/affect"
	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/bus"
	"github.com/dynesshely/bot-harness/internal/config"
	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/httpx"
	"github.com/dynesshely/bot-harness/internal/logging"
	"github.com/dynesshely/bot-harness/internal/media"
	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
	"github.com/dynesshely/bot-harness/internal/tools"
	"github.com/dynesshely/bot-harness/internal/webhook"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			// `bot-harness healthcheck` probes the local admin API and exits 0/1.
			// Having it built in keeps the runtime image free of curl/wget.
			os.Exit(healthcheck())
		case "token":
			os.Exit(adminTokenCmd(os.Stdout, os.Stderr))
		case "migrate":
			// `bot-harness migrate` copies SQLite into PostgreSQL and proves
			// the copy. It runs as a separate process against a read-only
			// source handle, so it never disturbs the running bot.
			os.Exit(migrateCommand(os.Args[2:], os.Stdout, os.Stderr))
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// healthcheck queries this process's own /api/health endpoint.
func healthcheck() int {
	addr := listenAddr()
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Accept the ":8080" shorthand that SplitHostPort rejects.
		if strings.HasPrefix(addr, ":") {
			host, port = "127.0.0.1", strings.TrimPrefix(addr, ":")
		} else {
			fmt.Fprintln(os.Stderr, "healthcheck: cannot parse HTTP_ADDR:", err)
			return 1
		}
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	// Bypass any HTTP_PROXY injected into the container: this probe always
	// talks to loopback, and a proxy would answer 502 for it.
	client := &http.Client{
		Timeout:   4 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/api/health")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: HTTP", resp.StatusCode)
		return 1
	}
	return 0
}

func run() error {
	// 唯一还从环境读的配置是数据库位置：它必须先于数据库存在。
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// DATABASE_URL (postgres://…) wins over DB_PATH; an empty value keeps the
	// SQLite file, so existing deployments are unaffected.
	st, err := store.Open(cfg.DataSource())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	// 设置 = 内置默认值 + 数据库覆盖。
	settingsMgr, err := settings.New(rootCtx, st, settings.Defaults())
	if err != nil {
		return err
	}
	// 一次性把遗留 .env 值搬进数据库：已有覆盖的键不动，重复启动是空操作。
	seeded, err := settingsMgr.SeedFromEnv(rootCtx, os.Getenv)
	if err != nil {
		return fmt.Errorf("seed settings from environment: %w", err)
	}
	// 后台口令也是数据库配置：为空时生成一次并持久化，之后不再变化。
	if strings.TrimSpace(settingsMgr.Effective().AdminToken) == "" {
		suffix, err := randomHex(16)
		if err != nil {
			return fmt.Errorf("generate admin token: %w", err)
		}
		patch := map[string]json.RawMessage{
			"admin_token": json.RawMessage(strconv.Quote("botharness-" + suffix)),
		}
		if _, _, err := settingsMgr.Apply(rootCtx, patch); err != nil {
			return fmt.Errorf("generate admin token: %w", err)
		}
		announceAdminToken(os.Stderr, settingsMgr.Effective().AdminToken, isTerminal(os.Stderr))
	}
	cur := settingsMgr.Effective()

	ring := logging.NewRing(cur.LogBufferSize)
	eventBus := bus.New()
	logHandler, _ := logging.NewHandler(logging.Options{
		Writer: os.Stdout,
		Level:  logging.ParseLevel(cur.LogLevel),
		Ring:   ring,
		Bus:    eventBus,
	})
	logger := slog.New(logHandler)
	slog.SetDefault(logger)
	appLog := logger.With("component", "app")

	appLog.Info("starting bot-harness",
		"version", cfg.Version,
		"driver", st.Driver(),
		// st.Target() masks the password: a DSN in the log must never leak it.
		"db", st.Target(),
		"transport", cur.QQTransport,
		"sandbox", cur.QQSandbox,
		"api_base", qqAPIBase(cur),
		"model", cur.Model,
	)
	if len(seeded) > 0 {
		appLog.Info("imported legacy .env values into the database", "keys", strings.Join(seeded, ","))
	}
	// 旧 .env 的残留在升级后极其容易误导：改一行、重启、发现没生效。
	// 这里把「已经死了的键」和「只导入过一次的键」分开说清楚，并且只对
	// ADMIN_TOKEN 做值比较（那是真正会把人锁在门外的一项）。
	if report := settings.DiagnoseLegacyEnv(os.Getenv, cur); !report.Empty() {
		// 一次性导入属预期行为，INFO 即可；真正需要处理的是「已经死了的键」和
		// 「.env 里的口令已经不是事实源」这两类，它们各占一行 WARN。
		if len(report.Seeded) > 0 {
			appLog.Info("旧版 .env 键仍在环境里：值只在首次启动导入一次，之后改 .env 不生效（请到后台「设置」里改）",
				"keys", strings.Join(report.Seeded, ","))
		}
		if len(report.Retired) > 0 {
			appLog.Warn(fmt.Sprintf("%d 个旧版 .env 键已被数据库接管、改 .env 不再生效，建议清理：%s",
				len(report.Retired), strings.Join(report.Retired, ", ")))
		}
		if report.AdminTokenStale {
			appLog.Warn(".env 的 ADMIN_TOKEN 已过期：它与数据库里的后台口令不一致，" +
				"改 .env 不会再影响后台登录；请用 `bot-harness token` 读取当前口令")
		}
	}
	if !qqCredentialsPresent(cur) {
		appLog.Warn("QQ 凭证尚未配置：到后台「设置 → 接入」填写 qq_app_id / qq_app_secret，" +
			"在此之前机器人不会连上平台")
	}

	// 提示词文件是可选的运维手段；数据库里的 system_prompt 优先级更高。
	promptText, err := core.LoadPromptFile(cur.SystemPromptFile)
	if err != nil {
		appLog.Warn("failed to read system_prompt_file, falling back to the built-in prompt",
			"path", cur.SystemPromptFile, "error", err.Error())
	}
	processPrompt := strings.TrimSpace(promptText)
	if processPrompt == "" {
		processPrompt = core.DefaultSystemPrompt
	}

	tlsOpts := httpx.TLSOptions{
		ExtraCAFiles:       cur.TLSExtraCAFiles,
		InsecureSkipVerify: cur.TLSInsecureVerify,
	}
	// Fail fast when a configured CA bundle is unusable: deferring it to the
	// first model call produces a confusing handshake error much later.
	outboundTLS, err := tlsOpts.Config()
	if err != nil {
		return err
	}
	aiHTTP, err := tlsOpts.Client(0)
	if err != nil {
		return err
	}
	apiHTTP, err := tlsOpts.Client(30 * time.Second)
	if err != nil {
		return err
	}
	if len(cur.TLSExtraCAFiles) > 0 {
		appLog.Info("extra CA bundle configured", "files", strings.Join(cur.TLSExtraCAFiles, string(os.PathListSeparator)))
	}
	if cur.TLSInsecureVerify {
		appLog.Warn("TLS_INSECURE_SKIP_VERIFY=true：出站 TLS 证书校验已被关闭，仅应在无法获取 CA 时临时使用")
	}
	// 执行能力同时要求开关与管理员身份：只开开关却没有管理员 = 谁也跑不了。
	if boot := settingsMgr.Effective(); boot.ExecEnabled && len(boot.AdminOpenIDs) == 0 {
		appLog.Warn("exec_enabled=true 但 admin_openids 为空：run_shell 对所有人关闭，" +
			"请先在「策略配置」里填写管理员 OpenID")
	}

	// 端点与密钥**只**来自数据库（后台「系统设置」），不再读 .env：
	// 构造时给空值，真正生效的值每次请求从设置里解析。
	aiClient := ai.NewClient("", "", aiHTTP)
	aiClient.SetBaseURLResolver(func() string {
		return settingsMgr.Get().OpenAIBaseURL
	})
	aiClient.SetAPIKeyResolver(func() string {
		return settingsMgr.Get().OpenAIAPIKey
	})
	// 把当前生效端点写进启动日志，排查「打到了哪个服务」时有用。
	if aiClient.BaseURL() == "" || !aiClient.APIKeySet() {
		appLog.Warn("模型端点或 API Key 尚未配置：请到后台「系统设置」里填写 openai_base_url 与 openai_api_key，" +
			"在此之前所有模型调用都会失败")
	} else {
		appLog.Info("ai endpoint resolved", "component", "ai", "base_url", aiClient.BaseURL())
	}
	tokens := qq.NewTokenManager(cur.QQAppID, cur.QQAppSecret, qqAPIBase(cur), apiHTTP)
	qqClient := qq.NewClient(qqAPIBase(cur), tokens, apiHTTP)
	sender := qq.NewSender(qqClient, logger.With("component", "sender"))

	mediaHTTP, err := tlsOpts.Client(0)
	if err != nil {
		return err
	}
	mediaBundle := media.NewBundle(
		// The media store owns its own statements, so it needs the same dialect
		// the store uses: without the driver name it would keep emitting "?"
		// placeholders and every media write would fail on PostgreSQL.
		media.NewStoreWithDriver(st.DB(), st.Driver()),
		media.NewDownloader(mediaHTTP, cur.MediaMaxBytes, 15*time.Second),
		logger.With("component", "media"),
		settingsMgr.Effective().VisionMaxImages,
	)

	// 情绪状态：默认关闭，但仍然构造服务并跑 ticker，这样后台一开开关就生效。
	// 关闭时所有接入点都走 "nil/空块" 分支，回复管线与没有这个功能时一致。
	affectSvc := affect.NewService(st, settingsMgr, logger.With("component", "affect"), affect.NewMetrics())
	go affectSvc.Run(rootCtx)

	dispatcher := core.New(core.Options{
		QQBotOpenID: cur.QQBotOpenID,
		Settings:    settingsMgr,
		Store:       st,
		AI:          aiClient,
		Sender:      sender,
		Bus:         eventBus,
		Logger:      logger,
		Prompt:      processPrompt,
		QQ:          qqClient,
		Media:       mediaBundle,
		Tools:       tools.Builtins(),
		Affect:      affectSvc,
	})
	dispatcher.Start(rootCtx)
	// 指令面板同步是纯展示层：延迟 8s 起，失败按 30s/1m/2m/5m/10m 退避重试，
	// 最多 6 次；平台拒绝（无权限 / 内容风险）只记 WARN，绝不影响启动。
	go dispatcher.RunCommandPanelSync(rootCtx)
	go dispatcher.WarmIdentity(rootCtx)
	go dispatcher.RunTaskScheduler(rootCtx)
	go dispatcher.RunCommitmentScheduler(rootCtx)
	go dispatcher.RunProactiveLoop(rootCtx)
	go dispatcher.WarmGroupNames(rootCtx)
	dispatcher.SetLimit(settingsMgr.Effective().MaxConcurrentLLM)
	go dispatcher.Limiter().RunPruner(rootCtx.Done())

	if err := dispatcher.Stats().Load(rootCtx, time.Now().Add(-7*24*time.Hour)); err != nil {
		appLog.Warn("failed to preload statistics", "error", err.Error())
	}

	// --- event channels ---

	var (
		wsClient *qq.WSClient
		hook     *webhook.Receiver
	)

	if webSocketEnabled(cur) {
		if qqCredentialsPresent(cur) {
			wsClient = qq.NewWSClient(qq.WSOptions{
				Client:    qqClient,
				Intents:   cur.QQIntents,
				Shards:    cur.QQShardCount,
				Logger:    logger.With("component", "ws"),
				Handler:   dispatcher.HandlePayload,
				OnState:   func(status qq.WSStatus) { eventBus.Publish("ws", status) },
				TLSConfig: outboundTLS,
			})
			go wsClient.Run(rootCtx)
		} else {
			appLog.Warn("WebSocket gateway disabled: QQ_APP_ID / QQ_APP_SECRET are missing")
		}
	}

	if webhookEnabled(cur) {
		hook, err = webhook.New(webhook.Options{
			AppSecret: cur.QQAppSecret,
			Path:      cur.QQWebhookPath,
			Logger:    logger.With("component", "webhook"),
			Handle:    func(p *qq.Payload) { dispatcher.HandlePayload(rootCtx, p) },
		})
		if err != nil {
			return err
		}
		appLog.Info("webhook receiver enabled", "path", hook.Path(), "public_key_hex", hook.PublicKeyHex())
	}

	// --- HTTP server ---

	adminServer := admin.New(admin.Deps{
		Config:     cfg,
		Settings:   settingsMgr,
		Store:      st,
		Dispatcher: dispatcher,
		AI:         aiClient,
		QQ:         qqClient,
		Tokens:     tokens,
		WS:         wsClient,
		Webhook:    hook,
		Bus:        eventBus,
		Logger:     logger,
		LogRing:    ring,
		Media:      mediaBundle,
		Affect:     affectSvc,
		StartedAt:  time.Now(),
	})

	mux := http.NewServeMux()
	mux.Handle("/", adminServer.Handler())
	if hook != nil {
		mux.Handle(hook.Path(), hook)
	}

	httpServer := &http.Server{
		Addr:              cur.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Bind before announcing: a port conflict must fail fast and truthfully.
	listener, err := net.Listen("tcp", cur.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cur.HTTPAddr, err)
	}
	if !isLoopbackAddr(cur.HTTPAddr) {
		appLog.Warn("admin panel is bound to a non-loopback address",
			"addr", cur.HTTPAddr,
			"hint", "make sure a reverse proxy / firewall protects it; a leaked admin token grants full control")
	}

	serverErr := make(chan error, 1)
	go func() {
		appLog.Info("admin panel listening", "addr", listener.Addr().String())
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	go maintenance(rootCtx, appLog, st, dispatcher, settingsMgr, mediaBundle.Store)

	select {
	case <-rootCtx.Done():
		appLog.Info("shutdown signal received, draining")
	case err := <-serverErr:
		if err != nil {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		appLog.Warn("http shutdown error", "error", err.Error())
	}
	if err := dispatcher.Stats().Flush(shutdownCtx); err != nil {
		appLog.Warn("failed to flush statistics", "error", err.Error())
	}
	appLog.Info("stopped")
	return nil
}

// maintenance runs the periodic housekeeping loop.
func maintenance(ctx context.Context, log *slog.Logger, st *store.Store, d *core.Dispatcher, sm *settings.Manager, mediaStore *media.Store) {
	flush := time.NewTicker(30 * time.Second)
	defer flush.Stop()
	house := time.NewTicker(time.Hour)
	defer house.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-flush.C:
			if err := d.Stats().Flush(ctx); err != nil {
				log.Warn("stats flush failed", "component", "maintenance", "error", err.Error())
			}
			d.Stats().Prune(8 * 24 * time.Hour)
		case <-house.C:
			if n, err := st.PruneDedup(ctx, 24*time.Hour); err == nil && n > 0 {
				log.Debug("pruned dedup keys", "component", "maintenance", "count", n)
			}
			if n, err := st.PruneRecords(ctx, 50000); err == nil && n > 0 {
				log.Info("pruned message records", "component", "maintenance", "count", n)
			}
			if n, err := st.PruneExpiredHumanStatuses(ctx, time.Now()); err == nil && n > 0 {
				log.Debug("pruned expired human statuses", "component", "maintenance", "count", n)
			}
			ttl := time.Duration(sm.Get().HistoryTTLHours) * time.Hour
			if n, err := st.PruneMessages(ctx, ttl); err == nil && n > 0 {
				log.Info("pruned expired context messages", "component", "maintenance", "count", n)
			}
			if n, err := mediaStore.Prune(ctx, ttl); err == nil && n > 0 {
				log.Info("pruned unreferenced media", "component", "maintenance", "count", n)
			}
		}
	}
}

// listenAddr resolves the address the server listens on: the database setting
// wins, HTTP_ADDR is a fallback for probes that run before the DB is readable.
func listenAddr() string {
	if p := strings.TrimSpace(os.Getenv("HTTP_ADDR")); p != "" {
		// 容器里 HTTP_ADDR 往往显式设置，且与数据库一致；优先用它省一次开库。
		if addr, ok := httpAddrFromDB(); ok && addr != "" {
			return addr
		}
		return p
	}
	if addr, ok := httpAddrFromDB(); ok && addr != "" {
		return addr
	}
	return ""
}

// httpAddrFromDB reads the http_addr setting without running migrations: the
// healthcheck must stay cheap and must not touch a live database's schema.
func httpAddrFromDB() (string, bool) {
	cfg, err := config.Load()
	if err != nil || cfg.DBPath == "" {
		return "", false
	}
	db, err := sql.Open("sqlite", "file:"+cfg.DBPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return "", false
	}
	defer func() { _ = db.Close() }()
	var raw string
	if err := db.QueryRow(`SELECT v FROM settings WHERE k = 'http_addr'`).Scan(&raw); err != nil {
		return "", false
	}
	var addr string
	if json.Unmarshal([]byte(raw), &addr) != nil {
		return "", false
	}
	return strings.TrimSpace(addr), true
}

// qqAPIBase derives the platform REST base from the sandbox flag.
func qqAPIBase(s settings.Settings) string {
	if s.QQSandbox {
		return "https://sandbox.api.sandbox.bot.qq.com"
	}
	return "https://api.bot.qq.com"
}

func qqCredentialsPresent(s settings.Settings) bool {
	return strings.TrimSpace(s.QQAppID) != "" && strings.TrimSpace(s.QQAppSecret) != ""
}

func webSocketEnabled(s settings.Settings) bool {
	return s.QQTransport == "websocket" || s.QQTransport == "both"
}

func webhookEnabled(s settings.Settings) bool {
	return s.QQTransport == "webhook" || s.QQTransport == "both"
}

// randomHex returns n cryptographically random bytes as lowercase hex. A
// failure is returned instead of falling back to a predictable literal: a
// fixed admin token would be worse than refusing to start.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// isTerminal reports whether f is attached to a character device (an
// interactive terminal). Logs must not leak a generated admin token into
// non-interactive environments such as container log collectors.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// announceAdminToken prints the newly generated token only when stderr is an
// interactive terminal. Operators on servers read it from the database with
// `docker exec ... bot-harness token` instead of scraping logs.
func announceAdminToken(w io.Writer, token string, tty bool) {
	if tty {
		fmt.Fprintf(w, "generated admin token (persisted in the database): %s\n", token)
		return
	}
	fmt.Fprintln(w, "generated admin token (persisted in the database). "+
		"It is not printed because stderr is not a terminal; read it with: "+
		"docker exec bot-harness /app/bot-harness token")
}

// isLoopbackAddr reports whether addr only accepts loopback connections. An
// empty host (":8080") means every interface and is therefore not loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		// Bare host without a port, or an unparseable value: be conservative.
		return false
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
