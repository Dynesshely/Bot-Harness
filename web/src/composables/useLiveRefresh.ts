import { onBeforeUnmount, ref, watch } from 'vue'
import { EVENTS_STREAM_URL } from '@/api'

export interface LiveRefreshOptions {
  /** localStorage 键名，用于记住开关状态。 */
  storageKey?: string
  /** 兜底轮询间隔（毫秒）；SSE 不可用时仍能保持新鲜。 */
  pollIntervalMs?: number
  /**
   * 收到 SSE 事件后判断是否需要刷新。
   * 返回 false 表示该事件与当前页面无关，直接忽略。
   */
  shouldRefresh?: (event: MessageEvent) => boolean
}

/**
 * 实时刷新：订阅后端的 SSE 事件流，在相关事件到达时触发回调；
 * 同时保留一个低频轮询作为兜底（反代/浏览器可能缓冲 SSE）。
 */
export function useLiveRefresh(onTick: () => void, options: LiveRefreshOptions = {}) {
  const { storageKey = 'bot-harness.liveRefresh', pollIntervalMs = 10000, shouldRefresh } = options

  const stored = typeof localStorage === 'undefined' ? null : localStorage.getItem(storageKey)
  const enabled = ref(stored !== '0')

  let source: EventSource | null = null
  let timer: number | undefined
  let debounce: number | undefined

  function tick() {
    if (debounce) window.clearTimeout(debounce)
    debounce = window.setTimeout(() => {
      debounce = undefined
      onTick()
    }, 250)
  }

  function handle(event: MessageEvent) {
    if (shouldRefresh && !shouldRefresh(event)) return
    tick()
  }

  function start() {
    stop()
    try {
      source = new EventSource(EVENTS_STREAM_URL, { withCredentials: true })
      source.addEventListener('message', handle)
      // 断线由 EventSource 自动重连；重连成功后补一次刷新。
      source.addEventListener('open', tick)
    } catch {
      source = null
    }
    timer = window.setInterval(tick, pollIntervalMs)
  }

  function stop() {
    source?.close()
    source = null
    if (timer) {
      window.clearInterval(timer)
      timer = undefined
    }
    if (debounce) {
      window.clearTimeout(debounce)
      debounce = undefined
    }
  }

  watch(
    enabled,
    (value) => {
      try {
        localStorage.setItem(storageKey, value ? '1' : '0')
      } catch {
        /* 隐私模式下忽略 */
      }
      if (value) start()
      else stop()
    },
    { immediate: true },
  )

  onBeforeUnmount(stop)

  return { enabled, stop, start }
}
