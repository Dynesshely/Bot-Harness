<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { conversationsApi, isAbortError, toMessage } from '@/api'
import type { Conversation, ConversationScene } from '@/api/types'
import { sceneBadge } from '@/utils/badges'
import { formatNumber, formatRelative, shortId } from '@/utils/format'

/**
 * 会话详情页左侧的会话切换栏。
 *
 * 只做「切换」这一件事：搜索 + 场景筛选 + 分页加载 + 高亮当前会话。
 * 清空 / 启停 / 合并等操作留在会话列表页，免得侧栏变成第二个管理页。
 */
const props = defineProps<{
  /** 当前打开的会话 id，用于高亮。 */
  activeId?: string
}>()

const router = useRouter()

const PAGE_SIZE = 30

const SCENES: { value: ConversationScene | ''; label: string }[] = [
  { value: '', label: '全部场景' },
  { value: 'group_member', label: '群成员' },
  { value: 'group_shared', label: '群共享' },
  { value: 'c2c', label: '单聊' },
]

const q = ref('')
const scene = ref<ConversationScene | ''>('')
const items = ref<Conversation[]>([])
const total = ref(0)
const loading = ref(false)
const loadingMore = ref(false)
const error = ref('')

const hasMore = computed(() => items.value.length < total.value)

let controller: AbortController | null = null
let loadTimer: number | null = null

async function load(more = false) {
  if (more && (loadingMore.value || !hasMore.value)) return
  controller?.abort()
  const current = new AbortController()
  controller = current
  if (more) loadingMore.value = true
  else loading.value = true
  error.value = ''
  try {
    const page = more ? Math.floor(items.value.length / PAGE_SIZE) + 1 : 1
    const data = await conversationsApi.list(
      {
        q: q.value.trim() || undefined,
        scene: scene.value || undefined,
        page,
        page_size: PAGE_SIZE,
      },
      current.signal,
    )
    const fresh = data.items ?? []
    if (more) {
      const known = new Set(items.value.map((item) => item.id))
      items.value = [...items.value, ...fresh.filter((item) => !known.has(item.id))]
    } else {
      items.value = fresh
    }
    total.value = data.total ?? 0
  } catch (err) {
    if (isAbortError(err)) return
    error.value = toMessage(err)
    if (!more) {
      items.value = []
      total.value = 0
    }
  } finally {
    if (controller === current) {
      loading.value = false
      loadingMore.value = false
      controller = null
    }
  }
}

/** 输入框每敲一下都请求太吵，等一下再发。 */
function scheduleLoad(delay = 220) {
  if (loadTimer !== null) window.clearTimeout(loadTimer)
  loadTimer = window.setTimeout(() => void load(), delay)
}

watch([q, scene], () => scheduleLoad())

onMounted(() => void load())

onBeforeUnmount(() => {
  if (loadTimer !== null) window.clearTimeout(loadTimer)
  controller?.abort()
})

function open(item: Conversation) {
  if (item.id === props.activeId) return
  void router.push({ name: 'conversation-detail', params: { id: item.id } })
}

function titleOf(item: Conversation): string {
  if (item.scene === 'c2c') return item.member_name || '单聊'
  return item.group_name || (item.group_openid ? `群 ${shortId(item.group_openid)}` : '未命名群')
}

function subtitleOf(item: Conversation): string {
  if (item.scene === 'group_shared') return '整群共享上下文'
  return item.member_name || (item.member_openid ? shortId(item.member_openid) : '—')
}
</script>

<template>
  <section class="card-fill">
    <div class="card-header shrink-0">
      <h2 class="card-title">
        <AppIcon name="chat" :size="15" class="text-sky-400" />
        切换会话
        <span class="text-xs font-normal text-slate-500">
          {{ loading && items.length === 0 ? '加载中…' : `共 ${formatNumber(total)} 个` }}
        </span>
      </h2>
      <button
        type="button"
        class="btn btn-sm"
        :disabled="loading"
        title="重新拉取会话列表"
        @click="load()"
      >
        <AppIcon name="refresh" :size="13" :class="loading ? 'animate-spin' : ''" />
      </button>
    </div>

    <div class="flex shrink-0 items-center gap-2 border-b border-slate-800/60 px-3 py-2">
      <div class="relative min-w-0 flex-1">
        <AppIcon
          name="search"
          :size="13"
          class="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-slate-500"
        />
        <input
          v-model="q"
          class="input py-1.5 pl-7 text-xs"
          type="search"
          placeholder="搜群名 / 成员 / OpenID"
        />
      </div>
      <div class="w-28 shrink-0">
        <SelectMenu
          v-model="scene"
          size="sm"
          aria-label="按场景筛选"
          placeholder="全部场景"
          :options="SCENES"
        />
      </div>
    </div>

    <p v-if="error" class="shrink-0 px-3 py-2 text-xs text-rose-300">{{ error }}</p>

    <div class="card-body scroll-y scrollbar-thin px-2 py-2">
      <EmptyState
        v-if="!loading && items.length === 0"
        title="没有匹配的会话"
        hint="换个关键词，或清空筛选条件。"
      />
      <ul v-else class="space-y-1.5">
        <li v-for="item in items" :key="item.id">
          <button
            type="button"
            class="w-full rounded-lg border px-2.5 py-2 text-left transition-colors"
            :class="
              item.id === activeId
                ? 'border-sky-500/50 bg-sky-500/10'
                : 'border-slate-800 bg-slate-900/40 hover:border-slate-600 hover:bg-slate-800/50'
            "
            :title="item.id"
            @click="open(item)"
          >
            <div class="flex items-center gap-2">
              <span
                class="min-w-0 flex-1 truncate text-sm"
                :class="item.id === activeId ? 'font-medium text-sky-200' : 'text-slate-200'"
              >
                {{ titleOf(item) }}
              </span>
              <span
                v-if="!item.enabled"
                class="badge shrink-0 border-amber-500/40 bg-amber-500/10 text-amber-300"
                title="该会话已暂停，机器人不会回复"
              >
                已暂停
              </span>
            </div>
            <div class="mt-1 flex items-center gap-2 text-[11px] text-slate-500">
              <StatusBadge :info="sceneBadge(item.scene)" />
              <span class="min-w-0 flex-1 truncate" :title="subtitleOf(item)">
                {{ subtitleOf(item) }}
              </span>
            </div>
            <div class="mt-0.5 flex items-center gap-2 text-[11px] text-slate-500">
              <span class="tabular-nums">{{ formatNumber(item.message_count) }} 条</span>
              <span class="ml-auto shrink-0">{{ formatRelative(item.last_active_at) }}</span>
            </div>
          </button>
        </li>
      </ul>

      <button
        v-if="hasMore"
        type="button"
        class="btn btn-sm mt-2 w-full justify-center"
        :disabled="loadingMore"
        @click="load(true)"
      >
        <AppIcon name="refresh" :size="13" :class="loadingMore ? 'animate-spin' : ''" />
        加载更多
      </button>
    </div>
  </section>
</template>
