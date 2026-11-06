import { reactive } from 'vue'

export type ToastType = 'success' | 'error' | 'warn' | 'info'

export interface Toast {
  id: number
  type: ToastType
  message: string
}

/** 全局单例 toast 队列（不引入任何 UI 库）。 */
const items = reactive<Toast[]>([])

let seq = 0

function push(type: ToastType, message: string, timeout = 4000): number {
  const id = ++seq
  items.push({ id, type, message })
  // 最多同时展示 5 条，避免刷屏
  if (items.length > 5) items.splice(0, items.length - 5)
  if (timeout > 0) window.setTimeout(() => dismiss(id), timeout)
  return id
}

export function dismiss(id: number): void {
  const index = items.findIndex((item) => item.id === id)
  if (index >= 0) items.splice(index, 1)
}

export const toast = {
  success: (message: string, timeout = 3500) => push('success', message, timeout),
  error: (message: string, timeout = 6000) => push('error', message, timeout),
  warn: (message: string, timeout = 6000) => push('warn', message, timeout),
  info: (message: string, timeout = 4000) => push('info', message, timeout),
}

export function useToasts(): Toast[] {
  return items
}
