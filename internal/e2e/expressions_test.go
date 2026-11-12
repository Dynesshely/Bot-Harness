package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/store"
)

// 表达方式库：开启后，本群的表达应出现在主模型的 system prompt 里；关闭时逐字节不变。
func TestExpressionInjectedIntoSystemPrompt(t *testing.T) {
	h := newHarness(t)
	if _, err := h.st.AddExpression(context.Background(), store.ExpressionInput{
		GroupOpenID: "G1", Situation: "有人问你吃了吗", Style: "刚吃完，你呢", Enabled: true,
	}); err != nil {
		t.Fatalf("seed expression: %v", err)
	}
	h.apply(t, `{"expression_enabled":true,"expression_mode":"fast","expression_inject_max":2}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("EXP1", "G1", "U1", "小明", "你吃了吗"))
	h.waitForSent(t, 1)
	prompt := h.systemPrompt(t)
	if !strings.Contains(prompt.text, "刚吃完，你呢") {
		t.Fatalf("expression missing from system prompt: %q", prompt.text)
	}
}

func TestExpressionOffKeepsPromptUnchanged(t *testing.T) {
	h := newHarness(t)
	if _, err := h.st.AddExpression(context.Background(), store.ExpressionInput{
		GroupOpenID: "G1", Situation: "有人问你吃了吗", Style: "刚吃完，你呢", Enabled: true,
	}); err != nil {
		t.Fatalf("seed expression: %v", err)
	}
	h.apply(t, `{"expression_enabled":false}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("EXP2", "G1", "U1", "小明", "你吃了吗"))
	h.waitForSent(t, 1)
	if strings.Contains(h.systemPrompt(t).text, "刚吃完，你呢") {
		t.Fatal("disabled expression should not be injected")
	}
}
