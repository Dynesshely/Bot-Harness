package core

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/settings"
)

// translateSystem is deliberately imperative: both passes are translation
// only, not a second chance to rewrite the answer. Preserving emoji, line
// breaks and quote markers keeps the delivered text structurally identical.
const translateSystem = "你是专业翻译。只输出译文，不要解释、不要加前后缀、不要用代码块包裹。" +
	"保留换行、emoji、引用标记和原有语气；不增删事实、数字或链接。"

type translateResult struct {
	Text       string
	Attempted  bool
	Translated bool
	Fallback   string
	Usage      ai.Usage
	LatencyMS  int64
}

// translateRoundTrip performs Chinese → English → Chinese as two independent
// model calls. Any failure returns the original polished text: a translation
// pass is an enhancement, never a reason to drop a reply.
func (d *Dispatcher) translateRoundTrip(ctx context.Context, s settings.Settings, text string) translateResult {
	res := translateResult{Text: text}
	if strings.TrimSpace(text) == "" || !hasHan(text) {
		res.Fallback = "empty or no Chinese"
		return res
	}
	model := strings.TrimSpace(firstNonEmpty(s.Model, s.PolishModel))
	if model == "" {
		res.Fallback = "no model"
		return res
	}
	res.Attempted = true
	start := time.Now()
	res.LatencyMS = 0

	english, usage1, err := d.translateOnce(ctx, s, model, text, "英文")
	if err != nil {
		res.Fallback = "zh→en: " + err.Error()
		res.Usage = usage1
		return res
	}
	back, usage2, err := d.translateOnce(ctx, s, model, english, "简体中文")
	res.Usage = addUsage(usage1, usage2)
	if err != nil {
		res.Fallback = "en→zh: " + err.Error()
		return res
	}
	if strings.TrimSpace(back) == "" {
		res.Fallback = "en→zh: empty result"
		return res
	}
	res.Text = strings.TrimSpace(back)
	res.Translated = true
	res.LatencyMS = time.Since(start).Milliseconds()
	return res
}

func (d *Dispatcher) translateOnce(ctx context.Context, s settings.Settings, model, text, target string) (string, ai.Usage, error) {
	if d.ai == nil {
		return "", ai.Usage{}, fmt.Errorf("ai client is not configured")
	}
	timeout := 20 * time.Second
	if s.RequestTimeoutSeconds > 0 && time.Duration(s.RequestTimeoutSeconds)*time.Second < timeout {
		timeout = time.Duration(s.RequestTimeoutSeconds) * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	maxTokens := EstimateTokens(text)*2 + 64
	if maxTokens < 2048 {
		maxTokens = 2048
	}
	if s.MaxTokens > 0 && maxTokens > s.MaxTokens {
		maxTokens = s.MaxTokens
	}
	res, err := d.ai.Chat(callCtx, ai.ChatRequest{
		Model: model,
		Messages: []ai.Message{
			{Role: ai.RoleSystem, Content: translateSystem},
			{Role: ai.RoleUser, Content: "把下面内容直接翻译成" + target + "：\n\n" + text},
		},
		Temperature: 0.1,
		MaxTokens:   maxTokens,
		Stream:      false,
	})
	if err != nil {
		return "", res.Usage, err
	}
	out := strings.TrimSpace(res.Text)
	if out == "" {
		return "", res.Usage, fmt.Errorf("empty translation")
	}
	return out, res.Usage, nil
}

func addUsage(a, b ai.Usage) ai.Usage {
	return ai.Usage{
		PromptTokens:     a.PromptTokens + b.PromptTokens,
		CompletionTokens: a.CompletionTokens + b.CompletionTokens,
		TotalTokens:      a.TotalTokens + b.TotalTokens,
	}
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
