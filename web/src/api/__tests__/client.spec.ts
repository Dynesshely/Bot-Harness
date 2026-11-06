import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ApiError,
  buildQuery,
  configApi,
  isAbortError,
  request,
  requestEnvelope,
  setUnauthorizedHandler,
  toMessage,
} from '@/api'

/**
 * 只伪造 fetch 的返回值：契约测试要钉住的是「信封怎么解析、错误怎么抛」，
 * 不是 HTTP 栈本身，所以不去依赖全局 Response（jsdom 里也不一定有）。
 */
function fakeResponse(body: unknown, status = 200): Response {
  const raw = typeof body === 'string' ? body : JSON.stringify(body)
  return { status, text: async () => raw } as unknown as Response
}

/** fetch 被 AbortController 取消时抛出的异常形状（浏览器是 DOMException）。 */
function abortError(): Error {
  const error = new Error('The operation was aborted.')
  error.name = 'AbortError'
  return error
}

const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  setUnauthorizedHandler(null)
  vi.unstubAllGlobals()
})

describe('buildQuery', () => {
  it('没有参数时返回空串', () => {
    expect(buildQuery()).toBe('')
    expect(buildQuery({})).toBe('')
  })

  it('跳过 undefined / null / 空串，但保留 0 与 false', () => {
    expect(
      buildQuery({ a: undefined, b: null, c: '', d: 0, e: false, page: 2 }),
    ).toBe('?d=0&e=false&page=2')
  })

  it('按 URL 规则编码', () => {
    expect(buildQuery({ q: 'a b', name: '中文', raw: 'x&y=z' })).toBe(
      '?q=a+b&name=%E4%B8%AD%E6%96%87&raw=x%26y%3Dz',
    )
  })
})

describe('requestEnvelope / request', () => {
  it('解析成功信封，request 只解包 data', async () => {
    fetchMock.mockResolvedValue(fakeResponse({ ok: true, data: { authenticated: true } }))
    await expect(request<{ authenticated: boolean }>('/auth/me')).resolves.toEqual({
      authenticated: true,
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('保留信封的顶层扩展字段（restart_required 在 data 之外）', async () => {
    fetchMock.mockResolvedValue(
      fakeResponse({ ok: true, data: { settings: { model: 'm' } }, restart_required: ['http_addr'] }),
    )
    const envelope = await requestEnvelope<{ settings: { model: string } }>('/config')
    expect(envelope.data).toEqual({ settings: { model: 'm' } })
    expect(envelope.restart_required).toEqual(['http_addr'])
  })

  it('请求参数：拼查询串、GET 不带 Content-Type、PUT 带 JSON body', async () => {
    fetchMock.mockResolvedValue(fakeResponse({ ok: true, data: null }))

    await request('/messages', { query: { limit: 20, status: undefined } })
    expect(fetchMock).toHaveBeenLastCalledWith(
      '/api/messages?limit=20',
      expect.objectContaining({
        method: 'GET',
        credentials: 'same-origin',
        headers: { Accept: 'application/json' },
        body: undefined,
      }),
    )

    await request('/policies', { method: 'PUT', body: { enabled: true } })
    expect(fetchMock).toHaveBeenLastCalledWith(
      '/api/policies',
      expect.objectContaining({
        method: 'PUT',
        headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: true }),
      }),
    )
  })
})

describe('错误信封与统一错误类型', () => {
  it('ok:false → ApiError 带上 code / message / status', async () => {
    fetchMock.mockResolvedValue(
      fakeResponse({ ok: false, error: { code: 'bad_request', message: '模型端点未配置' } }, 400),
    )
    const error = await request('/config').catch((err: unknown) => err)
    expect(error).toBeInstanceOf(ApiError)
    const apiError = error as ApiError
    expect(apiError.code).toBe('bad_request')
    expect(apiError.message).toBe('模型端点未配置')
    expect(apiError.status).toBe(400)
    expect(apiError.name).toBe('ApiError')
    expect(toMessage(error)).toBe('模型端点未配置')
  })

  it('错误信封缺字段时给出兜底 code / message', async () => {
    fetchMock.mockResolvedValue(fakeResponse({ ok: false }, 500))
    const error = (await request('/x').catch((err: unknown) => err)) as ApiError
    expect(error.code).toBe('unknown_error')
    expect(error.message).toBe('请求失败')
    expect(error.status).toBe(500)
  })

  it('不符合契约的响应（HTML / 空 body）抛 invalid_response', async () => {
    fetchMock.mockResolvedValue(fakeResponse('<html>502 Bad Gateway</html>', 502))
    const error = (await request('/x').catch((err: unknown) => err)) as ApiError
    expect(error.code).toBe('invalid_response')
    expect(error.status).toBe(502)

    fetchMock.mockResolvedValue(fakeResponse('', 500))
    const empty = (await request('/x').catch((err: unknown) => err)) as ApiError
    expect(empty.code).toBe('invalid_response')
    expect(empty.status).toBe(500)
  })

  it('网络不可达统一成 network_error（status 0）', async () => {
    fetchMock.mockRejectedValue(new TypeError('fetch failed'))
    const error = (await request('/status').catch((err: unknown) => err)) as ApiError
    expect(error).toBeInstanceOf(ApiError)
    expect(error.code).toBe('network_error')
    expect(error.status).toBe(0)
    expect(error.message).toBe('无法连接后端服务，请确认服务正在运行')
  })

  it('主动取消（AbortError）原样抛出，不当成错误提示', async () => {
    // 浏览器里是 DOMException；这里用 name='AbortError' 的 Error，避免依赖 jsdom 的 prototype 链
    const abort = abortError()
    fetchMock.mockRejectedValue(abort)
    const error = await request('/status').catch((err: unknown) => err)
    expect(error).toBe(abort)
    expect(isAbortError(error)).toBe(true)
    expect(toMessage(error)).toBe('The operation was aborted.')
  })
})

describe('401 处理', () => {
  it('401 + 错误信封：调用全局处理器并抛 ApiError', async () => {
    const onUnauthorized = vi.fn()
    setUnauthorizedHandler(onUnauthorized)
    fetchMock.mockResolvedValue(
      fakeResponse({ ok: false, error: { code: 'unauthorized', message: '登录状态已失效' } }, 401),
    )

    const error = (await request('/status').catch((err: unknown) => err)) as ApiError
    expect(error.code).toBe('unauthorized')
    expect(error.status).toBe(401)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('探测型请求（skipAuthRedirect）不触发跳转，但照样抛错', async () => {
    const onUnauthorized = vi.fn()
    setUnauthorizedHandler(onUnauthorized)
    fetchMock.mockResolvedValue(
      fakeResponse({ ok: false, error: { code: 'unauthorized', message: '未登录' } }, 401),
    )

    await expect(request('/auth/me', { skipAuthRedirect: true })).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('401 但没有错误信封时也走同一套登出逻辑', async () => {
    const onUnauthorized = vi.fn()
    setUnauthorizedHandler(onUnauthorized)
    fetchMock.mockResolvedValue(fakeResponse('', 401))

    const error = (await request('/status').catch((err: unknown) => err)) as ApiError
    expect(error.code).toBe('unauthorized')
    expect(error.message).toBe('登录状态已失效，请重新登录')
    expect(error.status).toBe(401)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('没注册处理器时不会崩（后端离线也能渲染登录页）', async () => {
    setUnauthorizedHandler(null)
    fetchMock.mockResolvedValue(fakeResponse({ ok: false, error: { code: 'unauthorized', message: 'x' } }, 401))
    await expect(request('/status')).rejects.toBeInstanceOf(ApiError)
  })
})

describe('configApi.update 契约（D-036：{settings, warnings, restart_required}）', () => {
  it('把 data.settings 当扁平设置返回，而不是把整个 data 当设置', async () => {
    fetchMock.mockResolvedValue(
      fakeResponse({
        ok: true,
        data: {
          settings: { model: 'gpt-x', reply_max_chars: 300 },
          warnings: ['temperature 超出范围'],
          restart_required: ['log_level'],
        },
      }),
    )

    const outcome = await configApi.update({ model: 'gpt-x' })
    expect(outcome.editable).toEqual({ model: 'gpt-x', reply_max_chars: 300 })
    expect(outcome.warnings).toEqual(['temperature 超出范围'])
    expect(outcome.restartRequired).toEqual(['log_level'])
    // 关键回归点：不能把 settings / warnings 这些包装键当控件值下发
    expect(outcome.editable).not.toHaveProperty('settings')
    expect(outcome.editable).not.toHaveProperty('warnings')

    expect(fetchMock).toHaveBeenLastCalledWith(
      '/api/config',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ model: 'gpt-x' }) }),
    )
  })

  it('顶层 restart_required 优先，且数组元素统一成字符串', async () => {
    fetchMock.mockResolvedValue(
      fakeResponse({
        ok: true,
        data: { settings: {}, restart_required: ['data_level'] },
        restart_required: ['top_level', 1],
      }),
    )
    const outcome = await configApi.update({})
    expect(outcome.restartRequired).toEqual(['top_level', '1'])
  })

  it('后端老版本 / 字段缺失时退化成空值，不抛异常', async () => {
    fetchMock.mockResolvedValue(fakeResponse({ ok: true, data: {} }))
    const outcome = await configApi.update({ model: 'm' })
    expect(outcome).toEqual({ editable: {}, warnings: [], restartRequired: [] })

    fetchMock.mockResolvedValue(fakeResponse({ ok: true }))
    await expect(configApi.update({ model: 'm' })).resolves.toEqual({
      editable: {},
      warnings: [],
      restartRequired: [],
    })
  })

  it('configApi.get 返回配置信封的 data', async () => {
    fetchMock.mockResolvedValue(
      fakeResponse({
        ok: true,
        data: { editable: { model: 'm' }, openai_api_key_set: true, openai_api_key_source: 'db' },
      }),
    )
    const data = await configApi.get()
    expect(data.editable).toEqual({ model: 'm' })
    expect(data.openai_api_key_set).toBe(true)
    expect(data.openai_api_key_source).toBe('db')
  })
})

describe('toMessage', () => {
  it.each([
    [new ApiError('bad_request', '参数不对', 400), '参数不对'],
    [new Error('炸了'), '炸了'],
    ['直接是字符串', '直接是字符串'],
    [new Error(''), '发生未知错误'],
    [undefined, '发生未知错误'],
    [{ weird: true }, '发生未知错误'],
  ])('把 %s 转成可展示文案', (input, expected) => {
    expect(toMessage(input)).toBe(expected)
  })

  it('isAbortError 只认 AbortError', () => {
    expect(isAbortError(abortError())).toBe(true)
    expect(isAbortError(new Error('aborted'))).toBe(false)
    expect(isAbortError('AbortError')).toBe(false)
  })
})
