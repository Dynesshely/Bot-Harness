package core

// T28: run_shell is switch + identity + non-empty allowlist. These tests pin
// both the per-call environment wiring (AdminCount) and the advertisement
// filter, so a non-admin never even sees the tool definition.

import (
	"sort"
	"testing"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/tools"
)

func TestToolEnvCarriesAdminCount(t *testing.T) {
	d := &Dispatcher{}
	s := settings.Defaults()
	s.AdminOpenIDs = []string{"U-ADMIN", "U-OTHER"}

	// Group member matched by member_openid.
	env := d.toolEnv(s, &Inbound{MemberOpenID: "U-ADMIN"}, convTarget{ID: "group:G1:U-ADMIN"})
	if !env.IsAdmin || env.AdminCount != 2 {
		t.Fatalf("admin env = %+v, want IsAdmin=true AdminCount=2", env)
	}
	// C2C matched by user_openid.
	env = d.toolEnv(s, &Inbound{UserOpenID: "U-OTHER"}, convTarget{ID: "c2c:U-OTHER"})
	if !env.IsAdmin || env.AdminCount != 2 {
		t.Fatalf("C2C admin env = %+v", env)
	}
	// Non-admin still learns how many admins are configured; the count is not a
	// secret and the tool uses it to tell "closed for you" from "closed for all".
	env = d.toolEnv(s, &Inbound{MemberOpenID: "U-RANDOM"}, convTarget{ID: "group:G1:U-RANDOM"})
	if env.IsAdmin || env.AdminCount != 2 {
		t.Fatalf("non-admin env = %+v", env)
	}
	// Empty allowlist: nobody is an admin.
	s.AdminOpenIDs = nil
	env = d.toolEnv(s, &Inbound{MemberOpenID: "U-ADMIN"}, convTarget{ID: "group:G1:U-ADMIN"})
	if env.IsAdmin || env.AdminCount != 0 {
		t.Fatalf("empty allowlist env = %+v, want IsAdmin=false AdminCount=0", env)
	}
}

func execPolicyToolNames(t *testing.T, s settings.Settings, cfg settings.GroupConfig, in *Inbound) []string {
	t.Helper()
	reg := tools.New()
	reg.Register(tools.NewShell(func() tools.ExecConfig {
		return tools.ExecConfig{Enabled: s.ExecEnabled}
	}))
	d := &Dispatcher{tools: reg}
	defs := d.agentTools(s, cfg, in)
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Function.Name)
	}
	sort.Strings(out)
	return out
}

func TestAgentToolsExecPolicyMatrix(t *testing.T) {
	if settings.Defaults().ExecEnabled {
		t.Fatal("exec 默认必须是关闭的（D-034）")
	}

	cases := []struct {
		name       string
		globalExec bool
		groupExec  bool
		admins     []string
		caller     string
		want       bool
	}{
		{"默认配置（exec 关，名单空）", false, false, nil, "U1", false},
		{"开启但管理员名单为空", true, true, nil, "U1", false},
		{"开启，名单有别人，调用者非管理员", true, true, []string{"U-ADMIN"}, "U1", false},
		{"开启+管理员", true, true, []string{"U-ADMIN"}, "U-ADMIN", true},
		{"按群关闭（全局开）", true, false, []string{"U-ADMIN"}, "U-ADMIN", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := settings.Defaults()
			s.ToolsEnabled = true
			s.ExecEnabled = tc.globalExec
			s.AdminOpenIDs = tc.admins
			cfg := settings.GroupConfig{ToolsEnabled: true, ExecEnabled: tc.groupExec}
			in := &Inbound{MemberOpenID: tc.caller}

			names := execPolicyToolNames(t, s, cfg, in)
			if got := hasTool(names, "run_shell"); got != tc.want {
				t.Fatalf("run_shell advertised = %v, want %v (tools=%v)", got, tc.want, names)
			}
		})
	}
}
