import { reactive } from 'vue'
import { authApi } from '@/api'

interface AuthState {
  /** 是否已经完成过一次 /api/auth/me 探测 */
  checked: boolean
  authenticated: boolean
  username: string
  authMode: string
}

const state = reactive<AuthState>({
  checked: false,
  authenticated: false,
  username: '',
  authMode: '',
})

let inflight: Promise<boolean> | null = null

export function useAuthState(): AuthState {
  return state
}

/** 判断登录态；已探测过则直接复用结果，避免每次路由跳转都打接口。 */
export function ensureAuth(force = false): Promise<boolean> {
  if (!force && state.checked) return Promise.resolve(state.authenticated)
  if (inflight) return inflight
  inflight = (async () => {
    try {
      const me = await authApi.me()
      state.authenticated = Boolean(me?.authenticated)
      state.username = me?.username ?? ''
      state.authMode = me?.auth_mode ?? ''
    } catch {
      state.authenticated = false
    } finally {
      state.checked = true
      inflight = null
    }
    return state.authenticated
  })()
  return inflight
}

export async function login(token: string): Promise<void> {
  await authApi.login(token)
  state.checked = true
  state.authenticated = true
  // 登录后补一次 me 以拿到 username / auth_mode（失败不影响登录结果）
  try {
    const me = await authApi.me()
    state.authenticated = Boolean(me?.authenticated)
    state.username = me?.username ?? ''
    state.authMode = me?.auth_mode ?? ''
  } catch {
    /* ignore */
  }
}

export async function logout(): Promise<void> {
  try {
    await authApi.logout()
  } finally {
    resetAuth()
  }
}

export function resetAuth(): void {
  state.checked = true
  state.authenticated = false
  state.username = ''
  state.authMode = ''
}
