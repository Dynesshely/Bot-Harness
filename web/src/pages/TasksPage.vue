<script setup lang="ts">
import { onMounted, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import SelectMenu from '@/components/SelectMenu.vue'
import { commitmentsApi, proactiveApi, tasksApi, toMessage } from '@/api'
import type { CommitmentItem, ProactiveOverview, TaskItem } from '@/api/types'
import { toast } from '@/composables/useToast'
import { formatDateTime } from '@/utils/format'

const STATUSES: { value: string; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'pending', label: '待发送' },
  { value: 'sent', label: '已发送' },
  { value: 'failed', label: '失败' },
  { value: 'canceled', label: '已取消' },
]

/** 承诺状态：waiting / leased / done / cancelled（后端 store.Commitment）。 */
const COMMITMENT_STATUS_LABELS: Record<string, string> = {
  waiting: '等待追问',
  leased: '追问中',
  done: '已完成',
  cancelled: '已取消',
}

const status = ref('')
const proactive = ref<ProactiveOverview | null>(null)
const items = ref<TaskItem[]>([])
const loading = ref(false)
const error = ref('')

const commitments = ref<CommitmentItem[]>([])
const commitmentsLoading = ref(false)
const commitmentsError = ref('')
const cancelingId = ref<number | null>(null)

async function loadCommitments() {
  commitmentsLoading.value = true
  commitmentsError.value = ''
  try {
    commitments.value = await commitmentsApi.list()
  } catch (e) {
    commitments.value = []
    commitmentsError.value = toMessage(e)
  } finally {
    commitmentsLoading.value = false
  }
}

async function cancelCommitment(item: CommitmentItem) {
  if (cancelingId.value !== null) return
  cancelingId.value = item.id
  try {
    await commitmentsApi.cancel(item.id)
    toast.success(`已取消承诺 #${item.id}`)
    await loadCommitments()
  } catch (e) {
    // 后端对 done / cancelled 返回 409：这是一次正常失败，照常提示。
    toast.error(toMessage(e))
  } finally {
    cancelingId.value = null
  }
}

function commitmentStatusLabel(value: string) {
  return COMMITMENT_STATUS_LABELS[value] ?? value
}

function commitmentBadge(value: string) {
  if (value === 'waiting') return 'border-sky-500/40 bg-sky-500/10 text-sky-300'
  if (value === 'leased') return 'border-amber-500/40 bg-amber-500/10 text-amber-300'
  if (value === 'done') return 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
  return 'border-slate-600/50 bg-slate-600/10 text-slate-300'
}

/** 只有未收口（waiting / leased）的行才能取消；终态行不渲染按钮。 */
function commitmentCancelable(value: string) {
  return value === 'waiting' || value === 'leased'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    items.value = await tasksApi.list(status.value || undefined)
  } catch (e) {
    error.value = toMessage(e)
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await Promise.all([load(), loadCommitments()])
  try {
    proactive.value = await proactiveApi.overview()
  } catch {
    proactive.value = null
  }
})

async function cancel(item: TaskItem) {
  try {
    await tasksApi.cancel(item.id)
    toast.success(`已取消 #${item.id}`)
    await load()
  } catch (e) {
    toast.error(toMessage(e))
  }
}

function statusBadge(s: string) {
  if (s === 'sent') return 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
  if (s === 'pending') return 'border-sky-500/40 bg-sky-500/10 text-sky-300'
  if (s === 'failed') return 'border-rose-500/40 bg-rose-500/10 text-rose-300'
  return 'border-slate-600/50 bg-slate-600/10 text-slate-300'
}

function destination(t: TaskItem) {
  return t.group_openid ? `群 ${t.group_openid}` : `私聊 ${t.member_openid}`
}
</script>

<template>
  <div class="space-y-4">
    <section v-if="proactive" class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="bolt" :size="16" class="text-amber-400" />
          自发主动状态
        </h2>
        <span class="text-xs text-slate-500">
          全局 {{ proactive.level }} · 每 {{ proactive.ratio }} 条人类消息换 1 点（最多 {{ proactive.max_credits }} 点）
          · 静默 {{ proactive.quiet_hours[0] }}:00–{{ proactive.quiet_hours[1] }}:00
          · 间隔 ≥ {{ proactive.min_interval_minutes }} 分钟
        </span>
      </div>
      <div class="card-body">
        <p v-if="!proactive.items.length" class="text-sm text-slate-500">
          还没有自发发言记录（默认关闭；需要在策略/设置里按群开启 auto）。
        </p>
        <div v-else class="table-wrap scroll-y sticky-head">
          <table class="table-base">
            <thead>
              <tr><th>群</th><th>上次发言</th><th>累计</th><th>连续无人回应</th><th>理由</th></tr>
            </thead>
            <tbody>
              <tr v-for="g in proactive.items" :key="g.group_openid">
                <td class="mono max-w-[16rem] truncate text-xs">{{ g.group_openid }}</td>
                <td class="whitespace-nowrap text-slate-400">{{ formatDateTime(g.last_sent_at) }}</td>
                <td class="tabular-nums text-slate-400">{{ g.sends }}</td>
                <td class="tabular-nums" :class="g.silent_streak >= 2 ? 'text-amber-400' : 'text-slate-400'">
                  {{ g.silent_streak }}
                </td>
                <td class="max-w-[22rem] whitespace-normal break-words text-xs text-slate-400">{{ g.last_reason }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>

    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="chatAlt" :size="16" class="text-amber-400" />
          承诺追问
        </h2>
        <div class="flex items-center gap-2">
          <span class="text-xs text-slate-500">共 {{ commitments.length }} 条</span>
          <button
            type="button"
            class="btn btn-ghost btn-sm"
            :disabled="commitmentsLoading"
            @click="loadCommitments"
          >
            <AppIcon name="refresh" :size="14" :class="commitmentsLoading ? 'animate-spin' : ''" />
            刷新
          </button>
        </div>
      </div>

      <div class="card-body space-y-3">
        <p class="text-xs text-slate-500">
          提醒发出后用户没有回应时，模型会按常识判断何时再追问，直到用户说完成 / 延后 / 取消。
          这里列出全部承诺（含已完成 / 已取消的历史行）；追问节奏与每人上限在
          「设置 → 回复与人味 → 承诺追问」里调整。
        </p>

        <p v-if="commitmentsError" class="text-sm text-rose-400">
          读取承诺失败：{{ commitmentsError }}
        </p>
        <EmptyState
          v-else-if="!commitments.length && !commitmentsLoading"
          title="还没有承诺记录"
          description="当提醒发出后用户没有回应、并且模型决定稍后再问时，这里会出现记录。"
        />

        <div v-else class="table-wrap scroll-y sticky-head">
          <table class="table-base">
            <thead>
              <tr>
                <th>事项</th>
                <th>状态</th>
                <th>已追问</th>
                <th>下次追问时间</th>
                <th>创建时间</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in commitments" :key="item.id">
                <td class="max-w-[24rem] whitespace-normal break-words">{{ item.text }}</td>
                <td>
                  <span class="badge" :class="commitmentBadge(item.status)" :title="item.status">
                    {{ commitmentStatusLabel(item.status) }}
                  </span>
                </td>
                <td class="tabular-nums text-slate-400">{{ item.nudge_count }}</td>
                <td class="whitespace-nowrap text-slate-400">{{ formatDateTime(item.next_check_at) }}</td>
                <td class="whitespace-nowrap text-slate-500">{{ formatDateTime(item.created_at) }}</td>
                <td>
                  <button
                    v-if="commitmentCancelable(item.status)"
                    type="button"
                    class="btn btn-ghost btn-sm text-rose-400"
                    :disabled="cancelingId === item.id"
                    title="取消这条承诺"
                    @click="cancelCommitment(item)"
                  >
                    <AppIcon name="trash" :size="14" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>

    <section class="card">
      <div class="card-header">
        <h2 class="card-title">
          <AppIcon name="clock" :size="16" class="text-sky-400" />
          定时任务
        </h2>
        <div class="flex items-center gap-2">
          <div class="w-40">
            <SelectMenu
              v-model="status"
              size="sm"
              aria-label="按状态筛选定时任务"
              placeholder="全部状态"
              :options="STATUSES"
              @change="load"
            />
          </div>
          <button type="button" class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
            <AppIcon name="refresh" :size="14" :class="loading ? 'animate-spin' : ''" />
            刷新
          </button>
        </div>
      </div>

      <div class="card-body space-y-3">
        <p class="text-xs text-slate-500">
          这些是用户明确要求、由机器人到点主动发出的提醒。停机超过 10 分钟的提醒会被标记失败而不是补发，
          避免「一觉醒来收到十条昨天的提醒」。每个提醒只对<b>发起人</b>可见、可取消——
          同一个群里的人互相看不到彼此的提醒。
        </p>

        <p v-if="error" class="text-sm text-rose-400">{{ error }}</p>
        <EmptyState
          v-else-if="!items.length && !loading"
          title="还没有定时任务"
          description="群里有人说「30 分钟后提醒我…」且模型调用了 schedule_task 时，这里会出现记录。"
        />

        <div v-else class="table-wrap scroll-y sticky-head">
          <table class="table-base">
            <thead>
              <tr>
                <th>#</th>
                <th>状态</th>
                <th>到期时间</th>
                <th>内容</th>
                <th>发起人</th>
                <th>发到</th>
                <th>备注</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in items" :key="item.id">
                <td class="tabular-nums text-slate-500">{{ item.id }}</td>
                <td>
                  <span class="badge" :class="statusBadge(item.status)">{{ item.status }}</span>
                </td>
                <td class="whitespace-nowrap text-slate-400">{{ formatDateTime(item.due_at) }}</td>
                <td class="max-w-[22rem] whitespace-normal break-words">{{ item.text }}</td>
                <td class="mono max-w-[12rem] truncate text-xs text-slate-400">{{ item.creator_openid || '—' }}</td>
                <td class="mono max-w-[12rem] truncate text-xs text-slate-500">{{ destination(item) }}</td>
                <td class="max-w-[16rem] truncate text-xs text-slate-500">{{ item.error }}</td>
                <td>
                  <button
                    v-if="item.status === 'pending' || item.status === 'leased'"
                    type="button"
                    class="btn btn-ghost btn-sm text-rose-400"
                    @click="cancel(item)"
                  >
                    <AppIcon name="trash" :size="14" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>
  </div>
</template>
