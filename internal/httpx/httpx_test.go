package httpx

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCertPEM saves the test server's self-signed certificate to a file, the
// same way an operator would drop a private CA bundle next to the project.
func writeCertPEM(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	block := &pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	return path
}

func TestConfigIsNilWhenNothingIsConfigured(t *testing.T) {
	empty := TLSOptions{}
	cfg, err := empty.Config()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config so Go keeps its defaults, got %#v", cfg)
	}
	if empty.Configured() {
		t.Fatal("Configured should be false")
	}
}

func TestExtraCABundleIsTrusted(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	caPath := writeCertPEM(t, srv)

	// Baseline: without the bundle the handshake must fail, otherwise this test
	// would not be proving anything.
	plain := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	if resp, err := plain.Get(srv.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected the self-signed server to be rejected without the extra CA")
	} else if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "x509") {
		t.Fatalf("expected a certificate error, got %v", err)
	}

	opts := TLSOptions{ExtraCAFiles: []string{caPath}}
	if !opts.Configured() {
		t.Fatal("Configured should be true once a bundle is set")
	}
	client, err := opts.Client(5 * time.Second)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("request with the extra CA should succeed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestMultipleBundlesAreAppended(t *testing.T) {
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("first"))
	}))
	defer first.Close()
	second := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("second"))
	}))
	defer second.Close()

	// One path list containing two bundles: both hosts must be trusted.
	opts := TLSOptions{ExtraCAFiles: []string{writeCertPEM(t, first), writeCertPEM(t, second)}}
	client, err := opts.Client(5 * time.Second)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	for name, srv := range map[string]*httptest.Server{"first": first, "second": second} {
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = resp.Body.Close()
	}
}

func TestMissingOrInvalidBundleIsAnError(t *testing.T) {
	if _, err := (TLSOptions{ExtraCAFiles: []string{"/nonexistent/ca.pem"}}).Config(); err == nil {
		t.Fatal("expected an error for a missing bundle")
	}

	junk := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(junk, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}
	if _, err := (TLSOptions{ExtraCAFiles: []string{junk}}).Config(); err == nil {
		t.Fatal("expected an error when no usable PEM certificate is present")
	}
}

func TestInsecureSkipVerifyAllowsSelfSigned(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	opts := TLSOptions{InsecureSkipVerify: true}
	client, err := opts.Client(5 * time.Second)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("insecure client should connect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestConfigSetsMinimumTLSVersion(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	cfg, err := TLSOptions{ExtraCAFiles: []string{writeCertPEM(t, srv)}}.Config()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.MinVersion < 0x0303 {
		t.Fatalf("expected at least TLS 1.2, got %#x", cfg.MinVersion)
	}
}
