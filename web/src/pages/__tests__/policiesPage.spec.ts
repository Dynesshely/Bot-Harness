import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import PoliciesPage from '@/pages/PoliciesPage.vue'

/**
 * 策略配置页的「按群覆盖」：群卡片渲染 / 搜索 / 字段保存。
 *
 * 三件事必须钉住：
 *   1. 每个群一张卡，字段以 card 内控件出现（不再是横向滚动的表格）；
 *   2. 搜索能按群名与 openid 过滤；
 *   3. 保存仍然只提交改动的字段，且 group_overrides 的形状与之前**完全一致**
 *      （透传所有字段 + 空枚举补 null）—— 这是 D-030 那条白名单事故的回归门。
 */

const api = vi.hoisted(() => ({
  policies: { get: vi.fn(), update: vi.fn() },
  groups: { names: vi.fn(), state: vi.fn() },
}))

vi.mock('@/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api')>()
  return {
    ...actual,
    policiesApi: api.policies,
    groupsApi: api.groups,
  }
})

const policiesFixture = {
  trigger_mode: 'at_only',
  context_mode: 'per_member',
  context_scope: 'addressed_only',
  group_allowlist: ['GROUP_ALLOWED_OPENID'],
  member_allowlist: [],
  admin_openids: [],
  blocked_keywords: [],
  reply_to_bots: true,
  quote_reply: true,
  group_overrides: [
    {
      group_openid: 'GROUP_A_OPENID_123456',
      enabled: true,
      trigger_mode: 'all_messages',
      context_mode: null,
      context_scope: null,
      reply_to_bots: null,
      quote_reply: null,
      tools: false,
      memory: null,
      web: null,
      exec: null,
      humanize: 'soft',
      polish: null,
      tasks: null,
      proactive: 'auto',
      affect: null,
      affect_inject_mode: 'line',
      join_gate: null,
      rate_limit_group_qpm: 12,
    },
    {
      group_openid: 'GROUP_B_OPENID_987654',
      enabled: false,
      trigger_mode: null,
      context_mode: 'shared',
      context_scope: 'include_unaddressed',
      reply_to_bots: true,
      quote_reply: false,
      tools: null,
      memory: true,
      web: false,
      exec: true,
      humanize: null,
      polish: true,
      tasks: false,
      proactive: 'off',
      affect: true,
      affect_inject_mode: null,
      join_gate: true,
      rate_limit_group_qpm: null,
    },
  ],
  platform: { known_groups: 0, full_access_groups: [], limited_groups: [] },
  warnings: [],
}

function mountPage() {
  return mount(PoliciesPage, {
    global: { stubs: { AppIcon: true, CopyButton: true } },
  })
}

beforeEach(() => {
  api.policies.get.mockReset()
  api.policies.update.mockReset()
  api.groups.names.mockReset()
  api.groups.names.mockResolvedValue({
    GROUP_A_OPENID_123456: '测试群 A',
    GROUP_ALLOWED_OPENID: '白名单群',
  })
  api.policies.get.mockResolvedValue(structuredClone(policiesFixture))
  api.policies.update.mockImplementation(async (patch) => ({ ...structuredClone(policiesFixture), ...patch }))
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('PoliciesPage 群卡片', () => {
  it('每个群一张卡，字段全部在卡片里，表格与横向滚动消失', async () => {
    const wrapper = mountPage()
    await flushPromises()

    const cards = wrapper.findAll('article')
    expect(cards).toHaveLength(2)
    // 没有表格了（这正是「不再横向滚动」的来源）
    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.find('.table-wrap').exists()).toBe(false)

    // 全部覆盖字段的标签都在
    const text = wrapper.text()
    for (const label of [
      '启用 enabled',
      '触发模式 trigger_mode',
      '上下文模式 context_mode',
      '上下文范围 context_scope',
      '机器人共存 reply_to_bots',
      '引用回复 quote_reply',
      '工具 tools',
      '记忆 memory',
      '联网 web',
      '执行 exec',
      '人味 humanize',
      '润色 polish',
      '定时 tasks',
      '主动 proactive',
      '情绪 affect',
      '注入档位 affect_inject_mode',
      '自主加入 join_gate',
      '群 QPM 限流 rate_limit_group_qpm',
    ]) {
      expect(text, '群卡片缺少字段：' + label).toContain(label)
    }

    // 每张卡内 18 个字段控件（17 个下拉 + 1 个数字输入）
    for (const card of cards) {
      expect(card.findAll('[role="combobox"]')).toHaveLength(17)
      expect(card.findAll('input[type="number"]')).toHaveLength(1)
    }
    wrapper.unmount()
  })

  it('群名接口按「覆盖表 + 白名单」的 openid 拉取，卡片显示「群名 (openid 前 8 位…)」', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await flushPromises()

    expect(api.groups.names).toHaveBeenCalled()
    const requested = api.groups.names.mock.calls.at(-1)?.[0] as string[]
    expect(requested).toContain('GROUP_A_OPENID_123456')
    expect(requested).toContain('GROUP_ALLOWED_OPENID')

    const first = wrapper.findAll('article')[0]
    // 前 8 位 + 省略号
    expect(first.text()).toContain('测试群 A (GROUP_A_…)')
    expect(first.text()).toContain('GROUP_A_OPENID_123456')
    // 群名没查到的群回退成 openid（不显示成空白）
    const second = wrapper.findAll('article')[1]
    expect(second.text()).toContain('GROUP_B_OPENID_987654')
    wrapper.unmount()
  })

  it('群名接口失败时静默回退到 openid，页面照常可用', async () => {
    api.groups.names.mockRejectedValue(new Error('boom'))
    const wrapper = mountPage()
    await flushPromises()
    await flushPromises()

    expect(wrapper.text()).toContain('GROUP_A_OPENID_123456')
    expect(wrapper.findAll('article')).toHaveLength(2)
    wrapper.unmount()
  })

  it('启用徽章按三态显示：true 启用 / false 暂停 / null 跟随默认（启用）', async () => {
    const wrapper = mountPage()
    await flushPromises()
    const [first, second] = wrapper.findAll('article')
    expect(first?.text()).toContain('已启用')
    expect(second?.text()).toContain('已暂停')

    // 第三行没有 enabled（= 跟随默认），徽章要说清楚它不是「暂停」
    api.policies.get.mockResolvedValue({
      ...structuredClone(policiesFixture),
      group_overrides: [
        ...structuredClone(policiesFixture.group_overrides),
        { group_openid: 'GROUP_C_OPENID_000000' },
      ],
    })
    const again = mountPage()
    await flushPromises()
    const third = again.findAll('article')[2]
    expect(third?.text()).toContain('跟随默认 · 启用')
    again.unmount()
    wrapper.unmount()
  })
})

describe('PoliciesPage 搜索', () => {
  it('按群名过滤', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await flushPromises()

    await wrapper.find('input[type="search"]').setValue('测试群 A')
    expect(wrapper.findAll('article')).toHaveLength(1)
    expect(wrapper.find('article').text()).toContain('GROUP_A_OPENID_123456')
    wrapper.unmount()
  })

  it('按 openid 片段过滤，大小写不敏感', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.find('input[type="search"]').setValue('group_b')
    expect(wrapper.findAll('article')).toHaveLength(1)
    expect(wrapper.find('article').text()).toContain('GROUP_B_OPENID_987654')
    wrapper.unmount()
  })

  it('没有匹配时给出空态而不是空白', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.find('input[type="search"]').setValue('不存在的群')
    expect(wrapper.findAll('article')).toHaveLength(0)
    expect(wrapper.text()).toContain('没有匹配')
    wrapper.unmount()
  })
})

describe('PoliciesPage 引用频率', () => {
  it('全局引用是 SelectMenu（不是原生开关），默认落在 occasionally', async () => {
    const wrapper = mountPage()
    await flushPromises()

    const trigger = wrapper.find('#cfg-quote-reply-mode')
    expect(trigger.exists(), '全局引用频率控件没渲染').toBe(true)
    expect(trigger.text()).toContain('occasionally')
    // 原生 <select> 已全站禁用，这里必须是自定义下拉
    expect(wrapper.find('select').exists()).toBe(false)
    wrapper.unmount()
  })

  it('改成 never 只提交 quote_reply_mode（不重复提交旧布尔）', async () => {
    const wrapper = mountPage()
    await flushPromises()

    await wrapper.find('#cfg-quote-reply-mode').trigger('click')
    const never = wrapper.findAll('[role="option"]').find((option) => option.text().includes('never'))
    expect(never, '下拉里没有 never 档').toBeTruthy()
    await never?.trigger('click')

    const button = wrapper.findAll('button').find((item) => item.text().includes('保存策略'))
    expect(button).toBeTruthy()
    await button?.trigger('click')
    await flushPromises()

    expect(api.policies.update).toHaveBeenCalledTimes(1)
    const patch = api.policies.update.mock.calls[0]?.[0] as Record<string, unknown>
    expect(Object.keys(patch)).toEqual(['quote_reply_mode'])
    expect(patch.quote_reply_mode).toBe('never')
    wrapper.unmount()
  })

  it('老后端只下发布尔时按 quote_reply 推导档位（false → never）', async () => {
    const fixture = structuredClone(policiesFixture)
    fixture.quote_reply = false
    delete (fixture as Record<string, unknown>).quote_reply_mode
    api.policies.get.mockResolvedValue(fixture)

    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.find('#cfg-quote-reply-mode').text()).toContain('never')
    wrapper.unmount()
  })
})

describe('PoliciesPage 保存', () => {
  async function saveAfterEdit(wrapper: ReturnType<typeof mountPage>) {
    const button = wrapper.findAll('button').find((item) => item.text().includes('保存策略'))
    expect(button, '找不到「保存策略」按钮').toBeTruthy()
    await button?.trigger('click')
    await flushPromises()
  }

  it('只改一个字段：payload 只带 group_overrides，且形状与后端契约一致', async () => {
    const wrapper = mountPage()
    await flushPromises()

    const card = wrapper.findAll('article')[0]
    // 卡片内第 18 个控件是群 QPM 数字框
    await card.findAll('input[type="number"]')[0]?.setValue('20')
    await saveAfterEdit(wrapper)

    expect(api.policies.update).toHaveBeenCalledTimes(1)
    const patch = api.policies.update.mock.calls[0]?.[0] as Record<string, unknown>
    // 只提交 group_overrides（其它字段没动）
    expect(Object.keys(patch)).toEqual(['group_overrides'])

    const overrides = patch.group_overrides as Record<string, unknown>[]
    expect(overrides).toHaveLength(2)
    // 改过的那一行
    expect(overrides[0]).toEqual({ ...policiesFixture.group_overrides[0], rate_limit_group_qpm: 20 })
    // 没改的行原样透传（含 D-035 的七项能力覆盖）
    expect(overrides[1]).toEqual(policiesFixture.group_overrides[1])
    // 固定字段清单（顺序无关），多一个少一个都会在这里暴露
    expect(Object.keys(overrides[0] ?? {}).sort()).toEqual(
      [
        'group_openid',
        'enabled',
        'trigger_mode',
        'context_mode',
        'context_scope',
        'reply_to_bots',
        'quote_reply',
        'polish',
        'tools',
        'proactive',
        'tasks',
        'memory',
        'humanize',
        'web',
        'exec',
        'affect',
        'affect_inject_mode',
        'join_gate',
        'rate_limit_group_qpm',
      ].sort(),
    )
    wrapper.unmount()
  })

  it('下拉改值写回草稿：选「暂停」后保存带上 enabled=false', async () => {
    const wrapper = mountPage()
    await flushPromises()

    const card = wrapper.findAll('article')[0]
    // 第一张卡第一个下拉就是「启用 enabled」
    await card.findAll('[role="combobox"]')[0]?.trigger('click')
    const pause = wrapper
      .findAll('[role="option"]')
      .find((option) => option.text().includes('暂停'))
    expect(pause, '启用下拉里没有「暂停」').toBeTruthy()
    await pause?.trigger('click')

    await saveAfterEdit(wrapper)
    const patch = api.policies.update.mock.calls[0]?.[0] as { group_overrides: Record<string, unknown>[] }
    expect(patch.group_overrides[0]?.enabled).toBe(false)
    wrapper.unmount()
  })

  it('自主加入 join_gate 三态覆盖写回 true / null，不影响其它字段', async () => {
    const wrapper = mountPage()
    await flushPromises()

    const card = wrapper.findAll('article')[0]
    // 17 个下拉里 join_gate 是第 17 个（QPM 数字框之前）
    const selects = card.findAll('[role="combobox"]')
    await selects[16]?.trigger('click')
    const enable = wrapper
      .findAll('[role="option"]')
      .find((option) => option.text() === '开启')
    expect(enable, 'join_gate 下拉里没有「开启」').toBeTruthy()
    await enable?.trigger('click')

    await saveAfterEdit(wrapper)
    const patch = api.policies.update.mock.calls[0]?.[0] as { group_overrides: Record<string, unknown>[] }
    expect(patch.group_overrides[0]?.join_gate).toBe(true)
    // 没改的字段原样透传
    expect(patch.group_overrides[1]).toEqual(policiesFixture.group_overrides[1])
    wrapper.unmount()
  })

  it('打开「跟随全局」的空枚举会显式补成 null（不是 undefined）', async () => {
    const wrapper = mountPage()
    await flushPromises()

    const card = wrapper.findAll('article')[0]
    // noise: 改一下 humanize 再改回「跟随全局」，确保 round-trip 后是 null
    const selects = card.findAll('[role="combobox"]')
    const humanize = selects[10]
    await humanize?.trigger('click')
    const follow = wrapper.findAll('[role="option"]').find((option) => option.text() === '跟随全局')
    await follow?.trigger('click')

    await saveAfterEdit(wrapper)
    const patch = api.policies.update.mock.calls[0]?.[0] as { group_overrides: Record<string, unknown>[] }
    expect(patch.group_overrides[0]?.humanize).toBeNull()
    wrapper.unmount()
  })
})
