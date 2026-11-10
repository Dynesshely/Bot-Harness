import { describe, expect, it } from 'vitest'
import { toGroupOverridePayload } from '@/utils/policy'
import type { GroupOverride } from '@/api/types'

/**
 * P0 回归：保存「按群覆盖」任意一行时，不能被前端白名单重建抹掉能力字段。
 *
 * 事故形态：PoliciesPage 以前按 8 个字段白名单重建每一行，用户只改了一行 QPM，
 * 保存后所有群的 tools / proactive / tasks / memory / humanize / web / exec
 * 全部变回「跟随全局」—— 界面不报错，纯静默丢数据。
 */

const CAPABILITY_KEYS = [
  'tools',
  'proactive',
  'tasks',
  'memory',
  'humanize',
  'web',
  'exec',
] as const

const FULL_ROW: GroupOverride = {
  group_openid: '  GROUP_OPENID_WITH_SPACES  ',
  enabled: true,
  trigger_mode: 'all_messages',
  context_mode: 'shared',
  context_scope: 'include_unaddressed',
  reply_to_bots: false,
  quote_reply: true,
  tools: false,
  proactive: 'auto',
  tasks: false,
  memory: true,
  humanize: 'strong',
  web: false,
  exec: true,
  affect: false,
  affect_inject_mode: 'full',
  polish: true,
  rate_limit_group_qpm: 12,
}

describe('toGroupOverridePayload', () => {
  it('七个能力覆盖字段保存后仍然在（P0）', () => {
    const [row] = toGroupOverridePayload([FULL_ROW])
    for (const key of CAPABILITY_KEYS) {
      expect(row[key], `保存后 ${key} 被抹掉了`).toEqual(FULL_ROW[key])
    }
  })

  it('情绪状态覆盖透传：affect 布尔与 affect_inject_mode 档位都不丢', () => {
    const [row] = toGroupOverridePayload([FULL_ROW])
    expect(row.affect).toBe(false)
    expect(row.affect_inject_mode).toBe('full')

    // 显式 true 仍然是 true，未填 = 跟随全局（null）
    expect(toGroupOverridePayload([{ ...FULL_ROW, affect: true }])[0].affect).toBe(true)
    const [empty] = toGroupOverridePayload([{ group_openid: 'g2' }])
    expect(empty.affect).toBeNull()
    expect(empty.affect_inject_mode).toBeNull()
  })

  it('两阶段生成的 polish 覆盖原样透传（显式 false 也不被吞）', () => {
    expect(toGroupOverridePayload([FULL_ROW])[0].polish).toBe(true)
    expect(toGroupOverridePayload([{ ...FULL_ROW, polish: false }])[0].polish).toBe(false)
    expect(toGroupOverridePayload([{ group_openid: 'g1' }])[0].polish).toBeNull()
  })

  it('原有字段也原样保留，group_openid 去首尾空格', () => {
    const [row] = toGroupOverridePayload([FULL_ROW])
    expect(row.group_openid).toBe('GROUP_OPENID_WITH_SPACES')
    expect(row.enabled).toBe(true)
    expect(row.trigger_mode).toBe('all_messages')
    expect(row.context_mode).toBe('shared')
    expect(row.context_scope).toBe('include_unaddressed')
    expect(row.reply_to_bots).toBe(false)
    expect(row.quote_reply).toBe(true)
    expect(row.rate_limit_group_qpm).toBe(12)
  })

  it('未认识的字段照样透传（后端以后加字段不会再被前端吞掉）', () => {
    const row = { ...FULL_ROW, future_capability: 'keep-me' } as GroupOverride
    const [out] = toGroupOverridePayload([row])
    expect((out as unknown as Record<string, unknown>).future_capability).toBe('keep-me')
  })

  it('空值统一成 null（跟随全局），而不是悄悄变成 false', () => {
    const [row] = toGroupOverridePayload([
      { group_openid: ' g1 ', tools: undefined, proactive: undefined, tasks: undefined },
    ])
    expect(row.tools).toBeNull()
    expect(row.proactive).toBeNull()
    expect(row.tasks).toBeNull()
    expect(row.memory).toBeNull()
    expect(row.humanize).toBeNull()
    expect(row.web).toBeNull()
    expect(row.exec).toBeNull()
    expect(row.enabled).toBeNull()
  })

  it('显式的 false 不会被当成空值吞掉', () => {
    const [row] = toGroupOverridePayload([
      { group_openid: 'g1', tools: false, web: false, exec: false, enabled: false },
    ])
    expect(row.tools).toBe(false)
    expect(row.web).toBe(false)
    expect(row.exec).toBe(false)
    expect(row.enabled).toBe(false)
  })

  it('行数与顺序不变，空数组仍是空数组', () => {
    const rows: GroupOverride[] = [
      { group_openid: 'a' },
      { group_openid: 'b' },
      { group_openid: 'c' },
    ]
    expect(toGroupOverridePayload(rows).map((row) => row.group_openid)).toEqual(['a', 'b', 'c'])
    expect(toGroupOverridePayload([])).toEqual([])
  })
})
