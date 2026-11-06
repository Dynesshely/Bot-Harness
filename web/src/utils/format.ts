/** 展示层格式化工具：时间一律按浏览器本地时区渲染。 */

function toDate(value?: string | null): Date | null {
  if (!value) return null
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? null : date
}

const pad = (n: number): string => String(n).padStart(2, '0')

/** 2026-07-21 10:59:00 */
export function formatDateTime(value?: string | null): string {
  const d = toDate(value)
  if (!d) return '—'
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

/** 10:59:00 */
export function formatTime(value?: string | null): string {
  const d = toDate(value)
  if (!d) return '—'
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

/** 07-21 10:59 */
export function formatShortDateTime(value?: string | null): string {
  const d = toDate(value)
  if (!d) return '—'
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 3 分钟前 / 2 小时前 */
export function formatRelative(value?: string | null): string {
  const d = toDate(value)
  if (!d) return '—'
  const diff = Date.now() - d.getTime()
  const abs = Math.abs(diff)
  const suffix = diff >= 0 ? '前' : '后'
  if (abs < 5_000) return '刚刚'
  if (abs < 60_000) return `${Math.floor(abs / 1000)} 秒${suffix}`
  if (abs < 3_600_000) return `${Math.floor(abs / 60_000)} 分钟${suffix}`
  if (abs < 86_400_000) return `${Math.floor(abs / 3_600_000)} 小时${suffix}`
  if (abs < 2_592_000_000) return `${Math.floor(abs / 86_400_000)} 天${suffix}`
  return formatDateTime(value)
}

export function formatNumber(value?: number | null): string {
  if (value === undefined || value === null || Number.isNaN(value)) return '—'
  return value.toLocaleString('zh-CN')
}

/** 字节 → 人类可读（1.5 MB） */
export function formatBytes(value?: number | null): string {
  if (value === undefined || value === null || Number.isNaN(value)) return '—'
  if (value < 1024) return `${value} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let size = value / 1024
  let unit = 0
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024
    unit += 1
  }
  return `${size.toFixed(size >= 100 ? 0 : size >= 10 ? 1 : 2)} ${units[unit]}`
}

/** 秒 → 1 天 2 小时 3 分 */
export function formatDuration(seconds?: number | null): string {
  if (seconds === undefined || seconds === null || Number.isNaN(seconds)) return '—'
  const total = Math.max(0, Math.floor(seconds))
  if (total < 60) return `${total} 秒`
  const days = Math.floor(total / 86_400)
  const hours = Math.floor((total % 86_400) / 3_600)
  const minutes = Math.floor((total % 3_600) / 60)
  const parts: string[] = []
  if (days) parts.push(`${days} 天`)
  if (hours) parts.push(`${hours} 小时`)
  if (minutes && !days) parts.push(`${minutes} 分`)
  return parts.join(' ') || '0 秒'
}

/** 毫秒 → 1.2 s / 320 ms */
export function formatMs(value?: number | null): string {
  if (value === undefined || value === null || Number.isNaN(value)) return '—'
  if (value < 1000) return `${Math.round(value)} ms`
  return `${(value / 1000).toFixed(value < 10_000 ? 2 : 1)} s`
}

/** 长 ID 中间省略：Z3JvdX…M0 */
export function shortId(value?: string | null, head = 6, tail = 4): string {
  if (!value) return '—'
  if (value.length <= head + tail + 1) return value
  return `${value.slice(0, head)}…${value.slice(-tail)}`
}

export function truncate(value?: string | null, max = 120): string {
  if (!value) return ''
  return value.length > max ? `${value.slice(0, max)}…` : value
}

/** datetime-local 输入值（YYYY-MM-DDTHH:mm）→ RFC3339 */
export function localInputToRfc3339(value: string): string | undefined {
  if (!value) return undefined
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return undefined
  return date.toISOString()
}

