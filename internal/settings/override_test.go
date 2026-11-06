package settings

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// groupOverrideFieldCount is the number of JSON fields on GroupOverride. Bumping
// it is a reminder to extend overrideIsEmpty (and the manifest) as well.
const groupOverrideFieldCount = 16

func boolPtr(v bool) *bool { return &v }

func strPtr(v string) *string { return &v }

// overrideIsEmpty 必须认识 GroupOverride 的每一个字段：漏一个，
// SetGroupQuoteReply(nil) 就会删掉一行其实还在生效的按群设置（D-030 回归）。
func TestOverrideIsEmptyCoversEveryGroupOverrideField(t *testing.T) {
	if got := reflect.TypeOf(GroupOverride{}).NumField(); got != groupOverrideFieldCount {
		t.Fatalf("GroupOverride 有 %d 个字段，测试清单按 %d 个写：新增字段后请同步 "+
			"overrideIsEmpty、GroupOverrideKeys 与本测试", got, groupOverrideFieldCount)
	}
	if !overrideIsEmpty(GroupOverride{GroupOpenID: "G1"}) {
		t.Fatal("只带 group_openid 的覆盖应视为空")
	}

	cases := []struct {
		name string
		ov   GroupOverride
	}{
		{"enabled", GroupOverride{Enabled: boolPtr(false)}},
		{"trigger_mode", GroupOverride{TriggerMode: TriggerAllMessages}},
		{"context_mode", GroupOverride{ContextMode: ContextShared}},
		{"context_scope", GroupOverride{ContextScope: ContextIncludeUnaddressed}},
		{"reply_to_bots", GroupOverride{ReplyToBots: boolPtr(false)}},
		{"quote_reply", GroupOverride{QuoteReply: boolPtr(false)}},
		{"tools", GroupOverride{Tools: boolPtr(false)}},
		{"proactive", GroupOverride{Proactive: strPtr("auto")}},
		{"tasks", GroupOverride{Tasks: boolPtr(false)}},
		{"memory", GroupOverride{Memory: boolPtr(false)}},
		{"humanize", GroupOverride{Humanize: strPtr("strong")}},
		{"polish", GroupOverride{Polish: boolPtr(false)}},
		{"web", GroupOverride{Web: boolPtr(false)}},
		{"exec", GroupOverride{Exec: boolPtr(true)}},
		{"rate_limit_group_qpm", GroupOverride{RateLimitGroupQPM: 11}},
	}
	if len(cases) != groupOverrideFieldCount-1 {
		t.Fatalf("字段清单不完整：%d 项，应为 %d 项（不含 group_openid）", len(cases), groupOverrideFieldCount-1)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if overrideIsEmpty(tc.ov) {
				t.Fatalf("%s 有值时 overrideIsEmpty 不能返回 true", tc.name)
			}
		})
	}
}

// 清掉「引用回复」覆盖时，其它按群设置必须原样留下 —— 这是 D-030 钉的行为。
func TestClearingQuoteReplyKeepsOtherPerGroupSettings(t *testing.T) {
	cases := []struct {
		name  string
		patch string
		want  GroupOverride
	}{
		{"tools", `"tools":false`, GroupOverride{Tools: boolPtr(false)}},
		{"proactive", `"proactive":"auto"`, GroupOverride{Proactive: strPtr("auto")}},
		{"tasks", `"tasks":false`, GroupOverride{Tasks: boolPtr(false)}},
		{"memory", `"memory":false`, GroupOverride{Memory: boolPtr(false)}},
		{"humanize", `"humanize":"strong"`, GroupOverride{Humanize: strPtr("strong")}},
		{"polish", `"polish":false`, GroupOverride{Polish: boolPtr(false)}},
		{"web", `"web":false`, GroupOverride{Web: boolPtr(false)}},
		{"exec", `"exec":true`, GroupOverride{Exec: boolPtr(true)}},
		{
			"tools plus quote_reply",
			`"tools":false,"quote_reply":true`,
			GroupOverride{Tools: boolPtr(false)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, st := newManager(t, false)
			ctx := context.Background()
			if _, _, err := m.Apply(ctx, mustPatch(t, `{"group_overrides":[{"group_openid":"G1",`+tc.patch+`}]}`)); err != nil {
				t.Fatalf("seed override: %v", err)
			}

			if err := m.SetGroupQuoteReply(ctx, "G1", nil); err != nil {
				t.Fatalf("clear quote reply: %v", err)
			}

			got := m.Get().GroupOverrides
			if len(got) != 1 {
				t.Fatalf("还有别的按群设置，覆盖行不该被删掉：%#v", got)
			}
			want := tc.want
			want.GroupOpenID = "G1"
			if !reflect.DeepEqual(got[0], want) {
				t.Fatalf("按群设置被改动了：\n got %#v\nwant %#v", got[0], want)
			}
			// 落库后也一样（SetGroupQuoteReply 走 Apply）。
			reloaded, err := New(ctx, st, Defaults())
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if again := reloaded.Get().GroupOverrides; len(again) != 1 || !reflect.DeepEqual(again[0], want) {
				t.Fatalf("重新加载后覆盖丢失：%#v", again)
			}
		})
	}
}

// 真正空的覆盖（只有引用回复这一项）在清掉后仍要整行删除，别留空记录。
func TestClearingQuoteReplyRemovesAnOtherwiseEmptyRow(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()
	if _, _, err := m.Apply(ctx, mustPatch(t, `{"group_overrides":[{"group_openid":"G1","quote_reply":true}]}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := m.SetGroupQuoteReply(ctx, "G1", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := m.Get().GroupOverrides; len(got) != 0 {
		t.Fatalf("空覆盖应被整行删除：%#v", got)
	}
}

// 按群的档位枚举必须校验：GroupConfig 会把它原样生效，非法值不能放进来。
func TestPerGroupLevelsAreValidated(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()

	bad := []string{
		`{"group_overrides":[{"group_openid":"G1","humanize":"loud"}]}`,
		`{"group_overrides":[{"group_openid":"G1","humanize":"OFF"}]}`,
		`{"group_overrides":[{"group_openid":"G1","proactive":"always"}]}`,
		`{"group_overrides":[{"group_openid":"G1","proactive":"ON"}]}`,
	}
	for _, raw := range bad {
		if _, _, err := m.Apply(ctx, mustPatch(t, raw)); err == nil {
			t.Fatalf("非法档位应被拒绝：%s", raw)
		}
	}
	if got := m.Get().GroupOverrides; len(got) != 0 {
		t.Fatalf("被拒绝的覆盖不该落库：%#v", got)
	}

	good := `{"group_overrides":[{"group_openid":"G1","humanize":"soft","proactive":"auto"}]}`
	if _, _, err := m.Apply(ctx, mustPatch(t, good)); err != nil {
		t.Fatalf("合法档位应被接受：%v", err)
	}
	cfg := m.GroupConfig("G1")
	if cfg.HumanizeLevel != "soft" || cfg.ProactiveLevel != "auto" {
		t.Fatalf("按群档位应原样生效：%+v", cfg)
	}

	// 空串 / null = 跟随全局，仍然合法。
	for _, raw := range []string{
		`{"group_overrides":[{"group_openid":"G1","humanize":"","proactive":""}]}`,
		`{"group_overrides":[{"group_openid":"G1","humanize":null,"proactive":null}]}`,
	} {
		if _, _, err := m.Apply(ctx, mustPatch(t, raw)); err != nil {
			t.Fatalf("跟随全局不该被拒绝：%s -> %v", raw, err)
		}
	}
}

// Apply 一份带全部 15 个字段的按群覆盖，读出来必须一个都不少（前端配置面完整性）。
func TestGroupOverrideRoundTripsAllFields(t *testing.T) {
	m, st := newManager(t, false)
	ctx := context.Background()
	all := `{"group_overrides":[{
		"group_openid":"G1",
		"enabled":false,
		"trigger_mode":"all_messages",
		"context_mode":"shared",
		"context_scope":"include_unaddressed",
		"reply_to_bots":false,
		"quote_reply":false,
		"tools":false,
		"proactive":"auto",
		"tasks":false,
		"memory":false,
		"humanize":"strong",
		"polish":false,
		"web":false,
		"exec":true,
		"rate_limit_group_qpm":11
	}]}`
	if _, _, err := m.Apply(ctx, mustPatch(t, all)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	check := func(t *testing.T, ov GroupOverride) {
		t.Helper()
		if ov.GroupOpenID != "G1" || ov.Enabled == nil || *ov.Enabled {
			t.Fatalf("group_openid / enabled 丢失：%#v", ov)
		}
		if ov.TriggerMode != TriggerAllMessages || ov.ContextMode != ContextShared ||
			ov.ContextScope != ContextIncludeUnaddressed {
			t.Fatalf("模式字段丢失：%#v", ov)
		}
		if ov.ReplyToBots == nil || *ov.ReplyToBots || ov.QuoteReply == nil || *ov.QuoteReply {
			t.Fatalf("回复类字段丢失：%#v", ov)
		}
		if ov.Tools == nil || *ov.Tools || ov.Tasks == nil || *ov.Tasks ||
			ov.Memory == nil || *ov.Memory || ov.Web == nil || *ov.Web || ov.Polish == nil || *ov.Polish {
			t.Fatalf("开关字段丢失：%#v", ov)
		}
		if ov.Proactive == nil || *ov.Proactive != "auto" ||
			ov.Humanize == nil || *ov.Humanize != "strong" {
			t.Fatalf("档位字段丢失：%#v", ov)
		}
		if ov.Exec == nil || !*ov.Exec {
			t.Fatalf("exec 字段丢失：%#v", ov)
		}
		if ov.RateLimitGroupQPM != 11 {
			t.Fatalf("rate_limit_group_qpm 丢失：%#v", ov)
		}
	}

	got := m.Get().GroupOverrides
	if len(got) != 1 {
		t.Fatalf("覆盖行数异常：%#v", got)
	}
	check(t, got[0])

	// 落库再读回：16 个字段都得在。
	reloaded, err := New(ctx, st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	again := reloaded.Get().GroupOverrides
	if len(again) != 1 {
		t.Fatalf("覆盖行数异常：%#v", again)
	}
	check(t, again[0])

	// 序列化后的键集合应与导出的 GroupOverride 字段清单一一对应。
	raw, err := json.Marshal(again[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, name := range GroupOverrideKeys() {
		if _, ok := keys[name]; !ok {
			t.Fatalf("序列化后缺少字段 %q：%s", name, raw)
		}
	}
	if len(keys) != groupOverrideFieldCount {
		t.Fatalf("字段数应为 %d，实际 %d：%s", groupOverrideFieldCount, len(keys), raw)
	}
}
