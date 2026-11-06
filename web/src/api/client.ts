import type { ApiErrorBody, ErrEnvelope, OkEnvelope } from './types'

/** 统一的 API 基址：生产由后端托管同源，开发由 vite 代理到 127.0.0.1:8080。 */
export const API_BASE = '/api'

/** 后端 SSE 事件流地址。 */
export const EVENTS_STREAM_URL = `${API_BASE}/events/stream`

export class ApiError extends Error {
  readonly code: string
  readonly status: number

  constructor(code: string, message: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.code = code
    this.status = status
  }
}

/** 从任意异常里取出适合展示给用户的中文文案。 */
export function toMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message
  if (error instanceof Error) return error.message || '发生未知错误'
  if (typeof error === 'string') return error
  return '发生未知错误'
}

/** 是否为「取消请求」导致的异常（不应提示用户）。 */
export function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError'
}

export type QueryValue = string | number | boolean | null | undefined

/** 组装查询串：跳过 undefined / null / 空字符串。 */
export function buildQuery(params?: Record<string, QueryValue>): string {
  if (!params) return ''
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    search.set(key, String(value))
  }
  const qs = search.toString()
  return qs ? `?${qs}` : ''
}

type UnauthorizedHandler = () => void

let unauthorizedHandler: UnauthorizedHandler | null = null

/** 注册全局 401 处理（跳转登录页）。由 main.ts 在 router 就绪后注入。 */
export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): void {
  unauthorizedHandler = handler
}

export type HttpMethod = 'GET' | 'POST' | 'PUT' | 'DELETE' | 'PATCH'

export interface RequestOptions {
  method?: HttpMethod
  query?: Record<string, QueryValue>
  body?: unknown
  signal?: AbortSignal
  /** 探测型请求（/auth/me、/auth/login）不触发全局跳转。 */
  skipAuthRedirect?: boolean
}

function isErrEnvelope(value: unknown): value is ErrEnvelope {
  if (typeof value !== 'object' || value === null) return false
  const candidate = value as { ok?: unknown }
  return candidate.ok === false
}

function isOkEnvelope(value: unknown): value is OkEnvelope<unknown> {
  if (typeof value !== 'object' || value === null) return false
  const candidate = value as { ok?: unknown }
  return candidate.ok === true
}

function handleUnauthorized(skipAuthRedirect: boolean | undefined): void {
  if (!skipAuthRedirect) unauthorizedHandler?.()
}

/**
 * 发起请求并返回完整信封（保留 data 之外的顶层扩展字段，如 restart_required）。
 * 契约：成功 `{ok:true,data:...}`，失败 `{ok:false,error:{code,message}}`。
 */
export async function requestEnvelope<T>(
  path: string,
  options: RequestOptions = {},
): Promise<OkEnvelope<T>> {
  const { method = 'GET', query, body, signal, skipAuthRedirect } = options
  const url = `${API_BASE}${path}${buildQuery(query)}`

  let response: Response
  try {
    response = await fetch(url, {
      method,
      credentials: 'same-origin',
      headers: {
        Accept: 'application/json',
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    })
  } catch (error) {
    if (isAbortError(error)) throw error
    throw new ApiError('network_error', '无法连接后端服务，请确认服务正在运行', 0)
  }

  const raw = await response.text()
  let parsed: unknown = null
  if (raw) {
    try {
      parsed = JSON.parse(raw)
    } catch {
      parsed = null
    }
  }

  if (isErrEnvelope(parsed)) {
    const body: ApiErrorBody = parsed.error ?? { code: 'unknown_error', message: '' }
    const status = response.status
    if (status === 401) handleUnauthorized(skipAuthRedirect)
    throw new ApiError(body.code || 'unknown_error', body.message || '请求失败', status)
  }

  if (!isOkEnvelope(parsed)) {
    if (response.status === 401) {
      handleUnauthorized(skipAuthRedirect)
      throw new ApiError('unauthorized', '登录状态已失效，请重新登录', 401)
    }
    throw new ApiError(
      'invalid_response',
      `响应格式不符合契约（HTTP ${response.status}）`,
      response.status,
    )
  }

  if (response.status === 401) {
    handleUnauthorized(skipAuthRedirect)
    throw new ApiError('unauthorized', '登录状态已失效，请重新登录', 401)
  }

  return parsed as OkEnvelope<T>
}

/** 发起请求并只解包 `data`。 */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const envelope = await requestEnvelope<T>(path, options)
  return envelope.data
}
