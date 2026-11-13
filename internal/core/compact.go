package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// 自动 compact：长会话不静默丢弃旧消息，而是把最旧的一批压缩成滚动摘要。
//
// 原消息永远留在 messages 表里（时间线/审计不变），只有喂给模型的上下文使用
// 「摘要 + through_id 之后的消息」。压缩在回复结束后异步跑，失败只 WARN。
const (
	compactKeepMessages     = 200
	compactTriggerPercent   = 80
	compactSummaryMaxTokens = 2048
	compactInputMaxChars    = 16000
	compactTimeout          = 45 * time.Second
)

// historyLimit is how many messages one turn loads. With a large context budget
// this is the point where compaction must kick in even before tokens fill up.
func historyLimit(s settings.Settings) int {
	limit := s.ContextMaxTurns * 2
	if limit < 200 {
		limit = 200
	}
	if limit > 5000 {
		limit = 5000
	}
	return limit
}

// compactSummaryBlock renders the persisted summary for the system prompt.
func compactSummaryBlock(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	return "【较早对话的压缩摘要（由系统压缩，不是用户原话；只作背景，不确定就按原对话核对）】\n" + summary
}

// startCompaction schedules one background compaction per conversation.
// It never blocks the reply.
func (d *Dispatcher) startCompaction(conversationID string, s settings.Settings, includeUnaddressed bool) {
	if d.st == nil || d.ai == nil || conversationID == "" {
		return
	}
	d.compactMu.Lock()
	if d.compacting == nil {
		d.compacting = map[string]bool{}
	}
	if d.compacting[conversationID] {
		d.compactMu.Unlock()
		return
	}
	d.compacting[conversationID] = true
	d.compactMu.Unlock()

	go func() {
		defer func() {
			d.compactMu.Lock()
			delete(d.compacting, conversationID)
			d.compactMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(d.base(), compactTimeout)
		defer cancel()
		if err := d.compactOnce(ctx, conversationID, s, includeUnaddressed); err != nil {
			d.log.Warn("context compaction failed", "component", "compact",
				"conversation", conversationID, "error", err.Error())
		}
	}()
}

// compactOnce summarizes the oldest messages beyond the keep window. It is
// exposed separately so tests can drive it without a background race.
func (d *Dispatcher) compactOnce(ctx context.Context, conversationID string, s settings.Settings, includeUnaddressed bool) error {
	cur, err := d.st.GetConversationSummary(ctx, conversationID)
	if err != nil {
		return err
	}
	msgs, err := d.st.MessagesAfter(ctx, conversationID, cur.ThroughMessageID, historyLimit(s)*2, includeUnaddressed)
	if err != nil {
		return err
	}
	if len(msgs) <= compactKeepMessages {
		return nil
	}
	tokens := estimateMessagesTokens(msgs)
	budget := s.ContextMaxTokens - s.MaxTokens
	if budget < 256 {
		budget = 256
	}
	trigger := budget * compactTriggerPercent / 100
	// Either the token budget is filling up, or we hit the load window and must
	// advance the cursor before older messages become unreachable.
	if tokens <= trigger && len(msgs) < historyLimit(s) {
		return nil
	}

	maxInput := compactInputMaxChars - len([]rune(cur.Summary)) - 128
	if maxInput < 2000 {
		maxInput = 2000
	}
	cutLimit := len(msgs) - compactKeepMessages
	if cutLimit > len(msgs) {
		cutLimit = len(msgs)
	}
	cut, used := 0, 0
	for cut < cutLimit {
		cost := len([]rune(msgs[cut].Content))
		if cost > 1200 {
			cost = 1200
		}
		if cut > 0 && used+cost > maxInput {
			break
		}
		used += cost
		cut++
	}
	if cut < 1 {
		// Never leave the cursor stuck on an oversized first message. The
		// summarizer truncates it, but the cursor must still advance.
		cut = 1
	}
	batch := msgs[:cut]
	summary, err := d.summarizeMessages(ctx, s, cur.Summary, batch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(summary) == "" {
		return nil
	}
	next := store.ConversationSummary{
		ConversationID:   conversationID,
		Summary:          summary,
		ThroughMessageID: batch[len(batch)-1].ID,
		Tokens:           EstimateTokens(summary),
		UpdatedAt:        time.Now().Format(time.RFC3339),
	}
	if err := d.st.UpsertConversationSummary(ctx, next); err != nil {
		return err
	}
	d.log.Info("context compacted", "component", "compact", "conversation", conversationID,
		"summarized", len(batch), "kept", len(msgs)-len(batch), "summary_tokens", next.Tokens)
	return nil
}

func (d *Dispatcher) summarizeMessages(ctx context.Context, s settings.Settings, previous string, msgs []store.Message) (string, error) {
	var b strings.Builder
	b.WriteString("已有摘要（可以为空）：\n")
	b.WriteString(truncateRunes(previous, 4000))
	b.WriteString("\n\n需要并入摘要的旧对话：\n")
	for _, m := range msgs {
		role := "群友"
		if m.Role == "assistant" {
			role = "你"
		}
		if m.Speaker != "" && m.Role != "assistant" {
			role = m.Speaker
		}
		fmt.Fprintf(&b, "%s：%s\n", role, truncateRunes(m.Content, 1200))
	}
	input := truncateRunes(b.String(), compactInputMaxChars)

	if d.aiSem != nil {
		if err := d.aiSem.Acquire(ctx); err != nil {
			return "", err
		}
		defer d.aiSem.Release()
	}
	model := strings.TrimSpace(s.Model)
	if model == "" {
		return "", fmt.Errorf("no model configured")
	}
	res, err := d.ai.Chat(ctx, ai.ChatRequest{
		Model: model,
		Messages: []ai.Message{
			{Role: ai.RoleSystem, Content: fmt.Sprintf(
				"你是对话压缩器。把旧对话压缩成事实摘要，保留人物关系、偏好、承诺、未完成事项与时间线；"+
					"不要编造，不要输出分析，只输出摘要正文，控制在 %d 字以内。", compactSummaryMaxTokens*2)},
			{Role: ai.RoleUser, Content: input},
		},
		Temperature: 0, MaxTokens: compactSummaryMaxTokens, Stream: false,
	})
	if err != nil {
		return "", err
	}
	if res == nil || strings.TrimSpace(res.Text) == "" {
		return "", fmt.Errorf("empty summary")
	}
	return strings.TrimSpace(res.Text), nil
}

// backgroundChat runs a non-turn model call under the same concurrency cap as
// a user-visible turn. Without this, memory extraction / expression learning /
// proactive reflection could each exceed max_concurrent_llm on their own.
func (d *Dispatcher) backgroundChat(ctx context.Context, req ai.ChatRequest) (*ai.ChatResult, error) {
	if d.aiSem != nil {
		if err := d.aiSem.Acquire(ctx); err != nil {
			return nil, err
		}
		defer d.aiSem.Release()
	}
	return d.ai.Chat(ctx, req)
}

func estimateMessagesTokens(msgs []store.Message) int {
	total := 0
	for _, m := range msgs {
		total += EstimateTokens(m.Content) + 4
	}
	return total
}
