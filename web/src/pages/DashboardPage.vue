<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import StatCard from '@/components/StatCard.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import TimeSeriesChart from '@/components/TimeSeriesChart.vue'
import CopyButton from '@/components/CopyButton.vue'
import { EVENTS_STREAM_URL, statsApi, toMessage } from '@/api'
import type {
  LogItem,
  MessageItem,
  SseEventName,
  StatsData,
  StatsRange,
  StatusData,
  WsEventPayload,
} from '@/api/types'
import { applyStatusSnapshot, fetchStatus, useStatusState } from '@/stores/status'
import { toast } from '@/composables/useToast'
import {
  contextModeBadge,
  messageStatusBadge,
  sseEventBadge,
  triggerModeBadge,
  wsStateBadge,
} from '@/utils/badges'
import {
  formatBytes,
  formatDateTime,
  formatDuration,
  formatMs,
  formatNumber,
  formatRelative,
  formatTime,
  shortId,
  truncate,
} from '@/utils/format'

const status = useStatusState()

/* ------------------------------------------------------------------ *
 * 统计
 * ------------------------------------------------------------------ */

const RANGES: StatsRange[] = ['1h', '6h', '24h', '7d']

const statsRange = ref<StatsRange>('24h')
const stats = ref<StatsData | null>(null)
const statsLoading = ref(false)
const statsError = ref('')

async function loadStats() {
  statsLoading.value = true
  statsError.value = ''
  try {
    stats.value = await statsApi.get(statsRange.value)
  } catch (error) {
    statsError.value = toMessage(error)
  } finally {
    statsLoading.value = false
  }
}

watch(statsRange, () => void loadStats())

/** 平台侧权限事实：有几个群真正会推送未 @ 的消息。 */
const platformHint = computed(() => {
  const p = status.data?.platform
  if (!p || p.known_groups === 0) return '平台权限尚未读取'
  if (p.limited_groups.length === 0) return `${p.full_access_groups.length} 个群可收到全部消息`
  if (p.full_access_groups.length === 0) return `${p.limited_groups.length} 个群仅 @机器人`
  return `${p.full_access_groups.length} 个群全量 / ${p.limited_groups.length} 个群仅@`
})

const totals = computed(() => stats.value?.totals ?? null)
const topGroups = computed(() => stats.value?.top_groups ?? [])
const topGroupMax = computed(() =>
  topGroups.value.reduce((max, item) => Math.max(max, item.messages ?? 0), 0),
)

/* ------------------------------------------------------------------ *
 * SSE 实时事件流
 * ------------------------------------------------------------------ */

const MAX_EVENTS = 200

interface StreamEvent {
  uid: number
  event: SseEventName
  at: number
  summary: string
  payload: unknown
}

const FILTERS: { value: 'all' | SseEventName; label: string }[] = [
  { value: 'all', label: '全部' },
  { value: 'message', label: '消息' },
  { value: 'log', label: '日志' },
  { value: 'ws', label: 'WS' },
  { value: 'status', label: '状态' },
]

const events = ref<StreamEvent[]>([])
const filter = ref<'all' | SseEventName>('all')
const paused = ref(false)
const skipped = ref(0)
const sseState = ref<'connecting' | 'open' | 'error' | 'closed'>('connecting')
const expanded = ref<number | null>(null)

let source: EventSource | null = null
let seq = 0

const filteredEvents = computed(() =>
  filter.value === 'all' ? events.value : events.value.filter((item) => item.event === filter.value),
)

const eventCounts = computed(() => {
  const counts: Record<string, number> = {}
  for (const item of events.value) counts[item.event] = (counts[item.event] ?? 0) + 1
  return counts
})

const sseBadge = computed(() => {
  switch (sseState.value) {
    case 'open':
      return { label: 'SSE 已连接', tone: 'emerald' as const }
    case 'connecting':
      return { label: 'SSE 连接中', tone: 'amber' as const }
    case 'error':
      return { label: 'SSE 重连中', tone: 'rose' as const }
    default:
      return { label: 'SSE 已关闭', tone: 'slate' as const }
  }
})

function summarize(name: SseEventName, payload: unknown): string {
  switch (name) {
    case 'message': {
      const item = payload as MessageItem
      const who = item?.member_name || shortId(item?.member_openid) || shortId(item?.group_openid)
      const direction = item?.direction === 'in' ? '接收' : '发送'
      const statusText = messageStatusBadge(item?.status).label
      return `${direction} · ${who} · [${statusText}] ${truncate(item?.content, 90)}`
    }
    case 'log': {
      const item = payload as LogItem
      return `[${(item?.level ?? '').toUpperCase()}] ${item?.component ?? '-'} · ${item?.message ?? ''}`
    }
    case 'ws': {
      const item = payload as WsEventPayload
      return `WebSocket ${item?.state ?? '未知'} · ${item?.connected ? '已连接' : '未连接'}`
    }
    case 'status': {
      const item = payload as StatusData
      return `状态刷新 · ws=${item?.ws?.state ?? '-'} · model=${item?.ai?.model ?? '-'} · uptime=${formatDuration(item?.uptime_seconds)}`
    }
    default:
      return name
  }
}

function makeHandler(name: SseEventName) {
  return (raw: Event) => {
    const data = (raw as MessageEvent).data
    let payload: unknown = data
    if (typeof data === 'string' && data.length > 0) {
      try {
        payload = JSON.parse(data)
      } catch {
        payload = data
      }
    }
    if (name === 'status' && payload && typeof payload === 'object') {
      applyStatusSnapshot(payload as StatusData)
    }
    if (name === 'ping') return
    if (paused.value) {
      skipped.value += 1
      return
    }
    seq += 1
    events.value.unshift({
      uid: seq,
      event: name,
      at: Date.now(),
      summary: summarize(name, payload),
      payload,
    })
    if (events.value.length > MAX_EVENTS) events.value.splice(MAX_EVENTS)
  }
}

function connect() {
  disconnect()
  sseState.value = 'connecting'
  source = new EventSource(EVENTS_STREAM_URL, { withCredentials: true })
  source.onopen = () => {
    sseState.value = 'open'
  }
  source.onerror = () => {
    sseState.value = 'error'
  }
  for (const name of FILTERS.filter((item) => item.value !== 'all').map((item) => item.value)) {
    source.addEventListener(name as string, makeHandler(name as SseEventName))
  }
  source.addEventListener('ping', makeHandler('ping'))
}

function disconnect() {
  source?.close()
  source = null
  sseState.value = 'closed'
}

function togglePause() {
  paused.value = !paused.value
  if (!paused.value) skipped.value = 0
}

function clearEvents() {
  events.value = []
  expanded.value = null
}

function pretty(payload: unknown): string {
  try {
    return JSON.stringify(payload, null, 2)
  } catch {
    return String(payload)
  }
}

/* ------------------------------------------------------------------ *
 * 生命周期
 * ------------------------------------------------------------------ */

onMounted(() => {
  connect()
  void loadStats()
  void fetchStatus()
})

onBeforeUnmount(() => {
  disconnect()
})

/* ------------------------------------------------------------------ *
 * 展示辅助
 * ------------------------------------------------------------------ */

const ws = computed(() => status.data?.ws ?? null)
const qq = computed(() => status.data?.qq ?? null)
const ai = computed(() => status.data?.ai ?? null)
const store = computed(() => status.data?.store ?? null)
const counters = computed(() => status.data?.counters ?? null)
const webhook = computed(() => status.data?.webhook ?? null)

const wsBadge = computed(() => wsStateBadge(ws.value?.state, ws.value?.connected))
const tokenBadge = computed(() =>
  qq.value?.token_valid
    ? { label: '有效', tone: 'emerald' as const }
    : { label: '无效 / 未获取', tone: 'rose' as const },
)

function refreshAll() {
  void fetchStatus()
  void loadStats()
  toast.info('已刷新状态与统计')
}
</script>

<template>
  <div class="space-y-5">
    <!-- 顶部状态卡 -->
    <section class="space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h2 class="flex items-center gap-2 text-sm font-semibold text-slate-300">
          <AppIcon name="dashboard" :size="16" class="text-sky-400" />
          运行状态
        </h2>
        <div class="flex items-center gap-2">
          <span v-if="status.error" class="badge border-rose-500/40 bg-rose-500/10 text-rose-300">
            {{ status.error }}
          </span>
          <span v-else-if="status.updatedAt" class="text-xs text-slate-500">
            数据时间 {{ formatTime(new Date(status.updatedAt).toISOString()) }}
          </span>
          <button type="button" class="btn btn-sm" @click="refreshAll">
            <AppIcon name="refresh" :size="14" />
            刷新
          </button>
        </div>
      </div>

      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label="WS 连接状态"
          :value="ws?.connected ? '已连接' : (ws?.state ?? '未连接')"
          :tone="ws?.connected ? 'emerald' : 'rose'"
          icon="bolt"
          :hint="
            ws
              ? `延迟 ${formatMs(ws.latency_ms)} · 心跳 ${formatMs(ws.heartbeat_interval_ms)} · 重连 ${formatNumber(ws.reconnects)} 次`
              : '暂无数据'
          "
        />
        <StatCard
          label="会话状态"
          :value="`${triggerModeBadge(status.data?.trigger_mode).label} / ${contextModeBadge(status.data?.context_mode).label}`"
          icon="chat"
          :hint="platformHint"
        />
        <StatCard
          label="当前模型"
          :value="ai?.model ?? '—'"
          icon="cpu"
          mono
          :hint="ai?.base_url || '未配置模型端点'"
        />
        <StatCard
          label="Token 有效期"
          :value="formatDuration(qq?.token_expires_in_seconds)"
          :tone="qq?.token_valid ? 'emerald' : 'rose'"
          icon="key"
          :hint="`${tokenBadge.label} · 已刷新 ${formatNumber(qq?.token_refreshes)} 次`"
        />
        <StatCard
          label="存储大小"
          :value="formatBytes(store?.size_bytes)"
          icon="database"
          :hint="store?.path ?? '—'"
        />
        <StatCard
          label="会话数"
          :value="formatNumber(store?.conversations)"
          icon="users"
          :hint="`消息记录 ${formatNumber(store?.messages)} 条`"
        />
        <StatCard
          label="累计接收事件"
          :value="formatNumber(counters?.events_received)"
          icon="inbox"
          :hint="`去重 ${formatNumber(counters?.events_deduped)} · 忽略 ${formatNumber(counters?.events_ignored)}`"
        />
        <StatCard
          label="累计回复"
          :value="formatNumber(counters?.replies_sent)"
          :tone="(counters?.replies_failed ?? 0) > 0 ? 'amber' : 'default'"
          icon="send"
          :hint="`失败 ${formatNumber(counters?.replies_failed)} 次`"
        />
      </div>
    </section>

    <!-- 运行详情 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="cpu" :size="16" class="text-sky-400" />
          运行详情
        </h2>
        <div class="flex flex-wrap items-center gap-2">
          <StatusBadge :info="wsBadge" dot />
          <StatusBadge :info="triggerModeBadge(status.data?.trigger_mode)" />
          <StatusBadge :info="contextModeBadge(status.data?.context_mode)" />
        </div>
      </div>
      <div class="card-body grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2 xl:grid-cols-3">
        <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
          <span class="text-slate-500">版本</span>
          <span class="text-slate-200">{{ status.data?.version ?? '—' }}</span>
        </div>
        <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
          <span class="text-slate-500">启动时间</span>
          <span class="text-slate-200">{{ formatDateTime(status.data?.started_at) }}</span>
        </div>
        <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
          <span class="text-slate-500">运行时长</span>
          <span class="text-slate-200">{{ formatDuration(status.data?.uptime_seconds) }}</span>
        </div>
        <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
          <span class="text-slate-500">传输方式</span>
          <span class="text-slate-200">{{ status.data?.transport ?? '—' }}</span>
        </div>
        <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
          <span class="text-slate-500">WS 会话</span>
          <span class="mono text-slate-200" :title="ws?.session_id ?? ''">
            {{ shortId(ws?.session_id, 8, 6) }}
          </span>
        </div>
        <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
          <span class="text-slate-500">最后事件</span>
          <span class="text-slate-200">{{ formatRelative(ws?.last_event_at) }}</span>
        </div>
        <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
          <span class="text-slate-500">AppID</span>
          <span class="mono text-slate-200">{{ qq?.app_id_masked ?? '—' }}</span>
        </div>
        <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
          <span class="text-slate-500">沙箱环境</span>
          <span class="text-slate-200">{{ qq?.sandbox ? '是' : '否' }}</span>
        </div>
        <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
          <span class="text-slate-500">Webhook</span>
          <span class="text-slate-200">
            {{ webhook?.enabled ? `已启用（${webhook?.path ?? '-'}）` : '未启用' }}
          </span>
        </div>
        <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
          <span class="text-slate-500">AI 并发</span>
          <span class="text-slate-200">
            {{ formatNumber(ai?.in_flight) }} / {{ formatNumber(ai?.max_concurrent) }}
          </span>
        </div>
        <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
          <span class="text-slate-500">AI 请求 / 错误</span>
          <span class="text-slate-200">
            {{ formatNumber(ai?.requests_total) }} / {{ formatNumber(ai?.errors_total) }}
          </span>
        </div>
        <div class="flex justify-between gap-3 border-b border-slate-800/60 pb-2 text-sm">
          <span class="text-slate-500">平均耗时</span>
          <span class="text-slate-200">{{ formatMs(ai?.avg_latency_ms) }}</span>
        </div>
        <div v-if="ws?.last_error" class="sm:col-span-2 xl:col-span-3">
          <p class="text-xs text-rose-300">WS 最近错误：{{ ws.last_error }}</p>
        </div>
        <div v-if="ai?.last_error" class="sm:col-span-2 xl:col-span-3">
          <p class="text-xs text-rose-300">AI 最近错误：{{ ai.last_error }}</p>
        </div>
        <div v-if="qq?.last_token_error" class="sm:col-span-2 xl:col-span-3">
          <p class="text-xs text-amber-300">Token 最近错误：{{ qq.last_token_error }}</p>
        </div>
      </div>
    </section>

    <!-- 统计图表 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="dashboard" :size="16" class="text-sky-400" />
          消息统计
        </h2>
        <div class="flex flex-wrap items-center gap-2">
          <span v-if="stats?.bucket" class="text-xs text-slate-500">
            聚合粒度 {{ stats.bucket }}
          </span>
          <div class="flex overflow-hidden rounded-lg border border-slate-700">
            <button
              v-for="range in RANGES"
              :key="range"
              type="button"
              class="cursor-pointer px-3 py-1 text-xs font-medium transition"
              :class="
                statsRange === range
                  ? 'bg-sky-600 text-white'
                  : 'bg-slate-900 text-slate-400 hover:bg-slate-800'
              "
              @click="statsRange = range"
            >
              {{ range }}
            </button>
          </div>
        </div>
      </div>

      <div class="card-body space-y-4">
        <p
          v-if="statsError"
          class="flex items-center gap-2 rounded-lg border border-rose-500/40 bg-rose-950/40 px-3 py-2 text-xs text-rose-200"
        >
          <AppIcon name="warning" :size="15" class="text-rose-400" />
          {{ statsError }}
        </p>

        <div class="grid grid-cols-2 gap-3 lg:grid-cols-5">
          <div class="rounded-lg border border-slate-800 bg-slate-950/50 px-3 py-2">
            <p class="text-xs text-slate-500">接收</p>
            <p class="text-base font-semibold text-sky-300 tabular-nums">
              {{ formatNumber(totals?.in) }}
            </p>
          </div>
          <div class="rounded-lg border border-slate-800 bg-slate-950/50 px-3 py-2">
            <p class="text-xs text-slate-500">发送</p>
            <p class="text-base font-semibold text-emerald-300 tabular-nums">
              {{ formatNumber(totals?.out) }}
            </p>
          </div>
          <div class="rounded-lg border border-slate-800 bg-slate-950/50 px-3 py-2">
            <p class="text-xs text-slate-500">错误</p>
            <p class="text-base font-semibold text-rose-300 tabular-nums">
              {{ formatNumber(totals?.errors) }}
            </p>
          </div>
          <div class="rounded-lg border border-slate-800 bg-slate-950/50 px-3 py-2">
            <p class="text-xs text-slate-500">平均耗时</p>
            <p class="text-base font-semibold text-slate-200 tabular-nums">
              {{ formatMs(totals?.avg_latency_ms) }}
            </p>
          </div>
          <div class="rounded-lg border border-slate-800 bg-slate-950/50 px-3 py-2">
            <p class="text-xs text-slate-500">tokens</p>
            <p class="text-base font-semibold text-slate-200 tabular-nums">
              {{ formatNumber(totals?.tokens) }}
            </p>
          </div>
        </div>

        <div class="relative">
          <div
            v-if="statsLoading"
            class="absolute inset-0 z-10 flex items-center justify-center bg-slate-900/40"
          >
            <span class="h-5 w-5 animate-spin rounded-full border-2 border-slate-600 border-t-sky-400" />
          </div>
          <TimeSeriesChart :points="stats?.series ?? []" />
        </div>

        <div v-if="topGroups.length > 0" class="border-t border-slate-800 pt-4">
          <h3 class="mb-3 text-xs font-semibold tracking-wide text-slate-400 uppercase">
            活跃群 Top {{ topGroups.length }}
          </h3>
          <ul class="space-y-2">
            <li v-for="group in topGroups" :key="group.group_openid" class="space-y-1">
              <div class="flex items-center justify-between gap-3 text-sm">
                <span class="flex min-w-0 items-center gap-1">
                  <span class="truncate text-slate-300">
                    {{ group.group_name || shortId(group.group_openid) }}
                  </span>
                  <CopyButton :value="group.group_openid" label="群 OpenID" />
                </span>
                <span class="shrink-0 tabular-nums text-slate-400">
                  {{ formatNumber(group.messages) }}
                </span>
              </div>
              <div class="h-1.5 overflow-hidden rounded-full bg-slate-800">
                <div
                  class="h-full rounded-full bg-sky-500/70"
                  :style="{
                    width: `${topGroupMax > 0 ? Math.max(2, ((group.messages ?? 0) / topGroupMax) * 100) : 0}%`,
                  }"
                />
              </div>
            </li>
          </ul>
        </div>
      </div>
    </section>

    <!-- 实时事件流 -->
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="inbox" :size="16" class="text-sky-400" />
          实时事件流
          <span class="text-xs font-normal text-slate-500">
            （最多保留最近 {{ MAX_EVENTS }} 条）
          </span>
        </h2>
        <div class="flex flex-wrap items-center gap-2">
          <StatusBadge :info="sseBadge" dot />
          <button type="button" class="btn btn-sm" @click="togglePause">
            <AppIcon :name="paused ? 'play' : 'pause'" :size="14" />
            {{ paused ? '继续' : '暂停' }}
          </button>
          <button type="button" class="btn btn-sm" :disabled="events.length === 0" @click="clearEvents">
            <AppIcon name="trash" :size="14" />
            清空
          </button>
        </div>
      </div>

      <div class="card-body space-y-3">
        <div class="flex flex-wrap items-center gap-2">
          <button
            v-for="item in FILTERS"
            :key="item.value"
            type="button"
            class="badge cursor-pointer transition"
            :class="
              filter === item.value
                ? 'border-sky-500/50 bg-sky-500/15 text-sky-200'
                : 'border-slate-700 bg-slate-800/50 text-slate-400 hover:text-slate-200'
            "
            @click="filter = item.value"
          >
            {{ item.label }}
            <span class="tabular-nums opacity-70">
              {{ item.value === 'all' ? events.length : (eventCounts[item.value] ?? 0) }}
            </span>
          </button>
          <span v-if="paused" class="badge border-amber-500/40 bg-amber-500/10 text-amber-300">
            已暂停{{ skipped > 0 ? ` · 跳过 ${skipped} 条` : '' }}
          </span>
        </div>

        <div
          v-if="filteredEvents.length === 0"
          class="rounded-lg border border-dashed border-slate-800 px-4 py-10 text-center text-sm text-slate-500"
        >
          {{ events.length === 0 ? '等待事件推送…' : '该分类暂无事件' }}
        </div>

        <ul v-else class="max-h-[30rem] space-y-1.5 overflow-y-auto pr-1 scrollbar-thin">
          <li
            v-for="item in filteredEvents"
            :key="item.uid"
            class="rounded-lg border border-slate-800 bg-slate-950/40 px-3 py-2"
          >
            <div class="flex flex-wrap items-center gap-2 text-sm">
              <StatusBadge :info="sseEventBadge(item.event)" />
              <span class="mono text-slate-500">{{ formatTime(new Date(item.at).toISOString()) }}</span>
              <span class="min-w-0 flex-1 truncate text-slate-300" :title="item.summary">
                {{ item.summary }}
              </span>
              <button
                type="button"
                class="cursor-pointer text-xs text-slate-500 transition hover:text-sky-400"
                @click="expanded = expanded === item.uid ? null : item.uid"
              >
                {{ expanded === item.uid ? '收起' : '原始 JSON' }}
              </button>
            </div>
            <pre
              v-if="expanded === item.uid"
              class="mono mt-2 max-h-64 overflow-auto rounded-md border border-slate-800 bg-slate-950 p-3 text-slate-400 scrollbar-thin"
              >{{ pretty(item.payload) }}</pre
            >
          </li>
        </ul>
      </div>
    </section>
  </div>
</template>
