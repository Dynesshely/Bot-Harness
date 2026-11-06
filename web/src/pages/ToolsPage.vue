<script setup lang="ts">
import { onMounted, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import { toolCallsApi, toMessage } from '@/api'
import type { ToolCallItem } from '@/api/types'
import { formatDateTime } from '@/utils/format'
import { formatMs } from '@/utils/format'

const PAGE = 50

const items = ref<ToolCallItem[]>([])
const loading = ref(false)
const loadingMore = ref(false)
const error = ref('')
const expanded = ref<number | null>(null)

async function load(reset: boolean) {
  if (reset) {
    loading.value = true
  } else {
    loadingMore.value = true
  }
  error.value = ''
  try {
    const before = reset ? 0 : (items.value.at(-1)?.id ?? 0)
    const data = await toolCallsApi.list(PAGE, before)
    items.value = reset ? data.items : [...items.value, ...data.items]
  } catch (e) {
    error.value = toMessage(e)
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

onMounted(() => load(true))

function statusClass(status: string) {
  if (status === 'ok') return 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
  if (status === 'rejected') return 'border-amber-500/40 bg-amber-500/10 text-amber-300'
  return 'border-rose-500/40 bg-rose-500/10 text-rose-300'
}
</script>

<template>
  <div class="space-y-4">
    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="cpu" :size="16" class="text-sky-400" />
          工具调用
        </h2>
        <div class="flex items-center gap-2">
          <span class="text-xs text-slate-500">最近 {{ items.length }} 条</span>
          <button type="button" class="btn btn-ghost btn-sm" :disabled="loading" @click="load(true)">
            <AppIcon name="refresh" :size="14" :class="loading ? 'animate-spin' : ''" />
            刷新
          </button>
        </div>
      </div>

      <div class="card-body space-y-3">
        <p v-if="error" class="text-sm text-rose-400">{{ error }}</p>

        <EmptyState
          v-else-if="!items.length && !loading"
          title="还没有工具调用"
          description="模型真正调用工具时，这里会出现每次调用的工具名、参数、耗时与结果大小。"
        />

        <div v-else class="table-wrap scroll-y sticky-head">
          <table class="table-base">
            <thead>
              <tr>
                <th>时间</th>
                <th>工具</th>
                <th>状态</th>
                <th>耗时</th>
                <th>结果字符</th>
                <th>会话</th>
                <th>参数</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="item in items"
                :key="item.id"
                class="cursor-pointer"
                @click="expanded = expanded === item.id ? null : item.id"
              >
                <td class="whitespace-nowrap text-slate-400">{{ formatDateTime(item.created_at) }}</td>
                <td class="mono">{{ item.tool }}</td>
                <td>
                  <span class="badge" :class="statusClass(item.status)">{{ item.status }}</span>
                </td>
                <td class="tabular-nums text-slate-400">{{ formatMs(item.latency_ms) }}</td>
                <td class="tabular-nums text-slate-400">{{ item.result_chars }}</td>
                <td class="mono text-xs text-slate-500">{{ item.conversation_id }}</td>
                <td class="mono max-w-[18rem] truncate text-xs text-slate-400">{{ item.args }}</td>
              </tr>
              <tr v-if="expanded !== null">
                <td colspan="7" class="bg-slate-950/60 text-xs text-slate-400">
                  <pre class="whitespace-pre-wrap break-words">{{ items.find((i) => i.id === expanded)?.args }}</pre>
                  <p v-if="items.find((i) => i.id === expanded)?.error" class="mt-1 text-rose-400">
                    {{ items.find((i) => i.id === expanded)?.error }}
                  </p>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div v-if="items.length" class="flex justify-center">
          <button type="button" class="btn btn-sm" :disabled="loadingMore" @click="load(false)">
            {{ loadingMore ? '加载中…' : '加载更多' }}
          </button>
        </div>
      </div>
    </section>
  </div>
</template>
