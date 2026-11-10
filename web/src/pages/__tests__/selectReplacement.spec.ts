import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * 「全站不再有原生 <select>」的防复发门。
 *
 * 原生下拉在暗色主题下会弹出系统浅色菜单，与后台风格割裂；
 * 换成 SelectMenu 之后如果谁又写回 <select>，这个用例直接红 ——
 * 扫全目录比人工 review 可靠，也顺带覆盖以后新增的页面。
 */

const SRC = join(process.cwd(), 'src')

/** 递归收集所有 .vue（跳过单测目录：那里允许在注释里提到 <select>）。 */
function vueFiles(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      if (entry === '__tests__') continue
      found.push(...vueFiles(full))
      continue
    }
    if (entry.endsWith('.vue')) found.push(full)
  }
  return found
}

/** 去掉注释，避免文档里提到 <select> 就算违规。 */
function stripComments(source: string): string {
  return source.replace(/<!--[\s\S]*?-->/g, '').replace(/\/\*[\s\S]*?\*\//g, '')
}

const FILES = vueFiles(SRC)

describe('原生 <select> 已全部替换', () => {
  it('扫描到的 .vue 数量合理（防目录算错后静默假绿）', () => {
    expect(FILES.length).toBeGreaterThan(30)
  })

  it('没有任何模板还在用 <select> / <option>', () => {
    const offenders = FILES.filter((file) => /<\/?(select|option)\b/.test(stripComments(readFileSync(file, 'utf8')))).map(
      (file) => relative(SRC, file),
    )
    expect(offenders, '这些文件里还有原生下拉：' + offenders.join(', ')).toEqual([])
  })

  it('原先有下拉的页面/组件都改用了 SelectMenu', () => {
    const expected = [
      'pages/PoliciesPage.vue',
      'pages/JoinPage.vue',
      'pages/AffectPage.vue',
      'pages/ConversationsPage.vue',
      'pages/MessagesPage.vue',
      'pages/MemoriesPage.vue',
      'pages/TasksPage.vue',
      'pages/LogsPage.vue',
      'components/PaginationBar.vue',
      'components/ConversationSidebar.vue',
      'pages/settings/ProviderSettings.vue',
      'pages/settings/ReplySettings.vue',
      'pages/settings/CapabilitySettings.vue',
      'pages/settings/ProactiveSettings.vue',
      'pages/settings/ConnectionSettings.vue',
    ]
    const missing = expected.filter((file) => {
      const source = readFileSync(join(SRC, file), 'utf8')
      // 单引号 / 双引号都算（项目里是单引号）
      return !/from\s+['"][^'"]*SelectMenu\.vue['"]/.test(source)
    })
    expect(missing, '这些文件没有引入 SelectMenu：' + missing.join(', ')).toEqual([])
  })
})
