import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import appRouter from '@/router'

// 布局会挂状态轮询与登录态；单测里全部替换成纯假数据，避免真发请求。
vi.mock('@/stores/status', () => ({
  fetchStatus: vi.fn(async () => undefined),
  startStatusPolling: vi.fn(),
  stopStatusPolling: vi.fn(),
  useStatusState: () => ({ data: null, error: '', updatedAt: 0 }),
}))

vi.mock('@/stores/auth', () => ({
  ensureAuth: vi.fn(async () => true),
  login: vi.fn(async () => undefined),
  logout: vi.fn(async () => undefined),
  resetAuth: vi.fn(),
  useAuthState: () => ({
    checked: true,
    authenticated: true,
    username: 'admin',
    authMode: 'token',
  }),
}))

const NAV_LABELS = [
  '概览',
  '会话管理',
  '消息记录',
  '策略配置',
  '情绪状态',
  '定时任务',
  '长期记忆',
  '工具调用',
  '日志',
  '系统设置',
]

function mountLayout(router: ReturnType<typeof createRouter>) {
  return mount(AppLayout, {
    global: {
      plugins: [router],
      stubs: {
        // 图标 / 状态徽章与导航无关；RouterView 不渲染子页面（只测外壳）
        AppIcon: true,
        StatusBadge: true,
        RouterView: true,
      },
    },
  })
}

function navLinks(wrapper: ReturnType<typeof mount>) {
  return wrapper.findAll('a.nav-link')
}

describe('AppLayout 侧边栏', () => {
  it('真路由下 10 个入口全部渲染，顺序与文案与 NAV 一致', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    await appRouter.push('/')
    const wrapper = mountLayout(appRouter)

    const links = navLinks(wrapper)
    expect(links).toHaveLength(NAV_LABELS.length)
    expect(links.map((link) => link.text())).toEqual(NAV_LABELS)
    // 每个链接都必须真的解析成功，否则渲染期就会 MATCHER_NOT_FOUND
    for (const link of links) {
      expect(link.attributes('href')?.startsWith('/')).toBe(true)
    }
    // 没有「路由不存在，已跳过」的告警
    const navWarnings = warn.mock.calls.filter((call) => String(call[0]).includes('[nav]'))
    expect(navWarnings).toEqual([])
    expect(wrapper.find('h1').text()).toBe('概览')
    // 侧栏品牌（产品名统一为 Bot Harness，别在改文案时回退）
    expect(wrapper.find('aside').text()).toContain('Bot Harness')
    expect(wrapper.find('aside').text()).not.toContain('QQ AI 机器人')
  })

  it('设置了子页面时，父入口保持高亮', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    await appRouter.push('/settings/reply')
    const wrapper = mountLayout(appRouter)

    const active = wrapper.findAll('a.nav-link-active')
    expect(active).toHaveLength(1)
    expect(active[0].text()).toBe('系统设置')
    // 顶栏标题取当前二级页的 meta.title
    expect(wrapper.find('h1').text()).toBe('回复与人味')
    expect(warn).not.toHaveBeenCalledWith(expect.stringContaining('[nav]'), expect.anything())
  })

  it('窄屏：侧栏默认收起，点「打开导航」后滑出（class 断言）', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    await appRouter.push('/')
    const wrapper = mountLayout(appRouter)

    const aside = wrapper.find('aside')
    // 移动端抽屉：默认平移出屏幕，桌面端（lg:）固定可见
    expect(aside.classes()).toContain('-translate-x-full')
    expect(aside.classes()).toContain('lg:translate-x-0')
    expect(aside.classes()).toContain('w-64')

    const openButton = wrapper.find('button[aria-label="打开导航"]')
    expect(openButton.exists()).toBe(true)
    await openButton.trigger('click')
    expect(aside.classes()).toContain('translate-x-0')
    expect(aside.classes()).not.toContain('-translate-x-full')

    // 打开后出现遮罩，点遮罩收起
    const overlay = wrapper.find('.fixed.inset-0.z-40')
    expect(overlay.exists()).toBe(true)
    await overlay.trigger('click')
    expect(aside.classes()).toContain('-translate-x-full')
    expect(warn).not.toHaveBeenCalledWith(expect.stringContaining('[nav]'), expect.anything())
  })

  it('路由被改名后只跳过缺失入口并告警，不会整页白屏', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    const partialRouter = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', name: 'dashboard', component: { template: '<div />' } },
        { path: '/login', name: 'login', component: { template: '<div />' } },
      ],
    })

    const wrapper = mountLayout(partialRouter)

    // 只有 dashboard 存在 → 只剩一个入口，其余被跳过
    const links = navLinks(wrapper)
    expect(links).toHaveLength(1)
    expect(links[0].text()).toBe('概览')
    // 但缺失的名字必须被报出来（否则改名无人察觉）
    const messages = warn.mock.calls.map((call) => call.map(String).join(' '))
    const navWarning = messages.find((message) => message.includes('[nav]'))
    expect(navWarning).toBeTruthy()
    for (const missing of [
      'conversations',
      'messages',
      'policies',
      'affect',
      'tasks',
      'memories',
      'tools',
      'logs',
      'settings',
    ]) {
      expect(navWarning).toContain(missing)
    }
    // 外壳仍然渲染出来了（不是白屏）
    expect(wrapper.find('h1').exists()).toBe(true)
  })
})
