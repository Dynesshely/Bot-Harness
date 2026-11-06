package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func staticWeb(cfg WebConfig) func() WebConfig { return func() WebConfig { return cfg } }

// AllowPrivate 只给测试用：httptest 监听在 127.0.0.1，正常路径会被 SSRF 防线拦住。
func testWebConfig() WebConfig {
	return WebConfig{Enabled: true, Timeout: 5 * time.Second, MaxBytes: 64 << 10, AllowPrivate: true}
}

func TestWebFetchConvertsHTMLToText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><h1>标题</h1><script>bad()</script><p>正文</p></body></html>`))
	}))
	defer srv.Close()

	tool := NewWebFetch(staticWeb(testWebConfig()))
	args, _ := json.Marshal(map[string]string{"url": srv.URL})
	res, err := tool.Run(context.Background(), args, &Env{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(res.Text, "标题") || !strings.Contains(res.Text, "正文") {
		t.Fatalf("正文没抓到：%q", res.Text)
	}
	if strings.Contains(res.Text, "bad()") {
		t.Fatalf("脚本没被清掉：%q", res.Text)
	}
}

func TestWebFetchRefusesInternalAddresses(t *testing.T) {
	// 这里故意用真实配置（AllowPrivate=false）：内网必须被拒。
	tool := NewWebFetch(staticWeb(WebConfig{Enabled: true, Timeout: time.Second}))
	for _, target := range []string{
		"http://127.0.0.1:8080/api/status",
		"http://169.254.169.254/latest/meta-data/",
		"file:///etc/passwd",
	} {
		args, _ := json.Marshal(map[string]string{"url": target})
		if _, err := tool.Run(context.Background(), args, &Env{}); err == nil {
			t.Fatalf("%s 必须被拒绝", target)
		}
	}
}

func TestWebSearchParsesSearxng(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("必须以 format=json 请求，实际 %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"结果一","url":"https://example.com/a","content":"摘要一"},
			{"title":"结果二","url":"https://example.com/b","content":"摘要二"}
		]}`))
	}))
	defer srv.Close()

	cfg := testWebConfig()
	cfg.SearchBackend = "searxng"
	cfg.SearchBaseURL = srv.URL
	tool := NewWebSearch(staticWeb(cfg))

	args, _ := json.Marshal(map[string]string{"query": "golang 定时器"})
	res, err := tool.Run(context.Background(), args, &Env{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotQuery != "golang 定时器" {
		t.Fatalf("查询词没带上：%q", gotQuery)
	}
	if !strings.Contains(res.Text, "结果一") || !strings.Contains(res.Text, "https://example.com/b") {
		t.Fatalf("结果解析不对：%q", res.Text)
	}
}

func TestWebSearchWithoutBackendFails(t *testing.T) {
	tool := NewWebSearch(staticWeb(WebConfig{Enabled: true, Timeout: time.Second}))
	args, _ := json.Marshal(map[string]string{"query": "x"})
	if _, err := tool.Run(context.Background(), args, &Env{}); err == nil {
		t.Fatal("没有搜索后端时应报错，而不是假装搜索")
	}
}

func TestWebToolsRespectTheSwitch(t *testing.T) {
	off := staticWeb(WebConfig{Enabled: false})
	args, _ := json.Marshal(map[string]string{"url": "https://example.com", "query": "x"})
	if _, err := NewWebFetch(off).Run(context.Background(), args, &Env{}); err == nil {
		t.Fatal("关闭后 fetch 应拒绝")
	}
	if _, err := NewWebSearch(off).Run(context.Background(), args, &Env{}); err == nil {
		t.Fatal("关闭后 search 应拒绝")
	}
}

// runWebSearch builds a searxng-configured tool around a test server and runs
// one search with the given (optional) limit.
func runWebSearch(t *testing.T, handler http.HandlerFunc, args map[string]any) (Result, error) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := testWebConfig()
	cfg.SearchBackend = "searxng"
	cfg.SearchBaseURL = srv.URL
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return NewWebSearch(staticWeb(cfg)).Run(context.Background(), raw, &Env{})
}

// searchResultsJSON builds a SearxNG-shaped answer with n unique results.
func searchResultsJSON(n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf(`{"title":"结果 %d","url":"https://example.com/r%d","content":"摘要 %d"}`, i+1, i+1, i+1))
	}
	return `{"results":[` + strings.Join(parts, ",") + `]}`
}

// limit 可选，越界要夹取；默认 5。
func TestWebSearchLimitIsClamped(t *testing.T) {
	cases := []struct {
		name string
		give bool
		in   float64
		want int
	}{
		{name: "省略 → 默认 5", give: false, want: 5},
		{name: "1", give: true, in: 1, want: 1},
		{name: "3", give: true, in: 3, want: 3},
		{name: "10（上限）", give: true, in: 10, want: 10},
		{name: "15 条 fixture + limit 11 → 恰好 10", give: true, in: 11, want: 10},
		{name: "11 夹到 10", give: true, in: 11, want: 10},
		{name: "巨大值夹到 10", give: true, in: 1e9, want: 10},
		{name: "0 夹到 1", give: true, in: 0, want: 1},
		{name: "负数夹到 1", give: true, in: -5, want: 1},
		{name: "小数向下取整", give: true, in: 2.9, want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"query": "x"}
			if tc.give {
				args["limit"] = tc.in
			}
			res, err := runWebSearch(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(searchResultsJSON(15)))
			}, args)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if got := strings.Count(res.Text, "https://example.com/r"); got != tc.want {
				t.Fatalf("limit=%v：期望 %d 条，实际 %d 条\n%s", tc.in, tc.want, got, res.Text)
			}
		})
	}
}

// 空标题/空 URL 的条目跳过；同一 URL 只保留第一条。
func TestWebSearchSkipsEmptyAndDuplicateResults(t *testing.T) {
	body := `{"results":[
		{"title":"结果一","url":"https://example.com/a","content":"A"},
		{"title":"","url":"https://example.com/empty-title","content":"x"},
		{"title":"无链接","url":"","content":"y"},
		{"title":"重复条目","url":"https://example.com/a","content":"dup"},
		{"title":"结果二","url":"https://example.com/b","content":"B"}
	]}`
	res, err := runWebSearch(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}, map[string]any{"query": "x"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := strings.Count(res.Text, "https://example.com/a"); got != 1 {
		t.Fatalf("重复 URL 应只保留一条，实际 %d 条\n%s", got, res.Text)
	}
	if !strings.Contains(res.Text, "https://example.com/b") {
		t.Fatalf("正常结果丢失：%s", res.Text)
	}
	for _, dropped := range []string{"empty-title", "无链接"} {
		if strings.Contains(res.Text, dropped) {
			t.Fatalf("空标题/空 URL 的条目应跳过，实际包含 %q：\n%s", dropped, res.Text)
		}
	}
}

// 空结果：给出明确文案，而不是假装有内容。
func TestWebSearchEmptyResults(t *testing.T) {
	res, err := runWebSearch(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}, map[string]any{"query": "x"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(res.Text, "没有搜到结果") {
		t.Fatalf("空结果应有明确文案：%q", res.Text)
	}
}

// 非 JSON（典型：SearxNG 没开 json format，返回 HTML）必须给出可操作的报错。
func TestWebSearchExplainsNonJSONResponse(t *testing.T) {
	_, err := runWebSearch(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><form id="search"><input name="q"></form></body></html>`))
	}, map[string]any{"query": "x"})
	if err == nil {
		t.Fatal("HTML 响应应报错")
	}
	msg := err.Error()
	for _, want := range []string{"不是 JSON", "search.formats", "json"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("报错缺少 %q：%s", want, msg)
		}
	}
	if strings.Contains(msg, "<form") && !strings.Contains(msg, "响应开头") {
		t.Fatalf("只应带一小段响应片段：%s", msg)
	}
}

// HTTP 状态要能区分：403（json format 未启用 / 被拒）、429（限流）、其它（带后端错误文本）。
func TestWebSearchDistinguishesHTTPFailures(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr []string
	}{
		{
			name: "403 未启用 json", status: http.StatusForbidden,
			body:    `{"error":"forbidden"}`,
			wantErr: []string{"403", "search.formats", "json"},
		},
		{
			name: "429 限流", status: http.StatusTooManyRequests,
			body:    `{"error":"too many requests"}`,
			wantErr: []string{"429", "限流"},
		},
		{
			name: "502 带上游错误文本", status: http.StatusBadGateway,
			body:    `{"error":"upstream exploded"}`,
			wantErr: []string{"502", "upstream exploded"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runWebSearch(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}, map[string]any{"query": "x"})
			if err == nil {
				t.Fatalf("HTTP %d 应报错", tc.status)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("HTTP %d 的报错缺少 %q：%s", tc.status, want, err)
				}
			}
		})
	}
}

// unresponsive_engines 作为尾部 warning 透出，不阻断结果。
func TestWebSearchSurfacesUnresponsiveEngines(t *testing.T) {
	body := `{"results":[{"title":"结果一","url":"https://example.com/a","content":"A"}],
		"unresponsive_engines":[["wikipedia","timeout"],["google","access denied"]]}`
	res, err := runWebSearch(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}, map[string]any{"query": "x"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(res.Text, "https://example.com/a") {
		t.Fatalf("结果不该被 warning 顶掉：%s", res.Text)
	}
	for _, want := range []string{"⚠", "wikipedia（timeout）", "google（access denied）"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("warning 缺少 %q：%s", want, res.Text)
		}
	}
	if !strings.Contains(res.Text, "结果可能不完整") {
		t.Fatalf("warning 应说明结果可能不完整：%s", res.Text)
	}

	// 没有任何结果但有 unresponsive：两者都要说。
	res, err = runWebSearch(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"unresponsive_engines":[["wikipedia","timeout"]]}`))
	}, map[string]any{"query": "x"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(res.Text, "没有搜到结果") || !strings.Contains(res.Text, "wikipedia（timeout）") {
		t.Fatalf("空结果 + unresponsive 应同时说明：%q", res.Text)
	}
}

// 查询词必须完整编码：空格、中文、& = ? # % 与 emoji 都不能破坏 URL。
func TestWebSearchEncodesSpecialCharacters(t *testing.T) {
	queries := []string{
		"golang 定时器",
		"中文 空格 混排",
		"a&b=c?d#e",
		"100% + 50%",
		"emoji 🐱 test",
		"site:github.com golang",
	}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			var got string
			res, err := runWebSearch(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("q")
				if r.URL.Query().Get("format") != "json" {
					t.Errorf("必须以 format=json 请求：%q", r.URL.RawQuery)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"results":[]}`))
			}, map[string]any{"query": query})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if got != query {
				t.Fatalf("查询词 round-trip 失败：got %q want %q", got, query)
			}
			if !strings.Contains(res.Text, "没有搜到结果") {
				t.Fatalf("unexpected text: %q", res.Text)
			}
		})
	}
}

// 生产接线（AllowPrivate 恒 false）：SearxNG 作为私网 sidecar 必须能搜到。
// 放行范围只有「配置的那个 origin」；其它内网地址仍由 netguard 用例覆盖。
func TestWebSearchReachesConfiguredPrivateBackend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("必须以 format=json 请求：%q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"来自私网 sidecar",` +
			`"url":"https://example.com/a","content":"ok"}]}`))
	}))
	defer srv.Close()

	// 刻意不用 testWebConfig()：它的 AllowPrivate=true 是测试捷径，这里要的是生产策略。
	cfg := WebConfig{
		Enabled: true, Timeout: 5 * time.Second, MaxBytes: 64 << 10,
		SearchBackend: "searxng", SearchBaseURL: srv.URL,
	}
	args, _ := json.Marshal(map[string]string{"query": "golang"})
	res, err := NewWebSearch(staticWeb(cfg)).Run(context.Background(), args, &Env{})
	if err != nil {
		t.Fatalf("私网搜索后端应可用（窄口 guard），实际报错：%v", err)
	}
	if !strings.Contains(res.Text, "来自私网 sidecar") {
		t.Fatalf("没有解析到结果：%q", res.Text)
	}
}

// 配置成云元数据地址：即便它是「配置的 origin」，也必须在建 client 阶段拒绝。
func TestWebSearchRefusesAMetadataBackend(t *testing.T) {
	cfg := WebConfig{
		Enabled: true, Timeout: time.Second,
		SearchBackend: "searxng", SearchBaseURL: "http://169.254.169.254:8080",
	}
	args, _ := json.Marshal(map[string]string{"query": "x"})
	if _, err := NewWebSearch(staticWeb(cfg)).Run(context.Background(), args, &Env{}); err == nil {
		t.Fatal("元数据地址作为搜索后端必须被拒绝")
	}
}
