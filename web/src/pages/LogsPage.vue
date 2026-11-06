<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { isAbortError, logsApi, toMessage } from '@/api'
import type { LogItem, LogLevel } from '@/api/types'
import { levelBadge, levelTextClass } from '@/utils/badges'
import { formatDateTime, formatTime } from '@/utils/format'

const LEVELS: { value: LogLevel | ''; label: string }[] = [
  { value: '', label: '全部级别' },
  { value: 'debug', label: 'debug 及以上' },
  { value: 'info', label: 'info 及以上' },
  { value: 'warn', label: 'warn 及以上' },
  { value: 'error', label: '仅 error' },
]

const AUTO_REFRESH_MS = 3000
const LIMIT = 200

const level = ref<LogLevel | ''>('')
const q = ref('')
const autoRefresh = ref(false)

const items = ref<LogItem[]>([])
const loading = ref(false)
const loadingMore = ref(false)
const error = ref('')
const expandedId = ref<number | null>(null)
const lastLoadedAt = ref(0)

let pollTimer: number | null = null
let debounceTimer: number | null = null
let controller: AbortController | null = null

function sortDesc(list: LogItem[]): LogItem[] {
  return [...list].sort((a, b) => (b.id ?? 0) - (a.id ?? 0))
}

async function load(silent = false) {
  controller?.abort()
  const current = new AbortController()
  controller = current
  if (!silent) loading.value = true
  error.value = ''
  try {
    const list = await logsApi.list(
      { level: level.value || undefined, q: q.value.trim() || undefined, limit: LIMIT },
      current.signal,
    )
    items.value = sortDesc(list)
    lastLoadedAt.value = Date.now()
  } catch (err) {
    if (isAbortError(err)) return
    error.value = toMessage(err)
  } finally {
    if (controller === current) {
      loading.value = false
      controller = null
    }
  }
}

async function loadOlder() {
  const oldest = items.value[items.value.length - 1]
  if (!oldest || loadingMore.value) return
  loadingMore.value = true
  try {
    const list = await logsApi.list({
      level: level.value || undefined,
      q: q.value.trim() || undefined,
      limit: LIMIT,
      before_id: oldest.id,
    })
    const known = new Set(items.value.map((item) => item.id))
    const merged = [...items.value, ...sortDesc(list).filter((item) => !known.has(item.id))]
    items.value = sortDesc(merged)
  } catch (err) {
    if (!isAbortError(err)) error.value = toMessage(err)
  } finally {
    loadingMore.value = false
  }
}

function scheduleLoad(delay = 200) {
  if (debounceTimer !== null) window.clearTimeout(debounceTimer)
  debounceTimer = window.setTimeout(() => void load(), delay)
}

function startPolling() {
  stopPolling()
  pollTimer = window.setInterval(() => {
    if (document.visibilityState === 'hidden') return
    void load(true)
  }, AUTO_REFRESH_MS)
}

function stopPolling() {
  if (pollTimer !== null) {
    window.clearInterval(pollTimer)
    pollTimer = null
  }
}

watch(autoRefresh, (enabled) => {
  if (enabled) {
    startPolling()
    void load(true)
  } else {
    stopPolling()
  }
})

watch([level, q], () => scheduleLoad())

onMounted(() => void load())

onBeforeUnmount(() => {
  stopPolling()
  if (debounceTimer !== null) window.clearTimeout(debounceTimer)
  controller?.abort()
})

function resetFilters() {
  level.value = ''
  q.value = ''
  autoRefresh.value = false
}

const activeFilterCount = computed(() => [level.value, q.value.trim()].filter(Boolean).length)

const errorCount = computed(() => items.value.filter((item) => item.level === 'error').length)
const warnCount = computed(() => items.value.filter((item) => item.level === 'warn').length)

function prettyFields(fields?: Record<string, unknown> | null): string {
  if (!fields || Object.keys(fields).length === 0) return ''
  try {
    return JSON.stringify(fields, null, 2)
  } catch {
    return String(fields)
  }
}
</script>

<template>
  <div class="page-fill">
    <section class="shrink-0 card">
      <div class="card-body flex flex-wrap items-end gap-3">
        <div class="w-40">
          <label class="label" for="log-level">级别</label>
          <select id="log-level" v-model="level" class="input">
            <option v-for="item in LEVELS" :key="item.value" :value="item.value">
              {{ item.label }}
            </option>
          </select>
        </div>

        <div class="min-w-[14rem] flex-1">
          <label class="label" for="log-q">关键词</label>
          <div class="relative">
            <span class="absolute inset-y-0 left-0 flex items-center pl-2.5 text-slate-500">
              <AppIcon name="search" :size="15" />
            </span>
            <input
              id="log-q"
              v-model="q"
              class="input pl-8"
              type="search"
              placeholder="日志内容 / 组件名"
            />
          </div>
        </div>

        <label class="flex cursor-pointer items-center gap-2 rounded-lg border border-slate-800 px-3 py-2">
          <input v-model="autoRefresh" type="checkbox" class="h-4 w-4 accent-sky-500" />
          <span class="text-sm text-slate-300">自动刷新</span>
          <span class="text-xs text-slate-500">每 {{ AUTO_REFRESH_MS / 1000 }} 秒</span>
        </label>

        <div class="flex items-center gap-2">
          <button type="button" class="btn" :disabled="loading" @click="load()">
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
          <AppIcon name="list" :size="16" class="text-sky-400" />
          运行日志
          <span class="text-xs font-normal text-slate-500">（最近 {{ LIMIT }} 条）</span>
        </h2>
        <div class="flex flex-wrap items-center gap-3 text-xs text-slate-500">
          <span v-if="warnCount > 0" class="text-amber-300">warn {{ warnCount }}</span>
          <span v-if="errorCount > 0" class="text-rose-300">error {{ errorCount }}</span>
          <span v-if="lastLoadedAt">更新于 {{ formatTime(new Date(lastLoadedAt).toISOString()) }}</span>
        </div>
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
        title="暂无日志"
        hint="调整级别或关键词后重试；也可开启自动刷新等待新日志。"
      />

      <div v-else class="scroll-y divide-y divide-slate-800/70">
        <div
          v-for="item in items"
          :key="item.id"
          class="px-4 py-2 transition-colors hover:bg-slate-800/30"
        >
          <div class="flex flex-wrap items-baseline gap-2">
            <span class="mono shrink-0 text-slate-500">{{ formatDateTime(item.ts) }}</span>
            <StatusBadge :info="levelBadge(item.level)" />
            <span
              v-if="item.component"
              class="mono rounded border border-slate-700 bg-slate-800/60 px-1.5 py-0.5 text-slate-400"
            >
              {{ item.component }}
            </span>
            <span class="mono min-w-0 flex-1 break-words" :class="levelTextClass(item.level)">
              {{ item.message }}
            </span>
            <button
              v-if="item.fields && Object.keys(item.fields).length > 0"
              type="button"
              class="cursor-pointer text-xs text-slate-500 transition hover:text-sky-400"
              @click="expandedId = expandedId === item.id ? null : item.id"
            >
              {{ expandedId === item.id ? '收起字段' : '字段' }}
            </button>
          </div>
          <pre
            v-if="expandedId === item.id && item.fields"
            class="mono mt-2 max-h-56 overflow-auto rounded-md border border-slate-800 bg-slate-950 p-3 text-slate-400 scrollbar-thin"
            >{{ prettyFields(item.fields) }}</pre
          >
        </div>
      </div>

      <div v-if="items.length > 0" class="flex shrink-0 justify-center border-t border-slate-800 px-4 py-3">
        <button type="button" class="btn btn-sm" :disabled="loadingMore" @click="loadOlder">
          <AppIcon name="refresh" :size="14" :class="loadingMore ? 'animate-spin' : ''" />
          加载更早的日志
        </button>
      </div>
    </section>
  </div>
</template>
