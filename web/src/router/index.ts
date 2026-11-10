import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import { ensureAuth } from '@/stores/auth'

declare module 'vue-router' {
  interface RouteMeta {
    /** 页面标题（侧边栏 / 浏览器标题） */
    title?: string
    /** 无需登录即可访问 */
    public?: boolean
    /** 侧边栏隐藏 */
    hideInNav?: boolean
  }
}

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/pages/LoginPage.vue'),
    meta: { title: '登录', public: true, hideInNav: true },
  },
  {
    path: '/',
    component: () => import('@/components/AppLayout.vue'),
    children: [
      {
        path: '',
        name: 'dashboard',
        component: () => import('@/pages/DashboardPage.vue'),
        meta: { title: '概览' },
      },
      {
        path: 'conversations',
        name: 'conversations',
        component: () => import('@/pages/ConversationsPage.vue'),
        meta: { title: '会话管理' },
      },
      {
        path: 'conversations/:id',
        name: 'conversation-detail',
        component: () => import('@/pages/ConversationDetailPage.vue'),
        meta: { title: '会话详情', hideInNav: true },
      },
      {
        path: 'policies',
        name: 'policies',
        component: () => import('@/pages/PoliciesPage.vue'),
        meta: { title: '策略配置' },
      },
      {
        path: 'affect',
        name: 'affect',
        component: () => import('@/pages/AffectPage.vue'),
        meta: { title: '情绪状态' },
      },
      {
        path: 'join',
        name: 'join',
        component: () => import('@/pages/JoinPage.vue'),
        meta: { title: '自主加入' },
      },
      {
        path: 'messages',
        name: 'messages',
        component: () => import('@/pages/MessagesPage.vue'),
        meta: { title: '消息记录' },
      },
      {
        path: 'tasks',
        name: 'tasks',
        component: () => import('@/pages/TasksPage.vue'),
        meta: { title: '定时任务' },
      },
      {
        path: 'memories',
        name: 'memories',
        component: () => import('@/pages/MemoriesPage.vue'),
        meta: { title: '长期记忆' },
      },
      {
        path: 'tools',
        name: 'tools',
        component: () => import('@/pages/ToolsPage.vue'),
        meta: { title: '工具调用' },
      },
      {
        path: 'logs',
        name: 'logs',
        component: () => import('@/pages/LogsPage.vue'),
        meta: { title: '日志' },
      },
      {
        path: 'settings',
        component: () => import('@/pages/settings/SettingsLayout.vue'),
        meta: { title: '系统设置' },
        children: [
          // 侧边栏（AppLayout）按 name 跳转，而 name 必须挂在「空路径 + 重定向」的子路由上。
          // 挂在父路由上时 push({ name: 'settings' }) 只匹配到父记录（没有子页面），
          // 会渲染出一个只有 tab 和保存栏的空壳（vue-router 的 R0103 警告的就是这件事）。
          { path: '', name: 'settings', redirect: '/settings/provider' },
          {
            path: 'provider',
            name: 'settings-provider',
            component: () => import('@/pages/settings/ProviderSettings.vue'),
            meta: { title: '模型与提示词', hideInNav: true },
          },
          {
            path: 'reply',
            name: 'settings-reply',
            component: () => import('@/pages/settings/ReplySettings.vue'),
            meta: { title: '回复与人味', hideInNav: true },
          },
          {
            path: 'capability',
            name: 'settings-capability',
            component: () => import('@/pages/settings/CapabilitySettings.vue'),
            meta: { title: '能力', hideInNav: true },
          },
          {
            path: 'proactive',
            name: 'settings-proactive',
            component: () => import('@/pages/settings/ProactiveSettings.vue'),
            meta: { title: '主动消息', hideInNav: true },
          },
          {
            path: 'connection',
            name: 'settings-connection',
            component: () => import('@/pages/settings/ConnectionSettings.vue'),
            meta: { title: '接入与运维', hideInNav: true },
          },
          {
            path: 'system',
            name: 'settings-system',
            component: () => import('@/pages/settings/SystemSettings.vue'),
            meta: { title: '系统与诊断', hideInNav: true },
          },
        ],
      },
    ],
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    redirect: { name: 'dashboard' },
  },
]

const router = createRouter({
  history: createWebHistory('/'),
  routes,
  scrollBehavior: () => ({ top: 0 }),
})

router.beforeEach(async (to) => {
  const authed = await ensureAuth()
  if (to.meta.public) {
    if (authed && to.name === 'login') {
      const redirect = typeof to.query.redirect === 'string' ? to.query.redirect : '/'
      return redirect
    }
    return true
  }
  if (!authed) {
    return {
      name: 'login',
      query: to.fullPath && to.fullPath !== '/' ? { redirect: to.fullPath } : {},
    }
  }
  return true
})

router.afterEach((to) => {
  const title = to.meta.title
  document.title = title ? `${title} · Bot Harness` : 'Bot Harness · 管理后台'
})

export default router
