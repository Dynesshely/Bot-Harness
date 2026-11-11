// 设置页共享上下文：多级子页面共用同一份 draft / patch / 保存逻辑。
import type { InjectionKey, Ref } from 'vue'
import { inject, provide } from 'vue'
import type { ConfigData, EditableConfig, TestAiResult } from '@/api/types'

export interface EditableDraft {
  system_prompt: string
  openai_base_url: string
  openai_api_key: string
  model: string
  temperature: number | null
  max_tokens: number | null
  request_timeout_seconds: number | null
  context_max_turns: number | null
  context_max_tokens: number | null
  history_ttl_hours: number | null
  reply_max_chars: number | null
  reply_max_segments: number | null
  early_reply_enabled: boolean
  early_reply_recall: boolean
  early_reply_delay_seconds: number | null
  early_reply_text: string
  reply_footer_enabled: boolean
  markdown_enabled: boolean
  /** 工具调用总开关（D-035）：关掉等于纯聊天，没有工具循环。 */
  tools_enabled: boolean
  /** 一次回复里最多几轮模型调用（每轮都可能跑工具）。 */
  tools_max_steps: number
  /** 整个工具循环的墙钟预算（秒），超时即收尾。 */
  tools_total_budget_seconds: number
  /** 单个工具结果进入模型前的截断长度（字符）。 */
  tools_result_max_chars: number
  memory_enabled: boolean
  memory_inject_max: number
  memory_max_per_scope: number
  memory_extract_enabled: boolean
  memory_extract_every: number
  web_enabled: boolean
  web_search_provider: string
  web_search_base_url: string
  exec_enabled: boolean
  exec_timeout_seconds: number
  exec_max_output_bytes: number
  humanize_level: string
  humanize_delay_scale: number
  /** 分段气泡档位 off/soft/medium/strong；off = 一条发完。 */
  humanize_segment_split: string
  /** 每条气泡前的模拟打字速度（秒/字，0.05–1）。 */
  humanize_typing_seconds_per_char: number
  /** 整轮模拟打字的等待上限（秒，0–30）。 */
  humanize_typing_max_seconds: number
  /** 群消息合并档位 off/fast/natural/steady。 */
  reply_debounce_mode: string
  /** 安静群的合并窗口（秒，0–15）。 */
  reply_debounce_quiet_seconds: number
  /** 活跃群的合并窗口（秒，0–15）。 */
  reply_debounce_active_seconds: number
  persona: string
  /** 引用频率分档 never/occasionally/often（策略页负责，但保存走 config）。 */
  quote_reply_mode: string
  /** 口吻轮换：备用说话方式（每轮按概率抽一条追加到系统提示词）。 */
  reply_style_variants: string[]
  /** 应用一条口吻变体的概率（0–1，默认 0.25）。 */
  reply_style_variant_probability: number | null
  /** 两阶段生成：草稿 → 润色。关掉就只用主模型一次生成。 */
  polish_enabled: boolean
  /** 润色模型名；空串 = 使用主模型。 */
  polish_model: string
  /** 润色温度 0–2（与后端默认 0.7 对齐）。 */
  polish_temperature: number | null
  /**
   * 润色输出上限。**0 = 自动**（后端按草稿长度估算，并受主模型 max_tokens 约束）
   * —— 0 是合法值，不是「留空」，绝不能当成空值丢掉；null 才表示输入框被清空、不提交。
   */
  polish_max_tokens: number | null
  /** 单次润色超时（秒），5–120，默认 20。 */
  polish_timeout_seconds: number | null
  /** 草稿短于该字数就跳过润色；0 = 总是润色（不设下限）。 */
  polish_min_chars: number | null
  tasks_enabled: boolean
  tasks_max_per_member: number
  tasks_max_delay_hours: number
  /** 承诺追问：提醒后用户没回应，模型按常识判断何时再问，直到收口。 */
  commitment_enabled: boolean
  /** 每个成员最多几条未收口承诺（1–50）。 */
  commitment_max_open_per_member: number
  /** 人类状态感知：自动推测聊天对象状态（群聊 / 私聊）。 */
  human_status_enabled: boolean
  /** 累积多少条新消息触发一次状态推测（5–200）。 */
  human_status_trigger_messages: number
  /** 状态缓存有效期（分钟，10–1440）。 */
  human_status_ttl_minutes: number
  /** 状态推测时取最近多少条消息构建成员时间线（20–400）。 */
  human_status_timeline_messages: number
  /** 允许模型自主调用图片分析工具。 */
  human_status_vision_enabled: boolean
  /** 图片分析使用的视觉模型名；空串 = 主模型。 */
  human_status_vision_model: string
  /** 情绪状态：默认关；关闭时现有行为零变化。 */
  affect_enabled: boolean
  /** 情绪内容包 id（默认 daily 日常心境）。 */
  affect_pack: string
  /** 情绪注入档位 off / line / full。 */
  affect_inject_mode: string
  /** 事件分类器 off / rules。 */
  affect_event_classifier: string
  /** 情绪 tick 间隔（秒），60–1800。 */
  affect_tick_seconds: number
  /** 单次注入 token 上限，40–300。 */
  affect_inject_max_tokens: number
  /** 自主加入：未 @ 群消息由模型判断是否加入；默认关。 */
  join_gate_enabled: boolean
  /** off 只记录不评估 / llm 交给模型判断。 */
  join_gate_mode: string
  /** 评估模型名；空串 = 主模型。 */
  join_gate_model: string
  /** 评估时取最近多少条群消息（5–50）。 */
  join_gate_window: number
  /** 最低加入置信度（0–1）。 */
  join_gate_confidence: number
  /** 同群两次评估最小间隔（秒，10–3600）。 */
  join_gate_eval_cooldown_seconds: number
  /** 上次判定 wait 时，满这个秒数可由该群的下一条新消息触发提前重评估（10–300）。 */
  join_gate_wait_retry_seconds: number
  /** 话题多久没有新消息算「休眠」（分钟，5–240）。 */
  join_gate_topic_dormant_minutes: number
  /** 话题多久没有新消息算「已结束」（分钟，30–1440，必须大于休眠阈值）。 */
  join_gate_topic_closed_minutes: number
  /** 两次加入最小间隔（分钟，1–240）。 */
  join_gate_min_interval_minutes: number
  /** 每小时加入上限（1–30）。 */
  join_gate_max_per_hour: number
  /** 每天加入上限（1–100）。 */
  join_gate_max_per_day: number
  proactive_level: string
  proactive_ratio: number
  proactive_max_credits: number
  proactive_quiet_start: number
  proactive_quiet_end: number
  proactive_min_interval_minutes: number
  vision_enabled: boolean
  vision_max_images: number | null
  manual_send_enabled: boolean
  manual_send_per_minute: number | null
  manual_send_per_conversation_qpm: number | null
  manual_send_max_segments: number | null
  rate_limit_user_qpm: number | null
  rate_limit_group_qpm: number | null
  group_cooldown_seconds: number | null
  max_concurrent_llm: number | null

  /** 接入与运维（原先在 .env，现在同样存数据库）。 */
  qq_app_id: string
  qq_app_secret: string
  qq_sandbox: boolean
  qq_transport: string
  /** null = 配置还没加载到（不要用本地猜的位掩码冒充真值）。 */
  qq_intents: number | null
  qq_shard_count: number
  qq_webhook_path: string
  qq_bot_openid: string
  /** 启动时自动把 Bot 命令同步到 QQ 指令面板；关掉只停自动同步，手动同步仍可用。 */
  command_panel_enabled: boolean
  admin_token: string
  /** null = 配置还没加载到。 */
  http_addr: string | null
  tls_extra_ca_files: string[]
  tls_insecure_verify: boolean
  system_prompt_file: string
  log_level: string
  log_buffer_size: number
  /** null = 配置还没加载到。 */
  media_max_bytes: number | null
}


export function emptyDraft(): EditableDraft {
  return {
    system_prompt: '',
    openai_base_url: '',
    openai_api_key: '',
    model: '',
    temperature: null,
    max_tokens: null,
    request_timeout_seconds: null,
    context_max_turns: null,
    context_max_tokens: null,
    history_ttl_hours: null,
    reply_max_chars: null,
    reply_max_segments: null,
    early_reply_enabled: false,
    early_reply_recall: false,
    early_reply_delay_seconds: null,
    early_reply_text: '',
    reply_footer_enabled: false,
    markdown_enabled: true,
    // 空草稿只是「加载前的占位」：与后端默认值对齐，避免界面闪一下假值。
    tools_enabled: true,
    tools_max_steps: 6,
    tools_total_budget_seconds: 100,
    tools_result_max_chars: 8000,
    memory_enabled: true,
    memory_inject_max: 6,
    memory_max_per_scope: 300,
    memory_extract_enabled: false,
    memory_extract_every: 10,
    web_enabled: true,
    web_search_provider: '',
    web_search_base_url: '',
    exec_enabled: false,
    exec_timeout_seconds: 15,
    exec_max_output_bytes: 8192,
    humanize_level: 'medium',
    humanize_delay_scale: 1,
    humanize_segment_split: 'medium',
    humanize_typing_seconds_per_char: 0.3,
    humanize_typing_max_seconds: 12,
    reply_debounce_mode: 'natural',
    reply_debounce_quiet_seconds: 2,
    reply_debounce_active_seconds: 5,
    persona: '',
    quote_reply_mode: 'occasionally',
    // 口吻轮换默认不启用（空数组）；概率与后端 Defaults() 对齐为 0.25。
    reply_style_variants: [],
    reply_style_variant_probability: 0.25,
    // 两阶段生成：与后端 Defaults() 对齐（默认开、主模型、0.7、0=自动、20s、0=不设下限）。
    polish_enabled: true,
    polish_model: '',
    polish_temperature: 0.7,
    polish_max_tokens: 0,
    polish_timeout_seconds: 20,
    polish_min_chars: 0,
    tasks_enabled: true,
    tasks_max_per_member: 5,
    tasks_max_delay_hours: 168,
    // 与后端 Defaults() 对齐：承诺追问与人类状态感知默认都开启。
    commitment_enabled: true,
    commitment_max_open_per_member: 10,
    human_status_enabled: true,
    human_status_trigger_messages: 20,
    human_status_ttl_minutes: 120,
    human_status_timeline_messages: 120,
    human_status_vision_enabled: true,
    human_status_vision_model: '',
    // 情绪状态与后端 Defaults() 对齐：默认关、日常心境、line、rules、300s、120 tokens。
    affect_enabled: false,
    affect_pack: 'daily',
    affect_inject_mode: 'line',
    affect_event_classifier: 'rules',
    affect_tick_seconds: 300,
    affect_inject_max_tokens: 120,
    // 自主加入与后端 Defaults() 对齐：默认关、llm、主模型、窗口 20、0.7、60s、5min、3/时、12/天。
    join_gate_enabled: false,
    join_gate_mode: 'llm',
    join_gate_model: '',
    join_gate_window: 20,
    join_gate_confidence: 0.7,
    join_gate_eval_cooldown_seconds: 60,
    join_gate_wait_retry_seconds: 30,
    join_gate_topic_dormant_minutes: 20,
    join_gate_topic_closed_minutes: 180,
    join_gate_min_interval_minutes: 5,
    join_gate_max_per_hour: 3,
    join_gate_max_per_day: 12,
    proactive_level: 'off',
    proactive_ratio: 10,
    proactive_max_credits: 3,
    proactive_quiet_start: 23,
    proactive_quiet_end: 8,
    proactive_min_interval_minutes: 10,
    vision_enabled: false,
    vision_max_images: null,
    manual_send_enabled: true,
    manual_send_per_minute: null,
    manual_send_per_conversation_qpm: null,
    manual_send_max_segments: null,
    rate_limit_user_qpm: null,
    rate_limit_group_qpm: null,
    group_cooldown_seconds: null,
    max_concurrent_llm: null,
    qq_app_id: '',
    qq_app_secret: '',
    qq_sandbox: false,
    qq_transport: 'websocket',
    // 这三个不写本地兜底值：加载完成前就显示 null，界面提示「未加载」，
    // 免得把「前端猜的默认值」当成后端真值（曾经 33554432 / :8080 / 4194304 就是这么来的）。
    qq_intents: null,
    qq_shard_count: 1,
    qq_webhook_path: '/qq/webhook',
    qq_bot_openid: '',
    // 与后端默认值对齐：启动时自动同步指令面板，关掉后仍可手动同步。
    command_panel_enabled: true,
    admin_token: '',
    http_addr: null,
    tls_extra_ca_files: [],
    tls_insecure_verify: false,
    system_prompt_file: '',
    log_level: 'info',
    log_buffer_size: 5000,
    media_max_bytes: null,
  }
}


export type NumericKey =
  | 'temperature'
  | 'max_tokens'
  | 'request_timeout_seconds'
  | 'context_max_turns'
  | 'context_max_tokens'
  | 'history_ttl_hours'
  | 'reply_max_chars'
  | 'reply_max_segments'
  | 'rate_limit_user_qpm'
  | 'rate_limit_group_qpm'
  | 'group_cooldown_seconds'
  | 'max_concurrent_llm'
  | 'early_reply_delay_seconds'
  | 'vision_max_images'
  | 'manual_send_per_minute'
  | 'manual_send_per_conversation_qpm'
  | 'manual_send_max_segments'
  | 'memory_inject_max'
  | 'memory_max_per_scope'
  | 'memory_extract_every'
  | 'tools_max_steps'
  | 'tools_total_budget_seconds'
  | 'tools_result_max_chars'
  | 'exec_timeout_seconds'
  | 'qq_intents'
  | 'qq_shard_count'
  | 'log_buffer_size'
  | 'media_max_bytes'
  | 'exec_max_output_bytes'
  | 'humanize_delay_scale'
  | 'humanize_typing_seconds_per_char'
  | 'humanize_typing_max_seconds'
  | 'reply_debounce_quiet_seconds'
  | 'reply_debounce_active_seconds'
  | 'tasks_max_per_member'
  | 'tasks_max_delay_hours'
  | 'commitment_max_open_per_member'
  | 'human_status_trigger_messages'
  | 'human_status_ttl_minutes'
  | 'human_status_timeline_messages'
  | 'affect_tick_seconds'
  | 'affect_inject_max_tokens'
  | 'join_gate_window'
  | 'join_gate_confidence'
  | 'join_gate_eval_cooldown_seconds'
  | 'join_gate_wait_retry_seconds'
  | 'join_gate_topic_dormant_minutes'
  | 'join_gate_topic_closed_minutes'
  | 'join_gate_min_interval_minutes'
  | 'join_gate_max_per_hour'
  | 'join_gate_max_per_day'
  | 'proactive_ratio'
  | 'proactive_max_credits'
  | 'proactive_quiet_start'
  | 'proactive_quiet_end'
  | 'proactive_min_interval_minutes'
  | 'polish_temperature'
  | 'polish_max_tokens'
  | 'polish_timeout_seconds'
  | 'polish_min_chars'
  | 'reply_style_variant_probability'

export const MODEL_PARAM_KEYS = [
  'temperature',
  'max_tokens',
  'request_timeout_seconds',
  'context_max_turns',
  'context_max_tokens',
  'history_ttl_hours',
  'reply_max_chars',
  'reply_max_segments',
  'rate_limit_user_qpm',
  'rate_limit_group_qpm',
  'group_cooldown_seconds',
  'max_concurrent_llm',
  'reply_footer_enabled',
  'vision_enabled',
  'vision_max_images',
] as const satisfies readonly (keyof EditableDraft)[]


/**
 * 只读块的展示名（启动期才知道、运行期改不了的项）。
 *
 * 存储不再暴露落盘位置，只报「引擎 + 连接」：具体落盘方式与部署形态无关，
 * 写出来只会随引擎切换而失真（PostgreSQL 迁移）。
 * 这里同时也是白名单：不认识的键不会直接以内部字段名渲染到界面上。
 */
export const READONLY_LABELS: Record<string, string> = {
  driver: '数据库引擎',
  storage: '连接地址',
  version: '版本',
}


export function assign(target: EditableDraft, source: EditableConfig | EditableDraft): void {
  target.system_prompt = source.system_prompt ?? ''
  target.qq_app_id = source.qq_app_id ?? ''
  // 秘密只进不出：后端不下发，这里始终留空（填了才会作为补丁发出去）。
  target.qq_app_secret = ''
  target.qq_sandbox = source.qq_sandbox ?? false
  target.qq_transport = source.qq_transport ?? 'websocket'
  target.qq_intents = source.qq_intents ?? null
  target.qq_shard_count = source.qq_shard_count ?? 1
  target.qq_webhook_path = source.qq_webhook_path ?? '/qq/webhook'
  target.qq_bot_openid = source.qq_bot_openid ?? ''
  target.command_panel_enabled = source.command_panel_enabled ?? true
  // 秘密只进不出：后端不下发，这里始终留空（填了才会作为补丁发出去）。
  target.admin_token = ''
  target.http_addr = source.http_addr ?? null
  target.tls_extra_ca_files = source.tls_extra_ca_files ?? []
  target.tls_insecure_verify = source.tls_insecure_verify ?? false
  target.system_prompt_file = source.system_prompt_file ?? ''
  target.log_level = source.log_level ?? 'info'
  target.log_buffer_size = source.log_buffer_size ?? 5000
  target.media_max_bytes = source.media_max_bytes ?? null
  target.openai_base_url = source.openai_base_url ?? ''
  // 后端永不回显密钥，这里也始终保持空；填了才会作为补丁发出去。
  target.openai_api_key = ''
  target.model = source.model ?? ''
  target.temperature = source.temperature ?? null
  target.max_tokens = source.max_tokens ?? null
  target.request_timeout_seconds = source.request_timeout_seconds ?? null
  target.context_max_turns = source.context_max_turns ?? null
  target.context_max_tokens = source.context_max_tokens ?? null
  target.history_ttl_hours = source.history_ttl_hours ?? null
  target.reply_max_chars = source.reply_max_chars ?? null
  target.reply_max_segments = source.reply_max_segments ?? null
  target.early_reply_enabled = source.early_reply_enabled ?? false
  target.early_reply_recall = source.early_reply_recall ?? false
  target.early_reply_delay_seconds = source.early_reply_delay_seconds ?? null
  target.early_reply_text = source.early_reply_text ?? ''
  target.reply_footer_enabled = source.reply_footer_enabled ?? false
  target.markdown_enabled = source.markdown_enabled ?? true
  target.tools_enabled = source.tools_enabled ?? true
  target.tools_max_steps = source.tools_max_steps ?? 6
  target.tools_total_budget_seconds = source.tools_total_budget_seconds ?? 100
  target.tools_result_max_chars = source.tools_result_max_chars ?? 8000
  target.memory_enabled = source.memory_enabled ?? true
  target.memory_inject_max = source.memory_inject_max ?? 6
  target.memory_max_per_scope = source.memory_max_per_scope ?? 300
  target.memory_extract_enabled = source.memory_extract_enabled ?? false
  target.memory_extract_every = source.memory_extract_every ?? 10
  target.web_enabled = source.web_enabled ?? true
  target.web_search_provider = source.web_search_provider ?? ''
  target.web_search_base_url = source.web_search_base_url ?? ''
  target.exec_enabled = source.exec_enabled ?? false
  target.exec_timeout_seconds = source.exec_timeout_seconds ?? 15
  target.exec_max_output_bytes = source.exec_max_output_bytes ?? 8192
  target.humanize_level = source.humanize_level ?? 'medium'
  target.humanize_delay_scale = source.humanize_delay_scale ?? 1
  target.humanize_segment_split = source.humanize_segment_split ?? 'medium'
  target.humanize_typing_seconds_per_char = source.humanize_typing_seconds_per_char ?? 0.3
  target.humanize_typing_max_seconds = source.humanize_typing_max_seconds ?? 12
  target.reply_debounce_mode = source.reply_debounce_mode ?? 'natural'
  target.reply_debounce_quiet_seconds = source.reply_debounce_quiet_seconds ?? 2
  target.reply_debounce_active_seconds = source.reply_debounce_active_seconds ?? 5
  target.persona = source.persona ?? ''
  target.quote_reply_mode = source.quote_reply_mode ?? ('quote_reply' in source && source.quote_reply === false ? 'never' : 'occasionally')
  target.reply_style_variants = [...(source.reply_style_variants ?? [])]
  target.reply_style_variant_probability = source.reply_style_variant_probability ?? 0.25
  target.polish_enabled = source.polish_enabled ?? true
  target.polish_model = source.polish_model ?? ''
  // ?? 只挡 undefined / null：后端下发的 0（自动 / 不设下限）必须原样留下。
  target.polish_temperature = source.polish_temperature ?? 0.7
  target.polish_max_tokens = source.polish_max_tokens ?? 0
  target.polish_timeout_seconds = source.polish_timeout_seconds ?? 20
  target.polish_min_chars = source.polish_min_chars ?? 0
  target.tasks_enabled = source.tasks_enabled ?? true
  target.tasks_max_per_member = source.tasks_max_per_member ?? 5
  target.tasks_max_delay_hours = source.tasks_max_delay_hours ?? 168
  target.commitment_enabled = source.commitment_enabled ?? true
  target.commitment_max_open_per_member = source.commitment_max_open_per_member ?? 10
  target.human_status_enabled = source.human_status_enabled ?? true
  target.human_status_trigger_messages = source.human_status_trigger_messages ?? 20
  target.human_status_ttl_minutes = source.human_status_ttl_minutes ?? 120
  target.human_status_timeline_messages = source.human_status_timeline_messages ?? 120
  // false 与空串都是真实值（关掉图片分析 / 改回主模型），?? 只挡 undefined / null。
  target.human_status_vision_enabled = source.human_status_vision_enabled ?? true
  target.human_status_vision_model = source.human_status_vision_model ?? ''
  // 情绪状态：与后端 Defaults() 对齐；false 与空串/默认值都要原样落下。
  target.affect_enabled = source.affect_enabled ?? false
  target.affect_pack = source.affect_pack ?? 'daily'
  target.affect_inject_mode = source.affect_inject_mode ?? 'line'
  target.affect_event_classifier = source.affect_event_classifier ?? 'rules'
  target.affect_tick_seconds = source.affect_tick_seconds ?? 300
  target.affect_inject_max_tokens = source.affect_inject_max_tokens ?? 120
  // 自主加入：默认关、llm、主模型；false 与空串都是真实值，?? 只挡 undefined / null。
  target.join_gate_enabled = source.join_gate_enabled ?? false
  target.join_gate_mode = source.join_gate_mode ?? 'llm'
  target.join_gate_model = source.join_gate_model ?? ''
  target.join_gate_window = source.join_gate_window ?? 20
  target.join_gate_confidence = source.join_gate_confidence ?? 0.7
  target.join_gate_eval_cooldown_seconds = source.join_gate_eval_cooldown_seconds ?? 60
  target.join_gate_wait_retry_seconds = source.join_gate_wait_retry_seconds ?? 30
  target.join_gate_topic_dormant_minutes = source.join_gate_topic_dormant_minutes ?? 20
  target.join_gate_topic_closed_minutes = source.join_gate_topic_closed_minutes ?? 180
  target.join_gate_min_interval_minutes = source.join_gate_min_interval_minutes ?? 5
  target.join_gate_max_per_hour = source.join_gate_max_per_hour ?? 3
  target.join_gate_max_per_day = source.join_gate_max_per_day ?? 12
  target.proactive_level = source.proactive_level ?? 'off'
  target.proactive_ratio = source.proactive_ratio ?? 10
  target.proactive_max_credits = source.proactive_max_credits ?? 3
  target.proactive_quiet_start = source.proactive_quiet_start ?? 23
  target.proactive_quiet_end = source.proactive_quiet_end ?? 8
  target.proactive_min_interval_minutes = source.proactive_min_interval_minutes ?? 10
  target.vision_enabled = source.vision_enabled ?? false
  target.vision_max_images = source.vision_max_images ?? null
  target.manual_send_enabled = source.manual_send_enabled ?? true
  target.manual_send_per_minute = source.manual_send_per_minute ?? null
  target.manual_send_per_conversation_qpm = source.manual_send_per_conversation_qpm ?? null
  target.manual_send_max_segments = source.manual_send_max_segments ?? null
  target.rate_limit_user_qpm = source.rate_limit_user_qpm ?? null
  target.rate_limit_group_qpm = source.rate_limit_group_qpm ?? null
  target.group_cooldown_seconds = source.group_cooldown_seconds ?? null
  target.max_concurrent_llm = source.max_concurrent_llm ?? null
}


/**
 * 数字字段（留空 = null = 不提交）。
 *
 * 抽出来是为了让「只提交改动字段」这条契约有单测钉住：这些 key 在草稿里是
 * `number | null`，null 表示「用户没填 / 不想改」，绝不能当成 0 或「清空」发出去。
 *
 * 反过来也必须成立：显式的 **0 是真实值**（例如 polish_max_tokens=0 = 自动、
 * polish_min_chars=0 = 不设下限），必须照发。判定只认 `=== null`，不认 falsy。
 */
export const NUMERIC_PATCH_KEYS: readonly NumericKey[] = [
  'temperature',
  'max_tokens',
  'request_timeout_seconds',
  'context_max_turns',
  'context_max_tokens',
  'history_ttl_hours',
  'reply_max_chars',
  'reply_max_segments',
  'early_reply_delay_seconds',
  'vision_max_images',
  'rate_limit_user_qpm',
  'rate_limit_group_qpm',
  'group_cooldown_seconds',
  'max_concurrent_llm',
  'manual_send_per_minute',
  'manual_send_per_conversation_qpm',
  'manual_send_max_segments',
  'memory_inject_max',
  'memory_max_per_scope',
  'memory_extract_every',
  'tools_max_steps',
  'tools_total_budget_seconds',
  'tools_result_max_chars',
  'exec_timeout_seconds',
  'qq_intents',
  'qq_shard_count',
  'log_buffer_size',
  'media_max_bytes',
  'exec_max_output_bytes',
  'humanize_delay_scale',
  'humanize_typing_seconds_per_char',
  'humanize_typing_max_seconds',
  'reply_debounce_quiet_seconds',
  'reply_debounce_active_seconds',
  'tasks_max_per_member',
  'tasks_max_delay_hours',
  'commitment_max_open_per_member',
  'human_status_trigger_messages',
  'human_status_ttl_minutes',
  'human_status_timeline_messages',
  'affect_tick_seconds',
  'affect_inject_max_tokens',
  'join_gate_window',
  'join_gate_confidence',
  'join_gate_eval_cooldown_seconds',
  'join_gate_wait_retry_seconds',
  'join_gate_topic_dormant_minutes',
  'join_gate_topic_closed_minutes',
  'join_gate_min_interval_minutes',
  'join_gate_max_per_hour',
  'join_gate_max_per_day',
  'proactive_ratio',
  'proactive_max_credits',
  'proactive_quiet_start',
  'proactive_quiet_end',
  'proactive_min_interval_minutes',
  'polish_temperature',
  'polish_max_tokens',
  'polish_timeout_seconds',
  'polish_min_chars',
  'reply_style_variant_probability',
]

/**
 * 从「当前草稿」与「已保存基线」算出要提交的补丁。
 *
 * 语义（D-036）：
 * - 只包含**有改动**的字段，没动过的不发，避免误覆盖后端默认值 / 按群覆盖；
 * - 数字字段 `null` = 留空 = **不提交**（不是 0，也不是清空）；
 * - 布尔字段 `false` 是**真实值**，必须照发（不能当成空值跳过）；
 * - 三个秘密（`openai_api_key` / `qq_app_secret` / `admin_token`）后端永不回显，
 *   空串 = 「没动它」，只有填了内容才作为补丁发出去；清空必须走显式动作。
 */
export function buildPatch(draft: EditableDraft, base: EditableDraft): Partial<EditableConfig> {
  const changed: Partial<EditableConfig> = {}
  // 接入与运维（原先在 .env）
  if (draft.qq_app_id !== base.qq_app_id) changed.qq_app_id = draft.qq_app_id
  // 秘密：空表示「没动它」，后端从不回填，所以不能把空当成清空。
  if (draft.qq_app_secret.trim()) changed.qq_app_secret = draft.qq_app_secret.trim()
  if (draft.qq_sandbox !== base.qq_sandbox) changed.qq_sandbox = draft.qq_sandbox
  if (draft.qq_transport !== base.qq_transport) changed.qq_transport = draft.qq_transport
  if (draft.qq_webhook_path !== base.qq_webhook_path) changed.qq_webhook_path = draft.qq_webhook_path
  if (draft.qq_bot_openid !== base.qq_bot_openid) changed.qq_bot_openid = draft.qq_bot_openid
  // 布尔 false 是真实值（= 关掉启动时自动同步），必须照发。
  if (draft.command_panel_enabled !== base.command_panel_enabled) {
    changed.command_panel_enabled = draft.command_panel_enabled
  }
  if (draft.admin_token.trim()) changed.admin_token = draft.admin_token.trim()
  // http_addr 为 null 只可能是「还没加载到」，不能拿 null 去覆盖后端值。
  if (draft.http_addr !== null && draft.http_addr !== base.http_addr) {
    changed.http_addr = draft.http_addr
  }
  if (draft.system_prompt_file !== base.system_prompt_file) {
    changed.system_prompt_file = draft.system_prompt_file
  }
  if (draft.log_level !== base.log_level) changed.log_level = draft.log_level
  if (draft.tls_insecure_verify !== base.tls_insecure_verify) {
    changed.tls_insecure_verify = draft.tls_insecure_verify
  }
  if (draft.tls_extra_ca_files.join('\n') !== (base.tls_extra_ca_files ?? []).join('\n')) {
    changed.tls_extra_ca_files = draft.tls_extra_ca_files
  }
  if (draft.system_prompt !== base.system_prompt) changed.system_prompt = draft.system_prompt
  if (draft.openai_base_url !== base.openai_base_url) {
    changed.openai_base_url = draft.openai_base_url
  }
  // 空 = 没动它（后端从来不下发真实密钥，所以「空」不可能是用户主动清空）。
  if (draft.openai_api_key.trim()) changed.openai_api_key = draft.openai_api_key.trim()
  if (draft.model !== base.model) changed.model = draft.model
  if (draft.early_reply_enabled !== base.early_reply_enabled) {
    changed.early_reply_enabled = draft.early_reply_enabled
  }
  if (draft.early_reply_text !== base.early_reply_text) changed.early_reply_text = draft.early_reply_text
  if (draft.early_reply_recall !== base.early_reply_recall) {
    changed.early_reply_recall = draft.early_reply_recall
  }
  if (draft.reply_footer_enabled !== base.reply_footer_enabled) {
    changed.reply_footer_enabled = draft.reply_footer_enabled
  }
  if (draft.markdown_enabled !== base.markdown_enabled) {
    changed.markdown_enabled = draft.markdown_enabled
  }
  if (draft.tools_enabled !== base.tools_enabled) {
    changed.tools_enabled = draft.tools_enabled
  }
  if (draft.memory_enabled !== base.memory_enabled) {
    changed.memory_enabled = draft.memory_enabled
  }
  if (draft.memory_inject_max !== base.memory_inject_max) {
    changed.memory_inject_max = draft.memory_inject_max
  }
  if (draft.memory_max_per_scope !== base.memory_max_per_scope) {
    changed.memory_max_per_scope = draft.memory_max_per_scope
  }
  if (draft.memory_extract_enabled !== base.memory_extract_enabled) {
    changed.memory_extract_enabled = draft.memory_extract_enabled
  }
  if (draft.memory_extract_every !== base.memory_extract_every) {
    changed.memory_extract_every = draft.memory_extract_every
  }
  if (draft.web_enabled !== base.web_enabled) {
    changed.web_enabled = draft.web_enabled
  }
  if (draft.web_search_provider !== base.web_search_provider) {
    changed.web_search_provider = draft.web_search_provider
  }
  if (draft.web_search_base_url !== base.web_search_base_url) {
    changed.web_search_base_url = draft.web_search_base_url
  }
  if (draft.exec_enabled !== base.exec_enabled) {
    changed.exec_enabled = draft.exec_enabled
  }
  if (draft.exec_timeout_seconds !== base.exec_timeout_seconds) {
    changed.exec_timeout_seconds = draft.exec_timeout_seconds
  }
  if (draft.exec_max_output_bytes !== base.exec_max_output_bytes) {
    changed.exec_max_output_bytes = draft.exec_max_output_bytes
  }
  if (draft.humanize_level !== base.humanize_level) {
    changed.humanize_level = draft.humanize_level
  }
  if (draft.humanize_delay_scale !== base.humanize_delay_scale) {
    changed.humanize_delay_scale = draft.humanize_delay_scale
  }
  if (draft.humanize_segment_split !== base.humanize_segment_split) {
    changed.humanize_segment_split = draft.humanize_segment_split
  }
  if (draft.humanize_typing_seconds_per_char !== base.humanize_typing_seconds_per_char) {
    changed.humanize_typing_seconds_per_char = draft.humanize_typing_seconds_per_char
  }
  if (draft.humanize_typing_max_seconds !== base.humanize_typing_max_seconds) {
    changed.humanize_typing_max_seconds = draft.humanize_typing_max_seconds
  }
  if (draft.reply_debounce_mode !== base.reply_debounce_mode) {
    changed.reply_debounce_mode = draft.reply_debounce_mode
  }
  if (draft.reply_debounce_quiet_seconds !== base.reply_debounce_quiet_seconds) {
    changed.reply_debounce_quiet_seconds = draft.reply_debounce_quiet_seconds
  }
  if (draft.reply_debounce_active_seconds !== base.reply_debounce_active_seconds) {
    changed.reply_debounce_active_seconds = draft.reply_debounce_active_seconds
  }
  if (draft.persona !== base.persona) {
    changed.persona = draft.persona
  }
  if (draft.quote_reply_mode !== base.quote_reply_mode) {
    changed.quote_reply_mode = draft.quote_reply_mode
  }
  // 口吻轮换：整行比较（顺序变了也算改动）；空数组是真实值（= 关闭）。
  if (draft.reply_style_variants.join('\n') !== (base.reply_style_variants ?? []).join('\n')) {
    changed.reply_style_variants = draft.reply_style_variants
  }
  // 两阶段生成：布尔 false 是真实值（关掉润色），照发；空串的 polish_model 也是真实值
  // （= 改回「使用主模型」），和秘密字段的「空 = 没动」不同。
  if (draft.polish_enabled !== base.polish_enabled) {
    changed.polish_enabled = draft.polish_enabled
  }
  if (draft.polish_model !== base.polish_model) {
    changed.polish_model = draft.polish_model
  }
  if (draft.tasks_enabled !== base.tasks_enabled) {
    changed.tasks_enabled = draft.tasks_enabled
  }
  if (draft.tasks_max_per_member !== base.tasks_max_per_member) {
    changed.tasks_max_per_member = draft.tasks_max_per_member
  }
  if (draft.tasks_max_delay_hours !== base.tasks_max_delay_hours) {
    changed.tasks_max_delay_hours = draft.tasks_max_delay_hours
  }
  // 承诺追问 / 人类状态感知：布尔 false 是真实值（关掉），照发；
  // 三个数字走 NUMERIC_PATCH_KEYS 的统一循环（0 不会被当成空值吞掉）。
  if (draft.commitment_enabled !== base.commitment_enabled) {
    changed.commitment_enabled = draft.commitment_enabled
  }
  if (draft.human_status_enabled !== base.human_status_enabled) {
    changed.human_status_enabled = draft.human_status_enabled
  }
  // 图片分析：布尔 false 照发；模型名空串 = 改回主模型（与「空 = 没动」的秘密字段不同）。
  if (draft.human_status_vision_enabled !== base.human_status_vision_enabled) {
    changed.human_status_vision_enabled = draft.human_status_vision_enabled
  }
  if (draft.human_status_vision_model !== base.human_status_vision_model) {
    changed.human_status_vision_model = draft.human_status_vision_model
  }
  // 情绪状态：布尔 false 是真实值（关掉），字符串空串同样照发（清空内容包由后端校验）。
  if (draft.affect_enabled !== base.affect_enabled) {
    changed.affect_enabled = draft.affect_enabled
  }
  if (draft.affect_pack !== base.affect_pack) {
    changed.affect_pack = draft.affect_pack
  }
  if (draft.affect_inject_mode !== base.affect_inject_mode) {
    changed.affect_inject_mode = draft.affect_inject_mode as EditableConfig['affect_inject_mode']
  }
  if (draft.affect_event_classifier !== base.affect_event_classifier) {
    changed.affect_event_classifier = draft.affect_event_classifier as EditableConfig['affect_event_classifier']
  }
  // 自主加入：布尔 false 是真实值（停用），模式 / 模型空串也是真实值
  //（join_gate_model='' = 改回主模型），必须照发。
  if (draft.join_gate_enabled !== base.join_gate_enabled) {
    changed.join_gate_enabled = draft.join_gate_enabled
  }
  if (draft.join_gate_mode !== base.join_gate_mode) {
    changed.join_gate_mode = draft.join_gate_mode
  }
  if (draft.join_gate_model !== base.join_gate_model) {
    changed.join_gate_model = draft.join_gate_model
  }
  if (draft.proactive_level !== base.proactive_level) {
    changed.proactive_level = draft.proactive_level
  }
  if (draft.proactive_ratio !== base.proactive_ratio) {
    changed.proactive_ratio = draft.proactive_ratio
  }
  if (draft.proactive_max_credits !== base.proactive_max_credits) {
    changed.proactive_max_credits = draft.proactive_max_credits
  }
  if (draft.proactive_quiet_start !== base.proactive_quiet_start) {
    changed.proactive_quiet_start = draft.proactive_quiet_start
  }
  if (draft.proactive_quiet_end !== base.proactive_quiet_end) {
    changed.proactive_quiet_end = draft.proactive_quiet_end
  }
  if (draft.proactive_min_interval_minutes !== base.proactive_min_interval_minutes) {
    changed.proactive_min_interval_minutes = draft.proactive_min_interval_minutes
  }
  if (draft.vision_enabled !== base.vision_enabled) changed.vision_enabled = draft.vision_enabled
  if (draft.manual_send_enabled !== base.manual_send_enabled) {
    changed.manual_send_enabled = draft.manual_send_enabled
  }

  for (const key of NUMERIC_PATCH_KEYS) {
    const value = draft[key]
    // null = 留空 = 不修改，跳过（后端会保留原值）。
    // 注意这里只拦 null：0 是合法值（如 polish_max_tokens=0 = 自动），必须照发。
    if (value === null) continue
    if (value !== base[key]) {
      ;(changed as Record<string, number>)[key] = value
    }
  }
  return changed
}


/**
 * 保存前的本地校验：后端 validateRanges 会拒、但能在界面上先拦住的组合。
 *
 * 返回空串表示可以保存。现在只有一条 —— 开了「提前安抚」却没有文案，
 * 后端会报 `early_reply_enabled requires a non-empty early_reply_text`。
 */
export function validateDraft(draft: EditableDraft): string {
  if (draft.early_reply_enabled && !draft.early_reply_text.trim()) {
    return '已开启「提前安抚」，请先填写安抚文案（early_reply_text），否则后端会拒绝保存'
  }
  return ''
}


/**
 * 分区页面从布局拿到的全部状态与动作。
 *
 * **状态字段里除了 `draft` / `loaded` 两个 reactive 草稿，其余都是 ref**：
 * 模板里直接读（`{{ error }}`、`:open="promptOpen"`）会自动解包，脚本里要写 `.value`。
 *
 * 为什么不是「reactive 容器里放 ref、子页解构出值」：解构拿到的是**那一刻的快照**，
 * 之后布局里改 ref 子页不会重渲染（弹窗永远打不开、模型列表永远空着）。
 * 所以这里 provide 的是 ref 本身。
 *
 * **子页不要直接给这些字段赋值**（尤其不能写 `promptOpen = true`）：
 * 走下面的动作方法，语义更清楚，也把「打开 / 关闭」收敛在一处。
 * 历史教训：ref 被塞进 reactive 容器后解构成普通 boolean，
 * 模板里的 `promptOpen = true` 被编译成 `promptOpen.value = true`，
 * 运行期直接抛 `Cannot create property 'value' on boolean 'false'`。
 */
export interface ConfigContext {
  draft: EditableDraft
  loaded: Ref<EditableDraft>
  config: Ref<ConfigData | null>
  error: Ref<string>
  loading: Ref<boolean>
  saving: Ref<boolean>
  dirty: Ref<boolean>
  /** 非空表示本地校验没过：保存按钮应禁用，文案即原因。 */
  validationError: Ref<string>
  restartRequired: Ref<string[]>
  apiKeySource: Ref<string>
  readonlyEntries: Ref<{ key: string; label: string; value: string }[]>
  modelIds: Ref<string[]>
  modelsBaseUrl: Ref<string>
  modelsFetchedAt: Ref<string>
  modelsLoading: Ref<boolean>
  modelsError: Ref<string>
  customModel: Ref<boolean>
  modelMissingFromList: Ref<boolean>
  promptOpen: Ref<boolean>
  modelParamsOpen: Ref<boolean>
  modelParamsDirtyCount: Ref<number>
  actionLoading: Ref<'' | 'reconnect' | 'token' | 'reset'>
  resetConfigOpen: Ref<boolean>
  aiOpen: Ref<boolean>
  aiPrompt: Ref<string>
  aiRunning: Ref<boolean>
  aiResult: Ref<TestAiResult | null>
  aiError: Ref<string>
  sendGroup: Ref<string>
  sendContent: Ref<string>
  sending: Ref<boolean>
  sendResult: Ref<string>
  sendError: Ref<string>
  loggingOut: Ref<boolean>
  /** 打开系统提示词大屏编辑弹窗。子页不要直接给 promptOpen 赋值。 */
  openPrompt: () => void
  closePrompt: () => void
  /** 切换「手动输入模型」与「从列表选择」。 */
  toggleCustomModel: () => void
  /** 展开 / 折叠模型参数面板。 */
  toggleModelParams: () => void
  closeTestAi: () => void
  openResetConfig: () => void
  cancelResetConfig: () => void
  save: () => Promise<void>
  discard: () => void
  reload: () => Promise<void>
  clearStoredKey: () => Promise<void>
  loadModels: (refresh: boolean) => Promise<void>
  runAction: (kind: 'reconnect' | 'token' | 'reset') => Promise<void>
  openTestAi: () => void
  runTestAi: () => Promise<void>
  runTestSend: () => Promise<void>
  onLogout: () => Promise<void>
}

const CONFIG_KEY: InjectionKey<ConfigContext> = Symbol('bot-harness-config')

export function provideConfig(ctx: ConfigContext): void {
  provide(CONFIG_KEY, ctx)
}

export function useConfig(): ConfigContext {
  const ctx = inject(CONFIG_KEY)
  if (!ctx) throw new Error('useConfig() 必须在设置布局内使用')
  return ctx
}
