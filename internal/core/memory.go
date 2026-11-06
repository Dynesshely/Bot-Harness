package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
	"github.com/dynesshely/bot-harness/internal/tools"
)

// memoryScopeFor maps a conversation onto the scope its memories live in:
// group chat -> that group, private chat -> that person. Global memories are
// written only by an explicit admin request and are readable everywhere.
func memoryScopeFor(env *tools.Env) (scope, groupOpenID, memberOpenID string) {
	if env.IsGroup {
		return store.MemoryGroup, env.GroupOpenID, ""
	}
	return store.MemoryMember, "", env.MemberOpenID
}

// memoryBlock renders the facts this conversation may remember, ready to append
// to the system prompt. Returns "" when memory is off or empty.
func (d *Dispatcher) memoryBlock(ctx context.Context, in *Inbound, s settings.Settings, cfg settings.GroupConfig, userText string) string {
	if !s.MemoryEnabled || !cfg.MemoryEnabled || d.st == nil {
		return ""
	}
	scope, groupOpenID, memberOpenID := memoryScopeFor(&tools.Env{
		IsGroup: in.GroupOpenID != "", GroupOpenID: in.GroupOpenID,
		MemberOpenID: firstNonEmpty(in.MemberOpenID, in.UserOpenID),
	})

	// 多取一些候选再按相关性排序：只取 inject_max 条会让「最新但不相关」
	// 的事实挤掉真正相关的旧事实。
	candidates := s.MemoryInjectMax * 5
	if candidates < 20 {
		candidates = 20
	}
	hits, err := d.st.VisibleMemories(ctx, scope, groupOpenID, memberOpenID, "", candidates)
	if err != nil {
		d.log.Warn("failed to load memories", "component", "memory", "error", err.Error())
		return ""
	}
	hits = rankMemories(hits, userText)
	if len(hits) > s.MemoryInjectMax {
		hits = hits[:s.MemoryInjectMax]
	}
	if len(hits) == 0 {
		return ""
	}
	ids := make([]int64, 0, len(hits))
	var b strings.Builder
	b.WriteString("【你记得的事（来自过去的对话，按需使用，不确定就别当成事实说）】")
	for _, m := range hits {
		fmt.Fprintf(&b, "\n- %s", m.Text)
		ids = append(ids, m.ID)
	}
	if err := d.st.TouchMemories(ctx, ids); err != nil {
		d.log.Debug("failed to touch memories", "component", "memory", "error", err.Error())
	}
	return b.String()
}

// rankMemories keeps the most relevant facts: those sharing words with the
// current message first, then by salience. Fetching a small page and scoring it
// here is cheaper and more predictable than full-text search on Chinese text.
func rankMemories(hits []store.Memory, query string) []store.Memory {
	if len(hits) <= 1 {
		return hits
	}
	words := queryWords(query)
	score := func(m store.Memory) float64 {
		s := m.Salience
		for _, w := range words {
			if strings.Contains(m.Text, w) {
				s += 3
			}
		}
		return s
	}
	// Insertion sort: the candidate list is tiny (tens of rows).
	out := make([]store.Memory, len(hits))
	copy(out, hits)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && score(out[j]) > score(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// queryWords splits a sentence into lookup terms of two or more characters.
func queryWords(query string) []string {
	terms := strings.FieldsFunc(query, func(r rune) bool {
		return !('一' <= r && r <= '鿿') && !('a' <= r && r <= 'z') &&
			!('A' <= r && r <= 'Z') && !('0' <= r && r <= '9')
	})
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if len([]rune(t)) >= 2 {
			out = append(out, t)
		}
	}
	return out
}

// --- tools -----------------------------------------------------------------

// registerMemoryTools wires remember/recall into the registry.
func (d *Dispatcher) registerMemoryTools(reg *tools.Registry) {
	if reg == nil || d.st == nil {
		return
	}
	reg.Register(&rememberTool{st: d.st, maxPerScope: d.memoryCap})
	reg.Register(&recallTool{st: d.st})
}

// webConfig / execConfig are read per call so settings changes apply instantly.
func (d *Dispatcher) webConfig() tools.WebConfig {
	if d.settings == nil {
		return tools.WebConfig{}
	}
	s := d.settings.Get()
	return tools.WebConfig{
		Enabled:       s.WebEnabled,
		SearchBackend: s.WebSearchProvider,
		SearchBaseURL: s.WebSearchBaseURL,
		Timeout:       20 * time.Second,
		MaxBytes:      512 << 10,
	}
}

func (d *Dispatcher) execConfig() tools.ExecConfig {
	if d.settings == nil {
		return tools.ExecConfig{}
	}
	s := d.settings.Get()
	return tools.ExecConfig{
		Enabled:   s.ExecEnabled,
		Timeout:   time.Duration(s.ExecTimeoutSeconds) * time.Second,
		MaxOutput: s.ExecMaxOutputBytes,
	}
}

// memoryCap is how many facts one scope may hold before the weakest is evicted.
func (d *Dispatcher) memoryCap() int {
	if d.settings == nil {
		return 300
	}
	if n := d.settings.Get().MemoryMaxPerScope; n > 0 {
		return n
	}
	return 300
}

type rememberTool struct {
	st          *store.Store
	maxPerScope func() int
}

func (t *rememberTool) Name() string  { return "remember" }
func (t *rememberTool) Group() string { return "memory" }
func (t *rememberTool) Risk() tools.Risk {
	return tools.SideEffect
}

func (t *rememberTool) Description() string {
	return "把值得长期记住的事实存进记忆（人物偏好、约定、重要背景）。闲聊不要存；" +
		"只存一句陈述，不要存整段对话。scope=global 只有管理员能用。"
}

func (t *rememberTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"text":{"type":"string","description":"要记住的一句话事实，例如「小明养了一只叫豆豆的猫」"},
		"scope":{"type":"string","enum":["group","member","global"],"description":"默认跟随当前会话；global 仅管理员"}},
		"required":["text"]}`)
}

func (t *rememberTool) Run(ctx context.Context, raw json.RawMessage, env *tools.Env) (tools.Result, error) {
	var args struct {
		Text  string `json:"text"`
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return tools.Result{}, fmt.Errorf("参数需要 {\"text\": \"...\"}: %w", err)
	}
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return tools.Result{}, fmt.Errorf("text 不能为空")
	}
	if runes := []rune(text); len(runes) > 200 {
		text = string(runes[:200])
	}

	scope, groupOpenID, memberOpenID := memoryScopeFor(env)
	switch args.Scope {
	case "", "group", "member":
		if args.Scope == "group" && !env.IsGroup {
			return tools.Result{Text: "这是私聊，没有群记忆可写；已按个人记忆保存。"}, t.save(ctx, store.MemoryGroup, groupOpenID, memberOpenID, text)
		}
		if args.Scope == "member" && env.IsGroup {
			return tools.Result{Text: "群里不能写个人记忆（会泄露隐私），已按群记忆保存。"}, t.save(ctx, scope, groupOpenID, memberOpenID, text)
		}
		if args.Scope != "" {
			scope = args.Scope
		}
	case "global":
		if !env.IsAdmin {
			return tools.Result{Text: "只有管理员才能写全局记忆，这条没有保存。"}, nil
		}
		return tools.Result{Text: "已记到全局记忆（所有会话可见）。"}, t.save(ctx, store.MemoryGlobal, "", "", text)
	default:
		return tools.Result{}, fmt.Errorf("scope 只能是 group / member / global")
	}

	where := "群记忆"
	if scope == store.MemoryMember {
		where = "个人记忆"
	}
	return tools.Result{Text: "已记住（" + where + "）：" + text}, t.save(ctx, scope, groupOpenID, memberOpenID, text)
}

func (t *rememberTool) save(ctx context.Context, scope, groupOpenID, memberOpenID, text string) error {
	if n, err := t.st.CountMemories(ctx, scope, groupOpenID, memberOpenID); err == nil &&
		t.maxPerScope != nil && n >= t.maxPerScope() {
		// Cap reached: drop the weakest fact so memory cannot grow without bound.
		if err := t.st.DropWeakestMemory(ctx, scope, groupOpenID, memberOpenID); err != nil {
			return err
		}
	}
	_, err := t.st.AddMemory(ctx, store.MemoryInput{
		Scope: scope, GroupOpenID: groupOpenID, MemberOpenID: memberOpenID,
		Kind: "fact", Text: text, Source: "tool",
	})
	return err
}

type recallTool struct{ st *store.Store }

func (t *recallTool) Name() string     { return "recall" }
func (t *recallTool) Group() string    { return "memory" }
func (t *recallTool) Risk() tools.Risk { return tools.ReadOnly }

func (t *recallTool) Description() string {
	return "按关键词检索过往记忆。当用户问「你还记得…」「上次说的…」时先调用它，不要凭印象编。"
}

func (t *recallTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"query":{"type":"string","description":"要查的关键词，留空则列出最相关的几条"}}}`)
}

func (t *recallTool) Run(ctx context.Context, raw json.RawMessage, env *tools.Env) (tools.Result, error) {
	var args struct {
		Query string `json:"query"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return tools.Result{}, fmt.Errorf("参数需要 {\"query\": \"...\"}: %w", err)
		}
	}
	scope, groupOpenID, memberOpenID := memoryScopeFor(env)
	hits, err := t.st.VisibleMemories(ctx, scope, groupOpenID, memberOpenID, strings.TrimSpace(args.Query), 20)
	if err != nil {
		return tools.Result{}, err
	}
	hits = rankMemories(hits, args.Query)
	if len(hits) > 6 {
		hits = hits[:6]
	}
	if len(hits) == 0 {
		return tools.Result{Text: "没有找到相关记忆。"}, nil
	}
	var b strings.Builder
	for _, m := range hits {
		fmt.Fprintf(&b, "- %s\n", m.Text)
	}
	ids := make([]int64, 0, len(hits))
	for _, m := range hits {
		ids = append(ids, m.ID)
	}
	_ = t.st.TouchMemories(ctx, ids)
	return tools.Result{Text: strings.TrimRight(b.String(), "\n")}, nil
}

// --- background extraction -------------------------------------------------

// extractPrompt is deliberately narrow: only durable, reusable facts, and JSON
// only, so a chatty model cannot turn this into a second conversation.
const extractPrompt = `从下面这一轮对话里，提取值得长期记住的事实（人物身份/偏好/约定/重要背景）。
要求：
- 只提取 0~2 条；没有就输出 []
- 每条是一句独立、可脱离上下文理解的陈述
- 不要提取寒暄、情绪、一次性的问题，也不要复述整段对话
只输出 JSON 数组，形如 [{"text":"小明养了一只叫豆豆的猫","kind":"fact"}]
对话：
用户：%s
助手：%s`

// memoryTurnTick counts finished turns per conversation so extraction can run
// once every N turns without a timer.
func (d *Dispatcher) memoryTurnTick(conversationID string) int {
	d.memTurnsMu.Lock()
	defer d.memTurnsMu.Unlock()
	if d.memTurns == nil {
		d.memTurns = map[string]int{}
	}
	d.memTurns[conversationID]++
	return d.memTurns[conversationID]
}

// extractMemories runs the background extraction when its turn comes. It is
// best-effort: every failure is logged and nothing reaches the user.
func (d *Dispatcher) extractMemories(conversationID string, in *Inbound, cfg settings.GroupConfig, s settings.Settings, userText, reply string) {
	if !s.MemoryEnabled || !cfg.MemoryEnabled || !s.MemoryExtractEnabled || d.st == nil || d.ai == nil {
		d.log.Debug("memory extraction skipped", "component", "memory",
			"memory", s.MemoryEnabled, "group", cfg.MemoryEnabled, "extract", s.MemoryExtractEnabled)
		return
	}
	if strings.TrimSpace(userText) == "" || strings.TrimSpace(reply) == "" {
		d.log.Debug("memory extraction skipped: empty turn", "component", "memory")
		return
	}
	if n := d.memoryTurnTick(conversationID); s.MemoryExtractEvery > 0 && n%s.MemoryExtractEvery != 0 {
		return
	}

	scope, groupOpenID, memberOpenID := memoryScopeFor(&tools.Env{
		IsGroup: in.GroupOpenID != "", GroupOpenID: in.GroupOpenID,
		MemberOpenID: firstNonEmpty(in.MemberOpenID, in.UserOpenID),
	})

	go func() {
		ctx, cancel := context.WithTimeout(d.base(), 30*time.Second)
		defer cancel()

		res, err := d.ai.Chat(ctx, ai.ChatRequest{
			Model:       s.Model,
			Messages:    []ai.Message{{Role: ai.RoleUser, Content: fmt.Sprintf(extractPrompt, userText, reply)}},
			Temperature: 0,
			MaxTokens:   400,
			Stream:      false,
		})
		if err != nil {
			d.log.Debug("memory extraction failed", "component", "memory", "error", err.Error())
			return
		}
		facts := parseExtractedFacts(res.Text)
		for _, f := range facts {
			if _, err := d.st.AddMemory(ctx, store.MemoryInput{
				Scope: scope, GroupOpenID: groupOpenID, MemberOpenID: memberOpenID,
				Kind: f.Kind, Text: f.Text, Source: "extract", Salience: 0.8,
			}); err != nil {
				d.log.Debug("failed to store extracted memory", "component", "memory", "error", err.Error())
			}
		}
		if len(facts) > 0 {
			d.log.Info("extracted facts from a turn", "component", "memory",
				"count", len(facts), "scope", scope)
		}
	}()
}

type extractedFact struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
}

// parseExtractedFacts accepts a JSON array, tolerating prose or code fences
// around it: models add "好的，以下是…" more often than not.
func parseExtractedFacts(raw string) []extractedFact {
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end <= start {
		return nil
	}
	var facts []extractedFact
	if err := json.Unmarshal([]byte(raw[start:end+1]), &facts); err != nil {
		return nil
	}
	out := make([]extractedFact, 0, len(facts))
	for _, f := range facts {
		text := strings.TrimSpace(f.Text)
		if len([]rune(text)) < 4 {
			continue
		}
		if runes := []rune(text); len(runes) > 200 {
			text = string(runes[:200])
		}
		kind := strings.TrimSpace(f.Kind)
		if kind == "" {
			kind = "fact"
		}
		out = append(out, extractedFact{Text: text, Kind: kind})
		if len(out) == 2 {
			break
		}
	}
	return out
}
