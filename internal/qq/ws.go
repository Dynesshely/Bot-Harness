package qq

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSStatus is the admin-visible state of the gateway connection.
type WSStatus struct {
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

// WSClient maintains the gateway connection and dispatches events.
type WSClient struct {
	client  *Client
	intents uint64
	shards  int
	log     *slog.Logger
	handler func(ctx context.Context, p *Payload)
	onState func(status WSStatus)

	// forceReconnect asks the running session to drop its connection.
	forceReconnect chan struct{}

	// tlsConfig carries the optional extra CA bundle for the gateway.
	tlsConfig *tls.Config

	// dispatch queues keep slow message handlers off the websocket read loop.
	// Sharding by group preserves per-group order without serializing all groups.
	dispatch     []chan *Payload
	dispatchOnce sync.Once

	mu          sync.RWMutex
	state       string
	connected   bool
	sessionID   string
	gatewayURL  string
	hbInterval  time.Duration
	latencyMS   int64
	lastEventAt time.Time
	reconnects  int
	resumes     int
	lastErr     string
	seq         int
	hasSeq      bool
}

// WSOptions configures a WSClient.
type WSOptions struct {
	Client  *Client
	Intents uint64
	Shards  int
	Logger  *slog.Logger
	Handler func(ctx context.Context, p *Payload)
	OnState func(status WSStatus)
	// TLSConfig optionally carries an extra CA bundle for the gateway.
	TLSConfig *tls.Config
}

// NewWSClient builds a gateway client. Call Run to start it.
func NewWSClient(opts WSOptions) *WSClient {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	shards := opts.Shards
	if shards < 1 {
		shards = 1
	}
	w := &WSClient{
		client:         opts.Client,
		intents:        opts.Intents,
		shards:         shards,
		log:            logger,
		handler:        opts.Handler,
		onState:        opts.OnState,
		tlsConfig:      opts.TLSConfig,
		forceReconnect: make(chan struct{}, 1),
		dispatch:       make([]chan *Payload, wsDispatchShards),
		state:          "idle",
	}
	for i := range w.dispatch {
		w.dispatch[i] = make(chan *Payload, wsDispatchQueue)
	}
	return w
}

// Ready returns a channel closed after the first successful READY.
// ForceReconnect asks the session loop to reconnect immediately.
func (w *WSClient) ForceReconnect() {
	select {
	case w.forceReconnect <- struct{}{}:
	default:
	}
}

// Status snapshots the connection state.
func (w *WSClient) Status() WSStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	st := WSStatus{
		Enabled:             true,
		Connected:           w.connected,
		State:               w.state,
		SessionID:           w.sessionID,
		Shard:               []int{0, w.shards},
		GatewayURL:          w.gatewayURL,
		HeartbeatIntervalMS: int(w.hbInterval.Milliseconds()),
		LatencyMS:           w.latencyMS,
		Reconnects:          w.reconnects,
		Resumes:             w.resumes,
	}
	if !w.lastEventAt.IsZero() {
		s := w.lastEventAt.Format(time.RFC3339)
		st.LastEventAt = &s
	}
	if w.lastErr != "" {
		s := w.lastErr
		st.LastError = &s
	}
	return st
}

func (w *WSClient) setState(state string, connected bool) {
	w.mu.Lock()
	w.state = state
	w.connected = connected
	w.mu.Unlock()
	if w.onState != nil {
		w.onState(w.Status())
	}
}

func (w *WSClient) setError(err error) {
	w.mu.Lock()
	if err != nil {
		w.lastErr = err.Error()
	} else {
		w.lastErr = ""
	}
	w.mu.Unlock()
}

// Run keeps a gateway connection alive until ctx is cancelled.
func (w *WSClient) Run(ctx context.Context) {
	w.startDispatch(ctx)
	const (
		minBackoff = time.Second
		maxBackoff = 60 * time.Second
	)
	backoff := minBackoff

	for {
		if ctx.Err() != nil {
			w.setState("stopped", false)
			return
		}

		err := w.session(ctx)
		if ctx.Err() != nil {
			w.setState("stopped", false)
			return
		}

		w.setState("reconnecting", false)
		w.mu.Lock()
		w.reconnects++
		w.mu.Unlock()
		if err != nil {
			w.setError(err)
			w.log.Warn("gateway session ended", "component", "ws", "error", err.Error())
		}

		if err == nil || errors.Is(err, errResumable) {
			// Clean drop or resume-able close: retry quickly, keeping the session.
			backoff = minBackoff
		}
		delay := backoff + time.Duration(rand.Int63n(int64(backoff/2+1)))
		w.log.Info("reconnecting to gateway", "component", "ws", "in_ms", delay.Milliseconds())

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			w.setState("stopped", false)
			return
		}
		if err != nil {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// errResumable marks a session drop that should be recovered with Resume.
var errResumable = errors.New("session can be resumed")

func (w *WSClient) session(ctx context.Context) error {
	w.setState("connecting", false)

	info, err := w.client.GetGateway(ctx)
	if err != nil {
		return fmt.Errorf("get gateway: %w", err)
	}
	gatewayURL := info.URL
	w.mu.Lock()
	w.gatewayURL = info.URL
	w.mu.Unlock()

	dialer := websocket.Dialer{
		HandshakeTimeout: 20 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
		TLSClientConfig:  w.tlsConfig,
	}
	header := map[string][]string{"User-Agent": {"bot-harness/1.0"}}
	conn, resp, err := dialer.DialContext(ctx, gatewayURL, header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial gateway: %w (http %d)", err, resp.StatusCode)
		}
		return fmt.Errorf("dial gateway: %w", err)
	}
	defer func() { _ = conn.Close() }()

	s := &session{ws: w, conn: conn, ackCh: make(chan struct{}, 4)}
	w.attach(s)
	defer w.detach()

	// 1. Hello
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var hello Payload
	if err := conn.ReadJSON(&hello); err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	if hello.Op != OpHello {
		return fmt.Errorf("unexpected first opcode %d (want hello)", hello.Op)
	}
	var helloData struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	_ = json.Unmarshal(hello.D, &helloData)
	if helloData.HeartbeatInterval <= 0 {
		helloData.HeartbeatInterval = 45000
	}
	interval := time.Duration(helloData.HeartbeatInterval) * time.Millisecond
	w.mu.Lock()
	w.hbInterval = interval
	w.mu.Unlock()

	// 2. Identify or Resume
	w.mu.RLock()
	sessionID, seq, hasSeq := w.sessionID, w.seq, w.hasSeq
	w.mu.RUnlock()

	resuming := sessionID != "" && hasSeq
	if resuming {
		if err := s.send(map[string]any{
			"op": OpResume,
			"d": map[string]any{
				"token":      "QQBot " + w.client.Tokens().Cached(),
				"session_id": sessionID,
				"seq":        seq,
			},
		}); err != nil {
			return fmt.Errorf("send resume: %w", err)
		}
		w.log.Info("resuming gateway session", "component", "ws", "session_id", sessionID, "seq", seq)
	} else {
		token, err := w.client.Tokens().Token(ctx, false)
		if err != nil {
			return fmt.Errorf("identify: %w", err)
		}
		if err := s.send(map[string]any{
			"op": OpIdentify,
			"d": map[string]any{
				"token":   "QQBot " + token,
				"intents": w.intents,
				"shard":   []int{0, w.shards},
				"properties": map[string]string{
					"$os":      "linux",
					"$browser": "bot-harness",
					"$device":  "bot-harness",
				},
			},
		}); err != nil {
			return fmt.Errorf("send identify: %w", err)
		}
		w.log.Info("identifying to gateway", "component", "ws", "intents", w.intents, "shards", w.shards)
	}

	// 3. Heartbeats
	hbCtx, cancelHB := context.WithCancel(ctx)
	defer cancelHB()
	go s.heartbeatLoop(hbCtx, interval)

	// 4. Main read loop
	return s.readLoop(ctx, resuming)
}

func (w *WSClient) attach(s *session) {
	w.mu.Lock()
	w.connected = true
	w.mu.Unlock()
}

func (w *WSClient) detach() {
	w.mu.Lock()
	w.connected = false
	w.mu.Unlock()
}

// session holds one live connection.
type session struct {
	ws      *WSClient
	conn    *websocket.Conn
	writeMu sync.Mutex
	ackCh   chan struct{}
}

func (s *session) send(v any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return s.conn.WriteJSON(v)
}

func (s *session) heartbeatLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	missed := 0
	var lastSent time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.ws.mu.RLock()
			seq, hasSeq := s.ws.seq, s.ws.hasSeq
			s.ws.mu.RUnlock()

			var d any
			if hasSeq {
				d = seq
			}
			if err := s.send(map[string]any{"op": OpHeartbeat, "d": d}); err != nil {
				s.ws.log.Warn("heartbeat send failed", "component", "ws", "error", err.Error())
				s.hardClose()
				return
			}
			if !lastSent.IsZero() && missed >= 2 {
				s.ws.log.Warn("heartbeat ack missing, forcing reconnect", "component", "ws", "missed", missed)
				s.hardClose()
				return
			}
			select {
			case <-s.ackCh:
				if !lastSent.IsZero() {
					s.ws.mu.Lock()
					s.ws.latencyMS = time.Since(lastSent).Milliseconds()
					s.ws.mu.Unlock()
				}
				missed = 0
			default:
				missed++
			}
			lastSent = time.Now()
		}
	}
}

func (s *session) hardClose() { _ = s.conn.Close() }

func (s *session) readLoop(ctx context.Context, resuming bool) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.ws.forceReconnect:
			s.ws.log.Info("manual reconnect requested", "component", "ws")
			return nil
		default:
		}

		_ = s.conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		var p Payload
		if err := s.conn.ReadJSON(&p); err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) {
				return s.ws.closeError(ce)
			}
			return fmt.Errorf("gateway read: %w", err)
		}

		if p.S != nil {
			s.ws.mu.Lock()
			s.ws.seq = *p.S
			s.ws.hasSeq = true
			s.ws.mu.Unlock()
		}

		switch p.Op {
		case OpDispatch:
			s.ws.handleDispatch(ctx, &p, resuming)
			resuming = false
		case OpHeartbeatACK:
			select {
			case s.ackCh <- struct{}{}:
			default:
			}
		case OpReconnect:
			s.ws.log.Info("gateway asked to reconnect", "component", "ws")
			return nil
		case OpInvalidSession:
			var resumable bool
			_ = json.Unmarshal(p.D, &resumable)
			s.ws.mu.Lock()
			s.ws.hasSeq = false
			s.ws.sessionID = ""
			s.ws.mu.Unlock()
			if !resumable {
				s.ws.log.Warn("invalid session, re-identifying", "component", "ws")
			}
			return nil
		case OpHello:
			// Re-hello mid-stream: ignore, heartbeats keep the link alive.
		default:
			s.ws.log.Debug("unhandled opcode", "component", "ws", "op", p.Op)
		}
	}
}

func (w *WSClient) closeError(ce *websocket.CloseError) error {
	switch ce.Code {
	case 4009: // connection expired: resume is allowed
		w.log.Info("gateway connection expired, will resume", "component", "ws")
		return errResumable
	case 4914, 4915:
		return fmt.Errorf("gateway closed %d (%s): bot is offline or banned", ce.Code, ce.Text)
	default:
		// Any other code: drop the session so the next attempt identifies afresh.
		w.mu.Lock()
		w.sessionID = ""
		w.hasSeq = false
		w.mu.Unlock()
		return fmt.Errorf("gateway closed %d: %s", ce.Code, ce.Text)
	}
}

func (w *WSClient) handleDispatch(ctx context.Context, p *Payload, resuming bool) {
	w.mu.Lock()
	w.lastEventAt = time.Now()
	w.mu.Unlock()

	switch p.T {
	case EventReady:
		var d struct {
			SessionID string `json:"session_id"`
			Shard     []int  `json:"shard"`
		}
		_ = json.Unmarshal(p.D, &d)
		w.mu.Lock()
		w.sessionID = d.SessionID
		w.mu.Unlock()
		w.setState("ready", true)
		w.setError(nil)
		w.log.Info("gateway ready", "component", "ws", "session_id", d.SessionID, "shard", d.Shard)
		return
	case EventResumed:
		w.mu.Lock()
		w.resumes++
		w.mu.Unlock()
		w.setState("ready", true)
		w.setError(nil)
		w.log.Info("gateway session resumed", "component", "ws")
		return
	}

	if resuming {
		w.setState("ready", true)
	}

	if w.handler == nil {
		return
	}
	idx := int(fnv32(dispatchKey(p)) % uint32(len(w.dispatch)))
	select {
	case w.dispatch[idx] <- p:
	default:
		// Never block the gateway read loop: a stalled handler must not stop
		// heartbeats or every other group. Drop is logged; the dispatcher's
		// msg_id dedup cannot recover it, so keep the queue generous.
		w.log.Warn("websocket dispatch queue full; dropping event",
			"component", "ws", "shard", idx, "event", p.T, "id", p.ID)
	}
}

const (
	wsDispatchShards = 8
	wsDispatchQueue  = 128
)

func (w *WSClient) startDispatch(ctx context.Context) {
	w.dispatchOnce.Do(func() {
		for i := range w.dispatch {
			go w.dispatchLoop(ctx, w.dispatch[i])
		}
	})
}

func (w *WSClient) dispatchLoop(ctx context.Context, ch <-chan *Payload) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-ch:
			if p == nil {
				return
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						w.log.Error("websocket handler panic", "component", "ws", "panic", fmt.Sprint(r))
					}
				}()
				w.handler(ctx, p)
			}()
		}
	}
}

func fnv32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

func dispatchKey(p *Payload) string {
	var d struct {
		GroupOpenID string `json:"group_openid"`
		UserOpenID  string `json:"user_openid"`
		Author      struct {
			ID         string `json:"id"`
			UserOpenID string `json:"user_openid"`
		} `json:"author"`
	}
	_ = json.Unmarshal(p.D, &d)
	switch {
	case d.GroupOpenID != "":
		return "g:" + d.GroupOpenID
	case d.UserOpenID != "":
		return "u:" + d.UserOpenID
	case d.Author.UserOpenID != "":
		return "u:" + d.Author.UserOpenID
	case d.Author.ID != "":
		return "a:" + d.Author.ID
	default:
		return "id:" + p.ID
	}
}

func (w *WSClient) SessionID() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sessionID
}
