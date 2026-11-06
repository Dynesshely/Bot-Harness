import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import router from '@/router'
import { ensureAuth, resetAuth } from '@/stores/auth'

/**
 * 真导航（含路由守卫）的行为测试。
 *
 * routes.spec.ts 只做「名字 ↔ 路由表」的静态/解析对齐；这里用真 router.push 走一遍
 * beforeEach，钉住两件曾经出过事的行为：
 *   1) 改名后按 name 跳转会 MATCHER_NOT_FOUND 整页白屏；
 *   2) 设置页的 name 落在空路径子路由上，只渲染出一个没有内容的空壳。
 */

const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()

function jsonResponse(body: unknown, status = 200): Response {
  return { status, text: async () => JSON.stringify(body) } as unknown as Response
}

/** 让下一次 /api/auth/me 返回指定登录态。 */
function mockAuth(authenticated: boolean): void {
  fetchMock.mockImplementation(async (input) => {
    const url = String(input)
    if (url.includes('/auth/me')) {
      return jsonResponse({ ok: true, data: { authenticated, username: 'admin' } })
    }
    return jsonResponse({ ok: false, error: { code: 'not_mocked', message: url } }, 404)
  })
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  resetAuth()
})

describe('登录守卫', () => {
  it('未登录访问受保护页面 → 跳登录页并带上 redirect', async () => {
    mockAuth(false)
    await ensureAuth(true)
    await router.push('/policies')

    expect(router.currentRoute.value.name).toBe('login')
    expect(router.currentRoute.value.query.redirect).toBe('/policies')
    expect(document.title).toBe('登录 · Bot Harness')
  })

  it('已登录访问受保护页面 → 放行', async () => {
    mockAuth(true)
    await ensureAuth(true)
    await router.push('/policies')

    expect(router.currentRoute.value.name).toBe('policies')
    expect(router.currentRoute.value.meta.title).toBe('策略配置')
    expect(document.title).toBe('策略配置 · Bot Harness')
  })

  it('已登录再访问登录页 → 回到 redirect 指定的页面', async () => {
    mockAuth(true)
    await ensureAuth(true)
    await router.push('/login?redirect=/logs')

    expect(router.currentRoute.value.name).toBe('logs')
  })

  it('已登录访问未知路径 → 回概览而不是白屏', async () => {
    mockAuth(true)
    await ensureAuth(true)
    await router.push('/no-such-page')

    expect(router.currentRoute.value.name).toBe('dashboard')
  })
})

describe('侧边栏每个入口都能真的导航过去', () => {
  it('NAV 的每个 name 导航后都落到有内容的页面（settings 必须落到二级页）', async () => {
    mockAuth(true)
    await ensureAuth(true)

    const { readFileSync } = await import('node:fs')
    const { join } = await import('node:path')
    const layout = readFileSync(join(process.cwd(), 'src/components/AppLayout.vue'), 'utf8')
    const navBlock = layout.slice(
      layout.indexOf('const NAV: NavItem[] = ['),
      layout.indexOf('\n]', layout.indexOf('const NAV: NavItem[] = [')),
    )
    const names = [...navBlock.matchAll(/name: '([^']+)'/g)].map((match) => match[1])
    expect(names.length).toBeGreaterThan(0)

    for (const name of names) {
      await router.push({ name })
      const current = router.currentRoute.value
      expect(current.name, '导航到 ' + name + ' 后没有落地路由').toBeTruthy()
      expect(current.matched.length, '导航到 ' + name + ' 后没有渲染记录').toBeGreaterThan(0)
      if (name === 'settings') {
        // 侧栏点「系统设置」必须进第一个二级页，不能停在只有 tab 的空壳上
        expect(String(current.name).startsWith('settings-'), 'settings 停在了空壳路由上').toBe(true)
      } else {
        expect(current.name).toBe(name)
      }
    }
  })

  it('设置页 6 个 tab 都能导航过去并渲染出组件', async () => {
    mockAuth(true)
    await ensureAuth(true)
    const tabs = [
      '/settings/provider',
      '/settings/reply',
      '/settings/capability',
      '/settings/proactive',
      '/settings/connection',
      '/settings/system',
    ]
    for (const tab of tabs) {
      await router.push(tab)
      expect(router.currentRoute.value.path).toBe(tab)
      // 根布局 + settings 父级 + 具体二级页
      expect(router.currentRoute.value.matched.length).toBe(3)
      expect(String(router.currentRoute.value.name).startsWith('settings-')).toBe(true)
    }
  })
})
