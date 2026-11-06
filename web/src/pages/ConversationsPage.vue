<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import ModalDialog from '@/components/ModalDialog.vue'
import PaginationBar from '@/components/PaginationBar.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import CopyButton from '@/components/CopyButton.vue'
import { conversationsApi, isAbortError, mergeApi, toMessage } from '@/api'
import type {
  Conversation,
  ConversationQuery,
  ConversationScene,
  MergeableGroup,
  MergeGroupResult,
} from '@/api/types'
import { toast } from '@/composables/useToast'
import { enabledBadge, sceneBadge } from '@/utils/badges'
import { formatNumber, formatRelative, shortId, truncate } from '@/utils/format'

const SCENES: { value: ConversationScene | ''; label: string }[] = [
  { value: '', label: '全部场景' },
  { value: 'group_member', label: '群成员' },
  { value: 'group_shared', label: '群共享' },
  { value: 'c2c', label: '单聊' },
]

const q = ref('')
const scene = ref<ConversationScene | ''>('')
const enabledFilter = ref<'' | 'true' | 'false'>('')
const page = ref(1)
const pageSize = ref(20)

const items = ref<Conversation[]>([])

/* ---------------- 合并同群会话 ---------------- */
const mergeOpen = ref(false)
const mergeLoading = ref(false)
const mergeGroups = ref<MergeableGroup[]>([])
const mergeTarget = ref<MergeableGroup | null>(null)
const mergePreview = ref<MergeGroupResult | null>(null)
const merging = ref(false)

async function openMerge() {
  mergeOpen.value = true
  mergeTarget.value = null
  mergePreview.value = null
  mergeLoading.value = true
  try {
    const data = await mergeApi.candidates()
    mergeGroups.value = data.items ?? []
  } catch (err) {
    toast.error(toMessage(err))
    mergeGroups.value = []
  } finally {
    mergeLoading.value = false
  }
}

async function selectMergeGroup(group: MergeableGroup) {
  if (!group.mergeable) {
    toast.error('该群当前不是「整群共享上下文」，请先到「策略配置」切换后再合并')
    return
  }
  mergeTarget.value = group
  mergePreview.value = null
  try {
    // 先试运行：服务端会严格校验所有会话都属于该群，并把条数报回来
    mergePreview.value = await mergeApi.run(group.group_openid, group.shared_conversation_id, true)
  } catch (err) {
    toast.error(toMessage(err))
    mergeTarget.value = null
  }
}

async function confirmMerge() {
  const group = mergeTarget.value
  if (!group || merging.value) return
  merging.value = true
  try {
    const result = await mergeApi.run(group.group_openid, group.shared_conversation_id, false)
    toast.success(
      `已合并 ${result.deleted_conversations} 个会话、${result.moved_messages} 条消息` +
        (result.deduped_messages ? `，去重 ${result.deduped_messages} 条` : ''),
    )
    mergeOpen.value = false
    mergeTarget.value = null
    mergePreview.value = null
    await load()
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    merging.value = false
  }
}

const mergeableCount = computed(() => mergeGroups.value.filter((g) => g.mergeable).length)
const total = ref(0)
const loading = ref(false)
const error = ref('')

/** 清空单个会话 */
const clearTarget = ref<Conversation | null>(null)
const clearing = ref(false)
/** 清空全部会话 */
const clearAllOpen = ref(false)
/** 正在切换启用状态的会话 ID */
const togglingId = ref<string | null>(null)

const queryParams = computed<ConversationQuery>(() => ({
  q: q.value.trim() || undefined,
  scene: scene.value || undefined,
  enabled: enabledFilter.value === '' ? undefined : enabledFilter.value === 'true',
  page: page.value,
  page_size: pageSize.value,
}))

let loadTimer: number | null = null
let controller: AbortController | null = null

function scheduleLoad(delay = 180) {
  if (loadTimer !== null) window.clearTimeout(loadTimer)
  loadTimer = window.setTimeout(() => void load(), delay)
}

async function load() {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  error.value = ''
  try {
    const data = await conversationsApi.list({ ...queryParams.value }, current.signal)
    items.value = data.items ?? []
    total.value = data.total ?? 0
  } catch (err) {
    if (isAbortError(err)) return
    error.value = toMessage(err)
    items.value = []
    total.value = 0
  } finally {
    if (controller === current) {
      loading.value = false
      controller = null
    }
  }
}

watch(queryParams, () => scheduleLoad())

function onFilterChange() {
  page.value = 1
}

function resetFilters() {
  q.value = ''
  scene.value = ''
  enabledFilter.value = ''
  page.value = 1
}

onMounted(() => {
  void load()
})

onBeforeUnmount(() => {
  if (loadTimer !== null) window.clearTimeout(loadTimer)
  controller?.abort()
})

async function confirmClearOne() {
  const target = clearTarget.value
  if (!target) return
  clearing.value = true
  try {
    await conversationsApi.clear(target.id)
    toast.success('已清空该会话上下文')
    clearTarget.value = null
    await load()
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    clearing.value = false
  }
}

async function toggleEnabled(item: Conversation) {
  togglingId.value = item.id
  try {
    await conversationsApi.setEnabled(item.id, !item.enabled)
    item.enabled = !item.enabled
    toast.success(item.enabled ? '会话已恢复' : '会话已暂停')
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    togglingId.value = null
  }
}

async function confirmClearAll() {
  clearing.value = true
  try {
    await conversationsApi.clearAll()
    toast.success('已清空全部会话')
    clearAllOpen.value = false
    page.value = 1
    await load()
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    clearing.value = false
  }
}

const activeFilterCount = computed(
  () => [q.value.trim(), scene.value, enabledFilter.value].filter(Boolean).length,
)
</script>

<template>
  <div class="page-fill">
    <!-- 工具栏（固定） -->
    <section class="shrink-0 card">
      <div class="card-body flex flex-wrap items-end gap-3">
        <div class="min-w-[12rem] flex-1">
          <label class="label" for="conv-q">搜索</label>
          <div class="relative">
            <span class="absolute inset-y-0 left-0 flex items-center pl-2.5 text-slate-500">
              <AppIcon name="search" :size="15" />
            </span>
            <input
              id="conv-q"
              v-model="q"
              class="input pl-8"
              type="search"
              placeholder="群名 / 群 OpenID / 成员名 / 成员 OpenID"
            />
          </div>
        </div>

        <div class="w-36">
          <label class="label" for="conv-scene">场景</label>
          <select
            id="conv-scene"
            v-model="scene"
            class="input"
            @change="onFilterChange"
          >
            <option v-for="item in SCENES" :key="item.value" :value="item.value">
              {{ item.label }}
            </option>
          </select>
        </div>

        <div class="w-32">
          <label class="label" for="conv-enabled">状态</label>
          <select
            id="conv-enabled"
            v-model="enabledFilter"
            class="input"
            @change="onFilterChange"
          >
            <option value="">全部状态</option>
            <option value="true">已启用</option>
            <option value="false">已暂停</option>
          </select>
        </div>

        <div class="flex items-center gap-2">
          <button type="button" class="btn" :disabled="loading" @click="load">
            <AppIcon name="refresh" :size="15" :class="loading ? 'animate-spin' : ''" />
            刷新
          </button>
          <button
            type="button"
            class="btn"
            :disabled="activeFilterCount === 0"
            @click="resetFilters"
          >
            重置
          </button>
          <button
            type="button"
            class="btn"
            title="把同一个群里分散的成员会话合并进该群的共享会话"
            @click="openMerge"
          >
            <AppIcon name="users" :size="15" />
            合并同群会话
            <span v-if="mergeableCount > 0" class="ml-1 rounded bg-sky-500/20 px-1.5 text-xs text-sky-300">
              {{ mergeableCount }}
            </span>
          </button>
          <button type="button" class="btn btn-danger" @click="clearAllOpen = true">
            <AppIcon name="trash" :size="15" />
            清空全部
          </button>
        </div>
      </div>
    </section>

    <!-- 列表 -->
    <section class="card-fill">
      <div class="card-header shrink-0">
        <h2 class="card-title">
          <AppIcon name="chat" :size="16" class="text-sky-400" />
          会话列表
        </h2>
        <span v-if="loading" class="text-xs text-slate-500">加载中…</span>
      </div>

      <p
        v-if="error"
        class="mx-4 mt-4 flex shrink-0 items-center gap-2 rounded-lg border border-rose-500/40 bg-rose-950/40 px-3 py-2 text-xs text-rose-200"
      >
        <AppIcon name="warning" :size="15" class="text-rose-400" />
        {{ error }}
      </p>

      <EmptyState
        v-if="!loading && items.length === 0"
        title="没有匹配的会话"
        hint="机器人收到消息后会自动创建会话；可尝试调整搜索条件。"
      />

      <div v-else class="table-wrap scroll-y sticky-head">
        <table class="table-base">
          <thead>
            <tr>
              <th class="th">会话</th>
              <th class="th">场景</th>
              <th class="th">群</th>
              <th class="th">成员</th>
              <th class="th">状态</th>
              <th class="th text-right">消息</th>
              <th class="th text-right">轮次</th>
              <th class="th text-right">tokens</th>
              <th class="th">最近活跃</th>
              <th class="th text-right">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in items" :key="item.id" class="row-hover">
              <td class="td">
                <RouterLink
                  class="link mono"
                  :to="{ name: 'conversation-detail', params: { id: item.id } }"
                  :title="item.id"
                >
                  {{ shortId(item.id, 8, 4) }}
                </RouterLink>
              </td>
              <td class="td"><StatusBadge :info="sceneBadge(item.scene)" /></td>
              <td class="td max-w-[12rem]">
                <div class="truncate text-slate-200" :title="item.group_name ?? ''">
                  {{ item.group_name || '—' }}
                </div>
                <div class="flex items-center gap-1">
                  <span class="mono truncate text-slate-500" :title="item.group_openid ?? ''">
                    {{ shortId(item.group_openid) }}
                  </span>
                  <CopyButton :value="item.group_openid" label="群 OpenID" />
                </div>
              </td>
              <td class="td max-w-[12rem]">
                <div class="truncate text-slate-200" :title="item.member_name ?? ''">
                  {{ item.member_name || (item.scene === 'group_shared' ? '整群共享' : '—') }}
                </div>
                <div class="flex items-center gap-1">
                  <span class="mono truncate text-slate-500" :title="item.member_openid ?? ''">
                    {{ item.member_openid ? shortId(item.member_openid) : '—' }}
                  </span>
                  <CopyButton :value="item.member_openid" label="成员 OpenID" />
                </div>
              </td>
              <td class="td"><StatusBadge :info="enabledBadge(item.enabled)" /></td>
              <td class="td text-right tabular-nums">{{ formatNumber(item.message_count) }}</td>
              <td class="td text-right tabular-nums">{{ formatNumber(item.turn_count) }}</td>
              <td class="td text-right tabular-nums">{{ formatNumber(item.token_estimate) }}</td>
              <td class="td whitespace-nowrap" :title="item.last_active_at ?? ''">
                {{ formatRelative(item.last_active_at) }}
              </td>
              <td class="td">
                <div class="flex flex-wrap items-center justify-end gap-1.5">
                  <RouterLink
                    class="btn btn-sm"
                    :to="{ name: 'conversation-detail', params: { id: item.id } }"
                  >
                    详情
                  </RouterLink>
                  <button
                    type="button"
                    class="btn btn-sm"
                    :disabled="togglingId === item.id"
                    @click="toggleEnabled(item)"
                  >
                    {{ item.enabled ? '暂停' : '恢复' }}
                  </button>
                  <button
                    type="button"
                    class="btn btn-sm btn-danger"
                    :title="`清空 ${truncate(item.member_name || item.group_name || item.id, 20)} 的上下文`"
                    @click="clearTarget = item"
                  >
                    清空
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="shrink-0 border-t border-slate-800 px-4 py-3">
        <PaginationBar
          v-model:page="page"
          v-model:page-size="pageSize"
          :total="total"
        />
      </div>
    </section>

    <!-- 合并同群会话 -->
    <ModalDialog
      :open="mergeOpen"
      title="合并同群会话"
      subtitle="把同一个群里分散的成员会话合并进该群的共享会话，仅在同一群内进行"
      width="max-w-2xl"
      @close="mergeOpen = false"
    >
      <div class="space-y-3">
        <p v-if="mergeLoading" class="text-sm text-slate-400">正在读取…</p>

        <EmptyState
          v-else-if="mergeGroups.length === 0"
          title="没有需要合并的会话"
          hint="每个群的会话都已经是单个（或还没有会话）。"
        />

        <template v-else>
          <div
            v-for="group in mergeGroups"
            :key="group.group_openid"
            class="rounded-lg border px-3 py-2"
            :class="
              mergeTarget?.group_openid === group.group_openid
                ? 'border-sky-500/60 bg-sky-500/10'
                : 'border-slate-800 bg-slate-950/40'
            "
          >
            <div class="flex flex-wrap items-center gap-2">
              <span class="truncate text-sm text-slate-200">
                {{ group.group_name || shortId(group.group_openid, 10, 6) }}
              </span>
              <CopyButton :value="group.group_openid" label="群 OpenID" />
              <span
                class="badge"
                :class="
                  group.mergeable
                    ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
                    : 'border-amber-500/40 bg-amber-500/10 text-amber-300'
                "
              >
                {{ group.mergeable ? '整群共享' : `当前 ${group.context_mode}` }}
              </span>
              <span class="text-xs text-slate-500">{{ group.conversations.length }} 个会话</span>
              <button
                type="button"
                class="btn btn-sm ml-auto"
                :disabled="!group.mergeable"
                @click="selectMergeGroup(group)"
              >
                合并到共享会话
              </button>
            </div>
            <p v-if="!group.mergeable" class="mt-1 text-xs text-amber-300">
              该群当前不是「整群共享上下文」，合并后的会话不会被使用；请先到「策略配置」切换。
            </p>
            <ul v-else class="mt-1.5 space-y-0.5 text-xs text-slate-500">
              <li v-for="c in group.conversations" :key="c.id">
                · {{ c.member_name || c.scene }} — {{ c.message_count }} 条
              </li>
            </ul>
          </div>
        </template>

        <div v-if="mergePreview" class="rounded-lg border border-sky-500/40 bg-sky-500/10 px-3 py-2 text-sm">
          <p class="text-sky-200">
            将把 <b>{{ mergePreview.source_ids.length }}</b> 个会话并入共享会话，搬移
            <b>{{ mergePreview.moved_messages }}</b> 条消息；合并后成员会话会被删除。
          </p>
          <p class="mt-1 text-xs text-sky-200/80">
            服务端已校验：所有会话都属于同一个群
            <span class="mono">{{ mergeTarget?.group_openid }}</span>
            {{ mergePreview.deduped_messages ? `；另有约 ${mergePreview.deduped_messages} 条重复的机器人消息会被去重` : '' }}
          </p>
          <div class="mt-2 flex justify-end gap-2">
            <button type="button" class="btn btn-sm" @click="mergeTarget = null">取消</button>
            <button type="button" class="btn btn-primary btn-sm" :disabled="merging" @click="confirmMerge">
              {{ merging ? '合并中…' : '确认合并' }}
            </button>
          </div>
        </div>
      </div>
    </ModalDialog>

    <!-- 清空单个会话 -->
    <ConfirmDialog
      :open="clearTarget !== null"
      title="清空会话上下文"
      danger
      confirm-text="清空"
      :loading="clearing"
      :message="`将删除该会话的全部消息记录（保留会话元数据），目标：${clearTarget?.member_name || clearTarget?.group_name || clearTarget?.id || ''}`"
      @cancel="clearTarget = null"
      @confirm="confirmClearOne"
    />

    <!-- 清空全部会话 -->
    <ConfirmDialog
      :open="clearAllOpen"
      title="清空全部会话"
      danger
      confirm-text="清空全部"
      require-text="CLEAR_ALL"
      :loading="clearing"
      message="该操作会删除所有会话与上下文记录，仅保留机器人配置。请输入 CLEAR_ALL 以确认。"
      @cancel="clearAllOpen = false"
      @confirm="confirmClearAll"
    />
  </div>
</template>
