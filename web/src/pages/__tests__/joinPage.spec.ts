import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import JoinPage from '@/pages/JoinPage.vue'

/**
 * 自主加入页：决策列表的 action 中文展示与话题 type。
 *
 * 契约 v2 把布尔 joined 改成三分类 action（join / wait / skip），
 * 老数据只有 joined —— 两条路都必须渲染成中文，不能把原始枚举直接丢给用户。
 */

const api = vi.hoisted(() => ({
  config: { get: vi.fn(), update: vi.fn() },
  conversations: { list: vi.fn() },
  join: { decisions: vi.fn(), topics: vi.fn() },
}))

vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return {
    ...actual,
    configApi: api.config,
    conversationsApi: api.conversations,
    joinApi: api.join,
  }
})

const configFixture = {
  editable: {
    join_gate_enabled: true,
    join_gate_mode: 'llm',
    join_gate_model: '',
    join_gate_window: 20,
    join_gate_confidence: 0.7,
    join_gate_eval_cooldown_seconds: 60,
    // 三个新键故意不放默认值：页面必须用 configContext 的兜底（30 / 20 / 180）。
    join_gate_min_interval_minutes: 5,
    join_gate_max_per_hour: 3,
    join_gate_max_per_day: 12,
  },
}

const conversationsFixture = {
  total: 1,
  page: 1,
  page_size: 20,
  items: [
    {
      id: 'conv-1',
      scene: 'group_member',
      group_openid: 'GROUP_A',
      group_name: '测试群',
      enabled: true,
      message_count: 3,
      turn_count: 1,
      token_estimate: 100,
    },
  ],
}

function decision(overrides: Record<string, unknown>) {
  return {
    id: 1,
    group_openid: 'GROUP_A',
    conversation_id: 'conv-1',
    message_id: 'm_1',
    speaker: '小明',
    preview: '预览',
    joined: false,
    confidence: 0.5,
    reason: '原因',
    topics: [],
    model: 'smoke-model',
    latency_ms: 100,
    created_at: '2026-07-21T10:00:00Z',
    ...overrides,
  }
}

const decisionsFixture = [
  decision({ id: 1, action: 'join', joined: true, preview: '有人懂这家店吗？' }),
  decision({ id: 2, action: 'wait', joined: false, preview: '话题还在发展' }),
  decision({ id: 3, action: 'skip', joined: false, preview: '哈哈哈哈' }),
  // 老数据：没有 action 字段，只有 joined
  decision({ id: 4, joined: true, preview: '老数据-加入' }),
  decision({ id: 5, joined: false, preview: '老数据-跳过' }),
]

const topicsFixture = {
  group_openid: 'GROUP_A',
  topics: [
    {
      id: 't1',
      title: '周末聚餐',
      summary: '约饭',
      message_ids: [12, 13],
      type: 'plan',
      status: 'active',
      resumable: true,
    },
    {
      id: 't2',
      title: '随手一聊',
      summary: '闲聊',
      message_ids: [14],
      type: 'chat',
      status: 'dormant',
      resumable: true,
    },
    // 老数据：没有 type / status / resumable，按 other / active 兜底。
    { id: 't3', title: '没类型的话题', summary: '老数据', message_ids: [15] },
    {
      id: 't4',
      title: '早就聊完的事',
      summary: '不要再提',
      message_ids: [16],
      type: 'other',
      status: 'closed',
      resumable: false,
    },
    {
      id: 't5',
      title: '暂歇但接不回',
      summary: '模型判定不可接回',
      message_ids: [17],
      type: 'help',
      status: 'dormant',
      resumable: false,
    },
  ],
  analyzed_at: '2026-07-21T10:00:00Z',
}

function mountPage() {
  return mount(JoinPage, { global: { stubs: { AppIcon: true } } })
}

beforeEach(() => {
  for (const group of Object.values(api)) {
    for (const fn of Object.values(group)) fn.mockReset()
  }
  api.config.get.mockResolvedValue(configFixture)
  api.config.update.mockImplementation(async (patch) => ({
    editable: { ...configFixture.editable, ...patch },
    warnings: [],
    restartRequired: [],
  }))
  api.conversations.list.mockResolvedValue(conversationsFixture)
  api.join.decisions.mockResolvedValue(decisionsFixture)
  api.join.topics.mockResolvedValue(topicsFixture)
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('JoinPage 决策 action 展示', () => {
  it('join/wait/skip 渲染成插话/等一等/不插话，并保留原始枚举', async () => {
    const wrapper = mountPage()
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('插话')
    expect(text).toContain('等一等')
    expect(text).toContain('不插话')
    // 原始枚举以等宽小字保留，方便对照后端日志 / JSON
    expect(text).toContain('join')
    expect(text).toContain('wait')
    expect(text).toContain('skip')
    wrapper.unmount()
  })

  it('老数据没有 action 时按 joined 兜底（true → 插话，false → 不插话）', async () => {
    const wrapper = mountPage()
    await flushPromises()

    const items = wrapper.findAll('li')
    const legacyJoin = items.find((item) => item.text().includes('老数据-加入'))
    const legacySkip = items.find((item) => item.text().includes('老数据-跳过'))
    expect(legacyJoin?.text()).toContain('插话')
    expect(legacySkip?.text()).toContain('不插话')
    wrapper.unmount()
  })
})

describe('JoinPage 话题 type', () => {
  it('最新话题面板显示中文类型；没有 type 的按「其他」', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await flushPromises()

    // 先选一个有会话记录的群，话题接口才会被调用
    await wrapper.find('#join-group').trigger('click')
    const option = wrapper.findAll('[role="option"]').find((item) => item.text().includes('测试群'))
    expect(option, '群下拉里没有 fixture 的群').toBeTruthy()
    await option?.trigger('click')
    await flushPromises()
    await flushPromises()

    const text = wrapper.text()
    expect(api.join.topics).toHaveBeenCalledWith('GROUP_A')
    expect(text).toContain('周末聚餐')
    expect(text).toContain('计划') // plan
    expect(text).toContain('闲聊') // chat
    expect(text).toContain('其他') // 缺省 type
    wrapper.unmount()
  })

  it('决策里的话题标题带类型与状态标注（老数据 status 按 active）', async () => {
    api.join.decisions.mockResolvedValue([
      decision({
        action: 'wait',
        topics: [
          {
            id: 't1',
            title: '周末聚餐',
            summary: '约饭',
            message_ids: [1],
            type: 'plan',
            status: 'dormant',
            resumable: true,
          },
          // 老数据：无 type / status / resumable
          { id: 't2', title: '旧话题', summary: '', message_ids: [2] },
        ],
      }),
    ])
    const wrapper = mountPage()
    await flushPromises()
    const text = wrapper.text()
    expect(text).toContain('周末聚餐（计划 · 暂歇，可接回）')
    expect(text).toContain('旧话题（其他 · 进行中）')
    wrapper.unmount()
  })
})

describe('JoinPage 话题 status / resumable', () => {
  async function mountWithTopics() {
    const wrapper = mountPage()
    await flushPromises()
    // 选择有会话记录的群后才会请求 topics
    await wrapper.find('#join-group').trigger('click')
    const option = wrapper.findAll('[role="option"]').find((item) => item.text().includes('测试群'))
    expect(option, '群下拉里没有 fixture 的群').toBeTruthy()
    await option?.trigger('click')
    await flushPromises()
    await flushPromises()
    return wrapper
  }

  it('最新话题面板把 status 渲染成中文，并标出 resumable', async () => {
    const wrapper = await mountWithTopics()
    const text = wrapper.text()

    expect(text).toContain('进行中')
    expect(text).toContain('暂歇')
    expect(text).toContain('已结束')
    expect(text).toContain('可接回')
    expect(text).toContain('不可接回')
    expect(text).toContain('不要提')
    // 老数据没有 status：按 active 显示，而不是空白徽章
    const legacy = wrapper
      .findAll('.rounded-lg')
      .find((card) => card.text().includes('没类型的话题'))
    expect(legacy?.text()).toContain('进行中')

    // 五个 fixture 话题都要渲染，未知字段不能让记录整条消失
    expect(text).toContain('周末聚餐')
    expect(text).toContain('随手一聊')
    expect(text).toContain('没类型的话题')
    expect(text).toContain('早就聊完的事')
    expect(text).toContain('暂歇但接不回')
    wrapper.unmount()
  })

  it('dormant 话题按 resumable 显示 可接回 / 不可接回', async () => {
    const wrapper = await mountWithTopics()
    const cards = wrapper.findAll('.rounded-lg')
    const resumable = cards.find((card) => card.text().includes('随手一聊'))
    const stuck = cards.find((card) => card.text().includes('暂歇但接不回'))
    expect(resumable?.text()).toContain('暂歇')
    expect(resumable?.text()).toContain('可接回')
    expect(stuck?.text()).toContain('暂歇')
    expect(stuck?.text()).toContain('不可接回')
    wrapper.unmount()
  })
})

describe('JoinPage 决策 addressed_to', () => {
  it('接口返回 addressed_to 时显示中文，缺失时不渲染', async () => {
    api.join.decisions.mockResolvedValue([
      decision({ id: 1, action: 'join', addressed_to: 'group', preview: '寻址-群' }),
      decision({ id: 2, action: 'skip', addressed_to: 'bot', preview: '寻址-机器人' }),
      decision({ id: 3, action: 'skip', preview: '没有寻址' }),
    ])
    const wrapper = mountPage()
    await flushPromises()

    const items = wrapper.findAll('li')
    const toGroup = items.find((item) => item.text().includes('寻址-群'))
    const toBot = items.find((item) => item.text().includes('寻址-机器人'))
    const none = items.find((item) => item.text().includes('没有寻址'))
    expect(toGroup?.text()).toContain('对群里')
    expect(toBot?.text()).toContain('对机器人')
    expect(none?.text()).not.toContain('对象不明')
    wrapper.unmount()
  })
})

describe('JoinPage 新增时间参数', () => {
  function numberInput(wrapper: ReturnType<typeof mountPage>, labelText: string) {
    const label = wrapper.findAll('label').find((item) => item.text().includes(labelText))
    expect(label, '找不到字段：' + labelText).toBeTruthy()
    const input = label?.element.parentElement?.querySelector<HTMLInputElement>(
      'input[type="number"]',
    )
    expect(input, '字段不是数字框：' + labelText).toBeTruthy()
    return input as HTMLInputElement
  }

  it('wait 重试 / 话题休眠 / 话题结束的默认值与范围与后端一致', async () => {
    const wrapper = mountPage()
    await flushPromises()

    const wait = numberInput(wrapper, 'wait 重试 join_gate_wait_retry_seconds')
    expect(wait.value).toBe('30')
    expect(wait.min).toBe('10')
    expect(wait.max).toBe('300')

    const dormant = numberInput(wrapper, '话题休眠 join_gate_topic_dormant_minutes')
    expect(dormant.value).toBe('20')
    expect(dormant.min).toBe('5')
    expect(dormant.max).toBe('240')

    const closed = numberInput(wrapper, '话题结束 join_gate_topic_closed_minutes')
    expect(closed.value).toBe('180')
    expect(closed.min).toBe('30')
    expect(closed.max).toBe('1440')
    wrapper.unmount()
  })
})

describe('JoinPage 群选择', () => {
  it('群下拉用自定义组件，选项带群名与会话数', async () => {
    const wrapper = mountPage()
    await flushPromises()

    await wrapper.find('#join-group').trigger('click')
    const options = wrapper.findAll('[role="option"]')
    // 「全部群」+ fixture 里的一个群
    expect(options).toHaveLength(2)
    expect(options[0]?.text()).toContain('全部群')
    expect(options[1]?.text()).toContain('测试群')
    expect(options[1]?.text()).toContain('1 个会话')
    wrapper.unmount()
  })
})
