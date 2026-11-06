package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCheckURLBlocksInternalTargets(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/",
		"http://127.0.0.1:8080/api/status",
		"http://localhost/",
		"http://10.1.2.3/",
		"http://192.168.1.1/",
		"http://172.16.0.1/",
		"http://169.254.169.254/latest/meta-data/", // 云元数据
		"http://[::1]/",
		"http://0.0.0.0/",
		"http://100.64.0.1/", // CGNAT
		"http://198.18.0.1/", // 基准测试网段
		"file:///etc/passwd",
		"ftp://example.com/",
		"http://user:pass@example.com/",
	}
	for _, raw := range cases {
		if _, err := checkURL(raw, false); err == nil {
			t.Fatalf("%s 应该被拒绝", raw)
		}
	}
}

func TestCheckURLAllowsPublicLiteral(t *testing.T) {
	for _, raw := range []string{"https://1.1.1.1/", "http://8.8.8.8:443/x"} {
		if _, err := checkURL(raw, false); err != nil {
			t.Fatalf("%s 不该被拒绝: %v", raw, err)
		}
	}
}

func TestSSRFGuardWorksOnHostnames(t *testing.T) {
	// localhost 这个名字会解析到回环地址，必须在解析后被拦下。
	if _, err := checkURL("http://localhost:6379/", false); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("localhost 应命中黑名单，实际 %v", err)
	}
}

func parseIP(s string) net.IP { return net.ParseIP(s) }

func indexOf(s, sub string) int { return strings.Index(s, sub) }

func TestIsPublicIP(t *testing.T) {
	public := []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"}
	for _, s := range public {
		if ip := parseIP(s); !isPublicIP(ip) {
			t.Fatalf("%s 应视为公网", s)
		}
	}
	blocked := []string{"127.0.0.1", "10.1.2.3", "192.168.0.1", "169.254.169.254", "::1", "fe80::1", "fc00::1", "0.0.0.0"}
	for _, s := range blocked {
		if ip := parseIP(s); isPublicIP(ip) {
			t.Fatalf("%s 应被拦截", s)
		}
	}
}

func TestHTMLToTextStripsMarkup(t *testing.T) {
	raw := `<html><head><style>body{color:red}</style><script>alert(1)</script></head>` +
		`<body><h1>标题</h1><p>正文 &amp; 更多&nbsp;内容</p></body></html>`
	got := collapseSpace(htmlToText(raw))
	if want := "标题 正文 & 更多 内容"; got != want {
		t.Fatalf("得到 %q，期望 %q", got, want)
	}
	if containsAny(got, "alert", "color:red") {
		t.Fatalf("脚本/样式没被清掉：%q", got)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) && (indexOf(s, sub) >= 0) {
			return true
		}
	}
	return false
}

// --- T34：搜索 origin 窄口 与 代理端点放行 ---

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// 只有配置的那个 origin（scheme+host+port）放行；同网段另一个端口仍拒。
func TestOriginGuardAllowsOnlyTheConfiguredOrigin(t *testing.T) {
	configured := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer configured.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("other"))
	}))
	defer other.Close()

	client, err := newOriginGuardClient(2*time.Second, mustParseURL(t, configured.URL))
	if err != nil {
		t.Fatalf("origin client: %v", err)
	}
	resp, err := client.Get(configured.URL + "/search?format=json&q=x")
	if err != nil {
		t.Fatalf("配置的 origin 应可访问：%v", err)
	}
	_ = resp.Body.Close()

	if resp, err := client.Get(other.URL + "/"); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("同网段另一个端口（%s）必须被拒绝", other.URL)
	} else if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("应命中 ErrBlockedAddress，实际 %v", err)
	}
}

// 即便把搜索后端配成元数据地址，也必须在建立 client 阶段就拒绝。
func TestOriginGuardRefusesMetadataOrigins(t *testing.T) {
	for _, raw := range []string{
		"http://169.254.169.254:8080",
		"http://169.254.0.1:8080",
		"http://100.100.100.200/",
		"http://[fd00:ec2::254]:8080",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := newOriginGuardClient(time.Second, mustParseURL(t, raw)); !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("%s 必须被拒绝，实际 %v", raw, err)
			}
		})
	}
}

// 同 origin 重定向可跟随；跨到另一个私网 origin 必须拒绝。
func TestOriginGuardRedirectPolicy(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("other"))
	}))
	defer other.Close()
	otherURL := other.URL

	mux := http.NewServeMux()
	mux.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("final"))
	})
	mux.HandleFunc("/cross", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherURL+"/steal", http.StatusFound)
	})
	home := httptest.NewServer(mux)
	defer home.Close()

	client, err := newOriginGuardClient(2*time.Second, mustParseURL(t, home.URL))
	if err != nil {
		t.Fatalf("origin client: %v", err)
	}
	resp, err := client.Get(home.URL + "/same")
	if err != nil {
		t.Fatalf("同 origin 重定向应跟随：%v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "final") {
		t.Fatalf("重定向结果不对：%q", body)
	}

	if resp, err := client.Get(home.URL + "/cross"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("重定向到另一个私网 origin 必须被拒绝")
	} else if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("应命中 ErrBlockedAddress，实际 %v", err)
	}
}

// 私网代理端点必须被 dial 守卫放行（M2 的回归）。
func TestGuardAllowsPrivateProxyEndpoint(t *testing.T) {
	var sawHost string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHost = r.URL.Host
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>via proxy</body></html>`))
	}))
	defer proxy.Close()
	proxyURL := mustParseURL(t, proxy.URL)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	client := newGuardClient(5*time.Second, false)
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("unexpected transport type")
	}
	// http.ProxyFromEnvironment 会缓存首次读取的环境变量，测试里直接把同一个代理
	// 端点塞给 transport（与 env 指向一致），用例就不依赖测试执行顺序。
	tr.Proxy = http.ProxyURL(proxyURL)

	resp, err := client.Get("http://93.184.216.34/")
	if err != nil {
		t.Fatalf("私网代理端点应被放行，实际 %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "via proxy") {
		t.Fatalf("请求没有经过代理：%q", body)
	}
	if sawHost != "93.184.216.34" {
		t.Fatalf("HTTP 代理应看到绝对 URI，实际 host %q", sawHost)
	}
}

// 配置了代理也不能访问非代理的私有目标（pre-flight + dial 双重拦截）。
func TestGuardStillBlocksNonProxyPrivateTargets(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer target.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)

	// 1) tool 层：pre-flight 直接拒绝私有目标。
	cfg := WebConfig{Enabled: true, Timeout: 2 * time.Second}
	args, _ := json.Marshal(map[string]string{"url": target.URL})
	if _, err := NewWebFetch(staticWeb(cfg)).Run(context.Background(), args, &Env{}); err == nil {
		t.Fatal("配置了代理时，非代理的私有目标仍必须被拒绝")
	}

	// 2) dial 层：即便绕过 pre-flight，守卫也不放行。
	client := newGuardClient(2*time.Second, false)
	tr := client.Transport.(*http.Transport)
	tr.Proxy = nil // 强制直连，确保失败来自地址守卫而不是代理
	if resp, err := client.Get(target.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("guard 不该放行私有目标")
	} else if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("应命中 ErrBlockedAddress，实际 %v", err)
	}
}
