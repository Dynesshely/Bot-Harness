import { readFileSync, readdirSync } from 'node:fs'
import { join, relative, sep } from 'node:path'
import { describe, expect, it } from 'vitest'
import router from '@/router'

/**
 * 路由 / 侧边栏一致性守卫。
 *
 * 背景（AGENTS.md 铁律 10）：AppLayout.vue 的 NAV 按路由 name 跳转，删或改一个 name
 * 会让 router.resolve 在渲染期抛 MATCHER_NOT_FOUND —— 整个后台白屏，而且 vue-tsc
 * 与 build 都拦不住（字符串不是类型）。这里把「源码里的名字」与「真路由表」对齐。
 */

// vitest 的 root 是 web/，所以用例里从 cwd 定位源码（jsdom 下 import.meta.url 不是 file: URL）。
const SRC = join(process.cwd(), 'src')

function readSource(relativePath: string): string {
  return readFileSync(join(SRC, relativePath), 'utf8')
}

/** 从 AppLayout.vue 的 NAV 数组里取出侧边栏条目的路由名。 */
function navNames(): string[] {
  const source = readSource('components/AppLayout.vue')
  const start = source.indexOf('const NAV: NavItem[] = [')
  expect(start, 'AppLayout.vue 里找不到 NAV 数组，测试需要同步更新').toBeGreaterThan(-1)
  const end = source.indexOf('\n]', start)
  expect(end).toBeGreaterThan(start)
  const block = source.slice(start, end)
  return [...block.matchAll(/name: '([^']+)'/g)].map((match) => match[1])
}

/** 从 SettingsLayout.vue 的 SECTIONS 里取出二级页的跳转地址。 */
function settingsSectionPaths(): string[] {
  const source = readSource('pages/settings/SettingsLayout.vue')
  const start = source.indexOf('const SECTIONS = [')
  expect(start, 'SettingsLayout.vue 里找不到 SECTIONS 数组，测试需要同步更新').toBeGreaterThan(-1)
  const end = source.indexOf('] as const', start)
  expect(end).toBeGreaterThan(start)
  const block = source.slice(start, end)
  return [...block.matchAll(/to: '([^']+)'/g)].map((match) => match[1])
}

/** 递归收集 src 下的 .ts / .vue 源码（排除测试自身）。 */
function collectSources(): { path: string; text: string }[] {
  const entries = readdirSync(SRC, { recursive: true, withFileTypes: true })
  return entries
    .filter((entry) => entry.isFile() && /\.(ts|vue)$/.test(entry.name))
    .map((entry) => join(entry.parentPath, entry.name))
    .filter((path) => !path.includes('__tests__') && !path.endsWith('.d.ts'))
    .map((path) => ({ path, text: readFileSync(path, 'utf8') }))
}

/** 设置页目录下的二级页文件（不含共用布局）。 */
function settingsSectionFiles(): string[] {
  return readdirSync(join(SRC, 'pages/settings'), { withFileTypes: true })
    .filter((entry) => entry.isFile() && entry.name.endsWith('.vue') && entry.name !== 'SettingsLayout.vue')
    .map((entry) => join(SRC, 'pages/settings', entry.name))
}

/** 把 :id 这类动态段填上占位值，避免 resolve 报缺参数。 */
function withParams(path: string): Record<string, string> {
  const params: Record<string, string> = {}
  for (const match of path.matchAll(/:(\w+)/g)) params[match[1]] = 'x'
  return params
}

describe('侧边栏 NAV 与路由表', () => {
  it('NAV 里的每个 name 都能被 router.resolve 解析（防 MATCHER_NOT_FOUND 白屏）', () => {
    const names = navNames()
    // 先钉住 NAV 本身没被解析成空数组（否则下面的循环会假绿）
    expect(names.length).toBeGreaterThan(0)

    for (const name of names) {
      expect(router.hasRoute(name), 'NAV 引用了不存在的路由：' + name).toBe(true)
      const resolved = router.resolve({ name })
      expect(resolved.matched.length, '路由 ' + name + ' 没有匹配到任何记录').toBeGreaterThan(0)
      // 不能被通配路由兜成 not-found / 重定向
      expect(resolved.name).toBe(name)
      expect(resolved.path.startsWith('/')).toBe(true)
    }
  })

  it('每个「该出现在侧栏」的一级路由都有入口（NAV 孤儿检查）', () => {
    const names = new Set(navNames())
    const expected = router
      .getRoutes()
      .filter((record) => typeof record.name === 'string')
      // 只算一级页面：'/' 与 '/xxx'（设置子页是 '/settings/xxx'，不计）
      .filter((record) => record.path.split('/').filter(Boolean).length <= 1)
      .filter((record) => record.redirect === undefined)
      .filter((record) => record.meta.hideInNav !== true)
      .map((record) => record.name as string)
    expect(expected.length).toBeGreaterThan(0)

    const missing = expected.filter((name) => !names.has(name))
    expect(missing, '这些页面没有任何侧栏入口（只能手输 URL）').toEqual([])
  })

  it('NAV 条目都有 meta.title，且侧边栏文案与页面标题一致', () => {
    const source = readSource('components/AppLayout.vue')
    for (const name of navNames()) {
      const resolved = router.resolve({ name })
      expect(resolved.meta.title, '路由 ' + name + ' 缺少 meta.title').toBeTruthy()
      const label = new RegExp("name: '" + name + "', label: '([^']+)'").exec(source)?.[1]
      expect(label, 'NAV 条目 ' + name + ' 的 label 解析失败').toBeTruthy()
      expect(label, '路由 ' + name + ' 的标题与侧边栏文案不一致').toBe(resolved.meta.title)
    }
  })

  it('自主加入页：/join ↔ name join，且侧栏有入口', () => {
    expect(router.resolve('/join').name).toBe('join')
    expect(router.resolve({ name: 'join' }).path).toBe('/join')
    expect(router.resolve({ name: 'join' }).meta.title).toBe('自主加入')
    expect(navNames()).toContain('join')
  })

  it('每个命名路由都能按自己的 name 解析回去（含带参数的路由）', () => {
    for (const record of router.getRoutes()) {
      if (typeof record.name !== 'string') continue
      const resolved = router.resolve({ name: record.name, params: withParams(record.path) })
      expect(resolved.matched.length, '路由 ' + record.name + ' 解析不到记录').toBeGreaterThan(0)
      expect(resolved.name).toBe(record.name)
    }
  })

  it('未知路径由通配路由兜底，不会出现空白页', () => {
    const resolved = router.resolve('/definitely/not/a/route')
    expect(resolved.name).toBe('not-found')
    expect(resolved.matched.length).toBeGreaterThan(0)
  })
})

describe('设置页 6 个二级页', () => {
  const sectionPaths = settingsSectionPaths()
  const childNames = router
    .getRoutes()
    .filter((record) => typeof record.name === 'string' && record.name.startsWith('settings-'))
    .map((record) => record.name as string)

  it('SECTIONS 解析成功，且 6 个二级页都在导航里（新增子页不忘加 tab）', () => {
    expect(sectionPaths.length).toBe(6)
    expect(childNames.sort()).toEqual(
      sectionPaths.map((path) => router.resolve(path).name as string).sort(),
    )
  })

  it('每个二级页的跳转地址都能解析到命名路由（不是通配兜底）', () => {
    for (const path of sectionPaths) {
      const resolved = router.resolve(path)
      expect(resolved.matched.length, '设置页 tab ' + path + ' 没有匹配到路由').toBeGreaterThan(0)
      expect(resolved.name, '设置页 tab ' + path + ' 落到了通配路由').not.toBe('not-found')
    }
  })

  it('二级页都标了 hideInNav，父级 settings 有名字（侧栏靠 name 跳转）', () => {
    expect(router.hasRoute('settings')).toBe(true)
    for (const name of childNames) {
      expect(router.resolve({ name }).meta.hideInNav, name + ' 应设置 hideInNav').toBe(true)
    }
    // 空路径子路由必须重定向到第一个二级页，否则点侧栏只会渲染一个空壳。
    // 真正的跳转行为在 navigation.spec.ts 里用真导航验证（resolve 不会跟 redirect）。
    expect(readSource('router/index.ts')).toContain("redirect: '/settings/provider'")
  })

  it('设置页共用一份配置上下文（6 个二级页都必须注入 useConfig）', () => {
    const files = settingsSectionFiles()
    expect(files.length).toBe(6)
    for (const file of files) {
      expect(readFileSync(file, 'utf8'), file + ' 没有使用 useConfig()').toContain('useConfig()')
    }
  })
})

describe('源码里按 name 的跳转', () => {
  it('router.push / router.replace 里写死的 name 都真实存在', () => {
    const pattern = /router\.(?:push|replace)\(\s*\{\s*name:\s*'([^']+)'/g
    const offenders: string[] = []
    let checked = 0
    for (const file of collectSources()) {
      for (const match of file.text.matchAll(pattern)) {
        checked += 1
        if (!router.hasRoute(match[1])) offenders.push(file.path + ' → ' + match[1])
      }
    }
    expect(checked, '没扫到任何 name 跳转，正则可能需要更新').toBeGreaterThan(0)
    expect(offenders).toEqual([])
  })

  it('每个页面文件都被路由表 import 过（没有孤儿页面）', () => {
    // 路由表里全是 () => import('@/pages/...') 惰性函数，所以直接对源码做字符串比对。
    const routerSource = readSource('router/index.ts')
    const pages = readdirSync(join(SRC, 'pages'), { recursive: true, withFileTypes: true })
      .filter((entry) => entry.isFile() && entry.name.endsWith('.vue'))
      .map((entry) => join(entry.parentPath, entry.name))
    expect(pages.length).toBeGreaterThan(0)

    const orphans = pages.filter((page) => {
      const relativePath = relative(SRC, page).split(sep).join('/')
      return !routerSource.includes("'@/" + relativePath + "'")
    })
    expect(orphans).toEqual([])
  })
})
