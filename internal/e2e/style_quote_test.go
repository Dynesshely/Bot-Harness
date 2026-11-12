package e2e

import (
	"context"
	"testing"
)

/* ------------------------------------------------------------------ *
 * 口吻轮换 + 引用频率分档
 * ------------------------------------------------------------------ */

// 概率 1（pin roll 到 [0,1) 内）时，stage 1 的系统提示词里必须带上抽中的变体。
func TestReplyStyleVariantIsInjectedIntoTheSystemPrompt(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{
		"reply_style_variants":["用 1–2 个字回复"],
		"reply_style_variant_probability":1
	}`)
	h.dis.SetStyleRoll(func() float64 { return 0.1 })

	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ_STYLE1", "G1", "U1", "小明", "在吗"))
	h.waitForSent(t, 1)

	probe := h.systemPrompt(t)
	if !probe.contains("用 1–2 个字回复") {
		t.Fatalf("概率 1 时系统提示词应带变体，实际：%s", probe.text)
	}
}

// 概率 0 时提示词里连标记都不能出现。
func TestReplyStyleVariantSkippedAtZeroProbability(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{
		"reply_style_variants":["用 1–2 个字回复"],
		"reply_style_variant_probability":0
	}`)
	h.dis.SetStyleRoll(func() float64 { return 0 })

	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ_STYLE2", "G1", "U1", "小明", "在吗"))
	h.waitForSent(t, 1)

	if probe := h.systemPrompt(t); probe.contains("本轮说话方式") || probe.contains("1–2 个字") {
		t.Fatalf("概率 0 时系统提示词不应带变体，实际：%s", probe.text)
	}
}

// never 档位下永远不引用（roll 再小也一样）。
func TestQuoteNeverNeverQuotes(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"quote_reply_mode":"never"}`)
	h.dis.SetQuoteRoll(func() float64 { return 0 })

	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ_NQ1", "G1", "U1", "小明", "别引用我"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(sent))
	}
	if sent[0].Body.MessageRef != nil {
		t.Fatalf("never 档位不该引用，实际 %#v", sent[0].Body.MessageRef)
	}
}

// occasionally：roll=0.1 引用、roll=0.5 不引用（同一档位下由随机源决定）。
func TestQuoteOccasionallyQuotesBelowThreshold(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"quote_reply_mode":"occasionally"}`)
	h.dis.SetQuoteRoll(func() float64 { return 0.1 })

	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ_OQ1", "G1", "U1", "小明", "引用这条"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(sent))
	}
	if sent[0].Body.MessageRef == nil || sent[0].Body.MessageRef.MessageID != "REFIDX_QQ_OQ1" {
		t.Fatalf("occasionally + roll=0.1 应引用触发消息，实际 %#v", sent[0].Body.MessageRef)
	}
}

func TestQuoteOccasionallySkipsAboveThreshold(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"quote_reply_mode":"occasionally"}`)
	h.dis.SetQuoteRoll(func() float64 { return 0.5 })

	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ_OQ2", "G1", "U1", "小明", "别引用这条"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(sent))
	}
	if sent[0].Body.MessageRef != nil {
		t.Fatalf("occasionally + roll=0.5 不该引用，实际 %#v", sent[0].Body.MessageRef)
	}
}

// often：roll=0.5 仍应引用（0.5 < 0.7）。
func TestQuoteOftenQuotesAtHalf(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"quote_reply_mode":"often"}`)
	h.dis.SetQuoteRoll(func() float64 { return 0.5 })

	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ_FQ1", "G1", "U1", "小明", "经常引用"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(sent))
	}
	if sent[0].Body.MessageRef == nil || sent[0].Body.MessageRef.MessageID != "REFIDX_QQ_FQ1" {
		t.Fatalf("often + roll=0.5 应引用触发消息，实际 %#v", sent[0].Body.MessageRef)
	}
}

// 旧布尔配置仍然兼容：quote_reply=false 映射为 never（roll=0 也不引用）。
func TestLegacyQuoteReplyFalseMapsToNever(t *testing.T) {
	h := newHarness(t)
	h.apply(t, `{"quote_reply":false}`)
	h.dis.SetQuoteRoll(func() float64 { return 0 })

	h.dis.HandlePayload(context.Background(), groupAtMessage("QQ_LQ1", "G1", "U1", "小明", "老配置"))
	sent := h.waitForSent(t, 1)
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(sent))
	}
	if sent[0].Body.MessageRef != nil {
		t.Fatalf("旧 quote_reply=false 应映射为 never，实际 %#v", sent[0].Body.MessageRef)
	}
	if got := h.settings.Get().QuoteReplyMode; got != "never" {
		t.Fatalf("旧布尔写入后档位应为 never，实际 %q", got)
	}
}
