<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import AppIcon from './AppIcon.vue'
import StatusBadge from './StatusBadge.vue'
import { logout, useAuthState } from '@/stores/auth'
import { fetchStatus, startStatusPolling, stopStatusPolling, useStatusState } from '@/stores/status'
import { toMessage } from '@/api'
import { toast } from '@/composables/useToast'
import { triggerModeBadge, wsStateBadge } from '@/utils/badges'
import { formatRelative } from '@/utils/format'

interface NavItem {
  name: string
  label: string
  icon: string
}

const NAV: NavItem[] = [
  { name: 'dashboard', label: '概览', icon: 'dashboard' },
  { name: 'conversations', label: '会话管理', icon: 'chat' },
  { name: 'messages', label: '消息记录', icon: 'mail' },
  { name: 'policies', label: '策略配置', icon: 'shield' },
  { name: 'affect', label: '情绪状态', icon: 'sparkles' },
  { name: 'join', label: '自主加入', icon: 'users' },
  { name: 'tasks', label: '定时任务', icon: 'clock' },
  { name: 'memories', label: '长期记忆', icon: 'database' },
  { name: 'tools', label: '工具调用', icon: 'bolt' },
  { name: 'logs', label: '日志', icon: 'list' },
  { name: 'settings', label: '系统设置', icon: 'cog' },
]

const route = useRoute()
const router = useRouter()
const auth = useAuthState()
const status = useStatusState()

const sidebarOpen = ref(false)
const refreshing = ref(false)
const loggingOut = ref(false)

const pageTitle = computed(() => route.meta.title ?? '管理后台')
const wsBadge = computed(() => wsStateBadge(status.data?.ws?.state, status.data?.ws?.connected))
const triggerBadge = computed(() => triggerModeBadge(status.data?.trigger_mode))
const updatedLabel = computed(() =>
  status.updatedAt ? `更新于 ${formatRelative(new Date(status.updatedAt).toISOString())}` : '尚未获取状态',
)

function isActive(name: string): boolean {
  if (name === 'dashboard') return route.name === 'dashboard'
  // 系统设置拆成了子页面（settings-provider 等），父入口要一直保持高亮。
  if (name === 'settings') {
    return typeof route.name === 'string' && route.name.startsWith('settings')
  }
  return route.name === name || route.path.startsWith(`/${name}`)
}

/**
 * 只渲染确实存在的路由。
 *
 * 之前这里直接 v-for 硬编码的名字：一旦某个路由改名，router.resolve 会在渲染期
 * 抛 MATCHER_NOT_FOUND，整个后台白屏。现在缺名字就跳过，最多少一个入口。
 */
const navItems = computed(() => {
  const missing = NAV.filter((item) => !router.hasRoute(item.name)).map((item) => item.name)
  if (missing.length > 0) console.warn('[nav] 路由不存在，已跳过：', missing.join(', '))
  return NAV.filter((item) => router.hasRoute(item.name))
})

watch(
  () => route.fullPath,
  () => {
    sidebarOpen.value = false
  },
)

onMounted(() => startStatusPolling(5000))
onBeforeUnmount(() => stopStatusPolling())

async function onRefresh() {
  refreshing.value = true
  await fetchStatus()
  refreshing.value = false
  if (status.error) toast.error(status.error)
}

async function onLogout() {
  loggingOut.value = true
  try {
    await logout()
    toast.success('已退出登录')
    await router.replace({ name: 'login' })
  } catch (error) {
    toast.error(toMessage(error))
  } finally {
    loggingOut.value = false
  }
}
</script>

<template>
  <div class="flex h-dvh overflow-hidden bg-slate-950">
    <!-- 移动端遮罩 -->
    <div
      v-if="sidebarOpen"
      class="fixed inset-0 z-40 bg-slate-950/70 backdrop-blur-sm lg:hidden"
      @click="sidebarOpen = false"
    />

    <!-- 侧边栏：移动端为抽屉，桌面端固定整屏高度，自身内部滚动 -->
    <aside
      class="fixed inset-y-0 left-0 z-50 flex w-64 shrink-0 flex-col border-r border-slate-800 bg-slate-900/70 backdrop-blur transition-transform duration-200 lg:static lg:z-auto lg:h-dvh lg:translate-x-0"
      :class="sidebarOpen ? 'translate-x-0' : '-translate-x-full'"
    >
      <div class="flex items-center gap-2.5 border-b border-slate-800 px-4 py-4">
        <span class="rounded-lg bg-sky-500/15 p-1.5 text-sky-400">
          <AppIcon name="sparkles" :size="20" />
        </span>
        <div class="min-w-0">
          <p class="truncate text-sm font-semibold text-slate-100">Bot Harness</p>
          <p class="truncate text-xs text-slate-500">管理后台</p>
        </div>
        <button
          type="button"
          class="ml-auto cursor-pointer rounded-md p-1 text-slate-400 hover:bg-slate-800 hover:text-slate-200 lg:hidden"
          aria-label="关闭导航"
          @click="sidebarOpen = false"
        >
          <AppIcon name="close" :size="16" />
        </button>
      </div>

      <nav class="flex-1 space-y-1 overflow-y-auto p-3 scrollbar-thin">
        <RouterLink
          v-for="item in navItems"
          :key="item.name"
          :to="{ name: item.name }"
          class="nav-link"
          :class="{ 'nav-link-active': isActive(item.name) }"
        >
          <AppIcon :name="item.icon" :size="17" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>

      <div class="space-y-3 border-t border-slate-800 p-3">
        <div class="rounded-lg border border-slate-800 bg-slate-950/50 px-3 py-2">
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-slate-500">运行状态</span>
            <StatusBadge :info="wsBadge" dot />
          </div>
          <div class="mt-2 flex items-center justify-between gap-2">
            <span class="text-xs text-slate-500">触发策略</span>
            <StatusBadge :info="triggerBadge" />
          </div>
          <p class="mt-1.5 text-[11px] text-slate-600">{{ updatedLabel }}</p>
        </div>

        <div class="flex items-center justify-between gap-2 px-1">
          <div class="min-w-0">
            <p class="truncate text-xs font-medium text-slate-300">
              {{ auth.username || 'admin' }}
            </p>
            <p class="truncate text-[11px] text-slate-600">
              {{ auth.authMode ? `认证方式：${auth.authMode}` : 'Token 认证' }}
            </p>
          </div>
          <button
            type="button"
            class="btn btn-sm"
            :disabled="loggingOut"
            title="退出登录"
            @click="onLogout"
          >
            <AppIcon name="logout" :size="14" />
            退出
          </button>
        </div>
      </div>
    </aside>

    <!-- 主区域：顶部栏固定，只有内容区滚动 -->
    <div class="flex h-dvh min-w-0 flex-1 flex-col">
      <header
        class="z-30 flex shrink-0 flex-wrap items-center gap-3 border-b border-slate-800 bg-slate-950/85 px-4 py-3 backdrop-blur"
      >
        <button
          type="button"
          class="cursor-pointer rounded-md p-1.5 text-slate-300 hover:bg-slate-800 lg:hidden"
          aria-label="打开导航"
          @click="sidebarOpen = true"
        >
          <AppIcon name="menu" :size="18" />
        </button>

        <h1 class="text-base font-semibold text-slate-100">{{ pageTitle }}</h1>

        <div class="ml-auto flex flex-wrap items-center gap-2">
          <span
            v-if="status.data?.ai?.model"
            class="badge border-slate-700 bg-slate-800/60 text-slate-300"
            :title="status.data?.ai?.base_url || '模型端点未配置（系统设置 → 模型端点）'"
          >
            <AppIcon name="cpu" :size="13" />
            {{ status.data?.ai?.model }}
          </span>
          <span
            v-if="status.data?.version"
            class="badge border-slate-700 bg-slate-800/60 text-slate-400"
          >
            v{{ status.data?.version }}
          </span>
          <button
            type="button"
            class="btn btn-sm"
            :disabled="refreshing"
            title="立即刷新状态"
            @click="onRefresh"
          >
            <AppIcon name="refresh" :size="14" :class="refreshing ? 'animate-spin' : ''" />
            刷新
          </button>
        </div>
      </header>

      <main class="scroll-area min-h-0 flex-1 p-4 lg:p-6">
        <RouterView />
      </main>
    </div>
  </div>
</template>
