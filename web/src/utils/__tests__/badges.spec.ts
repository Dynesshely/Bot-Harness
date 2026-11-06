import { describe, expect, it } from 'vitest'
import {
  TONE_CLASS,
  contextModeBadge,
  directionBadge,
  enabledBadge,
  levelBadge,
  levelTextClass,
  messageStatusBadge,
  roleLabel,
  sceneBadge,
  sseEventBadge,
  triggerModeBadge,
  wsStateBadge,
  type BadgeInfo,
} from '@/utils/badges'

/** 所有 badge 函数的产物都必须落在已登记的色调里，否则 Tailwind 不会生成对应类名。 */
function expectToneRegistered(info: BadgeInfo): void {
  expect(info.tone in TONE_CLASS).toBe(true)
  expect(TONE_CLASS[info.tone]).toMatch(/^border-/)
}

describe('messageStatusBadge', () => {
  it.each([
    ['received', '已接收', 'sky'],
    ['replied', '已回复', 'emerald'],
    ['early_reply', '提前安抚', 'violet'],
    ['manual', '手动发送', 'cyan'],
    ['command', '命令', 'slate'],
    ['ignored', '已忽略', 'slate'],
    ['deduped', '已去重', 'amber'],
    ['failed', '失败', 'rose'],
  ])('%s → %s / %s', (status, label, tone) => {
    const info = messageStatusBadge(status)
    expect(info).toEqual({ label, tone })
    expectToneRegistered(info)
  })

  it('未知状态退回原文案 + slate，空值显示「未知」', () => {
    expect(messageStatusBadge('weird')).toEqual({ label: 'weird', tone: 'slate' })
    expect(messageStatusBadge(undefined)).toEqual({ label: '未知', tone: 'slate' })
    expect(messageStatusBadge(null)).toEqual({ label: '未知', tone: 'slate' })
  })
})

describe('direction / scene / level', () => {
  it('directionBadge 区分收发', () => {
    expect(directionBadge('in')).toEqual({ label: '接收', tone: 'cyan' })
    expect(directionBadge('out')).toEqual({ label: '发送', tone: 'violet' })
    expect(directionBadge(undefined)).toEqual({ label: '未知', tone: 'slate' })
  })

  it('sceneBadge 区分群成员 / 群共享 / 单聊', () => {
    expect(sceneBadge('group_member')).toEqual({ label: '群成员', tone: 'sky' })
    expect(sceneBadge('group_shared')).toEqual({ label: '群共享', tone: 'violet' })
    expect(sceneBadge('c2c')).toEqual({ label: '单聊', tone: 'cyan' })
    expect(sceneBadge('other')).toEqual({ label: 'other', tone: 'slate' })
  })

  it('levelBadge 与 levelTextClass 对同一个 level 保持一致', () => {
    expect(levelBadge('debug')).toEqual({ label: 'DEBUG', tone: 'slate' })
    expect(levelBadge('info')).toEqual({ label: 'INFO', tone: 'sky' })
    expect(levelBadge('warn')).toEqual({ label: 'WARN', tone: 'amber' })
    expect(levelBadge('error')).toEqual({ label: 'ERROR', tone: 'rose' })
    expect(levelBadge(undefined)).toEqual({ label: 'LOG', tone: 'slate' })
    // 未知级别统一大写展示，不能出现 undefined
    expect(levelBadge('trace').label).toBe('TRACE')

    expect(levelTextClass('info')).toBe('text-slate-300')
    expect(levelTextClass('warn')).toBe('text-amber-300')
    expect(levelTextClass('error')).toBe('text-rose-300')
    expect(levelTextClass('debug')).toBe('text-slate-500')
    expect(levelTextClass('unknown')).toBe('text-slate-300')
  })
})

describe('triggerModeBadge / contextModeBadge / enabledBadge', () => {
  it('触发策略与上下文模式', () => {
    expect(triggerModeBadge('at_only')).toEqual({ label: '仅 @机器人', tone: 'sky' })
    expect(triggerModeBadge('all_messages')).toEqual({ label: '群内任意消息', tone: 'violet' })
    expect(triggerModeBadge(undefined)).toEqual({ label: '未设置', tone: 'slate' })
    expect(contextModeBadge('per_member')).toEqual({ label: '每人独立', tone: 'sky' })
    expect(contextModeBadge('shared')).toEqual({ label: '整群共享', tone: 'violet' })
    expect(contextModeBadge(undefined)).toEqual({ label: '未设置', tone: 'slate' })
  })

  it('enabledBadge 只有两种形态', () => {
    expect(enabledBadge(true)).toEqual({ label: '已启用', tone: 'emerald' })
    expect(enabledBadge(false)).toEqual({ label: '已暂停', tone: 'amber' })
  })
})

describe('wsStateBadge', () => {
  it('已连接优先于状态原文', () => {
    expect(wsStateBadge('connected', true)).toEqual({ label: '已连接 · connected', tone: 'emerald' })
    expect(wsStateBadge(undefined, true)).toEqual({ label: '已连接', tone: 'emerald' })
  })

  it('连接中为 amber，断开为 rose', () => {
    expect(wsStateBadge('connecting', false).tone).toBe('amber')
    expect(wsStateBadge('identifying', false).tone).toBe('amber')
    expect(wsStateBadge('resuming', false).tone).toBe('amber')
    expect(wsStateBadge('closed', false).tone).toBe('rose')
    expect(wsStateBadge('error', false).tone).toBe('rose')
    expect(wsStateBadge('disconnected', false).tone).toBe('rose')
  })

  it('未知 / 空状态不至于渲染出空 label', () => {
    expect(wsStateBadge(undefined, undefined)).toEqual({ label: '未连接', tone: 'slate' })
    expect(wsStateBadge('weird', false)).toEqual({ label: 'weird', tone: 'slate' })
  })
})

describe('roleLabel / sseEventBadge', () => {
  it('角色文案', () => {
    expect(roleLabel('user')).toBe('用户')
    expect(roleLabel('assistant')).toBe('AI')
    expect(roleLabel('system')).toBe('系统')
    expect(roleLabel(undefined)).toBe('未知')
    expect(roleLabel('tool')).toBe('tool')
  })

  it('SSE 事件名与色调', () => {
    expect(sseEventBadge('message')).toEqual({ label: 'message', tone: 'cyan' })
    expect(sseEventBadge('log')).toEqual({ label: 'log', tone: 'amber' })
    expect(sseEventBadge('ws')).toEqual({ label: 'ws', tone: 'violet' })
    expect(sseEventBadge('status')).toEqual({ label: 'status', tone: 'sky' })
    expect(sseEventBadge('other')).toEqual({ label: 'other', tone: 'slate' })
  })
})
