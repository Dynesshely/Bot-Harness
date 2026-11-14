// Package settings holds the runtime-mutable configuration surfaced by the
// admin panel. Values persisted in the database win over .env defaults.
package settings

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dynesshely/bot-harness/internal/store"
)

// Trigger modes (decision D-004).
const (
	TriggerAtOnly      = "at_only"      // E1: only messages that @ the bot
	TriggerAllMessages = "all_messages" // E2: every group message (needs full-group permission)
)

// Context modes (decision D-003).
const (
	ContextPerMember = "per_member" // C1: one context per group member
	ContextShared    = "shared"     // C2: one shared context per group
)

// Quote reply modes. Quoting every answer felt mechanical, so the default is a
// probability band (MaiBot's 引用频率分档): 0% / ~30% / ~70%.
const (
	QuoteReplyNever        = "never"        // 0%
	QuoteReplyOccasionally = "occasionally" // ~30% (default)
	QuoteReplyOften        = "often"        // ~70%
)

// QuoteReplyRate maps a mode to the probability of quoting one answer. An
// unknown or empty mode falls back to the default band.
func QuoteReplyRate(mode string) float64 {
	switch mode {
	case QuoteReplyNever:
		return 0
	case QuoteReplyOften:
		return 0.70
	case QuoteReplyOccasionally, "":
		return 0.30
	default:
		return 0.30
	}
}

// Reply style variant bounds: one bad admin entry must not be able to flood the
// system prompt or inject a paragraph.
const (
	ReplyStyleVariantMaxItems = 8
	ReplyStyleVariantMaxRunes = 200
)

// ReplyStyleVariantProbabilityDefault is the default chance of applying one of
// the configured style variants to a turn (~one in four answers).
const ReplyStyleVariantProbabilityDefault = 0.25

// Context scopes: how much of the group the model gets to read.
//
// This is deliberately separate from the trigger mode. "Only answer when @-ed"
// and "only let the model see @-ed messages" are different wishes, and an
// operator may well want the bot to stay quiet while still following the room.
const (
	// ContextAddressedOnly feeds the model just the exchanges that involved it.
	ContextAddressedOnly = "addressed_only"
	// ContextIncludeUnaddressed also feeds unaddressed group chatter and other
	// bots' messages, so the model can follow the conversation. It still only
	// *answers* according to the trigger mode.
	ContextIncludeUnaddressed = "include_unaddressed"
)

// GroupOverride customises behaviour for a single group.
type GroupOverride struct {
	GroupOpenID  string `json:"group_openid"`
	Enabled      *bool  `json:"enabled,omitempty"`
	TriggerMode  string `json:"trigger_mode,omitempty"`
	ContextMode  string `json:"context_mode,omitempty"`
	ContextScope string `json:"context_scope,omitempty"`
	ReplyToBots  *bool  `json:"reply_to_bots,omitempty"`
	// QuoteReply makes the bot answer as a quoted reply to the message that
	// triggered it (nil = follow the global default).
	QuoteReply *bool `json:"quote_reply,omitempty"`
	// Tools 覆盖本群的工具调用开关（nil = 跟随全局）。
	Tools *bool `json:"tools,omitempty"`
	// Proactive 覆盖本群的自发发言档位（off / auto）。
	Proactive *string `json:"proactive,omitempty"`
	// Tasks 覆盖本群的定时提醒开关。
	Tasks *bool `json:"tasks,omitempty"`
	// Memory / Web / Exec 覆盖本群的能力开关。
	Memory *bool `json:"memory,omitempty"`
	// Humanize 覆盖本群的「人味」档位（off/soft/medium/strong）。
	Humanize *string `json:"humanize,omitempty"`
	// Affect 覆盖本群的「情绪状态」开关（nil = 跟随全局 affect_enabled）。
	Affect *bool `json:"affect,omitempty"`
	// AffectInjectMode 覆盖本群的注入档位（off/line/full，空 = 跟随全局）。
	AffectInjectMode *string `json:"affect_inject_mode,omitempty"`
	// Polish 覆盖本群的「两阶段生成」开关（nil = 跟随全局 polish_enabled）。
	Polish *bool `json:"polish,omitempty"`
	// JoinGate 覆盖本群的「自主加入」开关（nil = 跟随全局 join_gate_enabled）。
	// 全局关是总开关：群覆盖只能收窄，不能越过全局关打开。
	JoinGate          *bool `json:"join_gate,omitempty"`
	Web               *bool `json:"web,omitempty"`
	Exec              *bool `json:"exec,omitempty"`
	RateLimitGroupQPM int   `json:"rate_limit_group_qpm,omitempty"`
}

// Settings is the complete runtime configuration document.
type Settings struct {
	SystemPrompt string `json:"system_prompt"`
	// OpenAIBaseURL 是模型端点的**唯一来源**（不再从 .env 读）：
	// 空字符串表示尚未配置，模型调用会明确报错而不是偷偷用某个默认地址。
	OpenAIBaseURL string `json:"openai_base_url"`
	// OpenAIAPIKey 与端点一样只来自数据库。它 never 被后台接口回显
	// （见 handleGetConfig）：只进不出的秘密不会从截图或浏览器缓存里漏出去。
	OpenAIAPIKey string  `json:"openai_api_key"`
	Model        string  `json:"model"`
	Temperature  float64 `json:"temperature"`
	// MaxTokens 是单次回复的输出上限。默认刻意保持 1024：请求里带上极大的
	// max_tokens 会让上游直接拒绝，而回复长度本来还有 reply_max_chars 兜底。
	// 校验上限放宽到 10000000 只是给将来留余量，不代表默认值要跟着变大。
	MaxTokens             int `json:"max_tokens"`
	RequestTimeoutSeconds int `json:"request_timeout_seconds"`
	ContextMaxTurns       int `json:"context_max_turns"`
	// ContextMaxTokens 是喂给模型的历史 token 预算（实际预算 = 该值 − MaxTokens）。
	// 默认 256000 面向长上下文模型；校验上限 10000000 是预留值，远大于当前模型窗口。
	ContextMaxTokens int `json:"context_max_tokens"`
	// ModelContextTokens is the model provider's real context window. The
	// effective prompt budget is min(ContextMaxTokens, ModelContextTokens) -
	// MaxTokens, so a 1M config cannot be sent to a 128k model by accident.
	ModelContextTokens int `json:"model_context_tokens"`
	HistoryTTLHours    int `json:"history_ttl_hours"`
	ReplyMaxChars      int `json:"reply_max_chars"`
	ReplyMaxSegments   int `json:"reply_max_segments"`
	// ReplyDebounceMode merges consecutive un-@ group messages into one turn:
	// off / fast / natural / steady (default natural). It is pure expression:
	// @ / quoting the bot / commands never wait, and the merged messages keep
	// their timeline rows plus one audit record each.
	ReplyDebounceMode string `json:"reply_debounce_mode"`
	// ReplyDebounceQuietSeconds / ReplyDebounceActiveSeconds are the wait windows
	// for a quiet / busy group (0–15). natural uses these numbers directly; fast
	// is fixed at 1/3 and steady at 4/7. A group counts as active once it has seen
	// >= 3 human messages in the last 60 seconds. Zero means "do not wait".
	ReplyDebounceQuietSeconds  int    `json:"reply_debounce_quiet_seconds"`
	ReplyDebounceActiveSeconds int    `json:"reply_debounce_active_seconds"`
	EarlyReplyEnabled          bool   `json:"early_reply_enabled"`
	EarlyReplyDelaySecs        int    `json:"early_reply_delay_seconds"`
	EarlyReplyText             string `json:"early_reply_text"`
	ReplyFooterEnabled         bool   `json:"reply_footer_enabled"`
	EarlyReplyRecall           bool   `json:"early_reply_recall"`
	// ToolsEnabled lets the model call functions. Off = plain chat, no tool loop.
	ToolsEnabled bool `json:"tools_enabled"`
	// ToolsMaxSteps caps model turns inside one reply (each turn may run tools).
	ToolsMaxSteps int `json:"tools_max_steps"`
	// ToolsTotalBudgetSeconds is the wall-clock budget for the whole loop.
	ToolsTotalBudgetSeconds int `json:"tools_total_budget_seconds"`
	// ToolsResultMaxChars truncates one tool result before it reaches the model.
	ToolsResultMaxChars int `json:"tools_result_max_chars"`

	// MemoryEnabled turns on long-term memory: remember/recall tools plus the
	// fact block injected into the system prompt.
	MemoryEnabled bool `json:"memory_enabled"`
	// MemoryInjectMax caps how many facts are injected into one prompt.
	MemoryInjectMax int `json:"memory_inject_max"`
	// MemoryMaxPerScope caps stored facts per scope; the weakest is evicted.
	MemoryMaxPerScope int `json:"memory_max_per_scope"`
	// MemoryExtractEnabled lets a cheap background call pull durable facts out
	// of a finished exchange (off by default: it costs one extra model call).
	MemoryExtractEnabled bool `json:"memory_extract_enabled"`
	// MemoryExtractEvery runs that extraction once per N turns of a conversation.
	MemoryExtractEvery int `json:"memory_extract_every"`

	// ProactiveLevel is the global default for spontaneous messages:
	// off / auto. Per-group overrides go through GroupOverride.Proactive; setting
	// this to off is the kill switch.
	ProactiveLevel string `json:"proactive_level"`
	// ProactiveRatio buys one unprompted message per N human messages.
	ProactiveRatio int `json:"proactive_ratio"`
	// ProactiveMaxCredits caps banked credits so a long silence cannot burst.
	ProactiveMaxCredits int `json:"proactive_max_credits"`
	// ProactiveQuietStart / End are local hours (may wrap midnight).
	ProactiveQuietStart int `json:"proactive_quiet_start"`
	ProactiveQuietEnd   int `json:"proactive_quiet_end"`
	// ProactiveMinIntervalMinutes spaces out unprompted messages per group.
	ProactiveMinIntervalMinutes int `json:"proactive_min_interval_minutes"`

	// TasksEnabled allows schedule_task / list_tasks / cancel_task: reminders the
	// user asked for. This is the only path that sends without being prompted.
	TasksEnabled bool `json:"tasks_enabled"`
	// TasksMaxPerMember caps pending reminders per person per conversation.
	TasksMaxPerMember int `json:"tasks_max_per_member"`
	// TasksMaxDelayHours caps how far ahead a reminder may be scheduled.
	TasksMaxDelayHours int `json:"tasks_max_delay_hours"`

	// CommitmentEnabled turns on open-loop commitments: a reminder keeps asking
	// until the user explicitly marks it done, postponed or cancelled. The model
	// decides when to ask again; there is no code-side frequency ceiling.
	CommitmentEnabled bool `json:"commitment_enabled"`
	// CommitmentMaxOpenPerMember caps how many open loops one person may have.
	// It is an anti-mistake fuse, not a nudge-frequency limit.
	CommitmentMaxOpenPerMember int `json:"commitment_max_open_per_member"`

	// HumanStatusEnabled turns on the inferred activity state cache.
	HumanStatusEnabled bool `json:"human_status_enabled"`
	// HumanStatusTriggerMessages is how many new messages trigger one inference.
	HumanStatusTriggerMessages int `json:"human_status_trigger_messages"`
	// HumanStatusTTLMinutes is how long an inference stays usable.
	HumanStatusTTLMinutes int `json:"human_status_ttl_minutes"`
	// HumanStatusTimelineMessages is how many recent messages feed the per-member
	// timeline (20-400). More messages mean better rhythm/active-hour hints at the
	// cost of prompt tokens.
	HumanStatusTimelineMessages int `json:"human_status_timeline_messages"`
	// HumanStatusVisionEnabled lets the model call analyze_image during an
	// inference. Each call costs one extra multimodal request.
	HumanStatusVisionEnabled bool `json:"human_status_vision_enabled"`
	// HumanStatusVisionModel is the model used for analyze_image; empty means the
	// main model. A text-only main model just returns an error to the tool call.
	HumanStatusVisionModel string `json:"human_status_vision_model"`

	// --- 情绪状态（affect）---
	//
	// 默认关闭；关闭时回复管线与开关不存在时完全一致。
	AffectEnabled bool `json:"affect_enabled"`
	// AffectPack selects the built-in content pack ("daily").
	AffectPack string `json:"affect_pack"`
	// AffectInjectMode is off / line / full.
	AffectInjectMode string `json:"affect_inject_mode"`
	// AffectEventClassifier is off / rules.
	AffectEventClassifier string `json:"affect_event_classifier"`
	// AffectTickSeconds is how often the decay ticker runs (60-1800).
	AffectTickSeconds int `json:"affect_tick_seconds"`
	// AffectInjectMaxTokens caps the injected block (40-300 tokens).
	AffectInjectMaxTokens int `json:"affect_inject_max_tokens"`

	// --- 自主加入（join gate）---
	//
	// 默认关闭；关闭时回复管线与 v0.2.0 逐字节一致。开启后，未 @ 机器人的
	// 群消息会先由一次轻量 LLM 调用判断「要不要加入」，同时顺带切分话题。
	// 全局开关是总开关，群覆盖只能收窄。
	JoinGateEnabled bool `json:"join_gate_enabled"`
	// JoinGateMode is off / llm. off 与 join_gate_enabled=false 等效。
	JoinGateMode string `json:"join_gate_mode"`
	// JoinGateModel is the model used for the decision; empty = main model.
	JoinGateModel string `json:"join_gate_model"`
	// JoinGateWindow is how many recent group messages feed the decision (5-50).
	JoinGateWindow int `json:"join_gate_window"`
	// JoinGateConfidence is the minimum confidence required to join (0-1).
	JoinGateConfidence float64 `json:"join_gate_confidence"`
	// JoinGateEvalCooldownSeconds is the minimum gap between two evaluations of
	// the same group (10-3600).
	JoinGateEvalCooldownSeconds int `json:"join_gate_eval_cooldown_seconds"`
	// JoinGateMinIntervalMinutes is the minimum gap between two joins (1-240).
	JoinGateMinIntervalMinutes int `json:"join_gate_min_interval_minutes"`
	// JoinGateMaxPerHour / JoinGateMaxPerDay cap joins per group.
	JoinGateMaxPerHour int `json:"join_gate_max_per_hour"`
	JoinGateMaxPerDay  int `json:"join_gate_max_per_day"`
	// JoinGateWaitRetrySeconds is the early re-evaluation delay after a "wait"
	// verdict (10-300): once the topic has had time to develop, the next message
	// may skip the normal evaluation cooldown.
	JoinGateWaitRetrySeconds int `json:"join_gate_wait_retry_seconds"`
	// JoinGateTopicDormantMinutes / JoinGateTopicClosedMinutes age topics out by
	// their newest message (5-240 / 30-1440). They are the code-side backstop for
	// the model: a topic with no new message for dormant minutes is downgraded to
	// dormant, and for closed minutes to closed. Downgrade only — age never
	// revives a topic the model itself called closed.
	JoinGateTopicDormantMinutes int `json:"join_gate_topic_dormant_minutes"`
	JoinGateTopicClosedMinutes  int `json:"join_gate_topic_closed_minutes"`

	// --- 表达方式库（expression）---
	//
	// 默认关闭；关闭时回复管线与没有这个库时完全一致。开启后，模型可以按
	// 「情境 → 说法」的语料学习本群的表达习惯，并在生成前召回几条参考。
	// 全局开关是总开关：关掉后学习、召回、注入都不发生。
	ExpressionEnabled bool `json:"expression_enabled"`
	// ExpressionMode picks the retrieval strategy: off / fast / precise.
	// fast = 关键词匹配（零额外调用、零依赖）；precise = 向量召回（需要
	// Embedding 模型，或在 expression_embedding_model 留空时复用主模型）。
	ExpressionMode string `json:"expression_mode"`
	// ExpressionEmbeddingModel is the embedding model used by precise mode;
	// empty means "reuse the main model endpoint's embedding model".
	ExpressionEmbeddingModel string `json:"expression_embedding_model"`
	// ExpressionRecallLimit caps how many candidates one recall may return (4-50).
	ExpressionRecallLimit int `json:"expression_recall_limit"`
	// ExpressionInjectMax caps how many expressions reach the prompt (1-5).
	ExpressionInjectMax int `json:"expression_inject_max"`
	// ExpressionLearnEnabled turns on learning new expressions from finished
	// turns. It has no effect while ExpressionEnabled is false.
	ExpressionLearnEnabled bool `json:"expression_learn_enabled"`
	// ExpressionLearnEvery learns once per N finished turns (1-100).
	ExpressionLearnEvery int `json:"expression_learn_every"`
	// ExpressionReviewEnabled queues learned expressions for review instead of
	// enabling them immediately. Turn it off to publish straight away.
	ExpressionReviewEnabled bool `json:"expression_review_enabled"`
	// ExpressionShareGlobal lets expressions learned in one group be recalled in
	// other groups too. Off keeps every expression scoped to its own group.
	ExpressionShareGlobal bool `json:"expression_share_global"`

	// --- 接入与运维（原先只存在于 .env）---

	// QQAppID / QQAppSecret 是开放平台凭证。Secret 只进不出。
	QQAppID     string `json:"qq_app_id"`
	QQAppSecret string `json:"qq_app_secret"`
	QQSandbox   bool   `json:"qq_sandbox"`
	// QQTransport: websocket | webhook | both（改完需重连/重启）。
	QQTransport string `json:"qq_transport"`
	// QQIntents 是订阅的事件位图，默认 GROUP_AND_C2C_EVENT。
	QQIntents uint64 `json:"qq_intents"`
	// QQShardCount 为分片数，一般保持 1。
	QQShardCount int `json:"qq_shard_count"`
	// QQWebhookPath 仅在 webhook 传输下使用。
	QQWebhookPath string `json:"qq_webhook_path"`
	// QQBotOpenID 是可选的自身 openid 兜底（bot_state 不可用时使用）。
	QQBotOpenID string `json:"qq_bot_openid"`

	// AdminToken 是后台口令；只进不出。改动会让已登录会话立即失效。
	AdminToken string `json:"admin_token"`
	// HTTPAddr 是监听地址（改完需要重启进程才生效）。
	HTTPAddr string `json:"http_addr"`
	// TLSExtraCAFiles / TLSInsecureVerify 影响出站 TLS（改完需要重启）。
	TLSExtraCAFiles   []string `json:"tls_extra_ca_files"`
	TLSInsecureVerify bool     `json:"tls_insecure_verify"`
	// SystemPromptFile 非空时优先从文件读提示词（每次调用重新读）。
	SystemPromptFile string `json:"system_prompt_file"`
	// LogLevel / LogBufferSize 日志级别与后台环形缓冲（缓冲容量需重启）。
	LogLevel      string `json:"log_level"`
	LogBufferSize int    `json:"log_buffer_size"`
	// MediaMaxBytes 单个附件的缓存上限。
	MediaMaxBytes int64 `json:"media_max_bytes"`

	// HumanizeLevel controls how the bot paces and styles its replies:
	// off / soft / medium / strong. Empty or unknown = medium.
	HumanizeLevel string `json:"humanize_level"`
	// HumanizeDelayScale multiplies every pause (0.5–3, default 1).
	HumanizeDelayScale float64 `json:"humanize_delay_scale"`
	// HumanizeSegmentSplit controls probabilistically splitting one answer into
	// several short bubbles: off / soft / medium / strong (off = one message).
	HumanizeSegmentSplit string `json:"humanize_segment_split"`
	// HumanizeTypingSecondsPerChar is the simulated typing time per character
	// before each bubble (0.05–1.0; Chinese/English are weighted inside).
	HumanizeTypingSecondsPerChar float64 `json:"humanize_typing_seconds_per_char"`
	// HumanizeTypingMaxSeconds caps the extra wait a whole turn may spend on
	// simulated typing (0–30; 0 = no cap but segment waits still apply).
	HumanizeTypingMaxSeconds int `json:"humanize_typing_max_seconds"`
	// Persona is one line of character description appended to the prompt.
	Persona string `json:"persona"`

	// PolishEnabled turns on the two-stage pipeline: the main model drafts an
	// answer, then a context-free polish call rewrites that draft in character.
	// When off, the draft is delivered unchanged.
	PolishEnabled bool `json:"polish_enabled"`
	// PolishModel is the model used for stage 2. Empty = reuse Model.
	PolishModel string `json:"polish_model"`
	// PolishTemperature is the polish call's temperature.
	PolishTemperature float64 `json:"polish_temperature"`
	// PolishMaxTokens caps stage 2; 0 means "auto" (draft tokens ×1.5, at least
	// 128 and never more than the main MaxTokens). An explicit cap must be in
	// [1, 8192] — the same window the admin UI's number field allows.
	PolishMaxTokens int `json:"polish_max_tokens"`
	// PolishTimeoutSeconds bounds stage 2. A polish call that runs longer than
	// this is abandoned and the draft is sent instead.
	PolishTimeoutSeconds int `json:"polish_timeout_seconds"`
	// PolishMinChars skips polishing when the draft is shorter than this many
	// runes (0 = always polish).
	PolishMinChars int `json:"polish_min_chars"`

	// WebEnabled turns on web_fetch / web_search.
	WebEnabled bool `json:"web_enabled"`
	// WebSearchProvider is "searxng" or empty (search disabled, fetch still works).
	WebSearchProvider string `json:"web_search_provider"`
	// WebSearchBaseURL is the SearxNG root; its /search?format=json is used.
	WebSearchBaseURL string `json:"web_search_base_url"`
	// ExecEnabled allows run_shell inside the container. Off by default: it runs
	// model-authored commands.
	ExecEnabled bool `json:"exec_enabled"`
	// ExecTimeoutSeconds kills a command after this long.
	ExecTimeoutSeconds int `json:"exec_timeout_seconds"`
	// ExecMaxOutputBytes caps captured stdout+stderr.
	ExecMaxOutputBytes int `json:"exec_max_output_bytes"`

	// MarkdownEnabled sends replies as msg_type=2. The platform needs a separate
	// template permission; the sender degrades to plain text by itself when that
	// permission is missing, so turning this on is safe.
	MarkdownEnabled bool `json:"markdown_enabled"`
	// ReplyToBots allows answering another bot that @s us. Unaddressed bot
	// chatter is never answered, regardless of this flag, to avoid two bots
	// ping-ponging forever.
	ReplyToBots bool `json:"reply_to_bots"`
	// QuoteReply is the legacy boolean form of QuoteReplyMode, kept for
	// compatibility (old databases, old admin clients, per-group overrides):
	// reading maps true → occasionally, false → never. New writes should use
	// QuoteReplyMode; the two are mirrored on every save.
	QuoteReply bool `json:"quote_reply"`
	// QuoteReplyMode decides how often an answer quotes the triggering message:
	// never / occasionally / often. Groups may still override it with the legacy
	// boolean (GroupOverride.QuoteReply: true → occasionally, false → never);
	// the more restrictive "never" always wins over the global band.
	QuoteReplyMode string `json:"quote_reply_mode"`
	// ReplyStyleVariants holds optional one-line speaking styles ("用 1–2 个字回复",
	// "带一点翻译腔"). Empty = the feature is off. At most one entry is appended
	// to the system prompt per turn, after persona/humanize and before memory.
	ReplyStyleVariants []string `json:"reply_style_variants"`
	// ReplyStyleVariantProbability is the chance (0–1) that a turn applies one of
	// ReplyStyleVariants.
	ReplyStyleVariantProbability float64 `json:"reply_style_variant_probability"`
	// VisionEnabled lets the model see images attached to the current message.
	VisionEnabled bool `json:"vision_enabled"`
	// VisionMaxImages caps how many images one turn may carry.
	VisionMaxImages      int `json:"vision_max_images"`
	RateLimitUserQPM     int `json:"rate_limit_user_qpm"`
	RateLimitGroupQPM    int `json:"rate_limit_group_qpm"`
	GroupCooldownSeconds int `json:"group_cooldown_seconds"`
	MaxConcurrentLLM     int `json:"max_concurrent_llm"`

	// ManualSendEnabled is the master switch for "send from the admin panel".
	// It exists to stop an accidental or leaked admin session from posting into
	// a real group, not as a security boundary: holding the admin token already
	// implies full control.
	ManualSendEnabled bool `json:"manual_send_enabled"`
	// ManualSendPerMinute caps manual sends across all conversations.
	ManualSendPerMinute int `json:"manual_send_per_minute"`
	// ManualSendPerConversationQPM caps manual sends inside one conversation.
	ManualSendPerConversationQPM int `json:"manual_send_per_conversation_qpm"`
	// ManualSendMaxSegments caps how many platform messages one manual send may
	// become. Unlike passive replies, active messages have no 5-reply ceiling,
	// but every segment spends the group's daily active-message quota.
	ManualSendMaxSegments int `json:"manual_send_max_segments"`

	// CommandPanelEnabled turns on the automatic startup sync of the QQ
	// command panel. It is display only: turning it off never disables the
	// commands themselves, and the admin action still works.
	CommandPanelEnabled bool `json:"command_panel_enabled"`

	TriggerMode string `json:"trigger_mode"`
	ContextMode string `json:"context_mode"`
	// ContextScope decides how much of the group reaches the model.
	ContextScope    string          `json:"context_scope"`
	GroupAllowlist  []string        `json:"group_allowlist"`
	MemberAllowlist []string        `json:"member_allowlist"`
	AdminOpenIDs    []string        `json:"admin_openids"`
	GroupOverrides  []GroupOverride `json:"group_overrides"`
	BlockedKeywords []string        `json:"blocked_keywords"`
}

// Defaults derives the initial settings document from static configuration.
// Zero or negative values fall back to the built-in defaults so that a
// partially populated config can never produce an invalid document.
// DefaultIntents is GROUP_AND_C2C_EVENT (1<<25): group @-messages plus C2C.
const DefaultIntents = uint64(1) << 25

// DefaultEarlyReplyText is sent when the model takes longer than
// early_reply_delay_seconds to produce an answer.
const DefaultEarlyReplyText = "有点复杂，让我多想会儿～稍后直接把答案发给你"

func Defaults() Settings {
	return Settings{
		SystemPrompt: "",
		// 端点与密钥只来自数据库（后台「系统设置」），.env 不再参与。
		OpenAIBaseURL:               "",
		OpenAIAPIKey:                "",
		Model:                       "gpt-4o-mini",
		Temperature:                 0.7,
		MaxTokens:                   1024,
		RequestTimeoutSeconds:       90,
		ContextMaxTurns:             12,
		ContextMaxTokens:            256000,
		ModelContextTokens:          128000,
		HistoryTTLHours:             72,
		ReplyMaxChars:               1200,
		ReplyMaxSegments:            3,
		ReplyDebounceMode:           "natural",
		ReplyDebounceQuietSeconds:   2,
		ReplyDebounceActiveSeconds:  5,
		EarlyReplyEnabled:           true,
		EarlyReplyDelaySecs:         3,
		EarlyReplyText:              DefaultEarlyReplyText,
		ToolsEnabled:                true,
		ToolsMaxSteps:               orInt(0, 6, 1, 12),
		ToolsTotalBudgetSeconds:     orInt(0, 100, 10, 300),
		ToolsResultMaxChars:         orInt(0, 8000, 500, 20000),
		ProactiveLevel:              "off",
		ProactiveRatio:              orInt(0, 10, 3, 100),
		ProactiveMaxCredits:         orInt(0, 3, 1, 10),
		ProactiveQuietStart:         23,
		ProactiveQuietEnd:           8,
		ProactiveMinIntervalMinutes: orInt(0, 10, 1, 240),
		TasksEnabled:                true,
		TasksMaxPerMember:           orInt(0, 5, 1, 50),
		TasksMaxDelayHours:          orInt(0, 168, 1, 720),
		CommitmentEnabled:           true,
		CommitmentMaxOpenPerMember:  orInt(0, 10, 1, 50),
		HumanStatusEnabled:          true,
		HumanStatusTriggerMessages:  orInt(0, 20, 5, 200),
		HumanStatusTTLMinutes:       orInt(0, 120, 10, 1440),
		HumanStatusTimelineMessages: orInt(0, 120, 20, 400),
		HumanStatusVisionEnabled:    true,
		HumanStatusVisionModel:      "",
		// 情绪状态默认关：框架在，但不动现有回复行为。
		AffectEnabled:         false,
		AffectPack:            "daily",
		AffectInjectMode:      "line",
		AffectEventClassifier: "rules",
		AffectTickSeconds:     300,
		AffectInjectMaxTokens: 120,
		// 自主加入默认关：框架在，但不改变任何现有群的行为。
		JoinGateEnabled:             false,
		JoinGateMode:                "llm",
		JoinGateModel:               "",
		JoinGateWindow:              orInt(0, 20, 5, 50),
		JoinGateConfidence:          0.7,
		JoinGateEvalCooldownSeconds: orInt(0, 60, 10, 3600),
		JoinGateMinIntervalMinutes:  orInt(0, 5, 1, 240),
		JoinGateMaxPerHour:          orInt(0, 3, 1, 30),
		JoinGateMaxPerDay:           orInt(0, 12, 1, 100),
		JoinGateWaitRetrySeconds:    orInt(0, 30, 10, 300),
		// 话题寿命兜底：20 分钟没新消息算「暂歇」，180 分钟算「已结束」。
		JoinGateTopicDormantMinutes: orInt(0, 20, 5, 240),
		JoinGateTopicClosedMinutes:  orInt(0, 180, 30, 1440),
		// 表达方式库默认关：框架在，但不改变任何现有部署的回复行为。
		// 学习默认开（总开关关时不生效），审核默认开（学习结果先进待审）。
		ExpressionEnabled:        false,
		ExpressionMode:           "fast",
		ExpressionEmbeddingModel: "",
		ExpressionRecallLimit:    orInt(0, 12, 4, 50),
		ExpressionInjectMax:      orInt(0, 3, 1, 5),
		ExpressionLearnEnabled:   true,
		ExpressionLearnEvery:     orInt(0, 20, 1, 100),
		ExpressionReviewEnabled:  true,
		ExpressionShareGlobal:    false,
		QQTransport:              "websocket",
		QQIntents:                DefaultIntents,
		QQShardCount:             1,
		QQWebhookPath:            "/qq/webhook",
		// Loopback by default: the admin panel is a privileged surface, so a
		// fresh install must not expose it on every interface. Operators who
		// put it behind a reverse proxy set 0.0.0.0:8080 explicitly (the
		// process logs a WARN when the bound address is not loopback).
		HTTPAddr:                     "127.0.0.1:8080",
		LogLevel:                     "info",
		LogBufferSize:                5000,
		MediaMaxBytes:                4 << 20,
		HumanizeLevel:                "medium",
		HumanizeDelayScale:           orFloat(0, 1, 0.5, 3),
		HumanizeSegmentSplit:         "medium",
		HumanizeTypingSecondsPerChar: orFloat(0, 0.3, 0.05, 1),
		HumanizeTypingMaxSeconds:     12,
		PolishEnabled:                true,
		PolishModel:                  "",
		PolishTemperature:            0.7,
		PolishMaxTokens:              0,
		PolishTimeoutSeconds:         20,
		PolishMinChars:               0,
		WebEnabled:                   true,
		ExecTimeoutSeconds:           orInt(0, 15, 3, 60),
		ExecMaxOutputBytes:           orInt(0, 8192, 512, 65536),
		MemoryEnabled:                true,
		MemoryInjectMax:              orInt(0, 6, 1, 20),
		MemoryMaxPerScope:            orInt(0, 300, 20, 5000),
		MemoryExtractEvery:           orInt(0, 10, 1, 50),
		MarkdownEnabled:              true,
		ReplyFooterEnabled:           true,
		EarlyReplyRecall:             true,
		ReplyToBots:                  true,
		QuoteReply:                   true,
		QuoteReplyMode:               QuoteReplyOccasionally,
		ReplyStyleVariants:           []string{},
		ReplyStyleVariantProbability: ReplyStyleVariantProbabilityDefault,
		VisionEnabled:                true,
		VisionMaxImages:              2,
		RateLimitUserQPM:             6,
		RateLimitGroupQPM:            20,
		GroupCooldownSeconds:         3,
		MaxConcurrentLLM:             4,

		// Manual sending is a runtime setting (no .env knob): the panel has to be
		// able to turn it off without a redeploy.
		ManualSendEnabled:            true,
		ManualSendPerMinute:          20,
		ManualSendPerConversationQPM: 6,
		ManualSendMaxSegments:        5,

		// 指令面板是纯展示层，默认开启；失败只记日志，不影响机器人。
		CommandPanelEnabled: true,

		TriggerMode:     TriggerAtOnly,
		ContextMode:     ContextPerMember,
		ContextScope:    ContextAddressedOnly,
		GroupAllowlist:  []string{},
		MemberAllowlist: []string{},
		AdminOpenIDs:    []string{},
		GroupOverrides:  []GroupOverride{},
		BlockedKeywords: []string{},
	}
}

func orString(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func orInt(v, def, min, max int) int {
	if v < min || v > max {
		return def
	}
	return v
}

func orFloat(v, def, min, max float64) float64 {
	if v < min || v > max {
		return def
	}
	return v
}

// Manager owns the live settings document.
type Manager struct {
	st         *store.Store
	base       Settings
	cur        atomic.Pointer[Settings]
	overridden map[string]bool
	mu         sync.Mutex
}

// New loads persisted overrides on top of the .env defaults.
func New(ctx context.Context, st *store.Store, base Settings) (*Manager, error) {
	m := &Manager{st: st, base: base, overridden: map[string]bool{}}

	current := base
	if st != nil {
		saved, err := st.LoadSettings(ctx)
		if err != nil {
			return nil, fmt.Errorf("settings: load overrides: %w", err)
		}
		// quote_reply 是 quote_reply_mode 的旧布尔视图；两者都落库时以新键为准，
		// 否则 map 迭代顺序会让 legacy 布尔把档位覆盖回 occasionally / never。
		_, hasQuoteMode := saved["quote_reply_mode"]
		for k, v := range saved {
			if k == "quote_reply" && hasQuoteMode {
				continue
			}
			// A stale, hand-edited or now-forbidden override must never brick
			// startup: skip the offending key and keep the rest.
			candidate := current
			if err := applyKey(&candidate, k, json.RawMessage(v)); err != nil {
				continue
			}
			normalize(&candidate)
			if err := validateRanges(&candidate); err != nil {
				continue
			}
			current = candidate
			m.overridden[k] = true
		}
	}
	m.cur.Store(&current)
	return m, nil
}

// Get returns the current settings document.
func (m *Manager) Get() Settings { return *m.cur.Load() }

// MaskSecrets returns a copy with every credential blanked. It is the single
// place the admin API and the chat tools use, so adding a new secret cannot
// leave one path leaking it.
func MaskSecrets(s Settings) Settings {
	s.OpenAIAPIKey = ""
	s.QQAppSecret = ""
	s.AdminToken = ""
	return s
}

// OverriddenKeys lists fields currently overridden from the admin panel.
func (m *Manager) OverriddenKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.overridden))
	for k := range m.overridden {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// KnownKeys lists every field the admin panel may change.
func KnownKeys() []string {
	m := map[string]json.RawMessage{}
	b, _ := json.Marshal(Defaults())
	_ = json.Unmarshal(b, &m)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Apply merges a partial patch, validates it, persists it and swaps it in.
func (m *Manager) Apply(ctx context.Context, patch map[string]json.RawMessage) (Settings, []string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	known := map[string]bool{}
	for _, k := range KnownKeys() {
		known[k] = true
	}
	next := m.Get()
	changed := map[string]json.RawMessage{}
	// 同一个补丁里同时出现新旧引用键时，新键优先：否则 map 迭代顺序会决定
	// 到底按档位还是按旧布尔生效（两条路径都会镜像对方，结果因此不确定）。
	_, patchHasQuoteMode := patch["quote_reply_mode"]
	for k, raw := range patch {
		if !known[k] {
			return Settings{}, nil, fmt.Errorf("unknown or read-only field %q", k)
		}
		if k == "quote_reply" && patchHasQuoteMode {
			// 仍然把（由档位镜像出来的）布尔落库，让只读旧键的程序看到一致值。
			changed[k] = raw
			continue
		}
		if err := applyKey(&next, k, raw); err != nil {
			return Settings{}, nil, fmt.Errorf("field %q: %w", k, err)
		}
		changed[k] = raw
	}
	normalize(&next)
	if err := Validate(&next); err != nil {
		return Settings{}, nil, err
	}

	if m.st != nil && len(changed) > 0 {
		values := map[string]string{}
		for k := range changed {
			raw, err := fieldRaw(&next, k)
			if err != nil {
				return Settings{}, nil, fmt.Errorf("settings: encode field %q: %w", k, err)
			}
			// Persist the normalised value, not the raw patch.
			values[k] = string(raw)
		}
		if err := m.st.SaveSettings(ctx, values); err != nil {
			return Settings{}, nil, fmt.Errorf("settings: persist: %w", err)
		}
		for k := range changed {
			m.overridden[k] = true
		}
	}
	m.cur.Store(&next)
	return next, []string{}, nil
}

// resetPreservedKeys are the write-only credentials a reset must not drop.
// They are credentials, not tunables: clearing the admin token used to leave the
// panel authenticating an empty password (T15), and clearing the model key made
// every model call fail with no obvious cause.
var resetPreservedKeys = []string{"admin_token", "openai_api_key", "qq_app_secret"}

// Reset drops every persisted override and returns to the built-in defaults,
// preserving the three write-only credentials. If no admin token exists yet, a
// new one is generated and persisted so the panel can never be left with an
// empty password.
func (m *Manager) Reset(ctx context.Context) (Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.Get()
	base := m.base
	normalize(&base)
	base.AdminToken = strings.TrimSpace(current.AdminToken)
	base.OpenAIAPIKey = strings.TrimSpace(current.OpenAIAPIKey)
	base.QQAppSecret = strings.TrimSpace(current.QQAppSecret)
	if base.AdminToken == "" {
		token, err := newAdminToken()
		if err != nil {
			return Settings{}, err
		}
		base.AdminToken = token
	}
	if err := Validate(&base); err != nil {
		return Settings{}, err
	}

	persisted := map[string]bool{}
	if m.st != nil {
		values := map[string]string{}
		for _, key := range resetPreservedKeys {
			raw, err := fieldRaw(&base, key)
			if err != nil {
				return Settings{}, fmt.Errorf("settings: encode reset field %q: %w", key, err)
			}
			// Only write non-empty values: an empty secret is "not configured",
			// and persisting "" would stop SeedFromEnv from importing it later.
			if string(raw) == "\"\"" {
				continue
			}
			values[key] = string(raw)
			persisted[key] = true
		}
		if err := m.st.DeleteAllSettings(ctx); err != nil {
			return Settings{}, fmt.Errorf("settings: reset: %w", err)
		}
		if err := m.st.SaveSettings(ctx, values); err != nil {
			return Settings{}, fmt.Errorf("settings: persist preserved credentials: %w", err)
		}
	}
	m.overridden = persisted
	m.cur.Store(&base)
	return base, nil
}

// newAdminToken mirrors the startup token: a stable prefix plus 128 bits of
// randomness, so the value can be recognised in a log line without being guessed.
func newAdminToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("settings: generate admin token: %w", err)
	}
	return "botharness-" + hex.EncodeToString(buf), nil
}

// Effective returns the settings in force.
//
// Trigger and context mode are plain stored configuration now: whether a group
// actually delivers non-@ messages is a platform-side setting that differs per
// group, so it is reported as a warning instead of silently overriding the
// operator's choice.
func (m *Manager) Effective() Settings {
	return m.Get()
}

// Validate checks ranges and enum membership.
func Validate(s *Settings) error {
	return validateRanges(s)
}

// validateRanges checks value ranges and enum membership, ignoring the
// permission gate so that stored overrides stay loadable.
func validateRanges(s *Settings) error {
	var problems []string
	if strings.TrimSpace(s.Model) == "" {
		problems = append(problems, "model must not be empty")
	}
	switch s.QQTransport {
	case "", "websocket", "webhook", "both":
	default:
		problems = append(problems, `qq_transport must be "websocket" / "webhook" / "both"`)
	}
	if s.QQShardCount < 1 || s.QQShardCount > 32 {
		problems = append(problems, "qq_shard_count must be within [1, 32]")
	}
	if p := strings.TrimSpace(s.QQWebhookPath); p != "" && !strings.HasPrefix(p, "/") {
		problems = append(problems, "qq_webhook_path must start with /")
	}
	if a := strings.TrimSpace(s.HTTPAddr); a == "" {
		problems = append(problems, "http_addr must not be empty")
	}
	switch s.LogLevel {
	case "", "debug", "info", "warn", "error":
	default:
		problems = append(problems, `log_level must be "debug" / "info" / "warn" / "error"`)
	}
	if s.LogBufferSize < 100 || s.LogBufferSize > 100000 {
		problems = append(problems, "log_buffer_size must be within [100, 100000]")
	}
	if s.MediaMaxBytes < 64<<10 || s.MediaMaxBytes > 64<<20 {
		problems = append(problems, "media_max_bytes must be within [64KiB, 64MiB]")
	}
	if v := strings.TrimSpace(s.OpenAIBaseURL); v != "" {
		// 留空 = 用 .env 的值；填了就必须是个能用的 http(s) 地址，
		// 否则要到第一次模型调用才报错，排查成本很高。
		u, err := url.Parse(v)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("openai_base_url is not a valid URL: %v", err))
		case u.Scheme != "http" && u.Scheme != "https":
			problems = append(problems, "openai_base_url must start with http:// or https://")
		case u.Host == "":
			problems = append(problems, "openai_base_url must include a host")
		}
	}
	if s.Temperature < 0 || s.Temperature > 2 {
		problems = append(problems, "temperature must be within [0, 2]")
	}
	if s.MaxTokens < 1 || s.MaxTokens > 10_000_000 {
		problems = append(problems, "max_tokens must be within [1, 10000000]")
	}
	if s.RequestTimeoutSeconds < 5 || s.RequestTimeoutSeconds > 600 {
		problems = append(problems, "request_timeout_seconds must be within [5, 600]")
	}
	if s.ContextMaxTurns < 1 || s.ContextMaxTurns > 5000 {
		problems = append(problems, "context_max_turns must be within [1, 100]")
	}
	if s.ContextMaxTokens < 256 || s.ContextMaxTokens > 10_000_000 {
		problems = append(problems, "context_max_tokens must be within [256, 10000000]")
	}
	if s.ModelContextTokens < 4096 || s.ModelContextTokens > 10_000_000 {
		problems = append(problems, "model_context_tokens must be within [4096, 10000000]")
	}
	if s.HistoryTTLHours < 1 || s.HistoryTTLHours > 8760 {
		problems = append(problems, "history_ttl_hours must be within [1, 8760]")
	}
	if s.ReplyMaxChars < 50 || s.ReplyMaxChars > 5000 {
		problems = append(problems, "reply_max_chars must be within [50, 5000]")
	}
	if s.ReplyMaxSegments < 1 || s.ReplyMaxSegments > 5 {
		// The passive-reply window allows at most 5 replies per message.
		problems = append(problems, "reply_max_segments must be within [1, 5]")
	}
	if s.VisionMaxImages < 1 || s.VisionMaxImages > 6 {
		problems = append(problems, "vision_max_images must be within [1, 6]")
	}
	if s.EarlyReplyDelaySecs < 1 || s.EarlyReplyDelaySecs > 60 {
		problems = append(problems, "early_reply_delay_seconds must be within [1, 60]")
	}
	if len([]rune(s.EarlyReplyText)) > 200 {
		problems = append(problems, "early_reply_text must be at most 200 characters")
	}
	if s.EarlyReplyEnabled && strings.TrimSpace(s.EarlyReplyText) == "" {
		problems = append(problems, "early_reply_text must not be empty while early_reply_enabled is true")
	}
	if s.RateLimitUserQPM < 0 || s.RateLimitUserQPM > 600 {
		problems = append(problems, "rate_limit_user_qpm must be within [0, 600]")
	}
	if s.RateLimitGroupQPM < 0 || s.RateLimitGroupQPM > 600 {
		problems = append(problems, "rate_limit_group_qpm must be within [0, 600]")
	}
	if s.GroupCooldownSeconds < 0 || s.GroupCooldownSeconds > 600 {
		problems = append(problems, "group_cooldown_seconds must be within [0, 600]")
	}
	if s.MaxConcurrentLLM < 1 || s.MaxConcurrentLLM > 64 {
		problems = append(problems, "max_concurrent_llm must be within [1, 64]")
	}
	if s.MemoryInjectMax < 1 || s.MemoryInjectMax > 20 {
		problems = append(problems, "memory_inject_max must be within [1, 20]")
	}
	switch s.ProactiveLevel {
	case "", "off", "auto":
	default:
		problems = append(problems, "proactive_level must be off/auto")
	}
	if s.ProactiveRatio < 3 || s.ProactiveRatio > 100 {
		problems = append(problems, "proactive_ratio must be within [3, 100]")
	}
	if s.ProactiveMaxCredits < 1 || s.ProactiveMaxCredits > 10 {
		problems = append(problems, "proactive_max_credits must be within [1, 10]")
	}
	if s.ProactiveQuietStart < 0 || s.ProactiveQuietStart > 23 || s.ProactiveQuietEnd < 0 || s.ProactiveQuietEnd > 23 {
		problems = append(problems, "proactive quiet hours must be within [0, 23]")
	}
	if s.ProactiveMinIntervalMinutes < 1 || s.ProactiveMinIntervalMinutes > 240 {
		problems = append(problems, "proactive_min_interval_minutes must be within [1, 240]")
	}
	if s.TasksMaxPerMember < 1 || s.TasksMaxPerMember > 50 {
		problems = append(problems, "tasks_max_per_member must be within [1, 50]")
	}
	if s.TasksMaxDelayHours < 1 || s.TasksMaxDelayHours > 720 {
		problems = append(problems, "tasks_max_delay_hours must be within [1, 720]")
	}
	if s.CommitmentMaxOpenPerMember < 1 || s.CommitmentMaxOpenPerMember > 50 {
		problems = append(problems, "commitment_max_open_per_member must be within [1, 50]")
	}
	if s.HumanStatusTriggerMessages < 5 || s.HumanStatusTriggerMessages > 200 {
		problems = append(problems, "human_status_trigger_messages must be within [5, 200]")
	}
	if s.HumanStatusTTLMinutes < 10 || s.HumanStatusTTLMinutes > 1440 {
		problems = append(problems, "human_status_ttl_minutes must be within [10, 1440]")
	}
	if s.HumanStatusTimelineMessages < 20 || s.HumanStatusTimelineMessages > 400 {
		problems = append(problems, "human_status_timeline_messages must be within [20, 400]")
	}
	if strings.TrimSpace(s.AffectPack) == "" {
		problems = append(problems, "affect_pack must not be empty")
	}
	switch s.AffectInjectMode {
	case "", "off", "line", "full":
	default:
		problems = append(problems, "affect_inject_mode must be off/line/full")
	}
	switch s.AffectEventClassifier {
	case "", "off", "rules":
	default:
		problems = append(problems, "affect_event_classifier must be off/rules")
	}
	if s.AffectTickSeconds < 60 || s.AffectTickSeconds > 1800 {
		problems = append(problems, "affect_tick_seconds must be within [60, 1800]")
	}
	if s.AffectInjectMaxTokens < 40 || s.AffectInjectMaxTokens > 300 {
		problems = append(problems, "affect_inject_max_tokens must be within [40, 300]")
	}
	switch s.JoinGateMode {
	case "", "off", "llm":
	default:
		problems = append(problems, "join_gate_mode must be off/llm")
	}
	if s.JoinGateWindow < 5 || s.JoinGateWindow > 50 {
		problems = append(problems, "join_gate_window must be within [5, 50]")
	}
	if s.JoinGateConfidence < 0 || s.JoinGateConfidence > 1 {
		problems = append(problems, "join_gate_confidence must be within [0, 1]")
	}
	if s.JoinGateEvalCooldownSeconds < 10 || s.JoinGateEvalCooldownSeconds > 3600 {
		problems = append(problems, "join_gate_eval_cooldown_seconds must be within [10, 3600]")
	}
	if s.JoinGateMinIntervalMinutes < 1 || s.JoinGateMinIntervalMinutes > 240 {
		problems = append(problems, "join_gate_min_interval_minutes must be within [1, 240]")
	}
	if s.JoinGateMaxPerHour < 1 || s.JoinGateMaxPerHour > 30 {
		problems = append(problems, "join_gate_max_per_hour must be within [1, 30]")
	}
	if s.JoinGateMaxPerDay < 1 || s.JoinGateMaxPerDay > 100 {
		problems = append(problems, "join_gate_max_per_day must be within [1, 100]")
	}
	if s.JoinGateWaitRetrySeconds < 10 || s.JoinGateWaitRetrySeconds > 300 {
		problems = append(problems, "join_gate_wait_retry_seconds must be within [10, 300]")
	}
	if s.JoinGateTopicDormantMinutes < 5 || s.JoinGateTopicDormantMinutes > 240 {
		problems = append(problems, "join_gate_topic_dormant_minutes must be within [5, 240]")
	}
	if s.JoinGateTopicClosedMinutes < 30 || s.JoinGateTopicClosedMinutes > 1440 {
		problems = append(problems, "join_gate_topic_closed_minutes must be within [30, 1440]")
	}
	if s.JoinGateTopicClosedMinutes <= s.JoinGateTopicDormantMinutes {
		// 否则 closed 会在 dormant 之前触发，两个档位就失去意义。
		problems = append(problems, "join_gate_topic_closed_minutes must be greater than join_gate_topic_dormant_minutes")
	}
	switch s.ExpressionMode {
	case "", "off", "fast", "precise":
	default:
		problems = append(problems, "expression_mode must be off/fast/precise")
	}
	if s.ExpressionRecallLimit < 4 || s.ExpressionRecallLimit > 50 {
		problems = append(problems, "expression_recall_limit must be within [4, 50]")
	}
	if s.ExpressionInjectMax < 1 || s.ExpressionInjectMax > 5 {
		problems = append(problems, "expression_inject_max must be within [1, 5]")
	}
	if s.ExpressionLearnEvery < 1 || s.ExpressionLearnEvery > 100 {
		problems = append(problems, "expression_learn_every must be within [1, 100]")
	}
	switch s.HumanizeLevel {
	case "", "off", "soft", "medium", "strong":
	default:
		problems = append(problems, "humanize_level must be off/soft/medium/strong")
	}
	if s.HumanizeDelayScale < 0.5 || s.HumanizeDelayScale > 3 {
		problems = append(problems, "humanize_delay_scale must be within [0.5, 3]")
	}
	switch s.HumanizeSegmentSplit {
	case "", "off", "soft", "medium", "strong":
	default:
		problems = append(problems, "humanize_segment_split must be off/soft/medium/strong")
	}
	if s.HumanizeTypingSecondsPerChar < 0.05 || s.HumanizeTypingSecondsPerChar > 1 {
		problems = append(problems, "humanize_typing_seconds_per_char must be within [0.05, 1]")
	}
	if s.HumanizeTypingMaxSeconds < 0 || s.HumanizeTypingMaxSeconds > 30 {
		problems = append(problems, "humanize_typing_max_seconds must be within [0, 30]")
	}
	switch s.ReplyDebounceMode {
	case "", "off", "fast", "natural", "steady":
	default:
		problems = append(problems, "reply_debounce_mode must be off/fast/natural/steady")
	}
	if s.ReplyDebounceQuietSeconds < 0 || s.ReplyDebounceQuietSeconds > 15 {
		problems = append(problems, "reply_debounce_quiet_seconds must be within [0, 15]")
	}
	if s.ReplyDebounceActiveSeconds < 0 || s.ReplyDebounceActiveSeconds > 15 {
		problems = append(problems, "reply_debounce_active_seconds must be within [0, 15]")
	}
	switch s.QuoteReplyMode {
	case "", QuoteReplyNever, QuoteReplyOccasionally, QuoteReplyOften:
	default:
		problems = append(problems, "quote_reply_mode must be never/occasionally/often")
	}
	if len(s.ReplyStyleVariants) > ReplyStyleVariantMaxItems {
		problems = append(problems, fmt.Sprintf(
			"reply_style_variants must have at most %d entries", ReplyStyleVariantMaxItems))
	}
	for i, variant := range s.ReplyStyleVariants {
		if n := len([]rune(variant)); n > ReplyStyleVariantMaxRunes {
			problems = append(problems, fmt.Sprintf(
				"reply_style_variants[%d] must be at most %d characters", i, ReplyStyleVariantMaxRunes))
		}
	}
	if s.ReplyStyleVariantProbability < 0 || s.ReplyStyleVariantProbability > 1 {
		problems = append(problems, "reply_style_variant_probability must be within [0, 1]")
	}
	if s.PolishTemperature < 0 || s.PolishTemperature > 2 {
		problems = append(problems, "polish_temperature must be within [0, 2]")
	}
	// 0 是「自动」：按草稿估算 token ×1.5 取上下限，所以合法值与固定上限并存。
	// 显式上限是 [1, 8192]，与后台控件（web/src/pages/settings/ReplySettings.vue 的
	// min=0 / max=8192）保持一致 —— 界面能选出来的值后端都必须接受。
	if s.PolishMaxTokens < 0 || s.PolishMaxTokens > 8192 {
		problems = append(problems, "polish_max_tokens must be 0 (auto) or within [1, 8192]")
	}
	if s.PolishTimeoutSeconds < 5 || s.PolishTimeoutSeconds > 120 {
		problems = append(problems, "polish_timeout_seconds must be within [5, 120]")
	}
	if s.PolishMinChars < 0 || s.PolishMinChars > 2000 {
		problems = append(problems, "polish_min_chars must be within [0, 2000]")
	}
	if s.ExecTimeoutSeconds < 3 || s.ExecTimeoutSeconds > 60 {
		problems = append(problems, "exec_timeout_seconds must be within [3, 60]")
	}
	if s.ExecMaxOutputBytes < 512 || s.ExecMaxOutputBytes > 65536 {
		problems = append(problems, "exec_max_output_bytes must be within [512, 65536]")
	}
	if s.WebSearchProvider != "" && s.WebSearchProvider != "searxng" {
		problems = append(problems, `web_search_provider must be "searxng" or empty`)
	}
	if v := strings.TrimSpace(s.WebSearchBaseURL); v != "" {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			problems = append(problems, "web_search_base_url must be an absolute http(s) URL")
		}
	}
	if s.MemoryExtractEvery < 1 || s.MemoryExtractEvery > 50 {
		problems = append(problems, "memory_extract_every must be within [1, 50]")
	}
	if s.MemoryMaxPerScope < 20 || s.MemoryMaxPerScope > 5000 {
		problems = append(problems, "memory_max_per_scope must be within [20, 5000]")
	}
	if s.ToolsMaxSteps < 1 || s.ToolsMaxSteps > 12 {
		problems = append(problems, "tools_max_steps must be within [1, 12]")
	}
	if s.ToolsTotalBudgetSeconds < 10 || s.ToolsTotalBudgetSeconds > 300 {
		problems = append(problems, "tools_total_budget_seconds must be within [10, 300]")
	}
	if s.ToolsResultMaxChars < 500 || s.ToolsResultMaxChars > 20000 {
		problems = append(problems, "tools_result_max_chars must be within [500, 20000]")
	}
	if s.ManualSendPerMinute < 0 || s.ManualSendPerMinute > 600 {
		problems = append(problems, "manual_send_per_minute must be within [0, 600]")
	}
	if s.ManualSendPerConversationQPM < 0 || s.ManualSendPerConversationQPM > 600 {
		problems = append(problems, "manual_send_per_conversation_qpm must be within [0, 600]")
	}
	if s.ManualSendMaxSegments < 1 || s.ManualSendMaxSegments > 10 {
		// Active messages have no platform-side 5-reply ceiling, but every segment
		// spends the group's daily quota, so the cap stays deliberately low.
		problems = append(problems, "manual_send_max_segments must be within [1, 10]")
	}

	switch s.TriggerMode {
	case TriggerAtOnly, TriggerAllMessages:
	default:
		problems = append(problems, fmt.Sprintf("trigger_mode must be %s|%s", TriggerAtOnly, TriggerAllMessages))
	}
	switch s.ContextMode {
	case ContextPerMember, ContextShared:
	default:
		problems = append(problems, fmt.Sprintf("context_mode must be %s|%s", ContextPerMember, ContextShared))
	}
	switch s.ContextScope {
	case ContextAddressedOnly, ContextIncludeUnaddressed:
	default:
		problems = append(problems,
			fmt.Sprintf("context_scope must be %s|%s", ContextAddressedOnly, ContextIncludeUnaddressed))
	}

	for i, ov := range s.GroupOverrides {
		if strings.TrimSpace(ov.GroupOpenID) == "" {
			problems = append(problems, fmt.Sprintf("group_overrides[%d].group_openid must not be empty", i))
		}
		if ov.TriggerMode != "" && ov.TriggerMode != TriggerAtOnly && ov.TriggerMode != TriggerAllMessages {
			problems = append(problems, fmt.Sprintf("group_overrides[%d].trigger_mode invalid", i))
		}
		if ov.ContextMode != "" && ov.ContextMode != ContextPerMember && ov.ContextMode != ContextShared {
			problems = append(problems, fmt.Sprintf("group_overrides[%d].context_mode invalid", i))
		}
		if ov.ContextScope != "" && ov.ContextScope != ContextAddressedOnly && ov.ContextScope != ContextIncludeUnaddressed {
			problems = append(problems, fmt.Sprintf("group_overrides[%d].context_scope invalid", i))
		}
		if ov.RateLimitGroupQPM < 0 || ov.RateLimitGroupQPM > 600 {
			problems = append(problems, fmt.Sprintf("group_overrides[%d].rate_limit_group_qpm out of range", i))
		}
		// Per-group enum fields are applied verbatim by GroupConfig, so an invalid
		// level written through settings_write would take effect as-is.
		switch derefLevel(ov.Humanize) {
		case "", "off", "soft", "medium", "strong":
		default:
			problems = append(problems, fmt.Sprintf("group_overrides[%d].humanize invalid", i))
		}
		switch derefLevel(ov.Proactive) {
		case "", "off", "auto":
		default:
			problems = append(problems, fmt.Sprintf("group_overrides[%d].proactive invalid", i))
		}
		switch derefLevel(ov.AffectInjectMode) {
		case "", "off", "line", "full":
		default:
			problems = append(problems, fmt.Sprintf("group_overrides[%d].affect_inject_mode invalid", i))
		}
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// derefLevel unwraps an optional per-group enum. nil means "follow the global
// setting", which is what the empty string signals during validation.
func derefLevel(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// GroupConfig returns the effective trigger/context/limits for one group.
func (m *Manager) GroupConfig(groupOpenID string) GroupConfig {
	s := m.Get()
	cfg := GroupConfig{
		Enabled:          true,
		TriggerMode:      s.TriggerMode,
		ContextMode:      s.ContextMode,
		ContextScope:     s.ContextScope,
		ReplyToBots:      s.ReplyToBots,
		QuoteReply:       s.QuoteReply,
		QuoteReplyMode:   s.QuoteReplyMode,
		ToolsEnabled:     s.ToolsEnabled,
		MemoryEnabled:    s.MemoryEnabled,
		WebEnabled:       s.WebEnabled,
		ExecEnabled:      s.ExecEnabled,
		AffectEnabled:    s.AffectEnabled,
		AffectInjectMode: s.AffectInjectMode,
		HumanizeLevel:    s.HumanizeLevel,
		// mode=off 与总开关关闭等效：两者都必须是可靠关闭，不能让
		// 「enabled=true + mode=off」把未 @ 消息从正常管线里截走。
		JoinGateEnabled: s.JoinGateEnabled && s.JoinGateMode != "off",
		PolishEnabled:   s.PolishEnabled,
		TasksEnabled:    s.TasksEnabled,
		ProactiveLevel:  s.ProactiveLevel,
		RateLimitGroup:  s.RateLimitGroupQPM,
		Cooldown:        s.GroupCooldownSeconds,
	}
	for _, ov := range s.GroupOverrides {
		if ov.GroupOpenID != groupOpenID {
			continue
		}
		if ov.Enabled != nil {
			cfg.Enabled = *ov.Enabled
		}
		if ov.TriggerMode != "" {
			cfg.TriggerMode = ov.TriggerMode
		}
		if ov.ContextMode != "" {
			cfg.ContextMode = ov.ContextMode
		}
		if ov.ContextScope != "" {
			cfg.ContextScope = ov.ContextScope
		}
		if ov.ReplyToBots != nil {
			cfg.ReplyToBots = *ov.ReplyToBots
		}
		if ov.QuoteReply != nil {
			// The override is the legacy boolean (true = occasionally). It can
			// only narrow the global band: false always means never, and a
			// globally-disabled quote reply stays disabled for every group.
			if !*ov.QuoteReply || s.QuoteReplyMode == QuoteReplyNever {
				cfg.QuoteReplyMode = QuoteReplyNever
			} else {
				cfg.QuoteReplyMode = QuoteReplyOccasionally
			}
		}
		if ov.Tools != nil {
			cfg.ToolsEnabled = *ov.Tools
		}
		if ov.Memory != nil {
			cfg.MemoryEnabled = *ov.Memory
		}
		if ov.Web != nil {
			cfg.WebEnabled = *ov.Web
		}
		if ov.Exec != nil {
			cfg.ExecEnabled = *ov.Exec
		}
		if ov.Humanize != nil {
			cfg.HumanizeLevel = *ov.Humanize
		}
		if ov.Affect != nil {
			// The global switch is a master kill switch: a group override can
			// only narrow it (disable / keep enabled), never enable a globally
			// disabled feature.
			cfg.AffectEnabled = s.AffectEnabled && *ov.Affect
		}
		if ov.AffectInjectMode != nil && *ov.AffectInjectMode != "" {
			cfg.AffectInjectMode = *ov.AffectInjectMode
		}
		if ov.JoinGate != nil {
			// 与 affect 相同的总开关语义：全局关时群覆盖打不开；
			// 群 false 永远可以关闭。
			cfg.JoinGateEnabled = s.JoinGateEnabled && *ov.JoinGate
		}
		if ov.Polish != nil {
			cfg.PolishEnabled = *ov.Polish
		}
		if ov.Tasks != nil {
			cfg.TasksEnabled = *ov.Tasks
		}
		if ov.Proactive != nil {
			cfg.ProactiveLevel = *ov.Proactive
		}
		if ov.RateLimitGroupQPM > 0 {
			cfg.RateLimitGroup = ov.RateLimitGroupQPM
		}
		break
	}
	// join_gate_mode=off means the feature is not in the pipeline at all. Without
	// this, enabled=true + mode=off would make maybeJoinGroupChat return false and
	// the dispatcher would observe+drop every un-@ message instead of falling
	// back to the legacy trigger gate.
	if s.JoinGateMode == "off" {
		cfg.JoinGateEnabled = false
	}
	// One more mirror at the end: QuoteReply is what pre-upgrade call sites read,
	// QuoteReplyMode is the new source of truth. Normalising here keeps them in
	// agreement no matter which branch above ran.
	if cfg.QuoteReplyMode == "" {
		cfg.QuoteReplyMode = QuoteReplyOccasionally
	}
	cfg.QuoteReply = cfg.QuoteReplyMode != QuoteReplyNever
	return cfg
}

// GroupConfig is the resolved per-group behaviour.
type GroupConfig struct {
	Enabled      bool
	TriggerMode  string
	ContextMode  string
	ContextScope string
	ReplyToBots  bool
	// QuoteReply is the legacy boolean view of QuoteReplyMode (false only when
	// the resolved mode is "never"). Kept because existing call sites and the
	// admin contract still read it.
	QuoteReply bool
	// QuoteReplyMode is the resolved quote frequency band for this group.
	QuoteReplyMode string
	ToolsEnabled   bool
	MemoryEnabled  bool
	WebEnabled     bool
	ExecEnabled    bool
	// AffectEnabled / AffectInjectMode are the resolved affect switches for this
	// group (global defaults, overridable per group).
	AffectEnabled    bool
	AffectInjectMode string
	// JoinGateEnabled is the resolved 自主加入 switch for this group. The global
	// join_gate_enabled is a master kill switch; a group override can only narrow.
	JoinGateEnabled bool
	HumanizeLevel   string
	// PolishEnabled is the resolved two-stage switch for this group.
	PolishEnabled  bool
	TasksEnabled   bool
	ProactiveLevel string
	RateLimitGroup int
	Cooldown       int
}

// --- field-level plumbing ---

func normalize(s *Settings) {
	// 端点末尾的斜杠会让拼出来的路径变成 //chat/completions，直接在这里收敛掉。
	s.OpenAIBaseURL = strings.TrimRight(strings.TrimSpace(s.OpenAIBaseURL), "/")
	s.OpenAIAPIKey = strings.TrimSpace(s.OpenAIAPIKey)
	s.PolishModel = strings.TrimSpace(s.PolishModel)
	s.JoinGateModel = strings.TrimSpace(s.JoinGateModel)
	s.ExpressionEmbeddingModel = strings.TrimSpace(s.ExpressionEmbeddingModel)
	if s.ExpressionMode == "" {
		// 缺省档位是 fast；off 与总开关关闭等效，但只能由显式配置给出。
		s.ExpressionMode = "fast"
	}
	if s.JoinGateMode == "" {
		// 缺省档位是 llm；off 只能由显式配置或总开关给出。
		s.JoinGateMode = "llm"
	}
	if s.ContextScope == "" {
		s.ContextScope = ContextAddressedOnly
	}
	if s.HumanizeSegmentSplit == "" {
		s.HumanizeSegmentSplit = "medium"
	}
	if s.ReplyDebounceMode == "" {
		s.ReplyDebounceMode = "natural"
	}
	if s.GroupAllowlist == nil {
		s.GroupAllowlist = []string{}
	}
	if s.MemberAllowlist == nil {
		s.MemberAllowlist = []string{}
	}
	if s.AdminOpenIDs == nil {
		s.AdminOpenIDs = []string{}
	}
	if s.GroupOverrides == nil {
		s.GroupOverrides = []GroupOverride{}
	}
	if s.BlockedKeywords == nil {
		s.BlockedKeywords = []string{}
	}
	s.GroupAllowlist = trimList(s.GroupAllowlist)
	s.MemberAllowlist = trimList(s.MemberAllowlist)
	s.AdminOpenIDs = trimList(s.AdminOpenIDs)
	s.BlockedKeywords = trimList(s.BlockedKeywords)
	s.ReplyStyleVariants = trimList(s.ReplyStyleVariants)
	// Quote reply: two representations of one wish. An empty mode means the
	// document predates quote_reply_mode (or only the legacy boolean was ever
	// written), so the legacy value is the source of truth there. When the mode
	// is set it wins, and the legacy boolean is mirrored for old readers.
	switch s.QuoteReplyMode {
	case QuoteReplyNever, QuoteReplyOccasionally, QuoteReplyOften:
		s.QuoteReply = s.QuoteReplyMode != QuoteReplyNever
	default:
		s.QuoteReplyMode = QuoteReplyOccasionally
		if !s.QuoteReply {
			s.QuoteReplyMode = QuoteReplyNever
		}
	}
}

func trimList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func applyKey(dst *Settings, key string, raw json.RawMessage) error {
	switch key {
	case "qq_app_id":
		return json.Unmarshal(raw, &dst.QQAppID)
	case "qq_app_secret":
		return json.Unmarshal(raw, &dst.QQAppSecret)
	case "qq_sandbox":
		return json.Unmarshal(raw, &dst.QQSandbox)
	case "qq_transport":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		dst.QQTransport = strings.ToLower(strings.TrimSpace(v))
		return nil
	case "qq_intents":
		return json.Unmarshal(raw, &dst.QQIntents)
	case "qq_shard_count":
		return json.Unmarshal(raw, &dst.QQShardCount)
	case "qq_webhook_path":
		return json.Unmarshal(raw, &dst.QQWebhookPath)
	case "qq_bot_openid":
		return json.Unmarshal(raw, &dst.QQBotOpenID)
	case "admin_token":
		return json.Unmarshal(raw, &dst.AdminToken)
	case "http_addr":
		return json.Unmarshal(raw, &dst.HTTPAddr)
	case "tls_extra_ca_files":
		return json.Unmarshal(raw, &dst.TLSExtraCAFiles)
	case "tls_insecure_verify":
		return json.Unmarshal(raw, &dst.TLSInsecureVerify)
	case "system_prompt_file":
		return json.Unmarshal(raw, &dst.SystemPromptFile)
	case "log_level":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		dst.LogLevel = strings.ToLower(strings.TrimSpace(v))
		return nil
	case "log_buffer_size":
		return json.Unmarshal(raw, &dst.LogBufferSize)
	case "media_max_bytes":
		return json.Unmarshal(raw, &dst.MediaMaxBytes)
	case "system_prompt":
		return json.Unmarshal(raw, &dst.SystemPrompt)
	case "model":
		return json.Unmarshal(raw, &dst.Model)
	case "temperature":
		return json.Unmarshal(raw, &dst.Temperature)
	case "max_tokens":
		return json.Unmarshal(raw, &dst.MaxTokens)
	case "request_timeout_seconds":
		return json.Unmarshal(raw, &dst.RequestTimeoutSeconds)
	case "context_max_turns":
		return json.Unmarshal(raw, &dst.ContextMaxTurns)
	case "context_max_tokens":
		return json.Unmarshal(raw, &dst.ContextMaxTokens)
	case "model_context_tokens":
		return json.Unmarshal(raw, &dst.ModelContextTokens)
	case "history_ttl_hours":
		return json.Unmarshal(raw, &dst.HistoryTTLHours)
	case "reply_max_chars":
		return json.Unmarshal(raw, &dst.ReplyMaxChars)
	case "reply_max_segments":
		return json.Unmarshal(raw, &dst.ReplyMaxSegments)
	case "early_reply_enabled":
		return json.Unmarshal(raw, &dst.EarlyReplyEnabled)
	case "early_reply_delay_seconds":
		return json.Unmarshal(raw, &dst.EarlyReplyDelaySecs)
	case "early_reply_text":
		return json.Unmarshal(raw, &dst.EarlyReplyText)
	case "proactive_level":
		var level string
		if err := json.Unmarshal(raw, &level); err != nil {
			return err
		}
		switch level {
		case "off", "auto":
			dst.ProactiveLevel = level
			return nil
		}
		return fmt.Errorf("proactive_level must be off/auto")
	case "proactive_ratio":
		return json.Unmarshal(raw, &dst.ProactiveRatio)
	case "proactive_max_credits":
		return json.Unmarshal(raw, &dst.ProactiveMaxCredits)
	case "proactive_quiet_start":
		return json.Unmarshal(raw, &dst.ProactiveQuietStart)
	case "proactive_quiet_end":
		return json.Unmarshal(raw, &dst.ProactiveQuietEnd)
	case "proactive_min_interval_minutes":
		return json.Unmarshal(raw, &dst.ProactiveMinIntervalMinutes)
	case "tasks_enabled":
		return json.Unmarshal(raw, &dst.TasksEnabled)
	case "tasks_max_per_member":
		return json.Unmarshal(raw, &dst.TasksMaxPerMember)
	case "tasks_max_delay_hours":
		return json.Unmarshal(raw, &dst.TasksMaxDelayHours)
	case "commitment_enabled":
		return json.Unmarshal(raw, &dst.CommitmentEnabled)
	case "commitment_max_open_per_member":
		return json.Unmarshal(raw, &dst.CommitmentMaxOpenPerMember)
	case "human_status_enabled":
		return json.Unmarshal(raw, &dst.HumanStatusEnabled)
	case "human_status_trigger_messages":
		return json.Unmarshal(raw, &dst.HumanStatusTriggerMessages)
	case "human_status_ttl_minutes":
		return json.Unmarshal(raw, &dst.HumanStatusTTLMinutes)
	case "human_status_timeline_messages":
		return json.Unmarshal(raw, &dst.HumanStatusTimelineMessages)
	case "human_status_vision_enabled":
		return json.Unmarshal(raw, &dst.HumanStatusVisionEnabled)
	case "human_status_vision_model":
		return json.Unmarshal(raw, &dst.HumanStatusVisionModel)
	case "affect_enabled":
		return json.Unmarshal(raw, &dst.AffectEnabled)
	case "affect_pack":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		dst.AffectPack = strings.TrimSpace(v)
		return nil
	case "affect_inject_mode":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case "off", "line", "full":
			dst.AffectInjectMode = v
			return nil
		}
		return fmt.Errorf("affect_inject_mode must be off/line/full")
	case "affect_event_classifier":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case "off", "rules":
			dst.AffectEventClassifier = v
			return nil
		}
		return fmt.Errorf("affect_event_classifier must be off/rules")
	case "affect_tick_seconds":
		return json.Unmarshal(raw, &dst.AffectTickSeconds)
	case "affect_inject_max_tokens":
		return json.Unmarshal(raw, &dst.AffectInjectMaxTokens)
	case "join_gate_enabled":
		return json.Unmarshal(raw, &dst.JoinGateEnabled)
	case "join_gate_mode":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case "off", "llm":
			dst.JoinGateMode = v
			return nil
		}
		return fmt.Errorf("join_gate_mode must be off/llm")
	case "join_gate_model":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		dst.JoinGateModel = strings.TrimSpace(v)
		return nil
	case "join_gate_window":
		return json.Unmarshal(raw, &dst.JoinGateWindow)
	case "join_gate_confidence":
		return json.Unmarshal(raw, &dst.JoinGateConfidence)
	case "join_gate_eval_cooldown_seconds":
		return json.Unmarshal(raw, &dst.JoinGateEvalCooldownSeconds)
	case "join_gate_min_interval_minutes":
		return json.Unmarshal(raw, &dst.JoinGateMinIntervalMinutes)
	case "join_gate_max_per_hour":
		return json.Unmarshal(raw, &dst.JoinGateMaxPerHour)
	case "join_gate_max_per_day":
		return json.Unmarshal(raw, &dst.JoinGateMaxPerDay)
	case "join_gate_wait_retry_seconds":
		return json.Unmarshal(raw, &dst.JoinGateWaitRetrySeconds)
	case "join_gate_topic_dormant_minutes":
		return json.Unmarshal(raw, &dst.JoinGateTopicDormantMinutes)
	case "join_gate_topic_closed_minutes":
		return json.Unmarshal(raw, &dst.JoinGateTopicClosedMinutes)
	case "expression_enabled":
		return json.Unmarshal(raw, &dst.ExpressionEnabled)
	case "expression_mode":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case "off", "fast", "precise":
			dst.ExpressionMode = v
			return nil
		}
		return fmt.Errorf("expression_mode must be off/fast/precise")
	case "expression_embedding_model":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		dst.ExpressionEmbeddingModel = strings.TrimSpace(v)
		return nil
	case "expression_recall_limit":
		return json.Unmarshal(raw, &dst.ExpressionRecallLimit)
	case "expression_inject_max":
		return json.Unmarshal(raw, &dst.ExpressionInjectMax)
	case "expression_learn_enabled":
		return json.Unmarshal(raw, &dst.ExpressionLearnEnabled)
	case "expression_learn_every":
		return json.Unmarshal(raw, &dst.ExpressionLearnEvery)
	case "expression_review_enabled":
		return json.Unmarshal(raw, &dst.ExpressionReviewEnabled)
	case "expression_share_global":
		return json.Unmarshal(raw, &dst.ExpressionShareGlobal)
	case "humanize_level":
		var level string
		if err := json.Unmarshal(raw, &level); err != nil {
			return err
		}
		switch level {
		case "off", "soft", "medium", "strong":
			dst.HumanizeLevel = level
			return nil
		}
		return fmt.Errorf("humanize_level must be off/soft/medium/strong")
	case "humanize_delay_scale":
		return json.Unmarshal(raw, &dst.HumanizeDelayScale)
	case "humanize_segment_split":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case "off", "soft", "medium", "strong":
			dst.HumanizeSegmentSplit = v
			return nil
		}
		return fmt.Errorf("humanize_segment_split must be off/soft/medium/strong")
	case "humanize_typing_seconds_per_char":
		return json.Unmarshal(raw, &dst.HumanizeTypingSecondsPerChar)
	case "humanize_typing_max_seconds":
		return json.Unmarshal(raw, &dst.HumanizeTypingMaxSeconds)
	case "reply_debounce_mode":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case "off", "fast", "natural", "steady":
			dst.ReplyDebounceMode = v
			return nil
		}
		return fmt.Errorf("reply_debounce_mode must be off/fast/natural/steady")
	case "reply_debounce_quiet_seconds":
		return json.Unmarshal(raw, &dst.ReplyDebounceQuietSeconds)
	case "reply_debounce_active_seconds":
		return json.Unmarshal(raw, &dst.ReplyDebounceActiveSeconds)
	case "polish_enabled":
		return json.Unmarshal(raw, &dst.PolishEnabled)
	case "polish_model":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		dst.PolishModel = strings.TrimSpace(v)
		return nil
	case "polish_temperature":
		return json.Unmarshal(raw, &dst.PolishTemperature)
	case "polish_max_tokens":
		return json.Unmarshal(raw, &dst.PolishMaxTokens)
	case "polish_timeout_seconds":
		return json.Unmarshal(raw, &dst.PolishTimeoutSeconds)
	case "polish_min_chars":
		return json.Unmarshal(raw, &dst.PolishMinChars)
	case "persona":
		return json.Unmarshal(raw, &dst.Persona)
	case "web_enabled":
		return json.Unmarshal(raw, &dst.WebEnabled)
	case "web_search_provider":
		return json.Unmarshal(raw, &dst.WebSearchProvider)
	case "web_search_base_url":
		return json.Unmarshal(raw, &dst.WebSearchBaseURL)
	case "exec_enabled":
		return json.Unmarshal(raw, &dst.ExecEnabled)
	case "exec_timeout_seconds":
		return json.Unmarshal(raw, &dst.ExecTimeoutSeconds)
	case "exec_max_output_bytes":
		return json.Unmarshal(raw, &dst.ExecMaxOutputBytes)
	case "memory_enabled":
		return json.Unmarshal(raw, &dst.MemoryEnabled)
	case "memory_inject_max":
		return json.Unmarshal(raw, &dst.MemoryInjectMax)
	case "memory_max_per_scope":
		return json.Unmarshal(raw, &dst.MemoryMaxPerScope)
	case "memory_extract_enabled":
		return json.Unmarshal(raw, &dst.MemoryExtractEnabled)
	case "memory_extract_every":
		return json.Unmarshal(raw, &dst.MemoryExtractEvery)
	case "tools_enabled":
		return json.Unmarshal(raw, &dst.ToolsEnabled)
	case "tools_max_steps":
		return json.Unmarshal(raw, &dst.ToolsMaxSteps)
	case "tools_total_budget_seconds":
		return json.Unmarshal(raw, &dst.ToolsTotalBudgetSeconds)
	case "tools_result_max_chars":
		return json.Unmarshal(raw, &dst.ToolsResultMaxChars)
	case "markdown_enabled":
		return json.Unmarshal(raw, &dst.MarkdownEnabled)
	case "reply_footer_enabled":
		return json.Unmarshal(raw, &dst.ReplyFooterEnabled)
	case "early_reply_recall":
		return json.Unmarshal(raw, &dst.EarlyReplyRecall)
	case "openai_base_url":
		return json.Unmarshal(raw, &dst.OpenAIBaseURL)
	case "openai_api_key":
		return json.Unmarshal(raw, &dst.OpenAIAPIKey)
	case "quote_reply":
		// Legacy boolean: true = occasionally, false = never. Writing it also
		// rewrites the mode, so a patch that only touches the old key still works.
		if err := json.Unmarshal(raw, &dst.QuoteReply); err != nil {
			return err
		}
		if dst.QuoteReply {
			dst.QuoteReplyMode = QuoteReplyOccasionally
		} else {
			dst.QuoteReplyMode = QuoteReplyNever
		}
		return nil
	case "quote_reply_mode":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case QuoteReplyNever, QuoteReplyOccasionally, QuoteReplyOften:
			dst.QuoteReplyMode = v
			// Mirror the legacy boolean for old readers and old admin clients.
			dst.QuoteReply = v != QuoteReplyNever
			return nil
		}
		return fmt.Errorf("quote_reply_mode must be never/occasionally/often")
	case "reply_style_variants":
		return json.Unmarshal(raw, &dst.ReplyStyleVariants)
	case "reply_style_variant_probability":
		return json.Unmarshal(raw, &dst.ReplyStyleVariantProbability)
	case "reply_to_bots":
		return json.Unmarshal(raw, &dst.ReplyToBots)
	case "vision_enabled":
		return json.Unmarshal(raw, &dst.VisionEnabled)
	case "vision_max_images":
		return json.Unmarshal(raw, &dst.VisionMaxImages)
	case "rate_limit_user_qpm":
		return json.Unmarshal(raw, &dst.RateLimitUserQPM)
	case "rate_limit_group_qpm":
		return json.Unmarshal(raw, &dst.RateLimitGroupQPM)
	case "group_cooldown_seconds":
		return json.Unmarshal(raw, &dst.GroupCooldownSeconds)
	case "max_concurrent_llm":
		return json.Unmarshal(raw, &dst.MaxConcurrentLLM)
	case "manual_send_enabled":
		return json.Unmarshal(raw, &dst.ManualSendEnabled)
	case "manual_send_per_minute":
		return json.Unmarshal(raw, &dst.ManualSendPerMinute)
	case "manual_send_per_conversation_qpm":
		return json.Unmarshal(raw, &dst.ManualSendPerConversationQPM)
	case "manual_send_max_segments":
		return json.Unmarshal(raw, &dst.ManualSendMaxSegments)
	case "command_panel_enabled":
		// 布尔开关没有取值范围；先解码成 bool 以保证非法 JSON 仍被拒绝。
		return json.Unmarshal(raw, &dst.CommandPanelEnabled)
	case "trigger_mode":
		return json.Unmarshal(raw, &dst.TriggerMode)
	case "context_mode":
		return json.Unmarshal(raw, &dst.ContextMode)
	case "context_scope":
		return json.Unmarshal(raw, &dst.ContextScope)
	case "group_allowlist":
		return json.Unmarshal(raw, &dst.GroupAllowlist)
	case "member_allowlist":
		return json.Unmarshal(raw, &dst.MemberAllowlist)
	case "admin_openids":
		return json.Unmarshal(raw, &dst.AdminOpenIDs)
	case "group_overrides":
		// 两个坑叠在一起：json.Unmarshal 复用切片底层数组的元素，
		// 而 JSON null 对 string 等非指针字段是 no-op。
		// 于是前端用来表示「跟随全局」的 null 清不掉旧值 —— 先把切片置空，
		// 保证每个元素都是零值再解码。（顺带避免修改到当前生效配置共享的底层数组。）
		dst.GroupOverrides = nil
		return json.Unmarshal(raw, &dst.GroupOverrides)
	case "blocked_keywords":
		return json.Unmarshal(raw, &dst.BlockedKeywords)
	default:
		return fmt.Errorf("unknown field")
	}
}

// fieldRaw extracts the normalised JSON value of one field.
func fieldRaw(s *Settings, key string) (json.RawMessage, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	raw, ok := fields[key]
	if !ok {
		return nil, fmt.Errorf("unknown field")
	}
	return raw, nil
}

// SetGroupQuoteReply sets (or, with nil, clears) the per-group "引用回复" override.
//
// The conversation detail page owns this switch, so it must be changeable without
// touching the whole policies document.
func (m *Manager) SetGroupQuoteReply(ctx context.Context, groupOpenID string, value *bool) error {
	if strings.TrimSpace(groupOpenID) == "" {
		return errors.New("group_openid is required")
	}
	cur := m.Get()
	overrides := make([]GroupOverride, 0, len(cur.GroupOverrides)+1)
	found := false
	for _, ov := range cur.GroupOverrides {
		if ov.GroupOpenID != groupOpenID {
			overrides = append(overrides, ov)
			continue
		}
		found = true
		ov.QuoteReply = value
		// 清掉覆盖时，如果这条覆盖已经没有别的设置了，就整条删掉，
		// 免得策略页的覆盖表格里留下一行空记录。
		if value == nil && overrideIsEmpty(ov) {
			continue
		}
		overrides = append(overrides, ov)
	}
	if !found {
		if value == nil {
			return nil
		}
		overrides = append(overrides, GroupOverride{GroupOpenID: groupOpenID, QuoteReply: value})
	}
	raw, err := json.Marshal(overrides)
	if err != nil {
		return err
	}
	_, _, err = m.Apply(ctx, map[string]json.RawMessage{"group_overrides": raw})
	return err
}

// overrideIsEmpty reports whether an override carries no actual setting.
//
// It must list **every** GroupOverride field: a missed one makes
// SetGroupQuoteReply(nil) drop a row that still holds a real per-group setting
// (D-030 regression). TestOverrideIsEmptyCoversEveryGroupOverrideField guards the count.
func overrideIsEmpty(ov GroupOverride) bool {
	return ov.Enabled == nil &&
		ov.TriggerMode == "" &&
		ov.ContextMode == "" &&
		ov.ContextScope == "" &&
		ov.ReplyToBots == nil &&
		ov.QuoteReply == nil &&
		ov.Tools == nil &&
		ov.Proactive == nil &&
		ov.Tasks == nil &&
		ov.Memory == nil &&
		ov.Humanize == nil &&
		ov.Affect == nil &&
		ov.AffectInjectMode == nil &&
		ov.Polish == nil &&
		ov.JoinGate == nil &&
		ov.Web == nil &&
		ov.Exec == nil &&
		ov.RateLimitGroupQPM == 0
}

// GroupQuoteReplyOverride returns the explicit per-group override, if any.
func (m *Manager) GroupQuoteReplyOverride(groupOpenID string) *bool {
	for _, ov := range m.Get().GroupOverrides {
		if ov.GroupOpenID == groupOpenID {
			return ov.QuoteReply
		}
	}
	return nil
}

// legacyEnvKeys maps the old environment variables onto settings keys. It exists
// only for the one-time migration: a deployment that used to configure these in
// .env keeps working after the switch to database-backed settings.
var legacyEnvKeys = map[string]string{
	"QQ_APP_ID":                "qq_app_id",
	"QQ_APP_SECRET":            "qq_app_secret",
	"QQ_SANDBOX":               "qq_sandbox",
	"QQ_TRANSPORT":             "qq_transport",
	"QQ_INTENTS":               "qq_intents",
	"QQ_SHARD_COUNT":           "qq_shard_count",
	"QQ_WEBHOOK_PATH":          "qq_webhook_path",
	"QQ_BOT_OPENID":            "qq_bot_openid",
	"ADMIN_TOKEN":              "admin_token",
	"HTTP_ADDR":                "http_addr",
	"TLS_EXTRA_CA_FILE":        "tls_extra_ca_files",
	"TLS_INSECURE_SKIP_VERIFY": "tls_insecure_verify",
	"SYSTEM_PROMPT_FILE":       "system_prompt_file",
	"LOG_LEVEL":                "log_level",
	"LOG_BUFFER_SIZE":          "log_buffer_size",
	"MEDIA_MAX_BYTES":          "media_max_bytes",
}

// SeedFromEnv imports legacy environment values for keys that the database has
// never seen. Existing overrides always win, and a key is imported at most once,
// so repeated starts are no-ops. Returns the keys that were seeded.
func (m *Manager) SeedFromEnv(ctx context.Context, lookup func(string) string) ([]string, error) {
	known := map[string]bool{}
	for _, k := range m.OverriddenKeys() {
		known[k] = true
	}
	patch := map[string]json.RawMessage{}
	seeded := make([]string, 0, len(legacyEnvKeys))
	for env, key := range legacyEnvKeys {
		if known[key] {
			continue
		}
		raw := strings.TrimSpace(lookup(env))
		if raw == "" {
			continue
		}
		value, ok := coerceLegacyValue(key, raw)
		if !ok {
			continue
		}
		patch[key] = value
		seeded = append(seeded, key)
	}
	if len(patch) == 0 {
		return nil, nil
	}
	if _, _, err := m.Apply(ctx, patch); err != nil {
		return seeded, err
	}
	sort.Strings(seeded)
	return seeded, nil
}

// coerceLegacyValue converts a textual env value into the JSON the settings
// schema expects for that key.
func coerceLegacyValue(key, raw string) (json.RawMessage, bool) {
	switch key {
	case "qq_sandbox", "tls_insecure_verify":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, false
		}
		return json.RawMessage(strconv.FormatBool(b)), true
	case "qq_intents":
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return nil, false
		}
		return json.RawMessage(strconv.FormatUint(n, 10)), true
	case "qq_shard_count", "log_buffer_size", "media_max_bytes":
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, false
		}
		return json.RawMessage(strconv.FormatInt(n, 10)), true
	case "tls_extra_ca_files":
		var files []string
		for _, part := range strings.Split(raw, string(os.PathListSeparator)) {
			if part = strings.TrimSpace(part); part != "" {
				files = append(files, part)
			}
		}
		if len(files) == 0 {
			return nil, false
		}
		b, _ := json.Marshal(files)
		return b, true
	default:
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, false
		}
		return b, true
	}
}

// MaskSecret renders a credential for display: first 4 chars then asterisks.
func MaskSecret(v string) string {
	if v == "" {
		return ""
	}
	r := []rune(v)
	if len(r) <= 4 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:4]) + strings.Repeat("*", 4)
}

// QQCredentialsPresent reports whether the platform side can authenticate.
func (s Settings) QQCredentialsPresent() bool {
	return strings.TrimSpace(s.QQAppID) != "" && strings.TrimSpace(s.QQAppSecret) != ""
}

// MissingSecrets lists credentials that are absent, for degraded-mode warnings.
func (s Settings) MissingSecrets() []string {
	var out []string
	if strings.TrimSpace(s.QQAppID) == "" {
		out = append(out, "qq_app_id")
	}
	if strings.TrimSpace(s.QQAppSecret) == "" {
		out = append(out, "qq_app_secret")
	}
	return out
}
