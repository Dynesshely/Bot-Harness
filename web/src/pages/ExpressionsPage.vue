<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import PaginationBar from '@/components/PaginationBar.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import { conversationsApi, expressionsApi, groupsApi, isAbortError, toMessage } from '@/api'
import type { Conversation, ExpressionItem, ExpressionQuery } from '@/api/types'
import { toast } from '@/composables/useToast'
import { formatDateTime, shortId } from '@/utils/format'

/**
 * 表达方式库管理页。
 *
 * 一行 = 一条「当 <情境> 时，可以用 <说法>」的语料。学习由回复管线在后台做，
 * 这里只负责人工维护与审核：搜索、按群过滤、启停、删除、强化、新增。
 *
 * 空群号 = 全局表达：所有群都能召回；有群号的表达只在那个群里召回
 * （除非打开了「全局共享」）。
 */

const ENABLED_OPTIONS: { value: string; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'true', label: '已启用' },
  { value: 'false', label: '待审 / 已停用' },
]

const q = ref('')
const groupOpenID = ref('')
const enabled = ref<'' | 'true' | 'false'>('')
const page = ref(1)
const pageSize = ref(20)

const items = ref<ExpressionItem[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref('')

/** 会话里出现过的群，用来做「群过滤」下拉与新增表单的群选择。 */
const conversations = ref<Conversation[]>([])
const groupNames = ref<Record<string, string>>({})

/** 新增表单。 */
const draftGroup = ref('')
const draftSituation = ref('')
const draftStyle = ref('')
const saving = ref(false)

/** 删除确认。 */
const pendingDelete = ref<ExpressionItem | null>(null)
const deleting = ref(false)

/** 行内操作的单飞标记：防止连点把同一条启停两次。 */
const busyID = ref<number | null>(null)

const queryParams = computed<ExpressionQuery>(() => ({
  group_openid: groupOpenID.value || undefined,
  enabled: enabled.value === '' ? '' : enabled.value === 'true',
  q: q.value.trim() || undefined,
  page: page.value,
  page_size: pageSize.value,
}))

let loadTimer: number | null = null
let controller: AbortController | null = null

function scheduleLoad(delay = 200) {
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
    const data = await expressionsApi.list({ ...queryParams.value }, current.signal)
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

onMounted(() => {
  void load()
  void loadGroups()
})

onBeforeUnmount(() => {
  if (loadTimer !== null) window.clearTimeout(loadTimer)
  controller?.abort()
})

function onFilterChange() {
  page.value = 1
}

function resetFilters() {
  q.value = ''
  groupOpenID.value = ''
  enabled.value = ''
  page.value = 1
}

const activeFilterCount = computed(
  () => [q.value.trim(), groupOpenID.value, enabled.value].filter(Boolean).length,
)

/* ------------------------------------------------------------------ *
 * 群名（仅展示优化）
 * ------------------------------------------------------------------ */

const groupOptions = computed(() => {
  const seen = new Map<string, string>()
  for (const conversation of conversations.value) {
    const openid = conversation.group_openid ?? ''
    if (!openid || seen.has(openid)) continue
    seen.set(openid, conversation.group_name || groupNames.value[openid] || '')
  }
  return Array.from(seen.entries())
    .map(([openid, name]) => ({ openid, name }))
    .sort((a, b) => (a.name || a.openid).localeCompare(b.name || b.openid, 'zh-CN'))
})

const groupFilterOptions = computed(() => [
  { value: '', label: '全部群' },
  ...groupOptions.value.map((group) => ({
    value: group.openid,
    label: group.name || shortId(group.openid, 10, 6),
    desc: group.name ? group.openid : undefined,
  })),
])

const groupSelectOptions = computed(() => [
  { value: '', label: '全局（所有群都能召回）' },
  ...groupOptions.value.map((group) => ({
    value: group.openid,
    label: group.name || shortId(group.openid, 10, 6),
    desc: group.name ? group.openid : undefined,
  })),
])

function groupLabel(openid: string): string {
  if (!openid) return '全局'
  const found = groupOptions.value.find((group) => group.openid === openid)
  const name = found?.name || groupNames.value[openid] || ''
  return name ? `${name}（${shortId(openid, 8, 6)}）` : shortId(openid, 10, 6)
}

async function loadGroups() {
  try {
    const data = await conversationsApi.list({ page: 1, page_size: 200 })
    conversations.value = Array.isArray(data?.items) ? data.items : []
  } catch {
    // 群列表只是筛选器与展示优化：拿不到就回退成手输 openid 的状态。
    conversations.value = []
  }
  const openids = groupOptions.value.map((group) => group.openid)
  if (openids.length === 0) return
  try {
    groupNames.value = (await groupsApi.names(openids)) ?? {}
  } catch {
    // 群名只是展示优化：拿不到就回退成 openid。
    groupNames.value = {}
  }
}

/* ------------------------------------------------------------------ *
 * 行内操作
 * ------------------------------------------------------------------ */

function replaceRow(row: ExpressionItem) {
  const index = items.value.findIndex((item) => item.id === row.id)
  if (index >= 0) items.value[index] = row
}

async function toggleEnabled(row: ExpressionItem) {
  if (busyID.value !== null) return
  busyID.value = row.id
  const next = !row.enabled
  try {
    await expressionsApi.setEnabled(row.id, next)
    replaceRow({ ...row, enabled: next })
    toast.success(next ? '已启用这条表达' : '已停用这条表达')
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    busyID.value = null
  }
}

async function reinforce(row: ExpressionItem) {
  if (busyID.value !== null) return
  busyID.value = row.id
  try {
    await expressionsApi.reinforce(row.id)
    replaceRow({ ...row, weight: (row.weight ?? 0) + 1 })
    toast.success('已强化：召回时会排得更靠前')
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    busyID.value = null
  }
}

function askDelete(row: ExpressionItem) {
  pendingDelete.value = row
}

function cancelDelete() {
  if (deleting.value) return
  pendingDelete.value = null
}

async function confirmDelete() {
  const row = pendingDelete.value
  if (!row || deleting.value) return
  deleting.value = true
  try {
    await expressionsApi.remove(row.id)
    toast.success('已删除')
    pendingDelete.value = null
    // 删掉最后一页的最后一行时回退一页，避免停在空白页上。
    if (items.value.length === 1 && page.value > 1) page.value -= 1
    else await load()
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    deleting.value = false
  }
}

/** 新增按钮的启用条件：与后端校验一致（情境 / 说法非空且 ≤200 字）。 */
const draftRunes = computed(() => ({
  situation: [...draftSituation.value.trim()].length,
  style: [...draftStyle.value.trim()].length,
}))

const canSave = computed(
  () =>
    !saving.value &&
    draftSituation.value.trim() !== '' &&
    draftStyle.value.trim() !== '' &&
    draftRunes.value.situation <= 200 &&
    draftRunes.value.style <= 200,
)

async function add() {
  if (!canSave.value) return
  saving.value = true
  try {
    const row = await expressionsApi.add({
      group_openid: draftGroup.value,
      situation: draftSituation.value.trim(),
      style: draftStyle.value.trim(),
    })
    draftSituation.value = ''
    draftStyle.value = ''
    toast.success(row.enabled ? '已加入表达库' : '已加入表达库（待审）')
    // 新行可能因为筛选条件不匹配而看不见；回到第一页重新拉取。
    page.value = 1
    await load()
  } catch (err) {
    toast.error(toMessage(err))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="page-fill">
    <section class="shrink-0 card">
      <div class="card-body flex flex-wrap items-end gap-3">
        <div class="min-w-[12rem] flex-1">
          <label class="label" for="expr-q">搜索</label>
          <div class="relative">
            <span class="absolute inset-y-0 left-0 flex items-center pl-2.5 text-slate-500">
              <AppIcon name="search" :size="15" />
            </span>
            <input
              id="expr-q"
              v-model="q"
              class="input pl-8"
              type="search"
              placeholder="情境 / 说法关键词"
            />
          </div>
        </div>

        <div class="w-56">
          <SelectMenu
            id="expr-group"
            v-model="groupOpenID"
            label="群过滤"
            :options="groupFilterOptions"
            aria-label="按群过滤"
            @change="onFilterChange"
          />
        </div>

        <div class="w-40">
          <SelectMenu
            id="expr-enabled"
            v-model="enabled"
            label="启用状态"
            :options="ENABLED_OPTIONS"
            @change="onFilterChange"
          />
        </div>

        <div class="flex items-center gap-2">
          <button type="button" class="btn" :disabled="loading" @click="load">
            <AppIcon name="refresh" :size="15" :class="loading ? 'animate-spin' : ''" />
            刷新
          </button>
          <button type="button" class="btn" :disabled="activeFilterCount === 0" @click="resetFilters">
            重置
          </button>
        </div>
      </div>
    </section>

    <!-- 人工新增：学习是自动的，这里补一条模型没学到但确实好用的说法 -->
    <section class="shrink-0 card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="plus" :size="16" class="text-emerald-400" />
          新增表达
        </h2>
        <span class="text-xs text-slate-500">同一群 + 同一情境 + 同一说法已存在时返回已有行</span>
      </div>
      <div class="card-body grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <SelectMenu
          id="expr-new-group"
          v-model="draftGroup"
          label="归属群"
          :options="groupSelectOptions"
          hint="留空 = 全局：所有群都能召回"
        />
        <div>
          <label class="label" for="expr-new-situation">情境 situation</label>
          <input
            id="expr-new-situation"
            v-model="draftSituation"
            class="input"
            placeholder="例如：被夸的时候"
            maxlength="200"
          />
          <p class="field-hint">{{ draftRunes.situation }} / 200 字</p>
        </div>
        <div>
          <label class="label" for="expr-new-style">说法 style</label>
          <input
            id="expr-new-style"
            v-model="draftStyle"
            class="input"
            placeholder="例如：谦虚两句再转移话题"
            maxlength="200"
          />
          <p class="field-hint">{{ draftRunes.style }} / 200 字</p>
        </div>
        <div class="flex items-end">
          <button type="button" class="btn btn-primary w-full" :disabled="!canSave" @click="add">
            {{ saving ? '保存中…' : '加入表达库' }}
          </button>
        </div>
      </div>
    </section>

    <section class="card-fill">
      <div class="card-header shrink-0">
        <h2 class="card-title">
          <AppIcon name="chatAlt" :size="16" class="text-sky-400" />
          表达方式
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
        title="还没有表达方式"
        hint="开启「表达方式」后，机器人会从群聊里学习；你也可以在上面手工加一条。"
      />

      <div v-else class="table-wrap scroll-y sticky-head">
        <table class="table-base">
          <thead>
            <tr>
              <th>#</th>
              <th>归属</th>
              <th class="min-w-[14rem]">情境</th>
              <th class="min-w-[16rem]">说法</th>
              <th>来源</th>
              <th>状态</th>
              <th>使用</th>
              <th>权重</th>
              <th>创建时间</th>
              <th class="text-right">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in items" :key="row.id">
              <td class="tabular-nums text-slate-500">{{ row.id }}</td>
              <td class="max-w-[12rem] truncate text-xs text-slate-400" :title="row.group_openid">
                {{ groupLabel(row.group_openid) }}
              </td>
              <td class="max-w-[18rem] whitespace-normal break-words">{{ row.situation }}</td>
              <td class="max-w-[20rem] whitespace-normal break-words">{{ row.style }}</td>
              <td class="text-xs text-slate-500">{{ row.source || '—' }}</td>
              <td>
                <span
                  class="badge"
                  :class="
                    row.enabled
                      ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
                      : 'border-amber-500/40 bg-amber-500/10 text-amber-300'
                  "
                >
                  {{ row.enabled ? '已启用' : '待审 / 停用' }}
                </span>
              </td>
              <td class="tabular-nums text-slate-400">{{ row.use_count ?? 0 }}</td>
              <td class="tabular-nums text-slate-400">{{ row.weight ?? 0 }}</td>
              <td class="whitespace-nowrap text-slate-400">{{ formatDateTime(row.created_at) }}</td>
              <td>
                <div class="flex items-center justify-end gap-1">
                  <button
                    type="button"
                    class="btn btn-sm"
                    :disabled="busyID !== null"
                    :title="row.enabled ? '停用这条（不再注入提示词）' : '审核通过，启用这条'"
                    @click="toggleEnabled(row)"
                  >
                    <AppIcon :name="row.enabled ? 'pause' : 'play'" :size="14" />
                    {{ row.enabled ? '停用' : '启用' }}
                  </button>
                  <button
                    type="button"
                    class="btn btn-sm"
                    :disabled="busyID !== null"
                    title="强化：提高召回排序权重"
                    @click="reinforce(row)"
                  >
                    <AppIcon name="arrowDown" :size="14" class="rotate-180" />
                    强化
                  </button>
                  <button
                    type="button"
                    class="btn btn-sm btn-ghost text-rose-400"
                    title="删除这条表达"
                    @click="askDelete(row)"
                  >
                    <AppIcon name="trash" :size="14" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="total > 0" class="shrink-0 border-t border-slate-800 px-4 py-3">
        <PaginationBar
          v-model:page="page"
          v-model:page-size="pageSize"
          :total="total"
        />
      </div>
    </section>

    <ConfirmDialog
      :open="pendingDelete !== null"
      title="删除表达方式"
      :message="pendingDelete ? `确认删除「${pendingDelete.situation} → ${pendingDelete.style}」？` : ''"
      confirm-text="删除"
      danger
      :loading="deleting"
      @confirm="confirmDelete"
      @cancel="cancelDelete"
    />
  </div>
</template>
