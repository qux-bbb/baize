// 共享 API 模块 — 统一管理认证状态和请求
const API = '/api'

// 模块级 auth 状态
let authToken: string | null = null
let authMustChange: boolean = false

// 事件派发（让 React 组件感知状态变化）
function dispatch(event: string) {
  window.dispatchEvent(new CustomEvent('baize-auth', { detail: event }))
}

export function initAuth() {
  authToken = localStorage.getItem('token')
  authMustChange = localStorage.getItem('must_change_password') === 'true'
}

export function setAuth(token: string, mustChange: boolean) {
  authToken = token
  authMustChange = mustChange
  localStorage.setItem('token', token)
  localStorage.setItem('must_change_password', String(mustChange))
}

export function clearAuth() {
  authToken = null
  authMustChange = false
  localStorage.removeItem('token')
  localStorage.removeItem('must_change_password')
  localStorage.removeItem('username')
}

export function getToken(): string | null {
  return authToken
}

export function isMustChangePassword(): boolean {
  return authMustChange
}

export async function fetchJSON<T>(url: string, options?: RequestInit): Promise<T> {
  // 优先用 options 里的 token 头，其次用模块变量，最后从 localStorage 恢复
  const headers: Record<string, string> = {}
  const explicitAuth = options?.headers as Record<string, string> | undefined
  if (explicitAuth?.Authorization) {
    // 已有显式 Authorization，直接使用
    Object.assign(headers, explicitAuth)
  } else {
    let token = authToken
    if (!token) token = localStorage.getItem('token')
    if (token) headers['Authorization'] = `Bearer ${token}`
    // 合并 options 中的其他 headers
    if (options?.headers) {
      Object.assign(headers, options.headers)
    }
  }

  const r = await fetch(url, { ...options, headers })

  if (r.status === 401) {
    let errMsg = 'unauthorized'
    try { const d = await r.json(); errMsg = d.error || errMsg } catch {}
    if (errMsg === 'must_change_password') {
      authMustChange = true
      localStorage.setItem('must_change_password', 'true')
      dispatch('must_change_password')
    }
    throw new Error(errMsg)
  }
  if (r.status === 204) return undefined as T
  return r.json()
}

export { API }
