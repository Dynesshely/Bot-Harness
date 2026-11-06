import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  formatBytes,
  formatDateTime,
  formatDuration,
  formatMs,
  formatNumber,
  formatRelative,
  formatShortDateTime,
  formatTime,
  localInputToRfc3339,
  shortId,
  truncate,
} from '@/utils/format'

/**
 * 全部时间断言都用「本地时间构造 → toISOString()」的方式喂数据，
 * 这样断言结果与机器时区无关（CI 与开发机时区不同也不会飘）。
 */
function localIso(
  year: number,
  month: number,
  day: number,
  hour: number,
  minute: number,
  second = 0,
): string {
  return new Date(year, month - 1, day, hour, minute, second).toISOString()
}

const INVALID_INPUTS = [undefined, null, '', 'not-a-date']

describe('formatDateTime / formatTime / formatShortDateTime', () => {
  it('按浏览器本地时区渲染，并补齐两位', () => {
    const iso = localIso(2026, 7, 21, 10, 59, 5)
    expect(formatDateTime(iso)).toBe('2026-07-21 10:59:05')
    expect(formatTime(iso)).toBe('10:59:05')
    expect(formatShortDateTime(iso)).toBe('07-21 10:59')
  })

  it('个位数月/日/时/分/秒也补零', () => {
    const iso = localIso(2026, 1, 2, 3, 4, 5)
    expect(formatDateTime(iso)).toBe('2026-01-02 03:04:05')
    expect(formatTime(iso)).toBe('03:04:05')
    expect(formatShortDateTime(iso)).toBe('01-02 03:04')
  })

  it.each(INVALID_INPUTS)('无效输入 %s 一律显示为 —', (value) => {
    expect(formatDateTime(value)).toBe('—')
    expect(formatTime(value)).toBe('—')
    expect(formatShortDateTime(value)).toBe('—')
    expect(formatRelative(value)).toBe('—')
  })
})

describe('formatRelative', () => {
  const NOW = new Date(2026, 6, 21, 12, 0, 0)

  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(NOW)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  function before(ms: number): string {
    return new Date(NOW.getTime() - ms).toISOString()
  }

  function after(ms: number): string {
    return new Date(NOW.getTime() + ms).toISOString()
  }

  it('5 秒内算「刚刚」', () => {
    expect(formatRelative(NOW.toISOString())).toBe('刚刚')
    expect(formatRelative(before(4_999))).toBe('刚刚')
  })

  it('分钟 / 小时 / 天各自向下取整', () => {
    expect(formatRelative(before(30_000))).toBe('30 秒前')
    expect(formatRelative(before(60_000))).toBe('1 分钟前')
    expect(formatRelative(before(59 * 60_000))).toBe('59 分钟前')
    expect(formatRelative(before(3_600_000))).toBe('1 小时前')
    expect(formatRelative(before(23 * 3_600_000))).toBe('23 小时前')
    expect(formatRelative(before(86_400_000))).toBe('1 天前')
    expect(formatRelative(before(29 * 86_400_000))).toBe('29 天前')
  })

  it('未来时间用「后」', () => {
    expect(formatRelative(after(30_000))).toBe('30 秒后')
    expect(formatRelative(after(2 * 60_000))).toBe('2 分钟后')
  })

  it('超过 30 天退化成绝对时间', () => {
    expect(formatRelative(before(40 * 86_400_000))).toBe(
      formatDateTime(before(40 * 86_400_000)),
    )
  })
})

describe('formatNumber', () => {
  it('按 zh-CN 分组', () => {
    expect(formatNumber(0)).toBe('0')
    expect(formatNumber(1234)).toBe('1,234')
    expect(formatNumber(-1234567)).toBe('-1,234,567')
  })

  it('空值与 NaN 显示 —', () => {
    expect(formatNumber(undefined)).toBe('—')
    expect(formatNumber(null)).toBe('—')
    expect(formatNumber(Number.NaN)).toBe('—')
  })
})

describe('formatBytes', () => {
  it('小于 1 KiB 用 B，其余按 1024 进位', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1023)).toBe('1023 B')
    expect(formatBytes(1024)).toBe('1.00 KB')
    expect(formatBytes(1536)).toBe('1.50 KB')
    expect(formatBytes(1024 * 1024)).toBe('1.00 MB')
    expect(formatBytes(1024 * 1024 * 1024 * 2)).toBe('2.00 GB')
  })

  it('大数字自动减少小数位，避免撑破布局', () => {
    expect(formatBytes(10.5 * 1024 * 1024)).toBe('10.5 MB')
    expect(formatBytes(150 * 1024 * 1024)).toBe('150 MB')
  })

  it('空值与 NaN 显示 —', () => {
    expect(formatBytes(undefined)).toBe('—')
    expect(formatBytes(null)).toBe('—')
    expect(formatBytes(Number.NaN)).toBe('—')
  })
})

describe('formatDuration', () => {
  it('按 秒 / 分 / 小时 / 天 组合', () => {
    expect(formatDuration(0)).toBe('0 秒')
    expect(formatDuration(45)).toBe('45 秒')
    expect(formatDuration(60)).toBe('1 分')
    expect(formatDuration(90)).toBe('1 分')
    expect(formatDuration(3600)).toBe('1 小时')
    expect(formatDuration(3661)).toBe('1 小时 1 分')
    expect(formatDuration(5400)).toBe('1 小时 30 分')
    expect(formatDuration(86400)).toBe('1 天')
    expect(formatDuration(90000)).toBe('1 天 1 小时')
  })

  it('负数归零、小数向下取整、空值显示 —', () => {
    expect(formatDuration(-10)).toBe('0 秒')
    expect(formatDuration(59.9)).toBe('59 秒')
    expect(formatDuration(undefined)).toBe('—')
    expect(formatDuration(Number.NaN)).toBe('—')
  })
})

describe('formatMs', () => {
  it('小于 1 秒用 ms，否则用 s', () => {
    expect(formatMs(0)).toBe('0 ms')
    expect(formatMs(320)).toBe('320 ms')
    expect(formatMs(999.4)).toBe('999 ms')
    expect(formatMs(1000)).toBe('1.00 s')
    expect(formatMs(2500)).toBe('2.50 s')
    expect(formatMs(10000)).toBe('10.0 s')
    expect(formatMs(60500)).toBe('60.5 s')
  })

  it('空值显示 —', () => {
    expect(formatMs(null)).toBe('—')
    expect(formatMs(Number.NaN)).toBe('—')
  })
})

describe('shortId / truncate', () => {
  it('长 ID 中间省略，短 ID 原样返回', () => {
    expect(shortId(undefined)).toBe('—')
    expect(shortId('')).toBe('—')
    expect(shortId('abc')).toBe('abc')
    // 长度 <= head + tail + 1 时不省略
    expect(shortId('abcdefghijk')).toBe('abcdefghijk')
    expect(shortId('abcdefghijkl')).toBe('abcdef…ijkl')
    expect(shortId('abcdefghijkl', 2, 2)).toBe('ab…kl')
  })

  it('truncate 超长才加省略号，空值返回空串', () => {
    expect(truncate(undefined)).toBe('')
    expect(truncate('')).toBe('')
    expect(truncate('短文本')).toBe('短文本')
    expect(truncate('a'.repeat(120))).toBe('a'.repeat(120))
    expect(truncate('a'.repeat(121))).toBe(`${'a'.repeat(120)}…`)
    expect(truncate('a'.repeat(10), 3)).toBe('aaa…')
  })
})

describe('localInputToRfc3339', () => {
  it('datetime-local 的本地时间转 RFC3339', () => {
    expect(localInputToRfc3339('2026-07-21T10:59')).toBe(
      new Date(2026, 6, 21, 10, 59).toISOString(),
    )
  })

  it('空值与非法值返回 undefined', () => {
    expect(localInputToRfc3339('')).toBeUndefined()
    expect(localInputToRfc3339('not-a-date')).toBeUndefined()
  })
})
