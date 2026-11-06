import type { ContextMode, TriggerMode } from '@/api/types'

/** badge 配色（Tailwind utility 组合，统一在此维护）。 */
export type Tone =
  | 'sky'
  | 'emerald'
  | 'amber'
  | 'rose'
  | 'slate'
  | 'violet'
  | 'cyan'
  | 'orange'

export interface BadgeInfo {
  label: string
  tone: Tone
}

export const TONE_CLASS: Record<Tone, string> = {
  sky: 'border-sky-500/40 bg-sky-500/10 text-sky-300',
  emerald: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300',
  amber: 'border-amber-500/40 bg-amber-500/10 text-amber-300',
  rose: 'border-rose-500/40 bg-rose-500/10 text-rose-300',
  slate: 'border-slate-600/50 bg-slate-600/10 text-slate-300',
  violet: 'border-violet-500/40 bg-violet-500/10 text-violet-300',
  cyan: 'border-cyan-500/40 bg-cyan-500/10 text-cyan-300',
  orange: 'border-orange-500/40 bg-orange-500/10 text-orange-300',
}

/** received | replied | early_reply | manual | command | ignored | deduped | failed */
export function messageStatusBadge(status?: string | null): BadgeInfo {
  switch (status) {
    case 'received':
      return { label: '已接收', tone: 'sky' }
    case 'replied':
      return { label: '已回复', tone: 'emerald' }
    case 'early_reply':
      return { label: '提前安抚', tone: 'violet' }
    case 'manual':
      return { label: '手动发送', tone: 'cyan' }
    case 'command':
      return { label: '命令', tone: 'slate' }
    case 'ignored':
      return { label: '已忽略', tone: 'slate' }
    case 'deduped':
      return { label: '已去重', tone: 'amber' }
    case 'failed':
      return { label: '失败', tone: 'rose' }
    default:
      return { label: status || '未知', tone: 'slate' }
  }
}

export function directionBadge(direction?: string | null): BadgeInfo {
  switch (direction) {
    case 'in':
      return { label: '接收', tone: 'cyan' }
    case 'out':
      return { label: '发送', tone: 'violet' }
    default:
      return { label: direction || '未知', tone: 'slate' }
  }
}

export function sceneBadge(scene?: string | null): BadgeInfo {
  switch (scene) {
    case 'group_member':
      return { label: '群成员', tone: 'sky' }
    case 'group_shared':
      return { label: '群共享', tone: 'violet' }
    case 'c2c':
      return { label: '单聊', tone: 'cyan' }
    default:
      return { label: scene || '未知', tone: 'slate' }
  }
}

export function levelBadge(level?: string | null): BadgeInfo {
  switch (level) {
    case 'debug':
      return { label: 'DEBUG', tone: 'slate' }
    case 'info':
      return { label: 'INFO', tone: 'sky' }
    case 'warn':
      return { label: 'WARN', tone: 'amber' }
    case 'error':
      return { label: 'ERROR', tone: 'rose' }
    default:
      return { label: (level || 'LOG').toUpperCase(), tone: 'slate' }
  }
}

/** 日志正文的级别配色（等宽字体区域）。 */
export function levelTextClass(level?: string | null): string {
  switch (level) {
    case 'debug':
      return 'text-slate-500'
    case 'info':
      return 'text-slate-300'
    case 'warn':
      return 'text-amber-300'
    case 'error':
      return 'text-rose-300'
    default:
      return 'text-slate-300'
  }
}

export function triggerModeBadge(mode?: TriggerMode | string | null): BadgeInfo {
  switch (mode) {
    case 'at_only':
      return { label: '仅 @机器人', tone: 'sky' }
    case 'all_messages':
      return { label: '群内任意消息', tone: 'violet' }
    default:
      return { label: mode || '未设置', tone: 'slate' }
  }
}

export function contextModeBadge(mode?: ContextMode | string | null): BadgeInfo {
  switch (mode) {
    case 'per_member':
      return { label: '每人独立', tone: 'sky' }
    case 'shared':
      return { label: '整群共享', tone: 'violet' }
    default:
      return { label: mode || '未设置', tone: 'slate' }
  }
}

export function enabledBadge(enabled: boolean): BadgeInfo {
  return enabled ? { label: '已启用', tone: 'emerald' } : { label: '已暂停', tone: 'amber' }
}

export function wsStateBadge(state?: string | null, connected?: boolean): BadgeInfo {
  if (connected) return { label: state ? `已连接 · ${state}` : '已连接', tone: 'emerald' }
  switch (state) {
    case 'connecting':
    case 'identifying':
    case 'resuming':
      return { label: state, tone: 'amber' }
    case 'closed':
    case 'error':
    case 'disconnected':
      return { label: state, tone: 'rose' }
    default:
      return { label: state || '未连接', tone: 'slate' }
  }
}

export function roleLabel(role?: string | null): string {
  switch (role) {
    case 'user':
      return '用户'
    case 'assistant':
      return 'AI'
    case 'system':
      return '系统'
    default:
      return role || '未知'
  }
}

export function sseEventBadge(event: string): BadgeInfo {
  switch (event) {
    case 'message':
      return { label: 'message', tone: 'cyan' }
    case 'log':
      return { label: 'log', tone: 'amber' }
    case 'ws':
      return { label: 'ws', tone: 'violet' }
    case 'status':
      return { label: 'status', tone: 'sky' }
    default:
      return { label: event, tone: 'slate' }
  }
}
