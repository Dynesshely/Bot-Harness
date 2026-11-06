package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// 一轮对话：限流 → 取上下文 → 调模型（必要时多模态）→ 落库 → 交付。

func (d *Dispatcher) processMessage(ctx context.Context, in *Inbound, cfg settings.GroupConfig, conv convTarget, rec *store.Record) {
	s := d.settings.Effective()

	// Rate limiting (platform-facing quotas plus our own fairness limits).
	userKey := "user:" + firstNonEmpty(in.MemberOpenID, in.UserOpenID)
	if !d.limiter.Allow(userKey, s.RateLimitUserQPM) {
		d.metrics.EventsIgnored.Add(1)
		d.finishRecord(rec, "ignored", "rate limited (user)", 0)
		return
	}
	if in.Scene == "group" && !d.limiter.Allow("group:"+in.GroupOpenID, cfg.RateLimitGroup) {
		d.metrics.EventsIgnored.Add(1)
		d.finishRecord(rec, "ignored", "rate limited (group)", 0)
		return
	}
	// E2 chatter gets an extra per-group cooldown so the bot cannot flood a group.
	if !in.AtBot && cfg.Cooldown > 0 && !d.limiter.Cooldown("cooldown:"+in.GroupOpenID, time.Duration(cfg.Cooldown)*time.Second) {
		d.metrics.EventsIgnored.Add(1)
		d.finishRecord(rec, "ignored", "group cooldown", 0)
		return
	}

	if !d.st.ConversationEnabled(ctx, conv.ID) {
		d.finishRecord(rec, "ignored", "conversation paused", 0)
		return
	}

	// Serialise per conversation so turns stay ordered. The passive reply window
	// is 5 minutes, so waiting longer than this is pointless.
	unlock, ok := d.locks.TryLock(conv.ID, 45*time.Second)
	if !ok {
		d.log.Warn("conversation busy, dropping message", "component", "dispatch", "conversation", conv.ID)
		d.replyBusy(ctx, in)
		d.finishRecord(rec, "ignored", "conversation busy", 0)
		return
	}
	defer unlock()

	start := time.Now()
	history, err := d.st.RecentMessages(ctx, conv.ID, s.ContextMaxTurns*2,
		cfg.ContextScope == settings.ContextIncludeUnaddressed)
	if err != nil {
		d.log.Warn("failed to load history", "component", "store", "error", err.Error())
	}

	// 缓存过期且消息够了就在后台刷新一次；本轮仍用现有缓存，不阻塞。
	d.refreshHumanStatusIfStale(ctx, conv, in)

	userText := in.Content
	systemPrompt := d.promptFor(s, cfg)
	if conv.Scene == SceneGroupShared {
		userText = fmt.Sprintf("%s：%s", firstNonEmpty(in.MemberName, "群成员"), in.Content)
		systemPrompt += "\n\n当前是群共享上下文：每条用户消息以「昵称：内容」的格式给出，你可以看到群里其他成员之前的发言。回复时直接回答最后一条消息即可，不要带昵称前缀。"
	}

	budget := s.ContextMaxTokens - s.MaxTokens
	if budget < 256 {
		budget = 256
	}
	// 长期记忆：把与本轮相关的事实贴进系统提示词（群记忆只在该群可见）。
	if mem := d.memoryBlock(ctx, in, s, cfg, userText); mem != "" {
		systemPrompt += "\n\n" + mem
	}
	// 未收口的承诺与聊天对象状态都是"参考信息"：只影响语气与时机，
	// 不在用户可见消息里复述。
	if block := d.commitmentBlock(ctx, in, conv, s, cfg); block != "" {
		systemPrompt += "\n\n" + block
	}
	if block := d.humanStatusBlock(ctx, conv.ID); block != "" {
		systemPrompt += "\n\n" + block
	}
	messages := BuildChatMessages(systemPrompt, history, userText, budget)

	// 本轮消息带图时改用多模态输入。只对本轮生效：历史图片会显著推高 token
	// 消耗，而绝大多数追问并不需要回看旧图。
	if parts := d.visionParts(ctx, s, in); len(parts) > 0 {
		last := len(messages) - 1
		messages[last] = ai.Message{Role: ai.RoleUser, Parts: parts}
		d.log.Info("sending an image to the model", "component", "ai",
			"images", len(parts)-1, "model", s.Model)
	}

	aiCtx, cancel := context.WithTimeout(ctx, time.Duration(s.RequestTimeoutSeconds)*time.Second)
	defer cancel()

	if err := d.aiSem.Acquire(aiCtx); err != nil {
		d.metrics.AIErrors.Add(1)
		d.stats.RecordError()
		d.replyFallback(ctx, in, "抱歉，我现在排队的人有点多，稍后再问我一次吧～", false)
		d.finishRecord(rec, "failed", "ai concurrency wait: "+err.Error(), time.Since(start).Milliseconds())
		return
	}
	defer d.aiSem.Release()

	d.metrics.AIInFlight.Add(1)
	d.metrics.AIRequests.Add(1)
	defer d.metrics.AIInFlight.Add(-1)

	// converse 负责整轮：工具循环、多模态降级重试、以及只发一次的「让我想想」。
	outcome, _ := d.converse(aiCtx, in, s, cfg, conv, messages, userText, hasVisionParts(messages))
	result, err := outcome.result, outcome.err
	if err != nil {
		d.metrics.AIErrors.Add(1)
		d.stats.RecordError()
		d.SetLastAIError(err.Error())
		if !errors.Is(aiCtx.Err(), context.DeadlineExceeded) {
			d.log.Warn("model call failed", "component", "ai", "error", err.Error())
		}
		fallback := "抱歉，我这边刚才出错了，稍后再试试～"
		if errors.Is(err, ai.ErrReasoningOnlyCompletion) {
			// The model spent its budget thinking and never wrote an answer.
			// Its chain of thought is never sent, so ask for a retry instead of
			// echoing reasoning into the group.
			fallback = "这个问题我一时没理清，换个说法再问一次？"
		}
		d.replyFallback(ctx, in, fallback, outcome.earlySent)
		d.finishRecord(rec, "failed", "ai: "+err.Error(), time.Since(start).Milliseconds())
		return
	}
	d.SetLastAIError("")

	// Once the user has been told "let me think", the answer must arrive as an
	// active message: the passive window may already be gone by then.
	forceActive := outcome.earlySent

	if strings.TrimSpace(result.Reasoning) != "" {
		// The scratchpad is dropped on purpose: it is only a signal that this
		// really was a reasoning model turn, never something to deliver.
		d.log.Debug("reasoning dropped from the reply",
			"component", "ai", "reasoning_chars", len(result.Reasoning))
	}
	d.metrics.AILatencySumMS.Add(result.LatencyMS)
	d.metrics.AILatencyCount.Add(1)
	d.stats.RecordLatency(result.LatencyMS)

	// 两阶段生成：result.Text 是主模型的**草稿**，下面这一步把它改成人设口吻。
	// 润色器只拿到「草稿 + 已解析的系统提示词」——没有历史、没有用户原文、
	// 没有工具结果。任何失败都回退草稿（newPolish 永不返回空 Text），
	// 所以这一轮不可能因为润色而失败。
	//
	// 注意：这里**不能**再 Acquire aiSem —— processMessage 在整个回合里都持有它，
	// 嵌套获取在 MaxConcurrentLLM=1 时必然死锁。
	//
	// 系统提示词用**最终那一步**实际用的那份（converse 回传）：工具循环会把
	// 记忆块追加进去，重用它才不会漏掉人设里的当轮事实。
	stagePrompt := systemPrompt
	if final := strings.TrimSpace(outcome.systemPrompt); final != "" {
		stagePrompt = final
	}
	draft := result.Text
	polish := d.newPolish(aiCtx, s, cfg, stagePrompt, draft)
	// 指标只数「真的发出去的 stage2 请求」与其中「失败回退」的次数：
	// 跳过（开关关、草稿为空、低于 polish_min_chars）既不算请求也不算失败。
	if polish.Attempted {
		d.metrics.PolishRequests.Add(1)
	}
	if polish.Attempted && !polish.Polished {
		d.metrics.PolishFallbacks.Add(1)
	}
	switch {
	case polish.Polished:
	case polish.Attempted:
		d.log.Warn("polish fell back to the draft", "component", "ai",
			"reason", polish.Fallback, "model", polish.Model,
			"draft_chars", len([]rune(draft)),
			"latency_ms", polish.LatencyMS, "conversation", conv.ID)
	default:
		d.log.Debug("polish skipped", "component", "ai",
			"reason", polish.Fallback, "draft_chars", len([]rune(draft)),
			"conversation", conv.ID)
	}
	sentText := polish.Text
	// 页脚用总 token：stage1 全程（含工具步）的 usage 加上 stage2 的 usage。
	stageUsage := outcome.usage.TotalTokens
	if stageUsage <= 0 {
		stageUsage = result.Usage.TotalTokens
	}
	totalTokens := stageUsage + polish.Usage.TotalTokens
	d.log.Info("two-stage reply", "component", "ai",
		"draft_chars", len([]rune(draft)),
		"polish_chars", len([]rune(sentText)),
		"polish_model", polish.Model,
		"polished", polish.Polished,
		"fallback", polish.Fallback,
		"polish_latency_ms", polish.LatencyMS,
		"draft_latency_ms", result.LatencyMS,
		"conversation", conv.ID)
	d.stats.RecordTokens(totalTokens)
	d.metrics.TokensUsed.Add(int64(totalTokens))

	// Persist the turn before delivering, so a delivery failure still leaves a
	// coherent context for the next question.
	_ = d.st.AppendMessage(ctx, store.MessageInput{
		ConversationID: conv.ID, Role: "user", Content: userText,
		Speaker: in.MemberName, SpeakerOpenID: in.MemberOpenID,
		QuoteIndex: in.QuoteIndex,
		Kind:       store.KindDialogue, Tokens: EstimateTokens(userText),
		// 主动额度只统计「没对机器人说话」的旁观发言；被 @ 的消息不算。
		Addressed:   in.AtBot,
		Attachments: in.Attachments,
	})
	_ = d.st.TouchConversation(ctx, conv.ID, EstimateTokens(userText))
	// 落库的 assistant 消息、记忆抽取的 reply、实际发送内容都用润色稿：
	// 上下文里必须是用户真正看到的那个版本，否则下一轮会「记成另一个人说的话」。
	assistantRowID, _ := d.st.AppendMessageID(ctx, store.MessageInput{
		ConversationID: conv.ID, Role: "assistant", Content: sentText,
		SpeakerOpenID: d.selfOpenID(conv.GroupOpenID),
		Kind:          store.KindDialogue, Tokens: EstimateTokens(sentText),
	})
	_ = d.st.TouchConversation(ctx, conv.ID, EstimateTokens(sentText))

	elapsed := time.Since(start)
	tokenCount := totalTokens
	if tokenCount <= 0 {
		// 估算：stage1 的提示词 + 草稿，再加 stage2 的（提示词 + 润色稿）。
		tokenCount = estimatePromptTokens(messages) + EstimateTokens(draft) +
			estimatePromptTokens(polish.Prompt) + EstimateTokens(sentText)
	}
	deliverText := sentText
	if s.ReplyFooterEnabled {
		deliverText = appendReplyFooter(sentText, tokenCount, elapsed)
	}

	// 引用回复：只引用触发这次回答的那条消息，按群设置决定是否启用。
	quoteIndex := ""
	if cfg.QuoteReply {
		quoteIndex = in.QuoteIndex
	}
	sendRes, sendErr := d.deliver(ctx, in, s, deliverText, forceActive, quoteIndex,
		humanizeLevel(s, cfg), start)
	latency := elapsed.Milliseconds()
	if sendErr == nil && sendRes != nil && sendRes.RefIdx != "" && assistantRowID > 0 {
		// The platform told us how to quote this message later; the row was
		// already written (before delivery), so patch it in.
		if err := d.st.SetMessageQuoteIndex(ctx, assistantRowID, sendRes.RefIdx); err != nil {
			d.log.Warn("failed to store the outbound quote index", "component", "store", "error", err.Error())
		}
	}

	outRec := &store.Record{
		Direction:      "out",
		Scene:          conv.Scene,
		ConversationID: conv.ID,
		GroupOpenID:    conv.GroupOpenID,
		// The member we answered, for the same reason as the inbound record.
		MemberOpenID: in.MemberOpenID,
		MemberName:   in.MemberName,
		Content:      deliverText,
		MsgID:        in.MsgID,
		Status:       "replied",
		LatencyMS:    latency,
	}
	if sendErr == nil && sendRes != nil {
		// Only a delivered message spends the platform's proactive quota, and
		// the msg_id is set even for active sends, so record how it went out.
		outRec.Delivery = store.DeliveryPassive
		if !sendRes.Passive {
			outRec.Delivery = store.DeliveryActive
		}
	}
	if sendRes != nil {
		outRec.Status = "replied"
	}
	if sendErr != nil {
		outRec.Status = "failed"
		outRec.Error = sendErr.Error()
		d.metrics.RepliesFailed.Add(1)
		d.stats.RecordError()
		d.log.Warn("failed to deliver reply", "component", "sender", "error", sendErr.Error())
	} else {
		d.metrics.RepliesSent.Add(1)
		d.stats.RecordOut()
	}
	if id, err := d.st.AddRecord(ctx, outRec); err == nil {
		outRec.ID = id
	}
	d.publish("message", outRec)

	if sendErr != nil {
		// 正式答复没发出去，就把「稍等」留着——否则用户什么也看不到。
		d.finishRecord(rec, "failed", "send: "+sendErr.Error(), latency)
		return
	}

	if outcome.earlySent && s.EarlyReplyRecall {
		d.recallEarlyNotice(ctx, in, outcome)
	}
	// 后台从这一轮抽取值得长期记住的事实（默认关，每 N 轮一次）。
	d.extractMemories(conv.ID, in, cfg, s, userText, sentText)
	d.finishRecord(rec, "replied", "", latency)
}

// callModel runs the completion and, when it takes longer than
// early_reply_delay_seconds, first sends a short acknowledgement so the user
// is not left staring at nothing for a minute.
//
// The notice spends one of the 5 passive reply slots for that msg_id; the real
// answer is therefore pushed as an active message.
// callModel runs one completion, optionally sending the early acknowledgement
// when it takes longer than early_reply_delay_seconds. Only the first call of a
// turn may do that: later tool steps would otherwise send it again.
func (d *Dispatcher) callModel(ctx context.Context, req ai.ChatRequest, in *Inbound, s settings.Settings, convID string, usedVision, allowEarly bool) modelOutcome {
	type reply struct {
		res *ai.ChatResult
		err error
	}
	// Buffered: the producer must never block on an abandoned consumer.
	done := make(chan reply, 1)
	go func() {
		res, err := d.ai.Chat(ctx, req)
		done <- reply{res: res, err: err}
	}()

	if !allowEarly || !s.EarlyReplyEnabled || in.MsgID == "" || s.EarlyReplyDelaySecs <= 0 {
		r := <-done
		return modelOutcome{result: r.res, err: r.err, usedVision: usedVision}
	}

	timer := time.NewTimer(time.Duration(s.EarlyReplyDelaySecs) * time.Second)
	defer timer.Stop()

	select {
	case r := <-done:
		return modelOutcome{result: r.res, err: r.err, usedVision: usedVision}
	case <-timer.C:
		sent, messageID := d.sendEarlyNotice(ctx, in, s, convID)
		r := <-done
		return modelOutcome{
			result:         r.res,
			err:            r.err,
			earlySent:      sent,
			earlyMessageID: messageID,
			earlySentAt:    time.Now(),
			usedVision:     usedVision,
		}
	}
}

// hasVisionParts reports whether the last message carries image parts.
func hasVisionParts(messages []ai.Message) bool {
	if len(messages) == 0 {
		return false
	}
	for _, p := range messages[len(messages)-1].Parts {
		if p.Type == "image_url" {
			return true
		}
	}
	return false
}

// visionParts turns the current turn's cached images into multimodal content
// parts, or returns nil when the turn has no usable image / vision is off.
func (d *Dispatcher) visionParts(ctx context.Context, s settings.Settings, in *Inbound) []ai.ContentPart {
	if !s.VisionEnabled || d.media == nil || len(in.Attachments) == 0 {
		return nil
	}
	parts := []ai.ContentPart{ai.TextPart(in.Content)}
	images := 0
	for _, a := range in.Attachments {
		if a.Kind != "image" || a.MediaID == "" {
			continue
		}
		if images >= s.VisionMaxImages {
			break
		}
		contentType, data, err := d.media.Load(ctx, a.MediaID)
		if err != nil || len(data) == 0 {
			if err != nil {
				d.log.Debug("failed to load a cached image", "component", "ai", "error", err.Error())
			}
			continue
		}
		parts = append(parts, ai.ImagePart(ai.DataURI(contentType, data)))
		images++
	}
	if images == 0 {
		return nil
	}
	return parts
}
