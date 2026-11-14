/**
 * ADMIN-API 契约（冻结版 v1）对应的 TypeScript 类型。
 * 来源：docs/ADMIN-API.md —— 该文件是唯一契约，本文件仅做类型映射，不改变语义。
 */

/* ------------------------------------------------------------------ *
 * 通用信封
 * ------------------------------------------------------------------ */

export interface ApiErrorBody {
  code: string
  message: string
}

/** 成功信封：允许契约新增顶层字段（如 restart_required / warnings），前端一律容错。 */
export interface OkEnvelope<T> {
  ok: true
  data: T
  [extra: string]: unknown
}

export interface ErrEnvelope {
  ok: false
  error: ApiErrorBody
  [extra: string]: unknown
}

/* ------------------------------------------------------------------ *
 * 1. 认证
 * ------------------------------------------------------------------ */

export interface LoginResult {
  authenticated: boolean
}

export interface MeResult {
  authenticated: boolean
  username?: string
  auth_mode?: string
}

/* ------------------------------------------------------------------ *
 * 2. 运行状态
 * ------------------------------------------------------------------ */

export type TriggerMode = 'at_only' | 'all_messages'
/** 上下文范围：模型能看到多少群内容（与触发模式解耦）。 */
export type ContextScope = 'addressed_only' | 'include_unaddressed'

export type ContextMode = 'per_member' | 'shared'
/**
 * 表达方式库的召回档位：off 不召回 / fast 关键词匹配（零额外调用）/
 * precise 向量召回（需要 embedding 模型，未配置时后端自动降级 fast）。
 */
export type ExpressionMode = 'off' | 'fast' | 'precise'
export type ConversationScene = 'group_member' | 'group_shared' | 'c2c'
export type MessageDirection = 'in' | 'out'
export type MessageStatus = 'received' | 'replied' | 'ignored' | 'deduped' | 'failed'
  | 'early_reply'
  /** 管理员在会话详情页手动发出的消息。 */
  | 'manual'
  /** Bot 命令处理（由命令触发、不走模型回复的那类消息）。 */
  | 'command'
export type LogLevel = 'debug' | 'info' | 'warn' | 'error'
export type StatsRange = '1h' | '6h' | '24h' | '7d'

export interface WsStatus {
  enabled: boolean
  connected: boolean
  state: string
  session_id?: string | null
  shard?: number[] | null
  gateway_url?: string | null
  heartbeat_interval_ms?: number | null
  latency_ms?: number | null
  last_event_at?: string | null
  reconnects?: number
  resumes?: number
  last_error?: string | null
}

export interface WebhookStatus {
  enabled: boolean
  path?: string
  received_total?: number
  verify_failures?: number
  last_event_at?: string | null
}

export interface QqStatus {
  app_id_masked?: string
  sandbox?: boolean
  token_valid?: boolean
  token_expires_in_seconds?: number | null
  token_refreshes?: number
  last_token_error?: string | null
  transport?: string
}

export interface AiStatus {
  /** 当前**生效**的模型端点（来自数据库设置）。空 = 未配置。 */
  base_url?: string
  base_url_set?: boolean
  api_key_set?: boolean
  model?: string
  in_flight?: number
  max_concurrent?: number
  requests_total?: number
  errors_total?: number
  avg_latency_ms?: number
  last_error?: string | null
}

/** 存储层状态（与具体引擎无关；GET /api/status 的 store）。 */
export interface StoreStatus {
  /** 存储引擎：postgres | sqlite。 */
  driver?: string
  /** 落盘位置（仅本机调试可见；Docker 部署下不对管理后台有意义）。 */
  path?: string
  /** 存储目标：库名 / 连接描述（密码已由后端遮蔽）。 */
  target?: string
  size_bytes?: number
  conversations?: number
  messages?: number
}

export interface StatusCounters {
  events_received?: number
  events_deduped?: number
  events_ignored?: number
  replies_sent?: number
  replies_failed?: number
}

export interface StatusData {
  version?: string
  started_at?: string | null
  uptime_seconds?: number
  transport?: string
  trigger_mode?: TriggerMode
  context_mode?: ContextMode
  /** 平台侧权限汇总（不再有 env 闸门）。 */
  platform?: PlatformSummary
  ws?: WsStatus
  webhook?: WebhookStatus
  qq?: QqStatus
  ai?: AiStatus
  store?: StoreStatus
  counters?: StatusCounters
}

/* ------------------------------------------------------------------ *
 * 3. 配置
 * ------------------------------------------------------------------ */

export interface ConfigSource {
  /** 数据库覆盖了多少个键（配置只有数据库一个来源）。 */
  overrides_from_db?: number
  overridden_keys?: string[]
}

/**
 * 只读快照：只剩「启动期才知道、运行期改不了」的几项。
 *
 * 存储已经与引擎解耦：这里报的是**引擎 + 连接**（密码由后端遮蔽），
 * 不再暴露落盘位置 —— 具体落盘方式对管理后台没有意义。
 */
export interface ReadonlyConfig {
  /** 存储引擎：postgres | sqlite。 */
  driver?: string
  /** 已遮蔽密码的连接描述，例如 postgres://bot@postgres:5432/bot。 */
  storage?: string
  /** 程序版本。 */
  version?: string
}

/** 运行时可改子集。 */
export interface EditableConfig {
  system_prompt?: string
  /**
   * 模型端点（OpenAI 兼容根地址）。**这是端点的唯一来源**，.env 已不再读取；
   * 留空表示未配置，模型调用会直接报错。
   */
  openai_base_url?: string
  /**
   * 模型 API Key。**只进不出**：GET /api/config 永远返回空串，
   * 只用 openai_api_key_set / openai_api_key_source 告诉你有没有设置。
   */
  openai_api_key?: string
  model?: string
  temperature?: number
  max_tokens?: number
  request_timeout_seconds?: number
  context_max_turns?: number
  context_max_tokens?: number
  model_context_tokens?: number
  history_ttl_hours?: number
  reply_max_chars?: number
  reply_max_segments?: number
  rate_limit_user_qpm?: number
  rate_limit_group_qpm?: number
  group_cooldown_seconds?: number
  max_concurrent_llm?: number
  /* 提前回复：模型超过 N 秒仍未产出时，先回一句「让我多想会儿」 */
  early_reply_enabled?: boolean
  early_reply_delay_seconds?: number
  early_reply_text?: string
  /** 自发主动：按人类消息比例攒额度，默认关、按群 opt-in。 */
  proactive_level?: string
  proactive_ratio?: number
  proactive_max_credits?: number
  proactive_quiet_start?: number
  proactive_quiet_end?: number
  proactive_min_interval_minutes?: number
  /** 委托式主动：schedule_task / list_tasks / cancel_task。 */
  tasks_enabled?: boolean
  tasks_max_per_member?: number
  tasks_max_delay_hours?: number
  /**
   * 承诺追问：提醒发出后用户没回应时，由模型按常识决定何时再问，
   * 直到用户说完成 / 延后 / 取消。
   */
  commitment_enabled?: boolean
  /** 每个成员最多同时保留几条未收口承诺（1–50，默认 10）。 */
  commitment_max_open_per_member?: number
  /**
   * 人类状态感知：自动推测聊天对象状态（群聊 / 私聊），
   * 用于调整回复姿态与时机。
   */
  human_status_enabled?: boolean
  /** 累积多少条新消息触发一次状态推测（5–200，默认 20）。 */
  human_status_trigger_messages?: number
  /** 推测结果缓存多久（分钟，10–1440，默认 120）。 */
  human_status_ttl_minutes?: number
  /** 状态推测时取最近多少条消息构建成员时间线（20–400，默认 120）。 */
  human_status_timeline_messages?: number
  /** 允许模型自主调用图片分析工具（默认 true）。 */
  human_status_vision_enabled?: boolean
  /** 图片分析使用的视觉模型名；空串 = 主模型。 */
  human_status_vision_model?: string
  /**
   * 自主加入：未 @ 的群消息由模型判断是否加入（全局总开关，默认关）。
   * 按群覆盖 group_overrides[].join_gate 只能在此基础上进一步关闭。
   */
  join_gate_enabled?: boolean
  /** 自主加入模式：off 只记录不评估 / llm 交给模型判断。 */
  join_gate_mode?: string
  /** 评估使用的模型名；空串 = 主模型。 */
  join_gate_model?: string
  /** 评估时取最近多少条群消息（5–50，默认 20）。 */
  join_gate_window?: number
  /** 模型判断「值得加入」的最低置信度（0–1，默认 0.7）。 */
  join_gate_confidence?: number
  /** 同群两次评估的最小间隔（秒，10–3600，默认 60）。 */
  join_gate_eval_cooldown_seconds?: number
  /** 上次判定 wait 时，满这个秒数可由下一条新消息触发提前重评估（10–300，默认 30）。 */
  join_gate_wait_retry_seconds?: number
  /** 话题多少分钟没新消息算休眠（5–240，默认 20）。 */
  join_gate_topic_dormant_minutes?: number
  /** 话题多少分钟没新消息算结束（30–1440，默认 180，必须大于休眠阈值）。 */
  join_gate_topic_closed_minutes?: number
  /** 两次加入之间的最小间隔（分钟，1–240，默认 5）。 */
  join_gate_min_interval_minutes?: number
  /** 每小时最多加入几次（1–30，默认 3）。 */
  join_gate_max_per_hour?: number
  /** 每天最多加入几次（1–100，默认 12）。 */
  join_gate_max_per_day?: number
  /**
   * 表达方式库：按「情境 → 说法」学习本群的表达习惯，生成前召回几条参考。
   * 全局总开关默认关；关闭时回复管线与没有这个库时完全一致。
   */
  expression_enabled?: boolean
  /** 召回档位：off 不召回 / fast 关键词匹配 / precise 向量召回。 */
  expression_mode?: ExpressionMode
  /** 向量召回使用的 embedding 模型；空串 = 复用主模型端点。 */
  expression_embedding_model?: string
  /** 一次召回最多返回几条候选（4–50，默认 12）。 */
  expression_recall_limit?: number
  /** 一次最多注入几条到提示词（1–5，默认 3）。 */
  expression_inject_max?: number
  /** 从已完成的回合里学习新的表达方式（总开关关时不生效）。 */
  expression_learn_enabled?: boolean
  /** 每 N 个回合学习一次（1–100，默认 20）。 */
  expression_learn_every?: number
  /** 学习结果先进待审队列，由「表达方式」页审核发布。 */
  expression_review_enabled?: boolean
  /** 允许跨群共享学到的表达方式。 */
  expression_share_global?: boolean
  /**
   * 情绪状态：按内容包积累 5 维数值，注入到系统提示词并影响回复行为。
   * 默认关；关闭时现有行为零变化。
   */
  affect_enabled?: boolean
  /** 内容包 id；默认 "daily"（日常心境）。 */
  affect_pack?: string
  /** 注入档位：off 不注入 / line 一行 / full 多段。 */
  affect_inject_mode?: AffectInjectMode
  /** 事件分类器：off 不分类 / rules 按内容包正则匹配。 */
  affect_event_classifier?: AffectEventClassifier
  /** 后台 tick 间隔（秒），范围 [60,1800]，默认 300。 */
  affect_tick_seconds?: number
  /** 单次注入的 token 上限，范围 [40,300]，默认 120。 */
  affect_inject_max_tokens?: number
  /** 接入与运维（原先在 .env，现在同样存数据库）。 */
  qq_app_id?: string
  qq_app_secret?: string
  qq_sandbox?: boolean
  qq_transport?: string
  qq_intents?: number
  qq_shard_count?: number
  qq_webhook_path?: string
  qq_bot_openid?: string
  /**
   * 启动时自动把 Bot 命令同步到 QQ 指令面板（群 / 私聊）。
   * 关掉只影响「启动时自动同步」，仍可在「接入与运维」里手动点「立即同步」。
   */
  command_panel_enabled?: boolean
  admin_token?: string
  http_addr?: string
  tls_extra_ca_files?: string[]
  tls_insecure_verify?: boolean
  system_prompt_file?: string
  log_level?: string
  log_buffer_size?: number
  media_max_bytes?: number

  /** 人味：回复节奏与风格（off/soft/medium/strong）。 */
  humanize_level?: string
  humanize_delay_scale?: number
  /** 分段气泡档位 off/soft/medium/strong。 */
  humanize_segment_split?: string
  /** 每条气泡前的模拟打字速度（秒/字）。 */
  humanize_typing_seconds_per_char?: number
  /** 整轮模拟打字等待上限（秒）。 */
  humanize_typing_max_seconds?: number
  /** 群消息合并档位 off/fast/natural/steady。 */
  reply_debounce_mode?: string
  /** 安静群 / 活跃群的合并窗口（秒）。 */
  reply_debounce_quiet_seconds?: number
  reply_debounce_active_seconds?: number
  persona?: string
  /** 引用频率分档 never/occasionally/often（兼容布尔 quote_reply）。 */
  quote_reply_mode?: string
  /** 兼容布尔视图；读取时用来推导 quote_reply_mode。 */
  quote_reply?: boolean
  /**
   * 口吻轮换：备用的说话方式（每条形如「用 1–2 个字回复」「带一点翻译腔」）。
   * 每轮按 reply_style_variant_probability 的概率抽一条追加到系统提示词；
   * 空数组 = 不启用。
   */
  reply_style_variants?: string[]
  /** 每轮应用一条口吻变体的概率（0–1，默认 0.25）。 */
  reply_style_variant_probability?: number
  /** 联网：web_fetch / web_search。 */
  web_enabled?: boolean
  web_search_provider?: string
  web_search_base_url?: string
  /** 容器内执行：run_shell（默认关）。 */
  exec_enabled?: boolean
  exec_timeout_seconds?: number
  exec_max_output_bytes?: number
  /** 长期记忆：remember/recall 工具 + 系统提示词里的「你记得的事」。 */
  memory_enabled?: boolean
  memory_inject_max?: number
  memory_max_per_scope?: number
  memory_extract_enabled?: boolean
  memory_extract_every?: number
  /** 工具调用总开关：关掉等于纯聊天，不下发任何工具定义。 */
  tools_enabled?: boolean
  /** 一次回复里最多几轮模型调用（每轮都可能执行工具）。 */
  tools_max_steps?: number
  /** 整个工具循环的墙钟预算（秒）。 */
  tools_total_budget_seconds?: number
  /** 单个工具结果进入模型前的截断长度（字符）。 */
  tools_result_max_chars?: number
  /** 以 msg_type=2 发 markdown（平台需单独模板权限；缺失时自动降级为纯文本）。 */
  markdown_enabled?: boolean
  /**
   * 两阶段生成：主模型先出一版草稿，再交给润色模型只改措辞。
   * 关掉就回到「一次生成」，不会产生第二次调用。
   */
  polish_enabled?: boolean
  /** 润色模型名；空串 = 复用主模型（同一个端点，所以必须是该端点认识的模型）。 */
  polish_model?: string
  /** 润色温度，0–2，默认 0.7。 */
  polish_temperature?: number
  /** 润色输出上限；**0 = 自动**（按草稿长度估算，受主模型 max_tokens 约束），范围 0–8192。 */
  polish_max_tokens?: number
  /** 单次润色的墙钟超时（秒），5–120，默认 20；超时直接发草稿。 */
  polish_timeout_seconds?: number
  /** 草稿短于该字数就跳过润色；0 = 不设下限，范围 0–2000。 */
  polish_min_chars?: number
  /* 是否在每条 AI 回复末尾追加「用时 / tokens 估算」 */
  reply_footer_enabled?: boolean
  /* 正式答复发出后撤回那条即时回复 */
  early_reply_recall?: boolean
  /* 让模型看到本轮消息里的图片 */
  vision_enabled?: boolean
  vision_max_images?: number
  /* 后台手动发送：总开关 / 全局每分钟 / 单会话每分钟 / 单次最大分段数 */
  manual_send_enabled?: boolean
  manual_send_per_minute?: number
  manual_send_per_conversation_qpm?: number
  manual_send_max_segments?: number
}

export interface ConfigData {
  source?: ConfigSource
  readonly?: ReadonlyConfig
  editable?: EditableConfig
  warnings?: string[]
  /** 是否已配置模型 API Key（密钥本身永远不会下发）。 */
  openai_api_key_set?: boolean
  /** Key 的来源：db（后台设置过）/ none（未配置）。.env 已不再参与。 */
  openai_api_key_source?: 'db' | 'none' | string
  /** QQ AppSecret 是否已配置（值永不回显）。 */
  qq_app_secret_set?: boolean
  /** 后台口令是否已配置（值永不回显）。 */
  admin_token_set?: boolean
}

/** PUT /api/config 的响应：更新后的完整 editable，可能附带 restart_required。 */
/** PUT /api/config 的响应体（data 部分）。 */
export interface ConfigUpdateResult {
  settings?: EditableConfig
  warnings?: string[]
  restart_required?: string[]
}

/* ------------------------------------------------------------------ *
 * 4. 策略
 * ------------------------------------------------------------------ */

export interface GroupOverride {
  group_openid: string
  /** null / 省略 = 跟随默认（启用）；false = 该群暂停。 */
  enabled?: boolean | null
  trigger_mode?: TriggerMode | null
  context_mode?: ContextMode | null
  /** 该群的上下文范围；null / 省略 = 跟随全局。 */
  context_scope?: ContextScope | null
  /** 该群是否回复其他机器人；null / 省略 = 跟随全局。 */
  reply_to_bots?: boolean | null
  /** 该群是否引用回复触发消息；null / 省略 = 跟随全局。 */
  quote_reply?: boolean | null
  /**
   * 能力覆盖（D-035）。**每一项都必须是可选的**：省略 = 跟随全局，
   * 且保存时必须原样透传 —— 曾经前端按白名单重建覆盖行，一保存就把这七项抹掉。
   */
  /** 该群是否启用两阶段生成（润色）；null / 省略 = 跟随全局。 */
  polish?: boolean | null
  /** 该群是否允许模型调用工具。 */
  tools?: boolean | null
  /** 该群自发主动档位（off / auto）。 */
  proactive?: string | null
  /** 该群定时提醒开关。 */
  tasks?: boolean | null
  /** 该群长期记忆开关。 */
  memory?: boolean | null
  /** 该群人味档位（off / soft / medium / strong）。 */
  humanize?: string | null
  /** 该群联网工具开关。 */
  web?: boolean | null
  /** 该群容器内执行开关。 */
  exec?: boolean | null
  /** 该群情绪状态开关；null / 省略 = 跟随全局。 */
  affect?: boolean | null
  /** 该群情绪注入档位（off / line / full）；null / 省略 = 跟随全局。 */
  affect_inject_mode?: AffectInjectMode | null
  /** 该群是否启用「自主加入」（未 @ 消息由模型判断是否加入）；null / 省略 = 跟随全局。 */
  join_gate?: boolean | null
  /** 0 / null = 不单独限流。 */
  rate_limit_group_qpm?: number | null
}

export interface Policies {
  trigger_mode: TriggerMode
  context_mode: ContextMode
  /** 模型能看到多少群内容。 */
  context_scope: ContextScope
  platform?: PlatformSummary
  group_allowlist: string[]
  member_allowlist: string[]
  admin_openids?: string[]
  group_overrides: GroupOverride[]
  blocked_keywords: string[]
  /** 是否回复「其他机器人 @ 我们」的消息（默认允许）。 */
  reply_to_bots: boolean
  /** 引用回复的兼容布尔视图（true = 非 never）。 */
  quote_reply: boolean
  /** 引用频率分档：never / occasionally / often（默认 occasionally ≈30%）。 */
  quote_reply_mode?: QuoteReplyMode
  warnings?: string[]
}

/* ------------------------------------------------------------------ *
 * 5. 会话与上下文
 * ------------------------------------------------------------------ */

export interface Conversation {
  id: string
  scene: ConversationScene
  group_openid?: string | null
  group_name?: string | null
  member_openid?: string | null
  member_name?: string | null
  enabled: boolean
  message_count: number
  turn_count: number
  token_estimate: number
  last_active_at?: string | null
  created_at?: string | null
}

/** 消息携带的附件；图片已在服务端缓存，用 media_id 取。 */
export interface MessageAttachment {
  kind: 'image' | 'voice' | 'video' | 'file' | string
  media_id?: string
  content_type?: string
  filename?: string
  width?: number
  height?: number
  size?: number
  asr_text?: string
}

export interface ConversationMessage {
  /**
   * dialogue = 机器人参与的轮次；observed = 旁观（未 @ 它）的群消息；
   * observed_bot = 群里另一个机器人的发言（同样只记录、不回复）。
   */
  kind?: 'dialogue' | 'observed' | 'observed_bot' | 'manual'
  /**
   * 平台侧的引用索引（REFIDX_…）。有值才能被引用回复；
   * 升级前写入的老消息没有这个值，右键引用会被后端拒绝。
   */
  quote_index?: string
  /** 该条消息的发送者昵称（助手消息为空）。 */
  speaker?: string
  /**
   * 发言人的 OpenID：昵称在同一个群里会重复，openid 才是唯一标识。
   * 助手消息里是机器人的群内 member_openid（私聊场景没有该标识，故为空）；
   * 升级前写入的历史消息只在能唯一确定发言人时才有值。
   */
  speaker_openid?: string
  id: number
  role: 'user' | 'assistant' | string
  content: string
  tokens?: number
  /** 消息携带的附件；图片已由服务端缓存，可用 media_id 取回。 */
  attachments?: MessageAttachment[]
  created_at?: string | null
}

/** 平台侧的群权限条目。 */
export interface PlatformGroupEntry {
  group_openid: string
  group_name?: string
  /** all | only_mention | mention_and_context */
  recv_msg_setting: string
  allow_proactive_msg?: boolean
}

export interface PlatformSummary {
  known_groups: number
  /** 平台侧已开启「接收所有消息」的群。 */
  full_access_groups: PlatformGroupEntry[]
  /** 平台侧仍是「仅 @机器人」的群。 */
  limited_groups: PlatformGroupEntry[]
}

/** 群会话附带平台侧的群内状态（GET /v2/groups/{id}/bot_state）。 */
export interface PlatformGroupState {
  member_openid?: string
  /** all | only_mention | mention_and_context —— 平台侧真实接收设置。 */
  recv_msg_setting?: string
  allow_proactive_msg?: boolean
  member_role?: string
  joined_at?: string
  fetched_at?: string
  error?: string
}

export interface ConversationDetail extends Conversation {
  messages: ConversationMessage[]
  /** 仅群会话有值；为 null 表示尚未从平台取到。 */
  platform?: PlatformGroupState | null
  /**
   * 该群今天已发出的主动消息条数（含手动发送与机器人的主动兜底）。
   * 平台配额是共享的（未认证 30 条/分钟、单群 1000 条/天），私聊恒为 0。
   */
  active_today?: number
  /** 该群当前生效的「引用回复」开关（兼容布尔视图）。 */
  quote_reply?: boolean
  /** 该群生效的引用频率档位。 */
  quote_reply_mode?: QuoteReplyMode
  /** 按群覆盖值；null 表示跟随全局。 */
  quote_reply_override?: boolean | null
  /** 全局默认值。 */
  quote_reply_global?: boolean
  /** 全局引用频率档位。 */
  quote_reply_global_mode?: QuoteReplyMode
}

/** POST /api/conversations/{id}/messages 的结果。 */
export interface ManualSendResult {
  message_ids: string[]
  message_id: string
  segments: number
  latency_ms: number
  /** 手动发送永远是主动消息。 */
  active: boolean
  /** 这次发送是否带了引用。 */
  quoted: boolean
  message_row_id?: number
  record_id?: number
  active_today: number
}

/** 可合并的群（会话管理页的合并对话框）。 */
export interface MergeableConversation {
  id: string
  scene: ConversationScene | string
  member_name?: string | null
  message_count: number
  token_estimate: number
  last_active_at?: string | null
}

export interface MergeableGroup {
  group_openid: string
  group_name?: string
  shared_conversation_id?: string
  context_mode: ContextMode
  /** 该群当前生效的上下文模式是否为「整群共享」。 */
  mergeable: boolean
  conversations: MergeableConversation[]
}

export interface MergeableGroupsData {
  items: MergeableGroup[]
  global_mode: ContextMode
  total_groups: number
  total_conversations: number
}

export interface MergeGroupResult {
  group_openid: string
  target_id: string
  source_ids: string[]
  moved_messages: number
  deduped_messages: number
  deleted_conversations: number
  target_message_count: number
  target_turn_count: number
  dry_run: boolean
}

export interface Paged<T> {
  total: number
  page: number
  page_size: number
  items: T[]
}

export interface ConversationQuery {
  q?: string
  scene?: ConversationScene | ''
  enabled?: boolean | ''
  page?: number
  page_size?: number
  sort?: string
}

export interface ConversationMessagesQuery {
  limit?: number
  before_id?: number
}

/* ------------------------------------------------------------------ *
 * 6. 消息与日志
 * ------------------------------------------------------------------ */

export interface MessageItem {
  id: number
  direction: MessageDirection
  scene?: ConversationScene | string
  group_openid?: string | null
  member_openid?: string | null
  member_name?: string | null
  content: string
  msg_id?: string | null
  status: MessageStatus
  error?: string | null
  latency_ms?: number | null
  created_at?: string | null
}

export interface MessageQuery {
  direction?: MessageDirection | ''
  status?: MessageStatus | ''
  q?: string
  page?: number
  page_size?: number
  since?: string
  until?: string
}

export interface LogItem {
  id: number
  ts: string
  level: LogLevel
  component?: string
  message: string
  fields?: Record<string, unknown> | null
}

export interface LogQuery {
  level?: LogLevel | ''
  q?: string
  limit?: number
  before_id?: number
}

export interface LogList {
  items: LogItem[]
}

/* ------------------------------------------------------------------ *
 * SSE 事件流
 * ------------------------------------------------------------------ */

export type SseEventName = 'status' | 'message' | 'log' | 'ws' | 'ping'

export interface WsEventPayload {
  state: string
  connected: boolean
}

/** SSE 事件的 data 载荷（按事件名区分）。 */

/* ------------------------------------------------------------------ *
 * 7. 统计
 * ------------------------------------------------------------------ */

export interface StatsPoint {
  ts: string
  in: number
  out: number
  errors: number
  avg_latency_ms?: number
  tokens?: number
}

export interface StatsTopGroup {
  group_openid: string
  group_name?: string | null
  messages: number
}

export interface StatsTotals {
  in: number
  out: number
  errors: number
  avg_latency_ms?: number
  tokens?: number
}

export interface StatsData {
  range: StatsRange
  bucket: string
  series: StatsPoint[]
  top_groups?: StatsTopGroup[]
  totals?: StatsTotals
}

/* ------------------------------------------------------------------ *
 * 8. 运维动作
 * ------------------------------------------------------------------ */

export interface TestAiResult {
  text: string
  latency_ms: number
  model?: string
  usage?: {
    prompt_tokens?: number
    completion_tokens?: number
    total_tokens?: number
  }
}

export interface TestSendResult {
  message_id: string
}

/** 指令面板的作用域：群 / 私聊。 */
export type SyncCommandScope = 'group' | 'c2c'

/** 单项同步结果：新建 / 更新 / 无变化。 */
export type SyncCommandAction = 'created' | 'updated' | 'unchanged'

/** POST /api/actions/sync-commands 的单项结果。 */
export interface SyncCommandItem {
  scope: SyncCommandScope
  /** 平台侧的指令面板 ID。 */
  panel_id: string
  action: SyncCommandAction
  /** 平台侧的面板版本号；新建面板拿不到版本，后端报 0。 */
  version: number
}

/**
 * POST /api/actions/sync-commands 的 data。
 * 失败时契约是 502 + error.code = upstream_error（不返回部分结果）。
 */
export interface SyncCommandsResult {
  results: SyncCommandItem[]
}

/** 动作失败时的 error.code 取值（契约 §8）。 */
/* ------------------------------------------------------------------ *
 * 9. 模型目录（GET /api/models）
 * ------------------------------------------------------------------ */

export interface ModelInfo {
  id: string
  object?: string
  owned_by?: string
  created?: number
}

export interface ModelsData {
  base_url: string
  count: number
  models: ModelInfo[]
  /** 仅包含 id 的便捷数组，顺序与 models 一致。 */
  ids: string[]
  /** 当前生效的模型（来自运行时配置）。 */
  current: string
  /** 当前模型是否出现在上游返回的列表中。 */
  current_in_list: boolean
  /** 本次响应是否命中服务端缓存。 */
  cached: boolean
  fetched_at: string
}

/** 引用回复频率分档：0% / ~30% / ~70%。 */
export type QuoteReplyMode = 'never' | 'occasionally' | 'often'

/** POST /api/conversations/{id}/quote-reply 的结果。 */
export interface QuoteReplyState {
  group_openid: string
  /** 生效的兼容布尔视图（false 仅当生效档位是 never）。 */
  quote_reply: boolean
  /** 该群生效的引用频率档位。 */
  quote_reply_mode?: QuoteReplyMode
  /** 按群覆盖值；null 表示跟随全局。 */
  override?: boolean | null
  /** 全局兼容布尔视图。 */
  global: boolean
  /** 全局档位。 */
  global_mode?: QuoteReplyMode
}

/* ------------------------------------------------------------------ *
 * 自主加入（join_gate）
 * ------------------------------------------------------------------ */

/** 话题类型（模型输出；缺省 / 未知按 other 展示）。 */
export type JoinTopicType = 'help' | 'chat' | 'question' | 'plan' | 'conflict' | 'joke' | 'other'

/**
 * 话题生命周期（模型输出，后端会按时间只降不升）：
 * active 进行中 / dormant 暂歇 / closed 已结束。
 * 老数据（契约 v2 之前）没有这个字段，页面按 active 展示。
 */
export type JoinTopicStatus = 'active' | 'dormant' | 'closed'

/** 话题里的一条消息（message_ids 对应群消息的本地 id）。 */
export interface JoinGateTopic {
  id: string
  title: string
  summary: string
  message_ids: number[]
  /** 话题类型；老数据 / 模型未给时为空。 */
  type?: JoinTopicType | string
  /** 话题状态；老数据 / 模型未给时按 active 展示。 */
  status?: JoinTopicStatus | string
  /** 这个话题还值不值得被新消息接回（closed 恒为 false，后端解析时已强制）。 */
  resumable?: boolean
}

/** 一条加入评估决策（joined=false 也会记录，便于解释「为什么没接」）。 */
export interface JoinDecisionItem {
  id: number
  group_openid: string
  conversation_id: string
  /** 触发评估的那条群消息 id。 */
  message_id: string
  speaker: string
  /** 消息预览（后端截断到 80 字）。 */
  preview: string
  joined: boolean
  /**
   * 三分类判定：join 插话 / wait 等一等 / skip 不插话。
   * 契约 v2 之后后端才返回；老库里没有该列时为空，前端按 joined 兜底。
   */
  action?: 'join' | 'wait' | 'skip' | string
  /** 模型判断这条消息是冲着谁说的（bot / group / someone_else / unknown）。 */
  addressed_to?: string
  confidence: number
  reason: string
  /**
   * 该次判定的话题。存储层是 TEXT（JSON 字符串），但 ADMIN-API 契约示例
   * 与未来后端可能直接返回数组或 {topics:[…]}，页面解析统一容错。
   */
  topics: string | JoinGateTopic[]
  model: string
  latency_ms: number
  created_at: string
}

/** 某个群最近一次话题分析结果。 */
export interface JoinTopicsState {
  group_openid: string
  /** 话题（JSON 字符串或数组）；结构同 JoinGateTopic[]。 */
  topics: string | JoinGateTopic[]
  analyzed_at: string
}

export interface JoinDecisionsData {
  decisions: JoinDecisionItem[]
}

export interface JoinTopicsData {
  topics: JoinTopicsState | null
}

/* ------------------------------------------------------------------ *
 * 工具调用审计（GET /api/tool-calls）
 * ------------------------------------------------------------------ */

export interface ToolCallItem {
  id: number
  conversation_id: string
  group_openid: string
  member_openid: string
  tool: string
  args: string
  status: string
  error: string
  latency_ms: number
  result_chars: number
  created_at: string
}

/* ------------------------------------------------------------------ *
 * 记忆（GET /api/memories）
 * ------------------------------------------------------------------ */

export interface MemoryItem {
  id: number
  scope: 'global' | 'group' | 'member' | string
  group_openid: string
  member_openid: string
  kind: string
  text: string
  source: string
  salience: number
  hits: number
  last_used_at: string
  created_at: string
}

/* ------------------------------------------------------------------ *
 * 表达方式库（GET /api/expressions）
 *
 * 一行 = 一条「当 <情境> 时，可以用 <说法>」的语料。学习（core）写入，
 * 后台只做审核与人工维护：新增 / 启停 / 删除 / 强化。
 * ------------------------------------------------------------------ */

export interface ExpressionItem {
  id: number
  /** 归属群；空 = 全局（管理员新增或显式共享）。 */
  group_openid: string
  /** 情境：什么场合下这么说。 */
  situation: string
  /** 说法：这个场合下可以怎么说。 */
  style: string
  /** 来源：admin 人工新增 / learn 模型学习 / import 导入。 */
  source: string
  enabled: boolean
  /** 被召回 / 使用的次数，用于观察是否有用。 */
  use_count: number
  /** 人工强化次数：被强化的行在召回排序里更靠前。 */
  weight?: number
  /** 向量召回用的向量（后端不下发时缺省）。 */
  has_embedding?: boolean
  created_at: string
  updated_at?: string
}

export interface ExpressionQuery {
  group_openid?: string
  enabled?: boolean | ''
  q?: string
  page?: number
  page_size?: number
}

export interface ExpressionInput {
  group_openid: string
  situation: string
  style: string
}

/* ------------------------------------------------------------------ *
 * 定时任务（GET /api/tasks）
 * ------------------------------------------------------------------ */

export interface TaskItem {
  id: number
  conversation_id: string
  group_openid: string
  member_openid: string
  /** 谁安排的：只有这个人能在聊天里列出/取消它 */
  creator_openid: string
  kind: string
  text: string
  due_at: string
  status: string
  attempts: number
  lease_until: string
  error: string
  created_at: string
  sent_at: string
}

/* ------------------------------------------------------------------ *
 * 承诺（GET /api/commitments）
 * ------------------------------------------------------------------ */

/**
 * 一条承诺（open loop），字段与后端 store.Commitment 的 JSON 一一对应。
 *
 * GET /api/commitments 返回**完整历史**（不过滤状态），因此列表里也会出现
 * done / cancelled 的终态行。状态机：waiting → leased（调度器抢到执行权）
 * → 发送成功后回到 waiting；done / cancelled 是终态。
 */
export interface CommitmentItem {
  id: number
  conversation_id: string
  group_openid: string
  member_openid: string
  /** 建立承诺的人；聊天工具按它隔离。 */
  creator_openid: string
  /** 承诺事项原文。 */
  text: string
  /** waiting 等待追问 / leased 追问中 / done 已完成 / cancelled 已取消。 */
  status: 'waiting' | 'leased' | 'done' | 'cancelled' | string
  /** 0–3（0 最低）。 */
  priority: number
  /** 可选截止时间；空串 = 未设置。 */
  deadline_at: string
  /** 下次追问时间；leased 时它是租约到期时间。 */
  next_check_at: string
  /** 上次追问时间；空串 = 还没追问过。 */
  last_nudge_at: string
  /** 已追问次数。 */
  nudge_count: number
  /** 收口时间；空串 = 未收口。 */
  ack_at: string
  /** 用户回执原话（done / cancelled 时记录）。 */
  ack_note: string
  /** 预留扩展（当前为空串）。 */
  meta: string
  created_at: string
}

/* ------------------------------------------------------------------ *
 * 自发主动状态（GET /api/proactive）
 * ------------------------------------------------------------------ */

export interface ProactiveStateItem {
  group_openid: string
  last_sent_at: string
  silent_streak: number
  sends: number
  last_reason: string
  updated_at: string
}

export interface ProactiveOverview {
  items: ProactiveStateItem[]
  count: number
  level: string
  ratio: number
  max_credits: number
  quiet_hours: number[]
  min_interval_minutes: number
}


/* ------------------------------------------------------------------ *
 * 情绪状态（Affect）
 * ------------------------------------------------------------------ */

/** 注入档位。 */
export type AffectInjectMode = 'off' | 'line' | 'full'
/** 事件分类器。 */
export type AffectEventClassifier = 'off' | 'rules'

/** 5 维数值（key 为内容包里的维度 key，值 0–100）。 */
export type AffectDims = Record<string, number>

export interface AffectPackDimension {
  key: string
  label: string
  min: number
  max: number
  baseline: number
  half_life_hours?: number
}

export interface AffectPackPreset {
  id: string
  name: string
  dims?: AffectDims
}

/** 内容包元信息（GET /api/affect/packs）。 */
export interface AffectPack {
  id: string
  name: string
  version?: string
  description?: string
  /**
   * 内容包的维度信息。内置目录接口（affect.PackInfo）只回 key 列表，
   * 而完整内容包会回对象数组；两种形状都要兼容。
   */
  dimensions?: AffectPackDimension[] | string[]
  /** 完整内容包是对象数组；目录接口只回数量。 */
  presets?: AffectPackPreset[] | number
  /** 完整内容包是事件定义数组；目录接口只回数量。 */
  events?: number | { id: string; label?: string; tone?: string }[]
}

/** 会话情绪状态摘要（GET /api/affect/conversations）。 */
export interface AffectConversation {
  id: string
  pack: string
  updated_at: string
  dims?: AffectDims
}

/** 某会话的完整情绪状态。 */
export interface AffectState {
  conversation_id: string
  pack_id: string
  /** 后端 Snapshot 会带上内容包显示名；目录接口没有时前端回退到 id。 */
  pack_name?: string
  dims: AffectDims
  /** 最近事件标记（id + 时间）。 */
  events?: { id: string; at: string }[]
  /** RFC3339；空 / 缺省 = 未暂停。 */
  paused_until?: string
  ticked_at?: string
  updated_at?: string
  version?: number
  /** 后端 Snapshot 把行为摘要嵌在 state 里；接口可能另放在 data.behavior。 */
  behavior?: AffectBehavior
}

/** 行为层参数（后端按维度值计算）。 */
export interface AffectBehavior {
  max_chars_factor: number
  max_segments: number
  humanize_scale: number
  silence_boost: number
  initiative_factor: number
  tone?: string
  reason?: string
}

/** 注入预览：line / full 两档的最终文本（off 时为空串）。 */
export interface AffectBlocks {
  line: string
  full: string
}

/** GET /api/affect/state 的 data。 */
export interface AffectStateData {
  state: AffectState | null
  behavior: AffectBehavior | null
  blocks: AffectBlocks
}

/** 历史点（GET /api/affect/history）。 */
export interface AffectHistoryPoint {
  at: string
  dims: AffectDims
}

/** 事件日志（GET /api/affect/events）。 */
export interface AffectEvent {
  id: number
  conversation_id: string
  event_id: string
  tone: string
  source: string
  created_at: string
}

/** GET /api/affect/conversations 的 data。 */
export interface AffectPacksData {
  packs: AffectPack[]
}

export interface AffectConversationsData {
  conversations: AffectConversation[]
}

export interface AffectHistoryData {
  points: AffectHistoryPoint[]
}

export interface AffectEventsData {
  events: AffectEvent[]
}
