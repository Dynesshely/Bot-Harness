import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { emptyDraft } from '@/composables/configContext'

/**
 * 「后端有配置项、前端却没有控件」的防复发测试。
 *
 * configContext.spec 里那条「每个草稿字段都接进 patch」只覆盖**草稿已有**的键，
 * 抓不到反向缺口：后端往 Settings 加了字段、前端表单没跟上 —— 结果就是后台
 * 改不了、用户以为功能没做（D-036 要求所有配置都能在后台改）。
 *
 * 这里直接解析 internal/settings/settings.go 的 json tag 做双向比对：
 *   - 后端键 ⊆（配置草稿键 ∪ 策略页键）        ← 每个后端配置项都有地方改
 *   - 配置草稿键 ⊆ 后端键                     ← 前端不要提交后端不认识的关键字
 * 后端将来新增字段而前端没补控件，这个用例会直接红。
 */

const SETTINGS_GO = join(process.cwd(), '..', 'internal', 'settings', 'settings.go')

/** 由「策略配置」页负责的字段（不在设置页草稿里，但同样有控件）。 */
const POLICY_FIELDS = new Set([
  'trigger_mode',
  'context_mode',
  'context_scope',
  'group_allowlist',
  'member_allowlist',
  'admin_openids',
  'group_overrides',
  'blocked_keywords',
  'reply_to_bots',
  'quote_reply',
])

function structJSONKeys(structName: string): string[] {
  const source = readFileSync(SETTINGS_GO, 'utf8')
  const start = source.indexOf('type ' + structName + ' struct {')
  expect(start, '在 internal/settings/settings.go 里找不到 ' + structName).toBeGreaterThan(-1)
  const end = source.indexOf('\n}', start)
  expect(end).toBeGreaterThan(start)
  const block = source.slice(start, end)
  return [...block.matchAll(/json:"([a-z0-9_]+)(?:,[a-z]+)?"/g)].map((match) => match[1])
}

const draftKeys = new Set(Object.keys(emptyDraft()))
const backendKeys = structJSONKeys('Settings')

describe('后端配置键 vs 前端控件键', () => {
  it('settings.go 解析成功（防解析失效后静默假绿）', () => {
    expect(backendKeys.length).toBeGreaterThan(60)
    expect(new Set(backendKeys).size).toBe(backendKeys.length)
  })

  it('后端的每个配置键都有前端入口（配置草稿或策略页）', () => {
    const missing = backendKeys.filter((key) => !draftKeys.has(key) && !POLICY_FIELDS.has(key))
    expect(
      missing,
      '这些后端配置项在前端没有控件：' + missing.join(', '),
    ).toEqual([])
  })

  it('策略页负责的字段都真实存在于后端 Settings（防止清单写错）', () => {
    const backend = new Set(backendKeys)
    const unknown = [...POLICY_FIELDS].filter((key) => !backend.has(key))
    expect(unknown).toEqual([])
  })

  it('配置草稿里的键后端都认识（防止拼错键名被后端忽略）', () => {
    const backend = new Set(backendKeys)
    const unknown = [...draftKeys].filter((key) => !backend.has(key))
    expect(unknown, '这些草稿键后端不认：' + unknown.join(', ')).toEqual([])
  })

  it('GroupOverride 的七个能力覆盖字段在后端结构里都有（P0 白名单事故的对侧）', () => {
    const overrideKeys = new Set(structJSONKeys('GroupOverride'))
    for (const key of ['tools', 'proactive', 'tasks', 'memory', 'humanize', 'web', 'exec']) {
      expect(overrideKeys.has(key), '后端 GroupOverride 缺字段：' + key).toBe(true)
    }
  })
})
