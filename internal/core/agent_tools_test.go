package core

import (
	"sort"
	"testing"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/tools"
)

// advertisedNames renders the tool defs this turn would advertise. The registry
// carries the web tools (they are registered by the dispatcher in production);
// only the fields agentTools filters on are populated.
func advertisedNames(t *testing.T, s settings.Settings, cfg settings.GroupConfig, in *Inbound) []string {
	t.Helper()
	reg := tools.New()
	reg.Register(tools.NewWebFetch(func() tools.WebConfig { return tools.WebConfig{Enabled: true} }))
	reg.Register(tools.NewWebSearch(func() tools.WebConfig { return tools.WebConfig{Enabled: true} }))
	d := &Dispatcher{tools: reg}
	defs := d.agentTools(s, cfg, in)
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Function.Name)
	}
	sort.Strings(out)
	return out
}

func hasTool(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// web_search 的完整组合矩阵（走工具名过滤，不是只测纯函数）：
// provider/base_url 四种组合 + 全局开关 + 按群 web=false。
// 工具定义是一种承诺：只有真的能搜时才下发 web_search；web_fetch 只受联网开关影响。
func TestWebSearchAdvertisementMatrix(t *testing.T) {
	cases := []struct {
		name       string
		globalWeb  bool
		groupWeb   bool
		provider   string
		baseURL    string
		wantFetch  bool
		wantSearch bool
	}{
		{
			name: "provider+base_url 齐全", globalWeb: true, groupWeb: true,
			provider: "searxng", baseURL: "https://searxng.internal.example",
			wantFetch: true, wantSearch: true,
		},
		{
			name: "缺 provider（url 有）", globalWeb: true, groupWeb: true,
			baseURL:   "https://searxng.internal.example",
			wantFetch: true, wantSearch: false,
		},
		{
			name: "缺 base_url（provider 有）", globalWeb: true, groupWeb: true,
			provider:  "searxng",
			wantFetch: true, wantSearch: false,
		},
		{
			name: "两者都空", globalWeb: true, groupWeb: true,
			wantFetch: true, wantSearch: false,
		},
		{
			name: "未知 provider + url", globalWeb: true, groupWeb: true,
			provider:  "google",
			baseURL:   "https://searxng.internal.example",
			wantFetch: true, wantSearch: false,
		},
		{
			name: "按群 web=false（齐全）", globalWeb: true, groupWeb: false,
			provider: "searxng", baseURL: "https://searxng.internal.example",
		},
		{
			name: "按群 web=false（未配置）", globalWeb: true, groupWeb: false,
		},
		{
			name: "全局 web=false（齐全）", globalWeb: false, groupWeb: true,
			provider: "searxng", baseURL: "https://searxng.internal.example",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := settings.Defaults()
			s.WebEnabled = tc.globalWeb
			s.WebSearchProvider = tc.provider
			s.WebSearchBaseURL = tc.baseURL
			cfg := settings.GroupConfig{ToolsEnabled: true, WebEnabled: tc.groupWeb}

			names := advertisedNames(t, s, cfg, &Inbound{})
			gotFetch, gotSearch := hasTool(names, "web_fetch"), hasTool(names, "web_search")
			if gotFetch != tc.wantFetch || gotSearch != tc.wantSearch {
				t.Fatalf("provider=%q base_url=%q globalWeb=%v groupWeb=%v：web_fetch=%v（want %v）求 web_search=%v（want %v）；实际工具=%v",
					tc.provider, tc.baseURL, tc.globalWeb, tc.groupWeb,
					gotFetch, tc.wantFetch, gotSearch, tc.wantSearch, names)
			}
		})
	}
}

// webSearchConfigured 是纯函数，直接钉住它的判定。
func TestWebSearchConfiguredPredicate(t *testing.T) {
	s := settings.Settings{}
	if webSearchConfigured(s) {
		t.Fatal("空配置不该算可用")
	}
	s.WebSearchProvider = "searxng"
	if webSearchConfigured(s) {
		t.Fatal("缺 base url 不算可用")
	}
	s.WebSearchBaseURL = "   "
	if webSearchConfigured(s) {
		t.Fatal("空白 base url 不算可用")
	}
	s.WebSearchBaseURL = "https://searxng.internal.example"
	if !webSearchConfigured(s) {
		t.Fatal("provider+地址齐全应算可用")
	}
	s.WebSearchProvider = ""
	if webSearchConfigured(s) {
		t.Fatal("缺 provider 不算可用")
	}
}
