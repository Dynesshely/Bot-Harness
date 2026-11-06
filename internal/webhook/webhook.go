// Package webhook implements the QQ open-platform HTTP callback receiver,
// including Ed25519 signature verification and URL validation.
package webhook

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dynesshely/bot-harness/internal/qq"
)

// Stats is the admin-visible state of the webhook receiver.
type Stats struct {
	Enabled        bool    `json:"enabled"`
	Path           string  `json:"path"`
	ReceivedTotal  int64   `json:"received_total"`
	VerifyFailures int64   `json:"verify_failures"`
	LastEventAt    *string `json:"last_event_at"`
}

// Receiver handles platform callbacks.
type Receiver struct {
	secret  string
	privKey ed25519.PrivateKey
	pubKey  ed25519.PublicKey
	path    string
	log     *slog.Logger

	// handle receives every verified event payload.
	handle func(p *qq.Payload)

	received       atomic.Int64
	verifyFailures atomic.Int64

	mu          sync.RWMutex
	lastEventAt time.Time
}

// Options configures a Receiver.
type Options struct {
	// AppSecret is the platform Bot Secret used to derive the Ed25519 keypair.
	AppSecret string
	// PublicKeyHex optionally overrides the derived public key.
	PublicKeyHex string
	Path         string
	Logger       *slog.Logger
	// Handle is called for every verified op=0 event.
	Handle func(p *qq.Payload)
}

// New builds a receiver. The keypair is derived from the bot secret exactly as
// the platform documents, so no extra configuration is required.
func New(opts Options) (*Receiver, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	r := &Receiver{
		secret: opts.AppSecret,
		path:   opts.Path,
		log:    logger,
		handle: opts.Handle,
	}
	if r.path == "" {
		r.path = "/qq/webhook"
	}

	if opts.PublicKeyHex != "" {
		raw, err := hex.DecodeString(strings.TrimSpace(opts.PublicKeyHex))
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("webhook: invalid public key")
		}
		r.pubKey = ed25519.PublicKey(raw)
		return r, nil
	}

	if opts.AppSecret == "" {
		return nil, fmt.Errorf("webhook: QQ_APP_SECRET is required to verify callbacks")
	}
	priv := DerivePrivateKey(opts.AppSecret)
	r.privKey = priv
	r.pubKey = priv.Public().(ed25519.PublicKey)
	return r, nil
}

// DerivePrivateKey reproduces the platform's key derivation from the bot secret.
func DerivePrivateKey(secret string) ed25519.PrivateKey {
	seed := secret
	for len(seed) < ed25519.SeedSize {
		seed = strings.Repeat(seed, 2)
	}
	return ed25519.NewKeyFromSeed([]byte(seed[:ed25519.SeedSize]))
}

func (r *Receiver) Path() string { return r.path }

// PublicKeyHex returns the derived public key in hex (for platform config).
func (r *Receiver) PublicKeyHex() string { return hex.EncodeToString(r.pubKey) }

// Stats snapshots the receiver state.
func (r *Receiver) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st := Stats{
		Enabled:        true,
		Path:           r.path,
		ReceivedTotal:  r.received.Load(),
		VerifyFailures: r.verifyFailures.Load(),
	}
	if !r.lastEventAt.IsZero() {
		s := r.lastEventAt.Format(time.RFC3339)
		st.LastEventAt = &s
	}
	return st
}

type envelope struct {
	Op int             `json:"op"`
	T  string          `json:"t"`
	ID string          `json:"id"`
	D  json.RawMessage `json:"d"`
}

// ServeHTTP implements http.Handler.
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, 2<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if !r.verify(req, body) {
		r.verifyFailures.Add(1)
		r.log.Warn("webhook signature verification failed", "component", "webhook",
			"remote", req.RemoteAddr)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	switch env.Op {
	case qq.OpCallbackVerify:
		r.handleValidation(w, env)
		return
	case qq.OpDispatch:
		r.received.Add(1)
		r.mu.Lock()
		r.lastEventAt = time.Now()
		r.mu.Unlock()

		payload := &qq.Payload{Op: env.Op, T: env.T, ID: env.ID, D: env.D}
		if r.handle != nil {
			// Answer immediately: the platform retries slow callbacks.
			go r.handle(payload)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"op":0}`))
		return
	default:
		r.log.Debug("ignoring webhook opcode", "component", "webhook", "op", env.Op)
		w.WriteHeader(http.StatusOK)
		return
	}
}

// verify checks the Ed25519 signature over timestamp+body.
func (r *Receiver) verify(req *http.Request, body []byte) bool {
	sigHeader := req.Header.Get("X-Signature-Ed25519")
	ts := req.Header.Get("X-Signature-Timestamp")
	if sigHeader == "" || ts == "" {
		return false
	}
	sig, err := hex.DecodeString(sigHeader)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	msg := make([]byte, 0, len(ts)+len(body))
	msg = append(msg, ts...)
	msg = append(msg, body...)
	return ed25519.Verify(r.pubKey, msg, sig)
}

func (r *Receiver) handleValidation(w http.ResponseWriter, env envelope) {
	var v struct {
		PlainToken string `json:"plain_token"`
		EventTs    string `json:"event_ts"`
	}
	if err := json.Unmarshal(env.D, &v); err != nil {
		http.Error(w, "bad validation payload", http.StatusBadRequest)
		return
	}
	if r.privKey == nil {
		http.Error(w, "no signing key configured", http.StatusInternalServerError)
		return
	}
	signature := hex.EncodeToString(ed25519.Sign(r.privKey, []byte(v.EventTs+v.PlainToken)))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"plain_token": v.PlainToken,
		"signature":   signature,
	})
	r.log.Info("answered webhook URL validation", "component", "webhook")
}
