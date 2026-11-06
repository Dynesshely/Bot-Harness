// Package httpx builds outbound HTTP/TLS clients with an optional extra CA
// bundle.
//
// Self-hosted model gateways (for example an internal one-api deployment on a
// *.internal.example hostname) are usually signed by a private CA that is not in
// the container image's trust store. TLS_EXTRA_CA_FILE lets the operator add
// those roots/intermediates without rebuilding the image.
package httpx

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"
)

// TLSOptions describes how outbound TLS should trust peers.
type TLSOptions struct {
	// ExtraCAFiles are PEM bundles (root and/or intermediate certificates)
	// appended to the system roots. Missing files are an error: silently
	// ignoring them only defers the failure to a confusing TLS handshake error.
	ExtraCAFiles []string
	// InsecureSkipVerify disables certificate verification entirely. Intended
	// only as a last resort for endpoints whose CA cannot be obtained.
	InsecureSkipVerify bool
}

// Configured reports whether any non-default TLS behaviour was requested.
func (o TLSOptions) Configured() bool {
	return len(o.ExtraCAFiles) > 0 || o.InsecureSkipVerify
}

// Config builds a *tls.Config, or returns nil when the defaults should be kept.
func (o TLSOptions) Config() (*tls.Config, error) {
	if !o.Configured() {
		return nil, nil
	}
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: o.InsecureSkipVerify,
	}
	if o.InsecureSkipVerify {
		return cfg, nil
	}

	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	loaded := 0
	for _, path := range o.ExtraCAFiles {
		if path == "" {
			continue
		}
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("tls: cannot read extra CA %q: %w", path, err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls: %q contains no usable PEM certificates", path)
		}
		loaded++
	}
	if loaded == 0 {
		return nil, fmt.Errorf("tls: no extra CA certificates were loaded")
	}
	cfg.RootCAs = pool
	return cfg, nil
}

// Transport builds an outbound transport honouring the proxy environment.
func (o TLSOptions) Transport() (*http.Transport, error) {
	tlsCfg, err := o.Config()
	if err != nil {
		return nil, err
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
		TLSClientConfig:       tlsCfg,
	}, nil
}

// Client builds an HTTP client. A zero timeout means "no client-level timeout".
func (o TLSOptions) Client(timeout time.Duration) (*http.Client, error) {
	transport, err := o.Transport()
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}
