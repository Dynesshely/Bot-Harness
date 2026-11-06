package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// WebConfig is read on every call so settings changes apply without a restart.
type WebConfig struct {
	Enabled       bool
	SearchBackend string // "searxng" or "" (disabled)
	SearchBaseURL string
	Timeout       time.Duration
	MaxBytes      int
	// AllowPrivate disables the SSRF guard. Test-only: never set from settings.
	AllowPrivate bool
}

// NewWebFetch fetches one page and returns it as plain text.
func NewWebFetch(cfg func() WebConfig) Tool { return &webFetchTool{cfg: cfg} }

// NewWebSearch queries the configured search backend.
func NewWebSearch(cfg func() WebConfig) Tool { return &webSearchTool{cfg: cfg} }

type webFetchTool struct{ cfg func() WebConfig }

func (t *webFetchTool) Name() string  { return "web_fetch" }
func (t *webFetchTool) Group() string { return "web" }
func (t *webFetchTool) Risk() Risk    { return ReadOnly }
func (t *webFetchTool) Description() string {
	return "抓取一个网页并转成纯文本。只支持 http/https 公网地址；内网、回环与元数据地址会被拒绝。"
}

func (t *webFetchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"url":{"type":"string","description":"要抓取的完整 URL"}},"required":["url"]}`)
}

func (t *webFetchTool) Run(ctx context.Context, raw json.RawMessage, _ *Env) (Result, error) {
	cfg := t.cfg()
	if !cfg.Enabled {
		return Result{}, errors.New("联网能力已关闭")
	}
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, fmt.Errorf("参数需要 {\"url\": \"https://…\"}: %w", err)
	}
	target, err := checkURL(args.URL, cfg.AllowPrivate)
	if err != nil {
		return Result{}, err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "bot-harness/1.0 (+admin-configured fetch)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5")

	resp, err := newGuardClient(timeout, cfg.AllowPrivate).Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("抓取失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	limit := int64(cfg.MaxBytes)
	if limit <= 0 {
		limit = 512 << 10
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return Result{}, fmt.Errorf("读取响应失败: %w", err)
	}

	text := string(body)
	contentType := resp.Header.Get("Content-Type")
	switch {
	case strings.Contains(contentType, "html"):
		text = htmlToText(text)
	case strings.Contains(contentType, "json"), strings.Contains(contentType, "text/"):
		// already text
	default:
		return Result{Text: fmt.Sprintf("（不支持的内容类型 %s，未读取正文）", contentType)}, nil
	}
	text = collapseSpace(text)
	if strings.TrimSpace(text) == "" {
		text = "（页面没有可提取的正文）"
	}
	header := fmt.Sprintf("HTTP %d · %s\n来源：%s\n\n", resp.StatusCode, contentType, resp.Request.URL)
	return Result{Text: header + text}, nil
}

const (
	// defaultSearchLimit / maxSearchLimit bound the optional limit argument.
	defaultSearchLimit = 5
	maxSearchLimit     = 10
	// searchResponseMaxBytes caps how much of the search answer we read.
	searchResponseMaxBytes = 1 << 20
)

// searchResult is the subset of one SearxNG result entry that we render.
type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// searchResponse mirrors the search API JSON answer (searx/webutils.py
// get_json_response): {query, results[], answers[], corrections[], infoboxes[],
// suggestions[], unresponsive_engines[[engine, error], …]}. Only the two fields
// we use are modelled; the rest is ignored.
type searchResponse struct {
	Results             []searchResult    `json:"results"`
	UnresponsiveEngines []json.RawMessage `json:"unresponsive_engines"`
}

type webSearchTool struct{ cfg func() WebConfig }

func (t *webSearchTool) Name() string  { return "web_search" }
func (t *webSearchTool) Group() string { return "web" }
func (t *webSearchTool) Risk() Risk    { return ReadOnly }
func (t *webSearchTool) Description() string {
	return "用搜索引擎查资料，返回结果的标题、链接与摘要。可用 limit 指定条数（1-10，默认 5）。" +
		"查完可以再用 web_fetch 打开具体页面。"
}

func (t *webSearchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"query":{"type":"string","description":"搜索关键词"},
		"limit":{"type":"number","description":"返回几条结果（1-10，默认 5）"}},"required":["query"]}`)
}

func (t *webSearchTool) Run(ctx context.Context, raw json.RawMessage, _ *Env) (Result, error) {
	cfg := t.cfg()
	if !cfg.Enabled {
		return Result{}, errors.New("联网能力已关闭")
	}
	if cfg.SearchBackend != "searxng" || strings.TrimSpace(cfg.SearchBaseURL) == "" {
		return Result{}, errors.New("未配置搜索后端（需要 searxng 的 JSON 接口）")
	}
	var args struct {
		Query string   `json:"query"`
		Limit *float64 `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, fmt.Errorf(`参数需要 {"query": "…", "limit": 5}: %w`, err)
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return Result{}, errors.New("query 不能为空")
	}
	// 省略 limit = 默认 5；显式给出时夹到 [1, 10]。
	limit := defaultSearchLimit
	if args.Limit != nil {
		limit = clampSearchLimit(*args.Limit)
	}

	baseURL, err := url.Parse(strings.TrimSpace(cfg.SearchBaseURL))
	if err != nil {
		return Result{}, fmt.Errorf("搜索后端地址无效: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return Result{}, errors.New("搜索后端地址必须是 http(s) URL")
	}
	if baseURL.Host == "" {
		return Result{}, errors.New("搜索后端地址缺少主机名")
	}
	searchURL := *baseURL
	searchURL.Path = strings.TrimRight(searchURL.Path, "/") + "/search"
	searchURL.RawQuery = "format=json&q=" + url.QueryEscape(query)
	target := searchURL.String()
	// 结构性校验（scheme/用户信息/主机名）。地址策略由 client 决定：
	// 生产（AllowPrivate=false）走「只放行配置的搜索 origin」的窄口守卫；
	// 测试夹具（AllowPrivate=true）沿用全局放行。
	if _, err := checkURL(target, true); err != nil {
		return Result{}, err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	var client *http.Client
	if cfg.AllowPrivate {
		client = newGuardClient(timeout, true)
	} else {
		// SearxNG 正常是私网 sidecar：只允许这个 origin，别的私网地址照旧拒绝。
		client, err = newOriginGuardClient(timeout, baseURL)
		if err != nil {
			return Result{}, fmt.Errorf("搜索后端地址不被允许: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bot-harness/1.0 (+searxng search)")

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("搜索请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, searchResponseMaxBytes))
	if err != nil {
		return Result{}, fmt.Errorf("读取搜索响应失败: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusForbidden:
		// SearxNG 对「请求未启用的 format」直接返回 403，这也是没开 json 时最常见的
		// 表现（官方文档 dev/search_api.html）。
		return Result{}, errors.New("搜索后端拒绝了请求（HTTP 403）：SearxNG 的 settings.yml 里 " +
			"search.formats 没有启用 json（官方文档：请求未启用的 format 会返回 403），" +
			"或者该来源被限流 / WAF 拦截")
	case http.StatusTooManyRequests:
		return Result{}, errors.New("搜索后端限流（HTTP 429）：请降低请求频率，" +
			"或调整 SearxNG 的 limiter / 上游搜索引擎的配额")
	}
	if resp.StatusCode >= 300 {
		if msg := backendErrorMessage(body); msg != "" {
			return Result{}, fmt.Errorf("搜索后端返回 HTTP %d：%s", resp.StatusCode, msg)
		}
		return Result{}, fmt.Errorf("搜索后端返回 HTTP %d", resp.StatusCode)
	}

	var parsed searchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		// 典型：SearxNG 没开 JSON，返回的是 HTML 页面。
		return Result{}, fmt.Errorf("搜索后端返回的不是 JSON（Content-Type=%q，响应开头：%q）："+
			"通常是 SearxNG 未启用 JSON 格式，需要在 settings.yml 的 search.formats 里加上 json"+
			"（参考 https://docs.searxng.org/dev/search_api.html）；未启用时 SearxNG 通常直接返回 HTTP 403",
			resp.Header.Get("Content-Type"), responseSnippet(body))
	}

	results := cleanSearchResults(parsed.Results, limit)
	warning := unresponsiveWarning(parsed.UnresponsiveEngines)
	if len(results) == 0 {
		text := "没有搜到结果。"
		if warning != "" {
			text += "\n\n" + warning
		}
		return Result{Text: text}, nil
	}

	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, collapseSpace(r.Content))
	}
	text := strings.TrimRight(b.String(), "\n")
	if warning != "" {
		text += "\n\n" + warning
	}
	return Result{Text: text}, nil
}

// clampSearchLimit maps an explicitly given limit onto [1, maxSearchLimit].
// "not given" is handled by the caller (defaultSearchLimit); this only clamps.
func clampSearchLimit(v float64) int {
	if v < 1 {
		return 1
	}
	// Compare before converting: a huge JSON number must not overflow int.
	if v >= maxSearchLimit {
		return maxSearchLimit
	}
	return int(v)
}

// cleanSearchResults drops malformed entries (empty title or URL) and repeats of
// the same URL, then keeps at most limit rows.
func cleanSearchResults(in []searchResult, limit int) []searchResult {
	if limit < 1 {
		limit = defaultSearchLimit
	}
	out := make([]searchResult, 0, min(limit, len(in)))
	seen := make(map[string]struct{}, len(in))
	for _, r := range in {
		title, link := strings.TrimSpace(r.Title), strings.TrimSpace(r.URL)
		if title == "" || link == "" {
			continue
		}
		if _, dup := seen[link]; dup {
			continue
		}
		seen[link] = struct{}{}
		out = append(out, searchResult{Title: title, URL: link, Content: strings.TrimSpace(r.Content)})
		if len(out) == limit {
			break
		}
	}
	return out
}

// unresponsiveWarning renders SearxNG's unresponsive_engines as a trailing note
// (never a failure). The field is a list of [engine, error] pairs generated by
// webutils.get_translated_errors; a bare string is tolerated for other backends.
func unresponsiveWarning(items []json.RawMessage) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, raw := range items {
		var pair []string
		if err := json.Unmarshal(raw, &pair); err == nil && len(pair) > 0 {
			name := strings.TrimSpace(pair[0])
			if name == "" {
				continue
			}
			if len(pair) > 1 {
				if reason := strings.TrimSpace(pair[1]); reason != "" {
					parts = append(parts, name+"（"+reason+"）")
					continue
				}
			}
			parts = append(parts, name)
			continue
		}
		var single string
		if err := json.Unmarshal(raw, &single); err == nil && strings.TrimSpace(single) != "" {
			parts = append(parts, strings.TrimSpace(single))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "⚠ 部分搜索引擎无响应，结果可能不完整：" + strings.Join(parts, "、")
}

// backendErrorMessage pulls {"error": "…"} out of a failed JSON answer, if any.
func backendErrorMessage(body []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Error)
}

// responseSnippet is a short, whitespace-collapsed prefix of a non-JSON body for
// the diagnostic message (never the whole page).
func responseSnippet(body []byte) string {
	s := collapseSpace(string(body))
	if runes := []rune(s); len(runes) > 120 {
		return string(runes[:120]) + "…"
	}
	return s
}

// --- HTML → text -----------------------------------------------------------

// htmlToText drops script/style blocks and tags, keeping readable text. It is
// deliberately simple: the goal is a digestible page, not a faithful renderer.
func htmlToText(html string) string {
	var b strings.Builder
	b.Grow(len(html) / 2)
	lower := strings.ToLower(html)
	for i := 0; i < len(html); {
		if lower[i] == '<' {
			// Skip whole script/style bodies.
			if strings.HasPrefix(lower[i:], "<script") || strings.HasPrefix(lower[i:], "<style") {
				tag := "</script>"
				if strings.HasPrefix(lower[i:], "<style") {
					tag = "</style>"
				}
				if end := strings.Index(lower[i:], tag); end >= 0 {
					i += end + len(tag)
					continue
				}
			}
			if end := strings.IndexByte(lower[i:], '>'); end >= 0 {
				i += end + 1
				b.WriteByte(' ')
				continue
			}
			break
		}
		b.WriteByte(html[i])
		i++
	}
	return unescapeEntities(b.String())
}

func unescapeEntities(s string) string {
	replacer := strings.NewReplacer(
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", `"`, "&#39;", "'", "&#x27;", "'", "&mdash;", "—", "&hellip;", "…",
	)
	return replacer.Replace(s)
}

// collapseSpace squeezes runs of whitespace so headings and indentation do not
// burn tokens.
func collapseSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}
