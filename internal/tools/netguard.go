package tools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// ErrBlockedAddress is returned when a URL resolves (or redirects) to an address
// the bot must not touch: loopback, RFC1918, link-local, cloud metadata…
var ErrBlockedAddress = errors.New("目标地址不允许访问（内网/回环/元数据地址）")

// isPublicIP reports whether an address is safe to fetch from.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// Carrier-grade NAT and benchmarking ranges are not public either.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
		if v4[0] == 198 && (v4[1] == 18 || v4[1] == 19) {
			return false
		}
	}
	return true
}

// metadataAliasIPs are cloud metadata endpoints that stay blocked even when an
// operator explicitly points the search backend at one.
var metadataAliasIPs = []net.IP{
	net.ParseIP("169.254.169.254"), // AWS / GCP / Azure / OpenStack metadata (also link-local)
	net.ParseIP("100.100.100.200"), // Alibaba Cloud metadata (inside CGNAT)
	net.ParseIP("fd00:ec2::254"),   // AWS IPv6 metadata alias
}

// isForbiddenOriginIP reports addresses refused even for an explicitly
// configured private origin: link-local, unspecified, multicast and the known
// cloud-metadata aliases. Ordinary private/loopback addresses are deliberately
// NOT refused here — the caller decides which single origin may use them.
func isForbiddenOriginIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, alias := range metadataAliasIPs {
		if ip.Equal(alias) {
			return true
		}
	}
	return false
}

// resolveHostAddrs turns a host (literal or name) into "ip:port" strings so the
// dial guard can match the socket it is about to open, port included.
func resolveHostAddrs(host, port string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{net.JoinHostPort(ip.String(), port)}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.JoinHostPort(ip.String(), port))
	}
	return out, nil
}

// proxyEndpointAddrs returns the "ip:port" endpoints of HTTP_PROXY /
// HTTPS_PROXY (upper- or lower-case), resolved when a guarded client is built.
//
// The dial guard sees the *proxy's* address, not the target's, so those exact
// endpoints are permitted; the target is still validated by the pre-flight
// checkURL and by every redirect hop. Residual risk (unchanged): with a proxy
// the proxy resolves the target name, so a rebinding target is invisible to the
// pre-flight lookup.
func proxyEndpointAddrs() map[string]struct{} {
	out := map[string]struct{}{}
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host, port := u.Hostname(), u.Port()
		if host == "" {
			// Tolerate the scheme-less "host:port" form.
			if withScheme, err := url.Parse("http://" + raw); err == nil {
				host, port = withScheme.Hostname(), withScheme.Port()
			}
		}
		if host == "" {
			continue
		}
		if port == "" {
			port = "80"
		}
		addrs, err := resolveHostAddrs(host, port)
		if err != nil {
			continue // fail closed: an unresolvable proxy simply is not allowed
		}
		for _, addr := range addrs {
			out[addr] = struct{}{}
		}
	}
	return out
}

// checkURL validates scheme, credentials and every address the host resolves to.
//
// The dialer repeats the address check on the socket it actually opens, which is
// what defeats a DNS-rebinding host (public on the first lookup, private on the
// second). When an outbound proxy is configured the dialer only sees the proxy,
// so the pre-flight resolution here is the only guard on the target.
func checkURL(raw string, allowPrivate bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("URL 解析失败: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("只支持 http/https，收到 %q", u.Scheme)
	}
	if u.User != nil {
		return nil, errors.New("URL 里不允许带用户名密码")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("URL 缺少主机名")
	}
	if allowPrivate {
		return u, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return nil, ErrBlockedAddress
		}
		return u, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("解析 %s 没有得到地址", host)
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, ErrBlockedAddress
		}
	}
	return u, nil
}

// newGuardClient builds an HTTP client that cannot reach private addresses,
// including on redirects.
//
// Two exceptions are deliberate and narrow:
//   - the exact HTTP_PROXY / HTTPS_PROXY endpoints (resolved to ip:port) are
//     permitted, because with a proxy the dial guard only ever sees the proxy;
//   - allowPrivate (test-only) disables the address policy entirely.
//
// The target itself is still validated by the pre-flight checkURL and by every
// redirect hop, so permitting the proxy does not permit a private target.
func newGuardClient(timeout time.Duration, allowPrivate bool) *http.Client {
	proxyAddrs := proxyEndpointAddrs()
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if allowPrivate {
				return nil
			}
			if _, ok := proxyAddrs[address]; ok {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || !isPublicIP(ip) {
				return ErrBlockedAddress
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConns:          8,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("重定向次数过多（上限 3 次）")
			}
			_, err := checkURL(req.URL.String(), allowPrivate)
			return err
		},
	}
}

// newOriginGuardClient builds the client used for the configured search backend.
//
// SearxNG normally runs as a sidecar on a private address, so the normal policy
// would make web_search unusable while still advertising the tool. This client
// narrows the exception instead of opening a global switch:
//
//   - only the configured origin (scheme+host+port) may resolve to a private
//     address, and only the exact resolved ip:port pairs are allowed;
//   - an origin resolving to link-local / a cloud-metadata alias is refused
//     outright (see isForbiddenOriginIP);
//   - redirects are followed only within the same origin; a different origin has
//     to pass the standard public-only check, and the dial guard still refuses
//     any other private address.
//
// The transport deliberately does not use the outbound proxy: the backend is
// expected to be reachable directly (docker network / NO_PROXY), and routing an
// internal search request through a proxy would only lose the address policy.
func newOriginGuardClient(timeout time.Duration, origin *url.URL) (*http.Client, error) {
	if origin == nil || (origin.Scheme != "http" && origin.Scheme != "https") {
		return nil, fmt.Errorf("搜索后端地址必须是 http(s) URL")
	}
	if origin.User != nil {
		return nil, errors.New("搜索后端地址里不允许带用户名密码")
	}
	host := origin.Hostname()
	if host == "" {
		return nil, errors.New("搜索后端地址缺少主机名")
	}
	port := origin.Port()
	if port == "" {
		if origin.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	addrs, err := resolveHostAddrs(host, port)
	if err != nil {
		return nil, fmt.Errorf("解析搜索后端 %s 失败: %w", host, err)
	}
	allowed := make(map[string]struct{}, len(addrs))
	for _, addr := range addrs {
		ipStr, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if isForbiddenOriginIP(net.ParseIP(ipStr)) {
			return nil, ErrBlockedAddress
		}
		allowed[addr] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("搜索后端 %s 没有解析出任何地址", host)
	}

	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if _, ok := allowed[address]; ok {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			// Public hops are fine (a redirect to a public page); every other
			// private address — including the same host on another port — is not.
			if ip == nil || isForbiddenOriginIP(ip) || !isPublicIP(ip) {
				return ErrBlockedAddress
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConns:          8,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("重定向次数过多（上限 3 次）")
			}
			if req.URL.Scheme == origin.Scheme && strings.EqualFold(req.URL.Host, origin.Host) {
				return nil // same origin: the dial guard already constrains it
			}
			_, err := checkURL(req.URL.String(), false)
			return err
		},
	}, nil
}
