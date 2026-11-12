import { describe, expect, it } from 'vitest'
import {
  MODEL_PARAM_KEYS,
  NUMERIC_PATCH_KEYS,
  assign,
  buildPatch,
  emptyDraft,
  validateDraft,
  type EditableDraft,
} from '@/composables/configContext'
import type { EditableConfig } from '@/api/types'

/** 草稿里的秘密字段：后端永不回显，空串代表「没动它」。 */
const SECRET_KEYS = ['openai_api_key', 'qq_app_secret', 'admin_token'] as const

/** 草稿里的可空字符串字段（填空草稿时要喂字符串而不是数字）。 */
const NULLABLE_STRING_KEYS = new Set(['http_addr'])

function draftOf(overrides: Partial<EditableDraft> = {}): EditableDraft {
  return Object.assign(emptyDraft(), overrides)
}

describe('emptyDraft', () => {
  it('每次返回新对象，数组字段不共享引用', () => {
    const a = emptyDraft()
    a.tls_extra_ca_files.push('/etc/ssl/extra.pem')
    a.model = 'x'
    const b = emptyDraft()
    expect(b.tls_extra_ca_files).toEqual([])
    expect(b.model).toBe('')
  })

  it('口吻轮换默认不启用：空数组 + 0.25 概率', () => {
    const draft = emptyDraft()
    expect(draft.reply_style_variants).toEqual([])
    expect(draft.reply_style_variant_probability).toBe(0.25)
    // 数组不共享引用（上一个用例已覆盖 tls，这里钉住新数组）
    draft.reply_style_variants.push('用 1–2 个字回复')
    expect(emptyDraft().reply_style_variants).toEqual([])
  })

  it('默认值：秘密为空、可选项为 null、开关取后端默认', () => {
    const draft = emptyDraft()
    for (const key of SECRET_KEYS) expect(draft[key]).toBe('')
    // 可选项留空 = 不修改，必须保持 null（不是 0 / 空串）
    expect(draft.temperature).toBeNull()
    expect(draft.max_tokens).toBeNull()
    expect(draft.vision_max_images).toBeNull()
    expect(draft.max_concurrent_llm).toBeNull()
    expect(draft.manual_send_per_minute).toBeNull()
    // 有默认值的数字项
    expect(draft.memory_inject_max).toBe(6)
    expect(draft.memory_max_per_scope).toBe(300)
    // 工具调用（D-035）默认与后端 Defaults() 对齐
    expect(draft.tools_enabled).toBe(true)
    expect(draft.tools_max_steps).toBe(6)
    expect(draft.tools_total_budget_seconds).toBe(100)
    expect(draft.tools_result_max_chars).toBe(8000)
    // 这三项不写本地兜底值：加载完成前保持 null，界面显示「未加载」，
    // 免得把前端猜的 33554432 / :8080 / 4194304 当成后端真值。
    expect(draft.qq_intents).toBeNull()
    expect(draft.http_addr).toBeNull()
    expect(draft.media_max_bytes).toBeNull()
    // 字符串 / 布尔 / 数组
    expect(draft.qq_transport).toBe('websocket')
    expect(draft.qq_webhook_path).toBe('/qq/webhook')
    // 启动时自动同步指令面板：与后端默认值对齐（默认开启）
    expect(draft.command_panel_enabled).toBe(true)
    expect(draft.log_level).toBe('info')
    expect(draft.humanize_level).toBe('medium')
    expect(draft.proactive_level).toBe('off')
    expect(draft.markdown_enabled).toBe(true)
    // 两阶段生成：默认值与后端 Defaults() 对齐；0 是合法值（自动 / 不设下限）
    expect(draft.polish_enabled).toBe(true)
    expect(draft.polish_model).toBe('')
    expect(draft.polish_temperature).toBe(0.7)
    expect(draft.polish_max_tokens).toBe(0)
    expect(draft.polish_timeout_seconds).toBe(20)
    expect(draft.polish_min_chars).toBe(0)
    expect(draft.memory_enabled).toBe(true)
    // 承诺追问 / 人类状态感知：与后端 Defaults() 对齐（默认都开）
    expect(draft.commitment_enabled).toBe(true)
    expect(draft.commitment_max_open_per_member).toBe(10)
    expect(draft.human_status_enabled).toBe(true)
    expect(draft.human_status_trigger_messages).toBe(20)
    expect(draft.human_status_ttl_minutes).toBe(120)
    expect(draft.human_status_timeline_messages).toBe(120)
    expect(draft.human_status_vision_enabled).toBe(true)
    expect(draft.human_status_vision_model).toBe('')
    // 情绪状态：与后端 Defaults() 对齐（默认关、daily、line、rules、300s、120 tokens）
    expect(draft.affect_enabled).toBe(false)
    expect(draft.affect_pack).toBe('daily')
    expect(draft.affect_inject_mode).toBe('line')
    expect(draft.affect_event_classifier).toBe('rules')
    expect(draft.affect_tick_seconds).toBe(300)
    expect(draft.affect_inject_max_tokens).toBe(120)
    // 自主加入：与后端 Defaults() 对齐（默认关、llm、主模型、窗口 20、0.7、60s、5min、3/时、12/天）
    expect(draft.join_gate_enabled).toBe(false)
    expect(draft.join_gate_mode).toBe('llm')
    expect(draft.join_gate_model).toBe('')
    expect(draft.join_gate_window).toBe(20)
    expect(draft.join_gate_confidence).toBe(0.7)
    expect(draft.join_gate_eval_cooldown_seconds).toBe(60)
    expect(draft.join_gate_wait_retry_seconds).toBe(30)
    expect(draft.join_gate_topic_dormant_minutes).toBe(20)
    expect(draft.join_gate_topic_closed_minutes).toBe(180)
    expect(draft.join_gate_min_interval_minutes).toBe(5)
    expect(draft.join_gate_max_per_hour).toBe(3)
    expect(draft.join_gate_max_per_day).toBe(12)
    expect(draft.exec_enabled).toBe(false)
    expect(draft.tls_insecure_verify).toBe(false)
    expect(draft.tls_extra_ca_files).toEqual([])
  })
})

describe('assign', () => {
  it('空配置（后端什么都没下发）落到与 emptyDraft 一致的默认值', () => {
    const target = draftOf({ model: 'stale', temperature: 9 })
    assign(target, {})
    expect(target).toEqual(emptyDraft())
  })

  it('逐字段拷贝后端下发的可编辑项', () => {
    const source: EditableConfig = {
      model: 'gpt-x',
      openai_base_url: 'https://one-api.internal.example/v1',
      temperature: 0.7,
      max_tokens: 1024,
      markdown_enabled: false,
      reply_max_chars: 300,
      tls_extra_ca_files: ['/etc/ssl/corp.pem'],
      qq_transport: 'webhook',
      qq_sandbox: true,
      command_panel_enabled: false,
      log_level: 'debug',
      persona: '住手',
      reply_style_variants: ['带一点翻译腔'],
      reply_style_variant_probability: 0.4,
      quote_reply_mode: 'often',
      polish_enabled: false,
      polish_model: 'polisher-mini',
      polish_temperature: 0,
      polish_max_tokens: 0,
      polish_timeout_seconds: 45,
      polish_min_chars: 120,
      commitment_enabled: false,
      commitment_max_open_per_member: 30,
      human_status_enabled: false,
      human_status_trigger_messages: 50,
      human_status_ttl_minutes: 720,
      human_status_timeline_messages: 240,
      human_status_vision_enabled: false,
      human_status_vision_model: 'qwen-vl-max',
      affect_enabled: true,
      affect_pack: 'daily',
      affect_inject_mode: 'full',
      affect_event_classifier: 'off',
      affect_tick_seconds: 180,
      affect_inject_max_tokens: 200,
      qq_intents: 1,
      http_addr: ':9000',
      media_max_bytes: 1024,
    }
    const target = emptyDraft()
    assign(target, source)
    expect(target.model).toBe('gpt-x')
    expect(target.openai_base_url).toBe('https://one-api.internal.example/v1')
    expect(target.temperature).toBe(0.7)
    expect(target.max_tokens).toBe(1024)
    expect(target.markdown_enabled).toBe(false)
    expect(target.reply_max_chars).toBe(300)
    expect(target.tls_extra_ca_files).toEqual(['/etc/ssl/corp.pem'])
    expect(target.qq_transport).toBe('webhook')
    expect(target.qq_sandbox).toBe(true)
    expect(target.command_panel_enabled).toBe(false)
    expect(target.log_level).toBe('debug')
    expect(target.persona).toBe('住手')
    expect(target.reply_style_variants).toEqual(['带一点翻译腔'])
    expect(target.reply_style_variant_probability).toBe(0.4)
    expect(target.quote_reply_mode).toBe('often')
    // ?? 只挡 undefined：0（自动 / 不设下限）与 false（关掉润色）都是真实值
    expect(target.polish_enabled).toBe(false)
    expect(target.polish_model).toBe('polisher-mini')
    expect(target.polish_temperature).toBe(0)
    expect(target.polish_max_tokens).toBe(0)
    expect(target.polish_timeout_seconds).toBe(45)
    expect(target.polish_min_chars).toBe(120)
    // 承诺追问 / 人类状态感知：false 与后端值都要原样落下
    expect(target.commitment_enabled).toBe(false)
    expect(target.commitment_max_open_per_member).toBe(30)
    expect(target.human_status_enabled).toBe(false)
    expect(target.human_status_trigger_messages).toBe(50)
    expect(target.human_status_ttl_minutes).toBe(720)
    expect(target.human_status_timeline_messages).toBe(240)
    expect(target.human_status_vision_enabled).toBe(false)
    expect(target.human_status_vision_model).toBe('qwen-vl-max')
    expect(target.affect_enabled).toBe(true)
    expect(target.affect_pack).toBe('daily')
    expect(target.affect_inject_mode).toBe('full')
    expect(target.affect_event_classifier).toBe('off')
    expect(target.affect_tick_seconds).toBe(180)
    expect(target.affect_inject_max_tokens).toBe(200)
    // 后端下发了才采用，不再用本地兜底值
    expect(target.qq_intents).toBe(1)
    expect(target.http_addr).toBe(':9000')
    expect(target.media_max_bytes).toBe(1024)
  })

  it('秘密只进不出：即使来源里带了值，草稿也留空（不把旧密钥当修改回传）', () => {
    const target = draftOf({
      openai_api_key: 'sk-leftover',
      qq_app_secret: 'secret-leftover',
      admin_token: 'token-leftover',
    })
    assign(target, {
      openai_api_key: 'sk-backend-should-never-send',
      qq_app_secret: 'backend-should-never-send',
      admin_token: 'backend-should-never-send',
    })
    for (const key of SECRET_KEYS) expect(target[key]).toBe('')
  })

  it('是整份快照覆盖而不是合并（「放弃修改」依赖这一点）', () => {
    const saved = draftOf({ model: 'base-model', max_tokens: 256, markdown_enabled: false })
    const draft = draftOf({ model: 'edited', max_tokens: null, markdown_enabled: true })
    assign(draft, saved)
    expect(draft).toEqual(saved)
  })

  it('可以接受另一份草稿作为来源（loaded → draft 回滚）', () => {
    const a = draftOf({ model: 'a' })
    const b = emptyDraft()
    assign(b, a)
    expect(b.model).toBe('a')
  })
})

describe('NUMERIC_PATCH_KEYS / MODEL_PARAM_KEYS', () => {
  it('数字补丁键不重复，且都在草稿里存在', () => {
    expect(new Set(NUMERIC_PATCH_KEYS).size).toBe(NUMERIC_PATCH_KEYS.length)
    const draft = emptyDraft() as unknown as Record<string, unknown>
    for (const key of NUMERIC_PATCH_KEYS) expect(key in draft).toBe(true)
    for (const key of MODEL_PARAM_KEYS) expect(key in draft).toBe(true)
  })

  it('MODEL_PARAM_KEYS 是草稿字段的子集且不重复', () => {
    expect(new Set(MODEL_PARAM_KEYS).size).toBe(MODEL_PARAM_KEYS.length)
  })
})

describe('validateDraft：保存前拦截后端必拒的组合', () => {
  it('开启「提前安抚」但没有文案 → 报错', () => {
    const draft = draftOf({ early_reply_enabled: true, early_reply_text: '   ' })
    expect(validateDraft(draft)).toContain('early_reply_text')
  })

  it('有文案 / 关掉开关 / 文案为默认值 → 放行', () => {
    expect(validateDraft(draftOf({ early_reply_enabled: true, early_reply_text: '让我想想' }))).toBe('')
    expect(validateDraft(draftOf({ early_reply_enabled: false, early_reply_text: '' }))).toBe('')
    expect(validateDraft(emptyDraft())).toBe('')
  })
})

describe('buildPatch：只提交改动字段', () => {
  it('完全没改动时返回空对象（不触发保存）', () => {
    const base = draftOf({ model: 'm', temperature: 0.5, markdown_enabled: false })
    expect(buildPatch(base, base)).toEqual({})
  })

  it('只包含被改动的那个字段', () => {
    const base = draftOf({ model: 'm', reply_max_chars: 100 })
    const draft = draftOf({ model: 'm2', reply_max_chars: 100 })
    expect(buildPatch(draft, base)).toEqual({ model: 'm2' })
  })

  it('布尔 false 是真实值，必须照发（不能当空值跳过）', () => {
    const base = draftOf({ markdown_enabled: true, vision_enabled: false })
    const draft = draftOf({ markdown_enabled: false, vision_enabled: true })
    expect(buildPatch(draft, base)).toEqual({ markdown_enabled: false, vision_enabled: true })
  })

  it('数字 null = 留空 = 不提交（两个方向都不提交）', () => {
    // 曾经设置过 → 清空输入框：不提交，后端保留原值
    const base = draftOf({ temperature: 0.9, max_tokens: 512 })
    const draft = draftOf({ temperature: null, max_tokens: null })
    expect(buildPatch(draft, base)).toEqual({})
    // 基线本来就没设置 → 仍然留空：也不提交
    expect(buildPatch(draftOf(), draftOf())).toEqual({})
  })

  it('数字真的改了才提交，包括改成 0', () => {
    const base = draftOf({ temperature: 0.9, max_tokens: 512 })
    const draft = draftOf({ temperature: 0, max_tokens: 512 })
    expect(buildPatch(draft, base)).toEqual({ temperature: 0 })
  })

  it('口吻轮换：空数组不提交、有内容才提交；概率 0 也会提交（0 是真实值）', () => {
    const base = draftOf({ reply_style_variants: ['用 1–2 个字回复'], reply_style_variant_probability: 0.25 })
    // 清空数组 = 关掉功能，属于真实改动
    expect(buildPatch(draftOf({ reply_style_variants: [], reply_style_variant_probability: 0.25 }), base))
      .toEqual({ reply_style_variants: [] })
    // 概率 0 = 每轮都不启用，也是真实值，不能被当成空值丢掉
    expect(
      buildPatch(
        draftOf({ reply_style_variants: ['用 1–2 个字回复'], reply_style_variant_probability: 0 }),
        base,
      ),
    ).toEqual({ reply_style_variant_probability: 0 })
    // 顺序变了算改动（与 tls_extra_ca_files 的整行比较一致）
    const two = draftOf({ reply_style_variants: ['a', 'b'] })
    expect(
      buildPatch(draftOf({ reply_style_variants: ['b', 'a'] }), two),
    ).toEqual({ reply_style_variants: ['b', 'a'] })
  })

  it('引用频率默认 occasionally，且与后端 Defaults() 对齐', () => {
    expect(emptyDraft().quote_reply_mode).toBe('occasionally')
  })

  it('polish_max_tokens = 0 会提交（0 = 自动，不是空值）', () => {
    const base = draftOf({ polish_max_tokens: 512 })
    expect(buildPatch(draftOf({ polish_max_tokens: 0 }), base)).toEqual({ polish_max_tokens: 0 })
  })

  it('polish_enabled = false 会提交（布尔 false 是真实值）', () => {
    const base = draftOf({ polish_enabled: true })
    expect(buildPatch(draftOf({ polish_enabled: false }), base)).toEqual({ polish_enabled: false })
  })

  it('承诺追问 / 人类状态：布尔 false 照发，数字（含 0）不丢', () => {
    const base = draftOf({
      commitment_enabled: true,
      commitment_max_open_per_member: 10,
      human_status_enabled: true,
      human_status_trigger_messages: 20,
      human_status_ttl_minutes: 120,
      human_status_timeline_messages: 120,
      human_status_vision_enabled: true,
      human_status_vision_model: 'vision-old',
    })
    expect(
      buildPatch(
        draftOf({
          commitment_enabled: false,
          commitment_max_open_per_member: 0,
          human_status_enabled: false,
          human_status_trigger_messages: 0,
          human_status_ttl_minutes: 0,
          human_status_timeline_messages: 0,
          human_status_vision_enabled: false,
          human_status_vision_model: '',
        }),
        base,
      ),
    ).toEqual({
      commitment_enabled: false,
      commitment_max_open_per_member: 0,
      human_status_enabled: false,
      human_status_trigger_messages: 0,
      human_status_ttl_minutes: 0,
      human_status_timeline_messages: 0,
      human_status_vision_enabled: false,
      human_status_vision_model: '',
    })
  })

  it('情绪状态：默认关闭不提交；显式 true / false / 档位 / 数值都会进补丁', () => {
    // 空草稿与空基线完全一致 → 一个键都不发
    expect(buildPatch(draftOf(), draftOf())).toEqual({})

    const base = draftOf()
    expect(
      buildPatch(
        draftOf({
          affect_enabled: true,
          affect_pack: 'custom-pack',
          affect_inject_mode: 'full',
          affect_event_classifier: 'off',
          affect_tick_seconds: 60,
          affect_inject_max_tokens: 300,
        }),
        base,
      ),
    ).toEqual({
      affect_enabled: true,
      affect_pack: 'custom-pack',
      affect_inject_mode: 'full',
      affect_event_classifier: 'off',
      affect_tick_seconds: 60,
      affect_inject_max_tokens: 300,
    })

    // 布尔 false 是真实值（停用），必须照发
    expect(buildPatch(draftOf({ affect_enabled: false }), draftOf({ affect_enabled: true }))).toEqual({
      affect_enabled: false,
    })
  })

  it('自主加入：默认关不提交；显式开关、模式、模型与数字都会进补丁', () => {
    // 空草稿与空基线完全一致 → 一个键都不发
    expect(buildPatch(draftOf(), draftOf())).toEqual({})

    // 布尔 false 是真实值，照发；join_gate_model 空串 = 改回主模型，也照发。
    expect(
      buildPatch(
        draftOf({
          join_gate_enabled: false,
          join_gate_mode: 'off',
          join_gate_model: '',
          join_gate_window: 30,
          join_gate_confidence: 0.82,
          join_gate_eval_cooldown_seconds: 120,
          join_gate_wait_retry_seconds: 45,
          join_gate_topic_dormant_minutes: 30,
          join_gate_topic_closed_minutes: 240,
          join_gate_min_interval_minutes: 10,
          join_gate_max_per_hour: 5,
          join_gate_max_per_day: 20,
        }),
        draftOf({
          join_gate_enabled: true,
          join_gate_model: 'cheap-model',
        }),
      ),
    ).toEqual({
      join_gate_enabled: false,
      join_gate_mode: 'off',
      join_gate_model: '',
      join_gate_window: 30,
      join_gate_confidence: 0.82,
      join_gate_eval_cooldown_seconds: 120,
      join_gate_wait_retry_seconds: 45,
      join_gate_topic_dormant_minutes: 30,
      join_gate_topic_closed_minutes: 240,
      join_gate_min_interval_minutes: 10,
      join_gate_max_per_hour: 5,
      join_gate_max_per_day: 20,
    })
  })

  it('表达方式库：默认值与后端 Defaults() 对齐，默认关不提交', () => {
    const draft = emptyDraft()
    expect(draft.expression_enabled).toBe(false)
    expect(draft.expression_mode).toBe('fast')
    expect(draft.expression_embedding_model).toBe('')
    expect(draft.expression_recall_limit).toBe(12)
    expect(draft.expression_inject_max).toBe(3)
    expect(draft.expression_learn_enabled).toBe(true)
    expect(draft.expression_learn_every).toBe(20)
    expect(draft.expression_review_enabled).toBe(true)
    expect(draft.expression_share_global).toBe(false)
    // 与基线一致 → 补丁里不出现任何 expression_* 键（不误覆盖后端值）
    expect(Object.keys(buildPatch(draft, emptyDraft()))).not.toContain('expression_enabled')
  })

  it('表达方式库：显式开关 / 档位 / 模型 / 数字 / 审核都会进补丁', () => {
    const base = emptyDraft()
    const draft = draftOf({
      expression_enabled: true,
      expression_mode: 'precise',
      expression_embedding_model: 'text-embedding-3-small',
      expression_recall_limit: 30,
      expression_inject_max: 5,
      expression_learn_enabled: false,
      expression_learn_every: 5,
      expression_review_enabled: false,
      expression_share_global: true,
    })
    const patch = buildPatch(draft, base)
    expect(patch.expression_enabled).toBe(true)
    expect(patch.expression_mode).toBe('precise')
    expect(patch.expression_embedding_model).toBe('text-embedding-3-small')
    expect(patch.expression_recall_limit).toBe(30)
    expect(patch.expression_inject_max).toBe(5)
    // 布尔 false 是真实值（停用学习 / 审核），必须照发
    expect(patch.expression_learn_enabled).toBe(false)
    expect(patch.expression_review_enabled).toBe(false)
    expect(patch.expression_learn_every).toBe(5)
    expect(patch.expression_share_global).toBe(true)
  })

  it('表达方式库：清空 embedding 模型 = 改回复用主模型，照发空串', () => {
    const base = draftOf({ expression_embedding_model: 'text-embedding-3-small' })
    const draft = draftOf({ expression_embedding_model: '' })
    expect(buildPatch(draft, base).expression_embedding_model).toBe('')
  })

  it('command_panel_enabled = false 会提交（关掉启动时自动同步）', () => {
    const base = draftOf({ command_panel_enabled: true })
    expect(buildPatch(draftOf({ command_panel_enabled: false }), base)).toEqual({
      command_panel_enabled: false,
    })
  })

  it('polish 数字留空 = 不提交；polish_model 清空 = 改回主模型，照发', () => {
    const base = draftOf({
      polish_temperature: 0.7,
      polish_timeout_seconds: 20,
      polish_model: 'polisher-mini',
    })
    const draft = draftOf({
      polish_temperature: null,
      polish_timeout_seconds: 20,
      polish_model: '',
    })
    // 与秘密字段的「空 = 没动」不同：空串的润色模型名是「改用主模型」这个真实意图。
    expect(buildPatch(draft, base)).toEqual({ polish_model: '' })
  })

  it('秘密字段：空 / 纯空白不提交，填了才去空格提交', () => {
    const base = draftOf({ model: 'm' })
    expect(buildPatch(draftOf({ model: 'm' }), base)).toEqual({})
    expect(
      buildPatch(
        draftOf({ model: 'm', openai_api_key: '   ', qq_app_secret: '\n', admin_token: ' ' }),
        base,
      ),
    ).toEqual({})
    expect(
      buildPatch(
        draftOf({ model: 'm', openai_api_key: '  sk-new  ', qq_app_secret: ' s3cret ', admin_token: ' t0ken ' }),
        base,
      ),
    ).toEqual({ openai_api_key: 'sk-new', qq_app_secret: 's3cret', admin_token: 't0ken' })
  })

  it('字符串清空也算改动（与「空 = 没动」的秘密字段不同）', () => {
    const base = draftOf({ model: 'm', persona: '住手' })
    const draft = draftOf({ model: 'm', persona: '' })
    expect(buildPatch(draft, base)).toEqual({ persona: '' })
  })

  it('路径数组按整行内容比较（顺序变了也算改动）', () => {
    const base = draftOf({ tls_extra_ca_files: ['/a.pem', '/b.pem'] })
    expect(buildPatch(draftOf({ tls_extra_ca_files: ['/a.pem', '/b.pem'] }), base)).toEqual({})
    expect(
      buildPatch(draftOf({ tls_extra_ca_files: ['/b.pem', '/a.pem'] }), base),
    ).toEqual({ tls_extra_ca_files: ['/b.pem', '/a.pem'] })
  })

  it('加载前的 null 占位不会被当成改动（http_addr / qq_intents / media_max_bytes）', () => {
    const unloaded = draftOf()
    expect(unloaded.http_addr).toBeNull()
    expect(unloaded.qq_intents).toBeNull()
    expect(unloaded.media_max_bytes).toBeNull()
    expect(buildPatch(draftOf(), unloaded)).toEqual({})
  })

  it('补丁里不允许出现 undefined（JSON 里会变成丢字段）', () => {
    const base = draftOf()
    const draft = draftOf({ model: 'm', temperature: 0.1 })
    for (const value of Object.values(buildPatch(draft, base))) {
      expect(value).not.toBeUndefined()
    }
  })

  // 注意：这条只保证「草稿里已有的字段」都接进了补丁，
  // 抓不到「后端已支持、而前端根本没有控件」的缺口（那类缺口由 backend-keys 比对测试兜底）。
  it('每个草稿字段都必须接进补丁逻辑（新增配置项漏接会在这里挂）', () => {
    const base = emptyDraft() as unknown as Record<string, unknown>
    const draft = emptyDraft() as unknown as Record<string, unknown>
    const secrets = new Set<string>(SECRET_KEYS)

    for (const key of Object.keys(base)) {
      if (secrets.has(key)) continue
      const current = base[key]
      if (current === null) {
        // 草稿里唯二的可空字符串是 http_addr（其余可空字段都是数字）
        draft[key] = NULLABLE_STRING_KEYS.has(key) ? ':9999' : 7
      } else if (typeof current === 'number') {
        draft[key] = current + 1
      } else if (typeof current === 'boolean') {
        draft[key] = !current
      } else if (Array.isArray(current)) {
        draft[key] = [...current, '/tmp/extra.pem']
      } else if (typeof current === 'string') {
        draft[key] = `${current}-changed`
      } else {
        throw new Error(`空草稿里出现了未覆盖的类型：${key}`)
      }
    }

    const patch = buildPatch(draft as unknown as EditableDraft, base as unknown as EditableDraft)
    for (const key of Object.keys(base)) {
      if (secrets.has(key)) continue
      expect(patch, `字段 ${key} 没有接进 buildPatch`).toHaveProperty(key)
    }
  })
})
