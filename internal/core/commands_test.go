package core

import (
	"context"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/settings"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		name     string
		in       *Inbound
		wantName string
		wantArgs []string
		wantOK   bool
	}{
		{
			name:     "被 @ 的命令",
			in:       &Inbound{Scene: "group", AtBot: true, Content: "/tokenstats"},
			wantName: "tokenstats",
			wantOK:   true,
		},
		{
			name:     "命令名大小写不敏感",
			in:       &Inbound{Scene: "group", AtBot: true, Content: "/TOKENSTATS ON"},
			wantName: "tokenstats",
			wantArgs: []string{"ON"},
			wantOK:   true,
		},
		{
			name:     "别名",
			in:       &Inbound{Scene: "group", AtBot: true, Content: "/Tokens off"},
			wantName: "tokens",
			wantArgs: []string{"off"},
			wantOK:   true,
		},
		{
			name:     "多余空白",
			in:       &Inbound{Scene: "group", AtBot: true, Content: "   /tokenstats   on   "},
			wantName: "tokenstats",
			wantArgs: []string{"on"},
			wantOK:   true,
		},
		{
			name:     "c2c 无需 @",
			in:       &Inbound{Scene: "c2c", Content: "/tokenstats"},
			wantName: "tokenstats",
			wantOK:   true,
		},
		{
			name:   "群里未 @ 不触发",
			in:     &Inbound{Scene: "group", AtBot: false, Content: "/tokenstats on"},
			wantOK: false,
		},
		{
			name:   "普通消息",
			in:     &Inbound{Scene: "group", AtBot: true, Content: "你好"},
			wantOK: false,
		},
		{
			name:   "全角斜杠不是命令",
			in:     &Inbound{Scene: "group", AtBot: true, Content: "／tokenstats"},
			wantOK: false,
		},
		{
			name:   "只有斜杠",
			in:     &Inbound{Scene: "group", AtBot: true, Content: "/"},
			wantOK: false,
		},
		{
			name:   "空内容",
			in:     &Inbound{Scene: "c2c", Content: "   "},
			wantOK: false,
		},
		{
			name:   "nil",
			in:     nil,
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, args, ok := parseCommand(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (name=%q args=%v)", ok, tc.wantOK, name, args)
			}
			if !tc.wantOK {
				return
			}
			if name != tc.wantName {
				t.Fatalf("name = %q, want %q", name, tc.wantName)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("args = %v, want %v", args, tc.wantArgs)
			}
			for i := range args {
				if args[i] != tc.wantArgs[i] {
					t.Fatalf("args = %v, want %v", args, tc.wantArgs)
				}
			}
		})
	}
}

func TestLookupCommandResolvesAliasesAndIgnoresUnknown(t *testing.T) {
	d := &Dispatcher{}
	d.registerBuiltinCommands()

	cmd, args, ok := d.lookupCommand(&Inbound{Scene: "group", AtBot: true, Content: "/tokens off"})
	if !ok || cmd == nil {
		t.Fatal("别名 tokens 应解析到内置命令")
	}
	if cmd.name != tokenStatsName {
		t.Fatalf("命令 = %q, want %q", cmd.name, tokenStatsName)
	}
	if len(args) != 1 || args[0] != "off" {
		t.Fatalf("args = %v, want [off]", args)
	}

	if _, _, ok := d.lookupCommand(&Inbound{Scene: "group", AtBot: true, Content: "/nope"}); ok {
		t.Fatal("未注册的命令不应命中（继续走普通消息）")
	}
	if _, _, ok := d.lookupCommand(&Inbound{Scene: "group", AtBot: false, Content: "/tokens"}); ok {
		t.Fatal("群里未 @ 的消息即使命令名合法也不应命中")
	}
}

// TestLookupCommandAcceptsPanelBareName pins the command-panel path: the
// platform strips the leading "/" from item.name and the client inserts that
// name verbatim, so a tap sends a bare "tokenstats". It must run the command
// with the command's default action, i.e. empty args.
func TestLookupCommandAcceptsPanelBareName(t *testing.T) {
	d := &Dispatcher{commands: map[string]*command{}}
	d.registerBuiltinCommands()

	cases := []struct {
		name     string
		in       *Inbound
		wantName string
		wantOK   bool
	}{
		{
			name:     "被 @ 的面板点击（无斜杠单命令名）",
			in:       &Inbound{Scene: "group", AtBot: true, Content: "tokenstats"},
			wantName: tokenStatsName,
			wantOK:   true,
		},
		{
			name:     "c2c 面板点击（无斜杠单命令名）",
			in:       &Inbound{Scene: "c2c", Content: "tokenstats"},
			wantName: tokenStatsName,
			wantOK:   true,
		},
		{
			name:     "前后空白不影响判断",
			in:       &Inbound{Scene: "c2c", Content: "  tokenstats\t"},
			wantName: tokenStatsName,
			wantOK:   true,
		},
		{
			name:     "大小写不敏感",
			in:       &Inbound{Scene: "group", AtBot: true, Content: "TokenStats"},
			wantName: tokenStatsName,
			wantOK:   true,
		},
		{
			name:     "别名 bare 也命中",
			in:       &Inbound{Scene: "group", AtBot: true, Content: "tokens"},
			wantName: tokenStatsName,
			wantOK:   true,
		},
		{
			name:     "公共命令的 bare 形态",
			in:       &Inbound{Scene: "c2c", Content: "HELP"},
			wantName: helpName,
			wantOK:   true,
		},
		{
			name:   "bare + 多个词不算命令（避免误判 help me with X）",
			in:     &Inbound{Scene: "group", AtBot: true, Content: "help me with X"},
			wantOK: false,
		},
		{
			name:   "bare 命令名带参数也不算命令",
			in:     &Inbound{Scene: "group", AtBot: true, Content: "tokenstats off"},
			wantOK: false,
		},
		{
			name:   "未注册的单词不算命令",
			in:     &Inbound{Scene: "group", AtBot: true, Content: "foo"},
			wantOK: false,
		},
		{
			name:   "群里未 @ 的 bare 命令名不触发",
			in:     &Inbound{Scene: "group", AtBot: false, Content: "tokenstats"},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, args, ok := d.lookupCommand(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				if cmd != nil || args != nil {
					t.Fatalf("未命中时不应返回命令：cmd=%v args=%v", cmd, args)
				}
				return
			}
			if cmd == nil || cmd.name != tc.wantName {
				t.Fatalf("命令 = %v, want %q", cmd, tc.wantName)
			}
			// 面板点击没有参数：命令必须按默认动作执行（tokenstats → status）。
			if len(args) != 0 {
				t.Fatalf("bare 命中时 args 必须为空，实际 %v", args)
			}
		})
	}
}

// TestParseBareCommandIsSyntacticOnly documents the split of responsibilities:
// parseCommand (slash form) and parseBareCommand (panel-tap form) decide shape
// only; registration is lookupCommand's job. The two must never both accept the
// same message, otherwise the slash form could be shadowed.
func TestParseBareCommandIsSyntacticOnly(t *testing.T) {
	cases := []struct {
		name      string
		in        *Inbound
		wantBare  bool
		wantSlash bool
	}{
		{"bare 单词", &Inbound{Scene: "c2c", Content: "foo"}, true, false},
		{"斜杠形式", &Inbound{Scene: "c2c", Content: "/foo"}, false, true},
		{"多词", &Inbound{Scene: "c2c", Content: "foo bar"}, false, false},
		{"未 @ 的群消息", &Inbound{Scene: "group", Content: "foo"}, false, false},
		{"全角斜杠不是 bare", &Inbound{Scene: "c2c", Content: "／tokenstats"}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBare := func() bool {
				_, ok := parseBareCommand(tc.in)
				return ok
			}()
			_, _, gotSlash := parseCommand(tc.in)
			if gotBare != tc.wantBare || gotSlash != tc.wantSlash {
				t.Fatalf("bare=%v slash=%v, want bare=%v slash=%v",
					gotBare, gotSlash, tc.wantBare, tc.wantSlash)
			}
			if gotBare && gotSlash {
				t.Fatal("同一条消息不能同时命中两种形态")
			}
		})
	}
}

func TestCommandAllowedUsesAdminOpenIDs(t *testing.T) {
	adminOnly := &command{name: tokenStatsName, adminOnly: true}
	open := &command{name: "public"}

	s := settings.Defaults()
	s.AdminOpenIDs = []string{"U-ADMIN"}
	empty := settings.Defaults()

	d := &Dispatcher{}
	cases := []struct {
		name string
		cmd  *command
		s    settings.Settings
		in   *Inbound
		want bool
	}{
		{"群管理员按 member_openid 命中", adminOnly, s, &Inbound{MemberOpenID: "U-ADMIN"}, true},
		{"c2c 管理员按 user_openid 命中", adminOnly, s, &Inbound{UserOpenID: "U-ADMIN"}, true},
		{"非管理员", adminOnly, s, &Inbound{MemberOpenID: "U-RANDOM"}, false},
		{"管理员名单为空", adminOnly, empty, &Inbound{MemberOpenID: "U-ADMIN"}, false},
		{"非 adminOnly 命令谁都能用", open, empty, &Inbound{MemberOpenID: "U-RANDOM"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.commandAllowed(tc.cmd, tc.s, tc.in); got != tc.want {
				t.Fatalf("commandAllowed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTokenStatsCommandArguments(t *testing.T) {
	sm, err := settings.New(context.Background(), nil, settings.Defaults())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	d := &Dispatcher{settings: sm}

	run := func(args ...string) string {
		t.Helper()
		out, err := runTokenStats(context.Background(), d, &Inbound{}, sm.Effective(), args)
		if err != nil {
			t.Fatalf("runTokenStats(%v): %v", args, err)
		}
		if out.Image != nil {
			t.Fatalf("runTokenStats(%v) 不应出图", args)
		}
		return out.Text
	}

	// 默认参数 = status；夹具默认页脚是开着的。
	if got := run(); !strings.Contains(got, "token 用量统计：已开启") || !strings.Contains(got, "/tokenstats [on|off|status]") {
		t.Fatalf("默认 status 回复 = %q", got)
	}

	// off 关闭并落进设置。
	if got := run("off"); !strings.Contains(got, "已关闭 token 用量统计") {
		t.Fatalf("off 回复 = %q", got)
	}
	if sm.Effective().ReplyFooterEnabled {
		t.Fatal("off 之后 reply_footer_enabled 应为 false")
	}
	if got := run(); !strings.Contains(got, "token 用量统计：已关闭") {
		t.Fatalf("off 之后 status 回复 = %q", got)
	}

	// on 打开；参数大小写不敏感。
	if got := run("ON"); !strings.Contains(got, "已开启 token 用量统计") {
		t.Fatalf("ON 回复 = %q", got)
	}
	if !sm.Effective().ReplyFooterEnabled {
		t.Fatal("on 之后 reply_footer_enabled 应为 true")
	}

	// 非法参数与多余参数都回用法，且不改变设置。
	if got := run("maybe"); !strings.Contains(got, "用法：") {
		t.Fatalf("非法参数回复 = %q", got)
	}
	if got := run("on", "off"); !strings.Contains(got, "用法：") {
		t.Fatalf("多余参数回复 = %q", got)
	}
	if !sm.Effective().ReplyFooterEnabled {
		t.Fatal("非法参数不应改变设置")
	}
}

func TestHelpCommandListsEveryCommandOnce(t *testing.T) {
	d := &Dispatcher{commands: map[string]*command{}}
	d.registerBuiltinCommands()
	d.registerCommand(&command{
		name:      "zeta",
		aliases:   []string{"z", "zz"},
		adminOnly: true,
		usage:     "/zeta [x]",
		desc:      "测试命令",
	})

	out, err := runHelp(context.Background(), d, &Inbound{}, settings.Defaults(), nil)
	if err != nil {
		t.Fatalf("runHelp: %v", err)
	}
	if out.Image != nil {
		t.Fatal("runHelp 不应出图")
	}
	reply := out.Text
	if !strings.HasPrefix(reply, "可用命令：") {
		t.Fatalf("help must start with 可用命令：: %q", reply)
	}

	lines := strings.Split(reply, "\n")
	if len(lines) != 6 {
		t.Fatalf("help lines = %#v", lines)
	}
	// 按 name 排序：help < human-status < tokenstats < zeta。
	if lines[1] != "/help — 查看可用命令" {
		t.Fatalf("help line = %q", lines[1])
	}
	if lines[2] != "/human-status [人数] — 分析最近24h发言者状态（仅管理员）" {
		t.Fatalf("human-status line = %q", lines[2])
	}
	if lines[3] != "/tokenstats [on|off|status] — 开关 token 用量统计（仅管理员）" {
		t.Fatalf("tokenstats line = %q", lines[3])
	}
	if lines[4] != "/zeta [x] — 测试命令（仅管理员）" {
		t.Fatalf("zeta line = %q", lines[4])
	}
	// 末行说明面板点击路径：不带斜杠的单命令名等价于默认动作。
	if !strings.Contains(lines[5], "tokenstats") || !strings.Contains(lines[5], "/tokenstats status") ||
		!strings.Contains(lines[5], "不带斜杠") {
		t.Fatalf("面板点击说明缺失或不清楚：%q", lines[5])
	}
	if strings.Contains(reply, "／") {
		t.Fatalf("help must use ASCII slashes only: %q", reply)
	}

	// 别名（tokens / z / zz）不能各自占一行。
	if got := strings.Count(reply, "开关 token 用量统计"); got != 1 {
		t.Fatalf("tokenstats appeared %d times: %q", got, reply)
	}
	for _, aliasLine := range []string{"\n/tokens ", "\n/z ", "\n/zz "} {
		if strings.Contains(reply, aliasLine) {
			t.Fatalf("aliases leaked into help: %q", reply)
		}
	}
}
