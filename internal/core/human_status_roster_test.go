package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// /human-status 的人数参数与「过去 24 小时发言者」硬限制。
//
// 名单来自 store.RecentSpeakers(conversation, now-24h, count)：命令把它作为
// 推理的硬范围（提示词、时间线、落库、渲染四处都过滤），所以这里的用例都从
// 真实 SQLite store 的 messages 构造，而不是塞一个假 store —— 24h 边界和
// 去重/排序本身由 store 的测试钉住，这里钉命令的行为。

// newHumanStatusRosterTest builds a dispatcher with a settings manager and a
// scripted model reply; the script records every request body.
func newHumanStatusRosterTest(t *testing.T, replies ...string) (*Dispatcher, *store.Store, *coreReplyScript, settings.Settings) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hs-roster.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sm, err := settings.New(context.Background(), st, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if len(replies) == 0 {
		replies = []string{coreHumanStatusVerdict}
	}
	script := &coreReplyScript{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(script.handler))
	t.Cleanup(srv.Close)
	d := &Dispatcher{
		settings: sm,
		st:       st,
		ai:       ai.NewClient(srv.URL, "sk-test", srv.Client()),
		aiSem:    newDynamicSem(4),
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	s := sm.Effective()
	s.Model = "test-model"
	s.HumanStatusTTLMinutes = 120
	s.HumanStatusTimelineMessages = 120
	s.MarkdownEnabled = false
	return d, st, script, s
}

// seedHumanStatusMessageAt stores one message and rewrites its created_at so the
// 24h window can be tested at an exact instant.
func seedHumanStatusMessageAt(t *testing.T, st *store.Store, conv, name, openID, content string, at time.Time) {
	t.Helper()
	id, err := st.AppendMessageID(context.Background(), store.MessageInput{
		ConversationID: conv, Role: "user", Kind: store.KindDialogue,
		Content: content, Speaker: name, SpeakerOpenID: openID,
	})
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if _, err := st.DB().ExecContext(context.Background(),
		"UPDATE messages SET created_at = ? WHERE id = ?", at.Format(time.RFC3339), id); err != nil {
		t.Fatalf("backdate message: %v", err)
	}
}

// humanStatusVerdictJSON renders a valid verdict array for the given members, so
// a test can assert which of them actually got stored/rendered.
func humanStatusVerdictJSON(members ...[2]string) string {
	items := make([]map[string]any, 0, len(members))
	for _, m := range members {
		items = append(items, map[string]any{
			"member_openid": m[0], "display_name": m[1], "status": "在线",
			"confidence": 0.8, "evidence": "最近发过言",
		})
	}
	raw, _ := json.Marshal(items)
	return string(raw)
}

func promptContent(body map[string]any, role string) string {
	msgs, _ := body["messages"].([]any)
	var b strings.Builder
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["role"] != role {
			continue
		}
		text, _ := m["content"].(string)
		b.WriteString(text)
		b.WriteString("\n")
	}
	return b.String()
}

func storedHumanStatusIDs(t *testing.T, st *store.Store, conv string) map[string]store.HumanStatus {
	t.Helper()
	items, err := st.ListHumanStatuses(context.Background(), conv)
	if err != nil {
		t.Fatalf("list statuses: %v", err)
	}
	out := make(map[string]store.HumanStatus, len(items))
	for _, item := range items {
		out[item.MemberOpenID] = item
	}
	return out
}

func TestParseHumanStatusCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
		ok   bool
	}{
		{"default", nil, humanStatusDefaultCount, true},
		{"twenty", []string{"20"}, 20, true},
		{"one", []string{"1"}, 1, true},
		{"max", []string{"200"}, humanStatusMaxCount, true},
		{"zero", []string{"0"}, 0, false},
		{"too-large", []string{"201"}, 0, false},
		{"negative", []string{"-3"}, 0, false},
		{"not-a-number", []string{"abc"}, 0, false},
		{"float", []string{"2.5"}, 0, false},
		{"empty", []string{""}, 0, false},
		{"extra-args", []string{"10", "20"}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseHumanStatusCount(tc.args)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("parseHumanStatusCount(%q) = %d, %v; want %d, %v", tc.args, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// 非法参数回用法，且不调用模型。
func TestRunHumanStatusInvalidCountReturnsUsage(t *testing.T) {
	d, _, script, s := newHumanStatusRosterTest(t)
	in := &Inbound{Scene: "group", GroupOpenID: "G1", MemberOpenID: "U1", MemberName: "小明"}

	out, err := runHumanStatus(context.Background(), d, in, s, []string{"abc"})
	if err != nil {
		t.Fatalf("runHumanStatus: %v", err)
	}
	if out.Text != humanStatusUsageText() {
		t.Fatalf("reply = %q, want the usage text", out.Text)
	}
	if !strings.Contains(out.Text, "/human-status [人数]") {
		t.Fatalf("usage must name the argument: %q", out.Text)
	}
	if out.Image != nil {
		t.Fatal("非法参数不应出图")
	}
	if script.calls() != 0 {
		t.Fatalf("非法参数不应调用模型，calls = %d", script.calls())
	}
	t.Logf("非法参数回复:\n%s", out.Text)
}

// roster 为空时直接回提示，不调用模型。
func TestRunHumanStatusEmptyRoster(t *testing.T) {
	d, st, script, s := newHumanStatusRosterTest(t)
	in := &Inbound{Scene: "group", GroupOpenID: "G1", MemberOpenID: "U1", MemberName: "小明"}
	conv := resolveConversation(in, d.settings.Effective().ContextMode)
	// 只有 25 小时前的发言：24h 硬限制下没有可分析对象。
	seedHumanStatusMessageAt(t, st, conv.ID, "小明", "U1", "昨天的消息", time.Now().Add(-25*time.Hour))

	out, err := runHumanStatus(context.Background(), d, in, s, nil)
	if err != nil {
		t.Fatalf("runHumanStatus: %v", err)
	}
	if out.Text != "过去 24 小时没有可分析的对象。" {
		t.Fatalf("reply = %q", out.Text)
	}
	if script.calls() != 0 {
		t.Fatalf("空 roster 不应调用模型，calls = %d", script.calls())
	}
}

// 默认 10：24h 内有 12 人发言，只分析最近发言的 10 人（最近优先）。
func TestRunHumanStatusDefaultCountAnalysesNewestTen(t *testing.T) {
	members := make([][2]string, 0, 12)
	for i := 1; i <= 12; i++ {
		members = append(members, [2]string{fmt.Sprintf("U%d", i), fmt.Sprintf("成员%d", i)})
	}
	d, st, script, s := newHumanStatusRosterTest(t, humanStatusVerdictJSON(members...))
	in := &Inbound{Scene: "group", GroupOpenID: "G1", MemberOpenID: "U1", MemberName: "成员1"}
	conv := resolveConversation(in, d.settings.Effective().ContextMode)
	now := time.Now()
	for i, m := range members {
		seedHumanStatusMessageAt(t, st, conv.ID, m[1], m[0], "说句话", now.Add(-time.Duration(i+1)*time.Minute))
	}

	out, err := runHumanStatus(context.Background(), d, in, s, nil)
	if err != nil {
		t.Fatalf("runHumanStatus: %v", err)
	}
	user := promptContent(script.request(0), "user")
	for i := 1; i <= 10; i++ {
		if !strings.Contains(user, fmt.Sprintf("(U%d)", i)) {
			t.Fatalf("roster prompt is missing U%d:\n%s", i, user)
		}
	}
	if strings.Contains(user, "(U11)") || strings.Contains(user, "(U12)") {
		t.Fatalf("roster prompt must stop at the 10 newest speakers:\n%s", user)
	}
	if out.ImageAlt != "[图片] 聊天对象状态推测（10 人）" {
		t.Fatalf("ImageAlt = %q", out.ImageAlt)
	}
	if got := len(storedHumanStatusIDs(t, st, conv.ID)); got != 10 {
		t.Fatalf("stored = %d, want 10", got)
	}
	t.Logf("默认 10 人 ImageAlt = %q", out.ImageAlt)
}

// 硬限制：只统计 24h 内发言者；更早的发言不分析，store 里其它人的状态也不渲染。
func TestRunHumanStatusOnlyRecentSpeakers(t *testing.T) {
	verdict := humanStatusVerdictJSON(
		[2]string{"U1", "小明"}, [2]string{"U2", "小红"}, [2]string{"U3", "老成员"},
	)
	d, st, script, s := newHumanStatusRosterTest(t, verdict)
	in := &Inbound{Scene: "group", GroupOpenID: "G1", MemberOpenID: "U1", MemberName: "小明"}
	conv := resolveConversation(in, d.settings.Effective().ContextMode)
	now := time.Now()
	seedHumanStatusMessageAt(t, st, conv.ID, "小明", "U1", "在打游戏", now.Add(-time.Hour))
	seedHumanStatusMessageAt(t, st, conv.ID, "小红", "U2", "在赶周报", now.Add(-2*time.Hour))
	seedHumanStatusMessageAt(t, st, conv.ID, "老成员", "U3", "昨天聊过", now.Add(-25*time.Hour))

	// U3 有一条未过期的旧状态：命令必须把它过滤掉，渲染里不能出现。
	if err := st.UpsertHumanStatus(context.Background(), store.HumanStatusInput{
		Scope: store.HumanStatusGroup, ConversationID: conv.ID, GroupOpenID: "G1",
		MemberOpenID: "U3", DisplayName: "老成员", Status: "旧状态", Confidence: 0.5,
		Evidence: "旧证据", UpdatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed stale status: %v", err)
	}

	out, err := runHumanStatus(context.Background(), d, in, s, nil)
	if err != nil {
		t.Fatalf("runHumanStatus: %v", err)
	}
	if strings.Contains(out.Text, "老成员") || strings.Contains(out.Text, "旧状态") {
		t.Fatalf("out-of-window member leaked into the reply: %q", out.Text)
	}
	if !strings.Contains(out.Text, "小明") || !strings.Contains(out.Text, "小红") {
		t.Fatalf("recent speakers missing from the reply: %q", out.Text)
	}
	if out.ImageAlt != "[图片] 聊天对象状态推测（2 人）" {
		t.Fatalf("ImageAlt = %q, want the 2 recent speakers", out.ImageAlt)
	}
	if got := strings.Count(out.Text, "\n- "); got != 2 {
		t.Fatalf("reply must have one line per analysed member (%d): %q", got, out.Text)
	}

	body := script.request(0)
	system := promptContent(body, "system")
	user := promptContent(body, "user")
	if !strings.Contains(system, "只分析这些成员") || !strings.Contains(user, "不要分析列表外的人") {
		t.Fatalf("roster restriction missing from the prompts:\nsystem=%q\nuser=%q", system, user)
	}
	if !strings.Contains(user, "- 小明 (U1)") || !strings.Contains(user, "- 小红 (U2)") {
		t.Fatalf("roster list missing recent speakers:\n%s", user)
	}
	if strings.Contains(user, "- 老成员 (U3)") {
		t.Fatalf("roster list must not contain an out-of-window speaker:\n%s", user)
	}
	if strings.Contains(user, "## 成员：老成员") {
		t.Fatalf("member timeline must only contain roster members:\n%s", user)
	}
	// 全局最近消息仍保留为语境（老成员的发言出现在那里），只是不允许分析他。
	if !strings.Contains(user, "## 全局最近消息") || !strings.Contains(user, "昨天聊过") {
		t.Fatalf("global transcript should stay as context:\n%s", user)
	}

	items := storedHumanStatusIDs(t, st, conv.ID)
	if got := items["U3"].Status; got != "旧状态" {
		t.Fatalf("out-of-roster status was overwritten: U3 = %q", got)
	}
	if len(items) != 3 {
		t.Fatalf("stored = %d rows, want the 2 analysed + the untouched stale row", len(items))
	}
	t.Logf("24h 过滤样例 ImageAlt = %q\n%s", out.ImageAlt, out.Text)
}

// 传 100 时只分析 24h 内的 15 人，模型多给的名单外对象必须被丢弃。
func TestRunHumanStatusCountCapsAtSpeakersInWindow(t *testing.T) {
	all := make([][2]string, 0, 18)
	for i := 1; i <= 18; i++ {
		all = append(all, [2]string{fmt.Sprintf("U%d", i), fmt.Sprintf("成员%d", i)})
	}
	d, st, _, s := newHumanStatusRosterTest(t, humanStatusVerdictJSON(all...))
	in := &Inbound{Scene: "group", GroupOpenID: "G1", MemberOpenID: "U1", MemberName: "成员1"}
	conv := resolveConversation(in, d.settings.Effective().ContextMode)
	now := time.Now()
	for i, m := range all {
		at := now.Add(-time.Duration(i+1) * time.Minute)
		if i >= 15 {
			at = now.Add(-time.Duration(25+i) * time.Hour) // 25h+ → 硬限制外
		}
		seedHumanStatusMessageAt(t, st, conv.ID, m[1], m[0], "说句话", at)
	}

	out, err := runHumanStatus(context.Background(), d, in, s, []string{"100"})
	if err != nil {
		t.Fatalf("runHumanStatus: %v", err)
	}
	if out.ImageAlt != "[图片] 聊天对象状态推测（15 人）" {
		t.Fatalf("ImageAlt = %q, want 15 (the speakers inside 24h)", out.ImageAlt)
	}
	if got := strings.Count(out.Text, "\n- "); got != 15 {
		t.Fatalf("reply lines = %d, want 15: %q", got, out.Text)
	}
	items := storedHumanStatusIDs(t, st, conv.ID)
	if len(items) != 15 {
		t.Fatalf("stored = %d, want 15", len(items))
	}
	for i := 16; i <= 18; i++ {
		if _, ok := items[fmt.Sprintf("U%d", i)]; ok {
			t.Fatalf("out-of-window member U%d was stored", i)
		}
	}
	t.Logf("传 100 / 24h 15 人样例 ImageAlt = %q", out.ImageAlt)
}

// 后台自动触发（不带 roster）必须保持原语义：整条时间线、最多 20 人。
func TestRefreshHumanStatusWithoutRosterKeepsWholeTimeline(t *testing.T) {
	verdict := humanStatusVerdictJSON(
		[2]string{"U1", "小明"}, [2]string{"U2", "小红"}, [2]string{"U3", "阿强"},
	)
	d, st, script, s := newHumanStatusRosterTest(t, verdict)
	now := time.Now()
	for i, m := range [][2]string{{"U1", "小明"}, {"U2", "小红"}, {"U3", "阿强"}} {
		seedHumanStatusMessageAt(t, st, "conv-legacy", m[1], m[0], "随便聊聊", now.Add(-time.Duration(i+1)*time.Minute))
	}

	if err := d.RefreshHumanStatus(context.Background(), convTarget{ID: "conv-legacy"}, nil, s); err != nil {
		t.Fatalf("RefreshHumanStatus: %v", err)
	}
	body := script.request(0)
	user := promptContent(body, "user")
	system := promptContent(body, "system")
	if strings.Contains(user, "只分析下列成员") || strings.Contains(system, "只分析这些成员") {
		t.Fatal("the background path must not carry a roster restriction")
	}
	for _, m := range [][2]string{{"U1", "小明"}, {"U2", "小红"}, {"U3", "阿强"}} {
		if !strings.Contains(user, "## 成员："+m[1]+" ("+m[0]+")") {
			t.Fatalf("legacy timeline lost member %s:\n%s", m[1], user)
		}
	}
	if got := len(storedHumanStatusIDs(t, st, "conv-legacy")); got != 3 {
		t.Fatalf("stored = %d, want 3", got)
	}
}

// 模型返回了名单外的成员时也不写库（名单是硬限制，不是提示）。
func TestInferHumanStatusDropsOutOfRosterResults(t *testing.T) {
	verdict := humanStatusVerdictJSON([2]string{"U1", "小明"}, [2]string{"U9", "局外人"})
	d, st, _, s := newHumanStatusRosterTest(t, verdict)
	now := time.Now()
	seedHumanStatusMessageAt(t, st, "conv-roster", "小明", "U1", "在吗", now.Add(-time.Minute))
	seedHumanStatusMessageAt(t, st, "conv-roster", "局外人", "U9", "路过", now.Add(-25*time.Hour))

	err := d.RefreshHumanStatusFor(context.Background(), convTarget{ID: "conv-roster"}, nil, s,
		humanStatusRefreshOptions{
			Members:  []store.SpeakerActivity{{OpenID: "U1", Name: "小明", LastAt: now.Add(-time.Minute).Format(time.RFC3339), Messages: 1}},
			Restrict: true,
		})
	if err != nil {
		t.Fatalf("RefreshHumanStatusFor: %v", err)
	}
	items := storedHumanStatusIDs(t, st, "conv-roster")
	if len(items) != 1 {
		t.Fatalf("stored = %+v, want only U1", items)
	}
	if _, ok := items["U9"]; ok {
		t.Fatal("out-of-roster member must not be stored")
	}
	if items["U1"].Status != "在线" {
		t.Fatalf("U1 status = %q", items["U1"].Status)
	}
}

// 图片标题/ImageAlt 按实际分析人数，而不是请求的人数。
func TestRunHumanStatusImageAltUsesAnalyzedCount(t *testing.T) {
	verdict := humanStatusVerdictJSON([2]string{"U1", "小明"}, [2]string{"U2", "小红"})
	d, st, _, s := newHumanStatusRosterTest(t, verdict)
	in := &Inbound{Scene: "group", GroupOpenID: "G1", MemberOpenID: "U1", MemberName: "小明"}
	conv := resolveConversation(in, d.settings.Effective().ContextMode)
	now := time.Now()
	seedHumanStatusMessageAt(t, st, conv.ID, "小明", "U1", "开黑", now.Add(-time.Minute))
	seedHumanStatusMessageAt(t, st, conv.ID, "小红", "U2", "加班", now.Add(-2*time.Minute))

	out, err := runHumanStatus(context.Background(), d, in, s, []string{"20"})
	if err != nil {
		t.Fatalf("runHumanStatus: %v", err)
	}
	if out.Image == nil {
		t.Fatal("成功的 /human-status 应返回图片")
	}
	if out.ImageAlt != "[图片] 聊天对象状态推测（2 人）" {
		t.Fatalf("ImageAlt = %q", out.ImageAlt)
	}
	if !strings.Contains(out.Text, "最近聊天对象状态（2 人）：") {
		t.Fatalf("text title = %q", out.Text)
	}
	t.Logf("/human-status 20 样例 ImageAlt = %q", out.ImageAlt)
}
