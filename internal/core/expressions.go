package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// 表达方式库（对齐 MaiBot 的 expression learner / selector）。
//
// 两段式：
//  1. 学习：每 N 轮从真实对话里抽「什么情境 -> 什么说法」的候选，异步入库，不阻塞回复；
//  2. 召回：回复前按 fast（加权随机）或 precise（embedding 余弦 + 小模型语境筛选）
//     取 0–inject_max 条，注入 systemPrompt；没有合适的就是空，不硬塞。
//
// 隐私边界：只使用**本群**的对话与表达（全局池需显式 share_global），
// 不把私聊内容带进群聊。

const (
	expressionPromptTimeout = 15 * time.Second
	expressionSelectTimeout = 2 * time.Second
	expressionMaxCandidates = 5
	expressionContextRunes  = 4000
)

// expressionCandidate is one row plus its parsed vector (precise mode only).
type expressionCandidate struct {
	store.Expression
	vec        []float32
	similarity float64
}

// expressionBlock returns the prompt block for this turn, or "" when the
// feature is off / no suitable expression exists. It never blocks the reply for
// more than expressionSelectTimeout, and every failure degrades to no block.
func (d *Dispatcher) expressionBlock(ctx context.Context, in *Inbound, s settings.Settings, userText string) string {
	if !s.ExpressionEnabled || s.ExpressionMode == "off" || d.st == nil || strings.TrimSpace(userText) == "" {
		return ""
	}
	evalCtx, cancel := context.WithTimeout(ctx, expressionSelectTimeout)
	defer cancel()

	rows, err := d.st.ExpressionsForGroup(evalCtx, in.GroupOpenID, s.ExpressionRecallLimit)
	if err != nil || len(rows) == 0 {
		if err != nil {
			d.log.Debug("expression recall failed", "component", "expression", "error", err.Error())
		}
		return ""
	}
	candidates := make([]expressionCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, expressionCandidate{Expression: row})
	}

	if s.ExpressionMode == "precise" {
		candidates = d.rankExpressionsByEmbedding(evalCtx, s, candidates, userText)
		if len(candidates) == 0 {
			return ""
		}
	}

	selected := d.selectExpressions(evalCtx, s, candidates, userText)
	if len(selected) == 0 {
		return ""
	}
	return formatExpressionBlock(selected)
}

// rankExpressionsByEmbedding sorts candidates by cosine similarity to the
// current user text. Candidates without a vector are embedded in one batch;
// any embedding failure leaves the original (fast) order and is logged.
func (d *Dispatcher) rankExpressionsByEmbedding(ctx context.Context, s settings.Settings,
	candidates []expressionCandidate, userText string) []expressionCandidate {
	if d.ai == nil {
		return candidates
	}
	model := strings.TrimSpace(s.ExpressionEmbeddingModel)
	if model == "" {
		model = s.Model
	}
	if model == "" {
		return candidates
	}

	contexts, err := d.ai.Embeddings(ctx, model, []string{userText})
	if err != nil || len(contexts) != 1 {
		d.log.Debug("expression embedding failed; using fast recall", "component", "expression", "error", errString(err))
		return candidates
	}

	missing := make([]int, 0, len(candidates))
	missingTexts := make([]string, 0, len(candidates))
	for i := range candidates {
		if len(candidates[i].vec) == 0 && candidates[i].Embedding != "" {
			_ = json.Unmarshal([]byte(candidates[i].Embedding), &candidates[i].vec)
		}
		if len(candidates[i].vec) == 0 {
			missing = append(missing, i)
			missingTexts = append(missingTexts, candidates[i].Situation+" "+candidates[i].Style)
		}
	}
	if len(missing) > 0 {
		if len(missingTexts) > ai.MaxEmbeddingInputs {
			missingTexts = missingTexts[:ai.MaxEmbeddingInputs]
			missing = missing[:ai.MaxEmbeddingInputs]
		}
		vecs, eerr := d.ai.Embeddings(ctx, model, missingTexts)
		if eerr != nil || len(vecs) != len(missing) {
			d.log.Debug("expression candidate embedding failed; using fast recall", "component", "expression", "error", errString(eerr))
			return candidates
		}
		for j, idx := range missing {
			candidates[idx].vec = vecs[j]
			if raw, merr := json.Marshal(vecs[j]); merr == nil {
				_ = d.st.UpdateExpressionEmbedding(ctx, candidates[idx].ID, string(raw), len(vecs[j]))
			}
		}
	}

	for i := range candidates {
		candidates[i].similarity = ai.Cosine(contexts[0], candidates[i].vec)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].similarity > candidates[j].similarity })
	// Drop the long tail: only the top recall_limit need to reach the selector.
	if len(candidates) > s.ExpressionRecallLimit {
		candidates = candidates[:s.ExpressionRecallLimit]
	}
	return candidates
}

// selectExpressions picks at most inject_max rows. In precise mode a cheap model
// call chooses by meaning; any error falls back to the first N candidates.
func (d *Dispatcher) selectExpressions(ctx context.Context, s settings.Settings,
	candidates []expressionCandidate, userText string) []store.Expression {
	max := s.ExpressionInjectMax
	if max < 1 {
		max = 3
	}
	if max > len(candidates) {
		max = len(candidates)
	}
	if len(candidates) <= max || d.ai == nil || strings.TrimSpace(s.Model) == "" {
		return fastSelectExpressions(candidates, max, s.ExpressionMode, rand.Float64)
	}

	var b strings.Builder
	b.WriteString("根据当前聊天语境，从下列表达方式里选择 0–")
	b.WriteString(fmt.Sprintf("%d", max))
	b.WriteString(" 条最自然、最贴合的。不要为了用而用；没有合适的就返回空数组。严格只输出 JSON：{\"selected_ids\":[…]}\n\n当前语境：\n")
	b.WriteString(truncateRunes(userText, 1000))
	b.WriteString("\n\n候选：")
	for _, c := range candidates {
		fmt.Fprintf(&b, "\n- id=%d 当%s时：%s", c.ID, c.Situation, c.Style)
	}

	callCtx, cancel := context.WithTimeout(ctx, expressionSelectTimeout)
	defer cancel()
	res, err := d.backgroundChat(callCtx, ai.ChatRequest{
		Model: s.Model, Messages: []ai.Message{{Role: ai.RoleUser, Content: b.String()}},
		Temperature: 0, MaxTokens: 120, Stream: false,
	})
	if err != nil || res == nil {
		return firstExpressions(candidates, max)
	}
	var parsed struct {
		SelectedIDs []int64 `json:"selected_ids"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(res.Text)), &parsed); err != nil {
		return firstExpressions(candidates, max)
	}
	byID := map[int64]store.Expression{}
	for _, c := range candidates {
		byID[c.ID] = c.Expression
	}
	out := make([]store.Expression, 0, max)
	seen := map[int64]bool{}
	for _, id := range parsed.SelectedIDs {
		if e, ok := byID[id]; ok && !seen[id] && len(out) < max {
			seen[id] = true
			out = append(out, e)
		}
	}
	return out
}

// similarity lives on the candidate in precise mode; declared separately to keep
// store.Expression free of ranking-only fields.
func (c *expressionCandidate) setSimilarity(v float64) { c.similarity = v }

// fastSelectExpressions picks without a model call. Fast mode samples by
// weight (use_count), so a commonly used phrase is preferred but the bot does
// not repeat the same three expressions forever; precise fallback keeps the
// cosine order.
func fastSelectExpressions(candidates []expressionCandidate, n int, mode string, rnd func() float64) []store.Expression {
	if n > len(candidates) {
		n = len(candidates)
	}
	if n <= 0 {
		return nil
	}
	indices := make([]int, n)
	if mode == "precise" || rnd == nil {
		for i := 0; i < n; i++ {
			indices[i] = i
		}
	} else {
		indices = splitWeightedSample(candidates, n, rnd)
	}
	out := make([]store.Expression, 0, n)
	for _, i := range indices {
		out = append(out, candidates[i].Expression)
	}
	return out
}

func firstExpressions(candidates []expressionCandidate, n int) []store.Expression {
	if n > len(candidates) {
		n = len(candidates)
	}
	out := make([]store.Expression, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, candidates[i].Expression)
	}
	return out
}

func formatExpressionBlock(rows []store.Expression) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("【可以自然借用的表达方式（视情况使用，不要每条都用、不要生硬套用）】")
	for _, e := range rows {
		situation := strings.TrimSpace(e.Situation)
		style := strings.TrimSpace(e.Style)
		if situation == "" || style == "" {
			continue
		}
		fmt.Fprintf(&b, "\n- 当%s时，可以用「%s」这类说法", situation, style)
	}
	if b.Len() == len("【可以自然借用的表达方式（视情况使用，不要每条都用、不要生硬套用）】") {
		return ""
	}
	return b.String()
}

// learnExpressions runs one background extraction if the conversation's turn
// counter says it is due. Failures are best-effort and never reach the user.
func (d *Dispatcher) learnExpressions(ctx context.Context, conversationID string, in *Inbound,
	s settings.Settings, userText, reply string) {
	if !s.ExpressionEnabled || !s.ExpressionLearnEnabled || s.ExpressionMode == "off" || d.st == nil || d.ai == nil {
		return
	}
	if strings.TrimSpace(userText) == "" || strings.TrimSpace(reply) == "" {
		return
	}
	every := s.ExpressionLearnEvery
	if every < 1 {
		every = 20
	}
	if !d.expressionTurnDue(conversationID, every) {
		return
	}

	go func() {
		runCtx, cancel := context.WithTimeout(d.base(), expressionPromptTimeout)
		defer cancel()
		history, err := d.st.RecentMessages(runCtx, conversationID, 20, true)
		if err != nil {
			return
		}
		var b strings.Builder
		for _, m := range history {
			role := "群友"
			if m.Role == "assistant" {
				role = "你"
			}
			fmt.Fprintf(&b, "%s：%s\n", role, truncateRunes(m.Content, 300))
		}
		fmt.Fprintf(&b, "群友：%s\n你：%s", truncateRunes(userText, 1200), truncateRunes(reply, 1200))
		input := truncateRunes(b.String(), expressionContextRunes)

		res, err := d.backgroundChat(runCtx, ai.ChatRequest{
			Model: s.Model,
			Messages: []ai.Message{{Role: ai.RoleSystem, Content: expressionLearnPrompt},
				{Role: ai.RoleUser, Content: input}},
			Temperature: 0, MaxTokens: 600, Stream: false,
		})
		if err != nil || res == nil {
			return
		}
		for _, c := range parseExpressionCandidates(res.Text) {
			group := in.GroupOpenID
			if s.ExpressionShareGlobal {
				group = ""
			}
			if _, err := d.st.AddExpression(runCtx, store.ExpressionInput{
				GroupOpenID: group, Situation: c.Situation, Style: c.Style,
				Source: "learn", Enabled: !s.ExpressionReviewEnabled,
			}); err != nil {
				d.log.Debug("failed to store expression", "component", "expression", "error", err.Error())
			}
		}
	}()
}

func (d *Dispatcher) expressionTurnDue(conversationID string, every int) bool {
	d.exprMu.Lock()
	defer d.exprMu.Unlock()
	if d.exprTurns == nil {
		d.exprTurns = map[string]int{}
	}
	d.exprTurns[conversationID]++
	return d.exprTurns[conversationID]%every == 0
}

const expressionLearnPrompt = `你是群聊表达方式的学习器。用户会给你一段真实群聊记录。
从中提取“可复用的表达方式”：在什么情境（situation）下，可以用什么说法（style）。
只提取有原文依据、自然、不像提示词模板的表达；不要把事实知识当成表达方式；不要编造。
每条 situation 与 style 都不超过 200 字，最多 5 条。没有合适的就返回空数组。
严格只输出 JSON：{"candidates":[{"situation":"适用情境","style":"表达方式或语言风格"}]}`

func parseExpressionCandidates(raw string) []store.ExpressionInput {
	var parsed struct {
		Candidates []struct {
			Situation string `json:"situation"`
			Style     string `json:"style"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &parsed); err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]store.ExpressionInput, 0, len(parsed.Candidates))
	for _, c := range parsed.Candidates {
		situation := strings.TrimSpace(c.Situation)
		style := strings.TrimSpace(c.Style)
		if situation == "" || style == "" {
			continue
		}
		if utf8RuneCount(situation) > 200 || utf8RuneCount(style) > 200 {
			continue
		}
		key := situation + "\x00" + style
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, store.ExpressionInput{Situation: situation, Style: style})
		if len(out) >= expressionMaxCandidates {
			break
		}
	}
	return out
}

// extractJSONObject pulls the first {...} span so a model that wraps JSON in a
// markdown fence still parses. The prompt asks for raw JSON; this is only a
// safety net, not a general-purpose parser.
func extractJSONObject(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		return raw[start : end+1]
	}
	return raw
}

func utf8RuneCount(s string) int { return len([]rune(s)) }

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// splitWeightedSample is used by fast mode to add variety without a model call.
// It returns indices in weighted-random order (higher use_count = more likely).
func splitWeightedSample(rows []expressionCandidate, n int, rnd func() float64) []int {
	if n > len(rows) {
		n = len(rows)
	}
	pool := make([]int, len(rows))
	for i := range pool {
		pool[i] = i
	}
	out := make([]int, 0, n)
	for len(out) < n && len(pool) > 0 {
		total := 0
		for _, i := range pool {
			total += rows[i].UseCount + 1
		}
		r := rnd() * float64(total)
		pick := pool[len(pool)-1]
		for j, i := range pool {
			r -= float64(rows[i].UseCount + 1)
			if r <= 0 {
				pick = i
				pool = append(pool[:j], pool[j+1:]...)
				break
			}
		}
		out = append(out, pick)
	}
	return out
}
