package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuiltinsCoverTheBasics(t *testing.T) {
	r := Builtins()
	names := r.Names()
	if len(names) != 2 || names[0] != "calc" || names[1] != "now" {
		t.Fatalf("注册表内容不对：%v", names)
	}
	defs := r.Definitions(nil)
	if len(defs) != 2 || defs[0].Type != "function" || defs[0].Function.Name == "" {
		t.Fatalf("schema 生成不对：%#v", defs)
	}
	for _, d := range defs {
		var schema map[string]any
		if err := json.Unmarshal(d.Function.Parameters, &schema); err != nil {
			t.Fatalf("%s 的 parameters 不是合法 JSON：%v", d.Function.Name, err)
		}
		if schema["type"] != "object" {
			t.Fatalf("%s 的 parameters 必须是 object schema", d.Function.Name)
		}
	}
}

func TestNowToolUsesTheEnvClock(t *testing.T) {
	r := Builtins()
	fixed := time.Date(2026, 10, 13, 9, 8, 7, 0, time.UTC)
	res, err := r.Call(context.Background(), "now", nil, &Env{Now: fixed}, 0)
	if err != nil {
		t.Fatalf("now: %v", err)
	}
	if !strings.Contains(res.Text, "2026-10-13 09:08:07") || !strings.Contains(res.Text, "星期二") {
		t.Fatalf("时间格式不对：%q", res.Text)
	}
}

func TestCalcEvaluatesArithmetic(t *testing.T) {
	r := Builtins()
	cases := map[string]string{
		`{"expr":"1+2*3"}`:         "7",
		`{"expr":"(1234*56+7)/8"}`: "8638.875",
		`{"expr":"10%3"}`:          "1",
		`{"expr":"-2 + 5"}`:        "3",
	}
	for raw, want := range cases {
		res, err := r.Call(context.Background(), "calc", json.RawMessage(raw), nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if !strings.HasSuffix(res.Text, "= "+want) {
			t.Fatalf("%s → %q，期望结尾 %q", raw, res.Text, want)
		}
	}
}

func TestCalcRejectsAnythingButArithmetic(t *testing.T) {
	r := Builtins()
	for _, raw := range []string{
		`{"expr":"1/0"}`,
		`{"expr":"a+b"}`,
		`{"expr":"len([]int{1})"}`,
		`{"expr":""}`,
		`not json`,
	} {
		if _, err := r.Call(context.Background(), "calc", json.RawMessage(raw), nil, 0); err == nil {
			t.Fatalf("%s 应被拒绝", raw)
		}
	}
}

func TestResultIsTruncated(t *testing.T) {
	r := Builtins()
	res, err := r.Call(context.Background(), "now", nil, &Env{}, 5)
	if err != nil {
		t.Fatalf("now: %v", err)
	}
	if !res.Truncated || !strings.Contains(res.Text, "已截断") {
		t.Fatalf("超长结果应被截断：%+v", res)
	}
}

func TestUnknownTool(t *testing.T) {
	r := Builtins()
	if _, err := r.Call(context.Background(), "nope", nil, nil, 0); err == nil {
		t.Fatal("未知工具应报错")
	}
}

func TestDefinitionsFilter(t *testing.T) {
	r := Builtins()
	only := r.Definitions(func(t Tool) bool { return t.Name() == "now" })
	if len(only) != 1 || only[0].Function.Name != "now" {
		t.Fatalf("过滤没生效：%#v", only)
	}
}
