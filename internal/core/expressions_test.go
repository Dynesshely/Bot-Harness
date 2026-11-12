package core

import (
	"context"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

func TestParseExpressionCandidates(t *testing.T) {
	raw := `{"candidates":[{"situation":"有人问吃了吗","style":"刚吃完"},{"situation":"有人问吃了吗","style":"刚吃完"},{"situation":"","style":"x"}]}`
	got := parseExpressionCandidates(raw)
	if len(got) != 1 || got[0].Situation != "有人问吃了吗" || got[0].Style != "刚吃完" {
		t.Fatalf("parse = %+v", got)
	}
	if got := parseExpressionCandidates("not json"); got != nil {
		t.Fatalf("bad json should be nil: %+v", got)
	}
	var b strings.Builder
	b.WriteString(`{"candidates":[`)
	for i := 0; i < 8; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"situation":"情境","style":"说法`)
		b.WriteString(string(rune('A' + i)))
		b.WriteString(`"}`)
	}
	b.WriteString(`]}`)
	if got := parseExpressionCandidates(b.String()); len(got) != 5 {
		t.Fatalf("cap = %d, want 5", len(got))
	}
}

func TestFormatExpressionBlock(t *testing.T) {
	block := formatExpressionBlock([]store.Expression{
		{Situation: "有人问吃了吗", Style: "刚吃完"},
		{Situation: "", Style: "bad"},
	})
	if !strings.Contains(block, "有人问吃了吗") || !strings.Contains(block, "刚吃完") {
		t.Fatalf("block = %q", block)
	}
	if strings.Contains(block, "bad") {
		t.Fatalf("empty situation leaked: %q", block)
	}
	if formatExpressionBlock(nil) != "" {
		t.Fatal("nil rows should produce no block")
	}
}

func TestFastSelectExpressionsWeighted(t *testing.T) {
	cands := []expressionCandidate{
		{Expression: store.Expression{ID: 1, UseCount: 100}},
		{Expression: store.Expression{ID: 2, UseCount: 1}},
		{Expression: store.Expression{ID: 3, UseCount: 1}},
	}
	// rnd=0 always picks the first item in the pool, which is id 1 here; the
	// point is that the function returns n items without duplicates.
	got := fastSelectExpressions(cands, 2, "fast", func() float64 { return 0.99 })
	if len(got) != 2 || got[0].ID == got[1].ID {
		t.Fatalf("select = %+v", got)
	}
}

func TestExpressionBlockOffAndFast(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/expr.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.AddExpression(ctx, store.ExpressionInput{GroupOpenID: "G1", Situation: "有人问吃了吗", Style: "刚吃完", Enabled: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	d := &Dispatcher{st: st}
	in := &Inbound{GroupOpenID: "G1"}

	off := settings.Defaults()
	if got := d.expressionBlock(ctx, in, off, "吃了吗"); got != "" {
		t.Fatalf("disabled should be empty: %q", got)
	}

	on := settings.Defaults()
	on.ExpressionEnabled = true
	on.ExpressionMode = "fast"
	on.ExpressionInjectMax = 3
	got := d.expressionBlock(ctx, in, on, "你吃了吗")
	if !strings.Contains(got, "刚吃完") {
		t.Fatalf("fast block = %q", got)
	}
	// 其它群看不到这条表达。
	other := &Inbound{GroupOpenID: "G2"}
	if got := d.expressionBlock(ctx, other, on, "你吃了吗"); got != "" {
		t.Fatalf("other group leaked: %q", got)
	}
}
