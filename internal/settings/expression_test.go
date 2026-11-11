package settings

import (
	"context"
	"encoding/json"
	"testing"
)

// 表达方式库的默认值必须是「框架在、行为不变」：总开关默认关，
// 学习 / 审核默认开（总开关关着时不生效），档位默认 fast。
func TestExpressionDefaults(t *testing.T) {
	m, _ := newManager(t, false)
	got := m.Get()
	if got.ExpressionEnabled {
		t.Fatal("expression_enabled must default to false")
	}
	if got.ExpressionMode != "fast" {
		t.Fatalf("expression_mode default = %q, want fast", got.ExpressionMode)
	}
	if got.ExpressionEmbeddingModel != "" {
		t.Fatalf("expression_embedding_model default = %q, want empty (reuse main model)", got.ExpressionEmbeddingModel)
	}
	if got.ExpressionRecallLimit != 12 {
		t.Fatalf("expression_recall_limit default = %d, want 12", got.ExpressionRecallLimit)
	}
	if got.ExpressionInjectMax != 3 {
		t.Fatalf("expression_inject_max default = %d, want 3", got.ExpressionInjectMax)
	}
	if !got.ExpressionLearnEnabled {
		t.Fatal("expression_learn_enabled must default to true")
	}
	if got.ExpressionLearnEvery != 20 {
		t.Fatalf("expression_learn_every default = %d, want 20", got.ExpressionLearnEvery)
	}
	if !got.ExpressionReviewEnabled {
		t.Fatal("expression_review_enabled must default to true")
	}
	if got.ExpressionShareGlobal {
		t.Fatal("expression_share_global must default to false")
	}
}

func TestExpressionKeysApplyAndValidate(t *testing.T) {
	m, _ := newManager(t, false)
	ctx := context.Background()

	good := map[string]json.RawMessage{
		"expression_enabled":         json.RawMessage("true"),
		"expression_mode":            json.RawMessage("\"precise\""),
		"expression_embedding_model": json.RawMessage("\"  text-embedding-3-small  \""),
		"expression_recall_limit":    json.RawMessage("50"),
		"expression_inject_max":      json.RawMessage("5"),
		"expression_learn_enabled":   json.RawMessage("false"),
		"expression_learn_every":     json.RawMessage("1"),
		"expression_review_enabled":  json.RawMessage("false"),
		"expression_share_global":    json.RawMessage("true"),
	}
	got, _, err := m.Apply(ctx, good)
	if err != nil {
		t.Fatalf("valid expression settings rejected: %v", err)
	}
	if !got.ExpressionEnabled || got.ExpressionMode != "precise" {
		t.Fatalf("expression switch / mode not applied: %+v", got)
	}
	if got.ExpressionEmbeddingModel != "text-embedding-3-small" {
		t.Fatalf("expression_embedding_model must be trimmed, got %q", got.ExpressionEmbeddingModel)
	}
	if got.ExpressionRecallLimit != 50 || got.ExpressionInjectMax != 5 {
		t.Fatalf("expression limits not applied: %+v", got)
	}
	if got.ExpressionLearnEnabled || got.ExpressionReviewEnabled || !got.ExpressionShareGlobal {
		t.Fatalf("expression booleans not applied: %+v", got)
	}
	if got.ExpressionLearnEvery != 1 {
		t.Fatalf("expression_learn_every not applied: %+v", got)
	}

	bad := []map[string]json.RawMessage{
		{"expression_mode": json.RawMessage("\"sometimes\"")},
		{"expression_recall_limit": json.RawMessage("3")},
		{"expression_recall_limit": json.RawMessage("51")},
		{"expression_inject_max": json.RawMessage("0")},
		{"expression_inject_max": json.RawMessage("6")},
		{"expression_learn_every": json.RawMessage("0")},
		{"expression_learn_every": json.RawMessage("101")},
		{"expression_enabled": json.RawMessage("\"yes\"")},
	}
	for _, patch := range bad {
		if _, _, err := m.Apply(ctx, patch); err == nil {
			t.Fatalf("invalid expression patch accepted: %v", patch)
		}
	}

	// 大小写与空白由 applyKey 收敛，界面上选出来的档位后端都必须接受。
	if _, _, err := m.Apply(ctx, mustPatch(t, `{"expression_mode":" FAST "}`)); err != nil {
		t.Fatalf("expression_mode must be trimmed and lower-cased before validation: %v", err)
	}

	// 边界值本身必须被接受：4 / 1 / 1 是合法下界。
	if _, _, err := m.Apply(ctx, mustPatch(t, `{"expression_recall_limit":4,"expression_inject_max":1,"expression_learn_every":1}`)); err != nil {
		t.Fatalf("expression lower bounds must be accepted: %v", err)
	}
}

// 空档位（老数据库）要收敛到 fast，不能报错把整份配置卡死。
func TestExpressionModeNormalisesEmpty(t *testing.T) {
	doc := Defaults()
	doc.ExpressionMode = ""
	normalize(&doc)
	if doc.ExpressionMode != "fast" {
		t.Fatalf("empty expression_mode must normalise to fast, got %q", doc.ExpressionMode)
	}
	if err := Validate(&doc); err != nil {
		t.Fatalf("normalised document must validate: %v", err)
	}
}

// 表达方式键必须出现在配置键清单（golden）里 —— 前端设置页照着它长控件。
func TestExpressionKeysAreKnown(t *testing.T) {
	known := map[string]bool{}
	for _, key := range KnownKeys() {
		known[key] = true
	}
	for _, key := range []string{
		"expression_enabled",
		"expression_mode",
		"expression_embedding_model",
		"expression_recall_limit",
		"expression_inject_max",
		"expression_learn_enabled",
		"expression_learn_every",
		"expression_review_enabled",
		"expression_share_global",
	} {
		if !known[key] {
			t.Fatalf("config key manifest is missing %q", key)
		}
	}
}
