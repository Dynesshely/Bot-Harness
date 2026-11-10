import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AffectPage from '@/pages/AffectPage.vue'

/**
 * 情绪状态页：空状态 / 有状态 / 暂停三条主路径。
 * 全部 API 都是 mock；不依赖后端是否已经实现 affect 端点。
 */

const api = vi.hoisted(() => ({
  config: { get: vi.fn(), update: vi.fn() },
  affect: {
    packs: vi.fn(),
    conversations: vi.fn(),
    state: vi.fn(),
    history: vi.fn(),
    events: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    preset: vi.fn(),
    reset: vi.fn(),
  },
}))

vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return {
    ...actual,
    configApi: api.config,
    affectApi: api.affect,
  }
})

const packsFixture = [
  {
    id: 'daily',
    name: '日常心境',
    description: '内置的日常情绪包',
    dimensions: [
      { key: 'valence', label: '心情', min: 0, max: 100, baseline: 55 },
      { key: 'energy', label: '精力', min: 0, max: 100, baseline: 50 },
      { key: 'stress', label: '压力', min: 0, max: 100, baseline: 35 },
      { key: 'closeness', label: '熟悉度', min: 0, max: 100, baseline: 45 },
      { key: 'patience', label: '耐心', min: 0, max: 100, baseline: 60 },
    ],
    events: [{ id: 'praise', label: '被夸', tone: 'positive' }],
    presets: [
      { id: 'calm', name: '平静' },
      { id: 'focused', name: '专注' },
      { id: 'cheerful', name: '愉快' },
      { id: 'low_battery', name: '低电量' },
    ],
  },
]

const conversationsFixture = [
  {
    id: 'conv-1',
    pack: 'daily',
    updated_at: '2026-07-21T09:59:00Z',
    dims: { valence: 62.5, energy: 48, stress: 31, closeness: 55, patience: 70 },
  },
]

const behaviorFixture = {
  max_chars_factor: 1.1,
  max_segments: 3,
  humanize_scale: 0.9,
  silence_boost: 0.05,
  initiative_factor: 1.15,
  tone: '轻松',
  reason: '心情好、精力一般',
}

const historyFixture = [
  { at: '2026-07-21T08:00:00Z', dims: { valence: 55, energy: 50, stress: 35, closeness: 45, patience: 60 } },
  { at: '2026-07-21T09:00:00Z', dims: { valence: 62.5, energy: 48, stress: 31, closeness: 55, patience: 70 } },
]

const eventsFixture = [
  {
    id: 1,
    conversation_id: 'conv-1',
    event_id: 'praise',
    tone: 'positive',
    source: 'rules',
    created_at: '2026-07-21T09:58:00Z',
  },
]

function configFixture(overrides: Record<string, unknown> = {}) {
  return {
    editable: {
      affect_enabled: true,
      affect_pack: 'daily',
      affect_inject_mode: 'line',
      affect_event_classifier: 'rules',
      affect_tick_seconds: 300,
      affect_inject_max_tokens: 120,
      ...overrides,
    },
    openai_api_key_set: false,
    qq_app_secret_set: false,
    admin_token_set: false,
  }
}

function stateFixture(pausedUntil = '') {
  return {
    state: {
      conversation_id: 'conv-1',
      pack_id: 'daily',
      dims: { valence: 62.5, energy: 48, stress: 31, closeness: 55, patience: 70 },
      events: [{ id: 'praise', at: '2026-07-21T09:58:00Z' }],
      paused_until: pausedUntil,
      ticked_at: '2026-07-21T09:55:00Z',
      updated_at: '2026-07-21T09:59:00Z',
      version: 3,
    },
    behavior: behaviorFixture,
    blocks: {
      line: '[此刻的状态｜仅作语气参考] 心情不错，语气轻快一点。',
      full: '[此刻的状态｜仅作语气参考] 心情不错。\n[自述] 今天还算有精神。',
    },
  }
}

function mountPage() {
  return mount(AffectPage, {
    global: {
      stubs: { AppIcon: true },
    },
  })
}

beforeEach(() => {
  for (const group of Object.values(api)) {
    for (const fn of Object.values(group)) fn.mockReset()
  }
  api.config.get.mockResolvedValue(configFixture())
  api.config.update.mockImplementation(async (patch: Record<string, unknown>) => ({
    editable: { ...configFixture().editable, ...patch },
    warnings: [],
    restartRequired: [],
  }))
  api.affect.packs.mockResolvedValue(packsFixture)
  api.affect.conversations.mockResolvedValue(conversationsFixture)
  api.affect.state.mockResolvedValue(stateFixture())
  api.affect.history.mockResolvedValue(historyFixture)
  api.affect.events.mockResolvedValue(eventsFixture)
  api.affect.pause.mockResolvedValue({})
  api.affect.resume.mockResolvedValue({})
  api.affect.preset.mockResolvedValue({})
  api.affect.reset.mockResolvedValue({})
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('AffectPage 空状态', () => {
  it('未启用时显示「一键启用日常心境」引导，且不请求会话状态', async () => {
    api.config.get.mockResolvedValue(configFixture({ affect_enabled: false }))
    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.text()).toContain('情绪状态尚未启用')
    expect(wrapper.text()).toContain('一键启用日常心境')
    expect(api.affect.conversations).not.toHaveBeenCalled()
    expect(api.affect.state).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('点一键启用会 PUT affect_enabled=true + affect_pack=daily，并开始加载会话', async () => {
    api.config.get.mockResolvedValue(configFixture({ affect_enabled: false }))
    const wrapper = mountPage()
    await flushPromises()

    const button = wrapper.findAll('button').find((item) => item.text().includes('一键启用日常心境'))
    expect(button).toBeTruthy()
    await button?.trigger('click')
    await flushPromises()

    expect(api.config.update).toHaveBeenCalledWith({ affect_enabled: true, affect_pack: 'daily' })
    expect(api.affect.conversations).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
})

describe('AffectPage 有状态', () => {
  it('渲染 5 维数值、行为摘要、历史曲线、事件与注入预览', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await flushPromises()
    await flushPromises()
    const text = wrapper.text()

    expect(text).toContain('日常心境')
    expect(text).toContain('心情')
    expect(text).toContain('62.50')
    expect(text).toContain('行为摘要')
    expect(text).toContain('1.10×')
    expect(text).toContain('1.15×')
    expect(text).toContain('历史曲线')
    expect(text).toContain('注入预览')
    // 注入块是 readonly textarea，内容在 value 而不是 text()
    const blockTexts = wrapper.findAll<HTMLTextAreaElement>('textarea')
    expect(blockTexts).toHaveLength(2)
    expect(blockTexts[0]?.element.value).toContain('心情不错，语气轻快一点')
    expect(blockTexts[1]?.element.value).toContain('今天还算有精神')
    expect(text).toContain('被夸')
    expect(text).toContain('规则分类')
    // svg 折线确实画出来了（2 个历史点、5 条线）
    expect(wrapper.findAll('svg path').length).toBeGreaterThanOrEqual(5)
    expect(api.affect.state).toHaveBeenCalledWith('conv-1')
    expect(api.affect.history).toHaveBeenCalledWith('conv-1', 24)
    wrapper.unmount()
  })

  it('切到 7d 会按 168 小时重新拉历史', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await flushPromises()

    const rangeButton = wrapper.findAll('button').find((item) => item.text() === '7d')
    expect(rangeButton).toBeTruthy()
    await rangeButton?.trigger('click')
    await flushPromises()
    expect(api.affect.history).toHaveBeenCalledWith('conv-1', 168)
    wrapper.unmount()
  })

  it('暂停中显示暂停标注，点恢复调用 resume', async () => {
    const pausedUntil = new Date(Date.now() + 30 * 60_000).toISOString()
    api.affect.state.mockResolvedValue(stateFixture(pausedUntil))
    const wrapper = mountPage()
    await flushPromises()
    await flushPromises()

    expect(wrapper.text()).toContain('暂停至')
    const resumeButton = wrapper.findAll('button').find((item) => item.text().includes('恢复'))
    expect(resumeButton).toBeTruthy()
    expect(resumeButton?.attributes('disabled')).toBeUndefined()
    await resumeButton?.trigger('click')
    await flushPromises()
    expect(api.affect.resume).toHaveBeenCalledWith('conv-1')
    wrapper.unmount()
  })

  it('配置区只提交改动的脏字段', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await flushPromises()

    const tickInput = wrapper.find<HTMLInputElement>('input[type="number"]')
    expect(tickInput.exists()).toBe(true)
    await tickInput.setValue('600')
    const saveButton = wrapper.findAll('button').find((item) => item.text().includes('保存配置'))
    expect(saveButton).toBeTruthy()
    await saveButton?.trigger('click')
    await flushPromises()
    expect(api.config.update).toHaveBeenCalledWith({ affect_tick_seconds: 600 })
    wrapper.unmount()
  })
})
