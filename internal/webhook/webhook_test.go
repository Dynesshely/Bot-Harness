package webhook

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/qq"
)

const testSecret = "naOC0ocQE3shWLAfffVLB1rhYPG7"

func sign(t *testing.T, body []byte, ts string) (string, string) {
	t.Helper()
	priv := DerivePrivateKey(testSecret)
	msg := append([]byte(ts), body...)
	return hex.EncodeToString(ed25519.Sign(priv, msg)), ts
}

func newTestReceiver(t *testing.T) (*Receiver, *[]*qq.Payload, *sync.Mutex) {
	t.Helper()
	var (
		mu       sync.Mutex
		received []*qq.Payload
	)
	r, err := New(Options{
		AppSecret: testSecret,
		Path:      "/qq/webhook",
		Handle: func(p *qq.Payload) {
			mu.Lock()
			defer mu.Unlock()
			received = append(received, p)
		},
	})
	if err != nil {
		t.Fatalf("new receiver: %v", err)
	}
	return r, &received, &mu
}

func TestValidationCallback(t *testing.T) {
	r, _, _ := newTestReceiver(t)

	body := []byte(`{"d":{"plain_token":"Arq0D5A61EgUu4OxUvOp","event_ts":"1725442341"},"op":13}`)
	ts := "1725442341"
	sig, ts := sign(t, body, ts)

	req := httptest.NewRequest(http.MethodPost, "/qq/webhook", bytes.NewReader(body))
	req.Header.Set("X-Signature-Ed25519", sig)
	req.Header.Set("X-Signature-Timestamp", ts)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		PlainToken string `json:"plain_token"`
		Signature  string `json:"signature"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PlainToken != "Arq0D5A61EgUu4OxUvOp" {
		t.Fatalf("plain_token not echoed: %q", resp.PlainToken)
	}

	// The signature must verify against the public key derived from the secret
	// and cover event_ts + plain_token, exactly as the platform requires.
	raw, err := hex.DecodeString(resp.Signature)
	if err != nil {
		t.Fatalf("signature is not hex: %v", err)
	}
	pub := DerivePrivateKey(testSecret).Public().(ed25519.PublicKey)
	if !ed25519.Verify(pub, []byte("1725442341Arq0D5A61EgUu4OxUvOp"), raw) {
		t.Fatal("validation signature does not verify")
	}
}

func TestDispatchEventIsForwarded(t *testing.T) {
	r, received, mu := newTestReceiver(t)

	body := []byte(`{"op":0,"t":"GROUP_AT_MESSAGE_CREATE","id":"EV1","d":{"id":"MSG1","content":"hi","group_openid":"G1"}}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig, ts := sign(t, body, ts)

	req := httptest.NewRequest(http.MethodPost, "/qq/webhook", bytes.NewReader(body))
	req.Header.Set("X-Signature-Ed25519", sig)
	req.Header.Set("X-Signature-Timestamp", ts)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Body.String(); got != `{"op":0}` {
		t.Fatalf("unexpected ack body %q", got)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*received)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(*received) != 1 {
		t.Fatalf("expected one forwarded payload, got %d", len(*received))
	}
	p := (*received)[0]
	if p.T != qq.EventGroupAtMessageCreate || p.ID != "EV1" {
		t.Fatalf("unexpected payload %#v", p)
	}
	st := r.Stats()
	if st.ReceivedTotal != 1 || st.LastEventAt == nil {
		t.Fatalf("unexpected stats %#v", st)
	}
}

func TestBadSignatureIsRejected(t *testing.T) {
	r, received, mu := newTestReceiver(t)

	body := []byte(`{"op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{}}`)
	cases := []struct {
		name string
		sig  string
		ts   string
	}{
		{"no headers", "", ""},
		{"not hex", "zzzz", "1"},
		{"wrong signature", hex.EncodeToString(make([]byte, ed25519.SignatureSize)), "1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/qq/webhook", bytes.NewReader(body))
			if tc.sig != "" {
				req.Header.Set("X-Signature-Ed25519", tc.sig)
			}
			if tc.ts != "" {
				req.Header.Set("X-Signature-Timestamp", tc.ts)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}

	// A signature over a tampered body must also fail.
	ts := "1725442341"
	sig, ts := sign(t, body, ts)
	tampered := []byte(`{"op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{"content":"evil"}}`)
	req := httptest.NewRequest(http.MethodPost, "/qq/webhook", bytes.NewReader(tampered))
	req.Header.Set("X-Signature-Ed25519", sig)
	req.Header.Set("X-Signature-Timestamp", ts)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered body accepted: %d", rec.Code)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(*received) != 0 {
		t.Fatal("no payload should have been forwarded")
	}
	if r.Stats().VerifyFailures != 4 {
		t.Fatalf("verify failures = %d, want 4", r.Stats().VerifyFailures)
	}
}

func TestDerivePrivateKeyMatchesPlatformSample(t *testing.T) {
	// Expected public key taken from the official documentation example.
	const want = "d7c362fe78aef81ff23287b493628b5db02a3c4fe30b215e4d19609b5d76673a"
	got := hex.EncodeToString(DerivePrivateKey(testSecret).Public().(ed25519.PublicKey))
	if got != want {
		t.Fatalf("derived public key mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	r, _, _ := newTestReceiver(t)
	req := httptest.NewRequest(http.MethodGet, "/qq/webhook", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
