import { reactive } from 'vue'
import { statusApi } from '@/api'
import type { StatusData } from '@/api/types'
import { toMessage } from '@/api'

interface StatusState {
  data: StatusData | null
  loading: boolean
  error: string
  /** 最近一次成功刷新的本地时间戳（ms） */
  updatedAt: number
}

const state = reactive<StatusState>({
  data: null,
  loading: false,
  error: '',
  updatedAt: 0,
})

let timer: number | null = null
let inflight = false

export function useStatusState(): StatusState {
  return state
}

/** SSE 推送的 status 事件与 /api/status 同构，直接覆盖。 */
export function applyStatusSnapshot(snapshot: StatusData): void {
  state.data = snapshot
  state.updatedAt = Date.now()
  state.error = ''
}

export async function fetchStatus(silent = false): Promise<void> {
  if (inflight) return
  inflight = true
  if (!silent) state.loading = true
  try {
    const data = await statusApi.get()
    applyStatusSnapshot(data)
  } catch (error) {
    state.error = toMessage(error)
  } finally {
    inflight = false
    state.loading = false
  }
}

/** 5 秒轮询（SSE 断线时的兜底数据源）。 */
export function startStatusPolling(intervalMs = 5000): void {
  stopStatusPolling()
  void fetchStatus()
  timer = window.setInterval(() => void fetchStatus(true), intervalMs)
}

export function stopStatusPolling(): void {
  if (timer !== null) {
    window.clearInterval(timer)
    timer = null
  }
}
