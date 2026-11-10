<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import PaginationBar from '@/components/PaginationBar.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import CopyButton from '@/components/CopyButton.vue'
import { isAbortError, messagesApi, toMessage } from '@/api'
import type { MessageDirection, MessageItem, MessageQuery, MessageStatus } from '@/api/types'
import { directionBadge, messageStatusBadge, sceneBadge } from '@/utils/badges'
import {
  formatMs,
  formatRelative,
  formatShortDateTime,
  localInputToRfc3339,
  shortId,
} from '@/utils/format'

const DIRECTIONS: { value: MessageDirection | ''; label: string }[] = [
  { value: '', label: '全部方向' },
  { value: 'in', label: '接收' },
  { value: 'out', label: '发送' },
]

const STATUSES: { value: MessageStatus | ''; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'received', label: '已接收' },
  { value: 'replied', label: '已回复' },
  { value: 'early_reply', label: '提前安抚' },
  { value: 'command', label: '命令' },
  { value: 'ignored', label: '已忽略' },
  { value: 'deduped', label: '已去重' },
  { value: 'failed', label: '失败' },
]

const q = ref('')
const direction = ref<MessageDirection | ''>('')
const status = ref<MessageStatus | ''>('')
const since = ref('')
const until = ref('')
const page = ref(1)
const pageSize = ref(50)

const items = ref<MessageItem[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref('')
const expandedId = ref<number | null>(null)

const queryParams = computed<MessageQuery>(() => ({
  q: q.value.trim() || undefined,
  direction: direction.value || undefined,
  status: status.value || undefined,
  since: localInputToRfc3339(since.value),
  until: localInputToRfc3339(until.value),
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
    const data = await messagesApi.list({ ...queryParams.value }, current.signal)
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

onMounted(() => void load())

onBeforeUnmount(() => {
  if (loadTimer !== null) window.clearTimeout(loadTimer)
  controller?.abort()
})

function onFilterChange() {
  page.value = 1
}

function resetFilters() {
  q.value = ''
  direction.value = ''
  status.value = ''
  since.value = ''
  until.value = ''
  page.value = 1
}

const activeFilterCount = computed(
  () =>
    [q.value.trim(), direction.value, status.value, since.value, until.value].filter(Boolean).length,
)
</script>

<template>
  <div class="page-fill">
    <section class="shrink-0 card">
      <div class="card-body flex flex-wrap items-end gap-3">
        <div class="min-w-[12rem] flex-1">
          <label class="label" for="msg-q">关键词</label>
          <div class="relative">
            <span class="absolute inset-y-0 left-0 flex items-center pl-2.5 text-slate-500">
              <AppIcon name="search" :size="15" />
            </span>
            <input
              id="msg-q"
              v-model="q"
              class="input pl-8"
              type="search"
              placeholder="消息内容 / 成员名 / 消息 ID"
            />
          </div>
        </div>

        <div class="w-32">
          <SelectMenu
            id="msg-direction"
            v-model="direction"
            label="方向"
            :options="DIRECTIONS"
            @change="onFilterChange"
          />
        </div>

        <div class="w-32">
          <SelectMenu
            id="msg-status"
            v-model="status"
            label="状态"
            :options="STATUSES"
            @change="onFilterChange"
          />
        </div>

        <div class="w-52">
          <label class="label" for="msg-since">起始时间</label>
          <input id="msg-since" v-model="since" class="input" type="datetime-local" @change="onFilterChange" />
        </div>

        <div class="w-52">
          <label class="label" for="msg-until">截止时间</label>
          <input id="msg-until" v-model="until" class="input" type="datetime-local" @change="onFilterChange" />
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

    <section class="card-fill">
      <div class="card-header shrink-0">
        <h2 class="card-title">
          <AppIcon name="mail" :size="16" class="text-sky-400" />
          消息记录
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
        title="没有匹配的消息"
        hint="调整关键词、方向或时间范围后重试。"
      />

      <div v-else class="table-wrap scroll-y sticky-head">
        <table class="table-base">
          <thead>
            <tr>
              <th class="th">#</th>
              <th class="th">时间</th>
              <th class="th">方向</th>
              <th class="th">场景</th>
              <th class="th">来源</th>
              <th class="th min-w-[18rem]">内容</th>
              <th class="th">消息 ID</th>
              <th class="th">状态</th>
              <th class="th text-right">耗时</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="item in items" :key="item.id">
              <tr class="row-hover cursor-pointer" @click="expandedId = expandedId === item.id ? null : item.id">
                <td class="td tabular-nums text-slate-500">{{ item.id }}</td>
                <td class="td whitespace-nowrap" :title="item.created_at ?? ''">
                  {{ formatShortDateTime(item.created_at) }}
                  <span class="block text-[11px] text-slate-500">
                    {{ formatRelative(item.created_at) }}
                  </span>
                </td>
                <td class="td"><StatusBadge :info="directionBadge(item.direction)" /></td>
                <td class="td"><StatusBadge :info="sceneBadge(item.scene)" /></td>
                <td class="td max-w-[11rem]">
                  <div class="truncate text-slate-200">{{ item.member_name || '—' }}</div>
                  <div class="flex items-center gap-1">
                    <span class="mono truncate text-slate-500" :title="item.group_openid ?? ''">
                      {{ shortId(item.group_openid) }}
                    </span>
                    <CopyButton :value="item.group_openid" label="群 OpenID" />
                  </div>
                </td>
                <td class="td max-w-[26rem]">
                  <p class="truncate text-slate-200" :title="item.content">{{ item.content }}</p>
                  <p v-if="item.error" class="truncate text-xs text-rose-300" :title="item.error">
                    {{ item.error }}
                  </p>
                </td>
                <td class="td max-w-[10rem]">
                  <div class="flex items-center gap-1">
                    <span class="mono truncate" :title="item.msg_id ?? ''">
                      {{ item.msg_id ? shortId(item.msg_id, 8, 4) : '—' }}
                    </span>
                    <CopyButton :value="item.msg_id" label="消息 ID" />
                  </div>
                </td>
                <td class="td"><StatusBadge :info="messageStatusBadge(item.status)" /></td>
                <td class="td text-right tabular-nums">{{ formatMs(item.latency_ms) }}</td>
              </tr>
              <tr v-if="expandedId === item.id" :key="`${item.id}-detail`" class="bg-slate-950/60">
                <td class="td" colspan="9">
                  <div class="grid grid-cols-1 gap-2 text-xs sm:grid-cols-2 xl:grid-cols-3">
                    <p class="flex items-center gap-1 text-slate-400">
                      群：<span class="mono break-all text-slate-300">{{ item.group_openid || '—' }}</span>
                      <CopyButton :value="item.group_openid" label="群 OpenID" />
                    </p>
                    <p class="flex items-center gap-1 text-slate-400">
                      成员：<span class="mono break-all text-slate-300">{{ item.member_openid || '—' }}</span>
                      <CopyButton :value="item.member_openid" label="成员 OpenID" />
                    </p>
                    <p class="flex items-center gap-1 text-slate-400">
                      消息 ID：<span class="mono break-all text-slate-300">{{ item.msg_id || '—' }}</span>
                      <CopyButton :value="item.msg_id" label="消息 ID" />
                    </p>
                  </div>
                  <pre
                    class="mono mt-2 max-h-56 overflow-auto rounded-md border border-slate-800 bg-slate-950 p-3 whitespace-pre-wrap text-slate-300 scrollbar-thin"
                    >{{ item.content }}</pre
                  >
                  <p v-if="item.error" class="mono mt-2 text-rose-300">错误：{{ item.error }}</p>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>

      <div class="shrink-0 border-t border-slate-800 px-4 py-3">
        <PaginationBar v-model:page="page" v-model:page-size="pageSize" :total="total" />
      </div>
    </section>
  </div>
</template>
