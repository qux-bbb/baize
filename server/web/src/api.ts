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
    } else {
      // 其他 401（token 无效/过期/吊销）→ 清除本地认证状态，跳回登录页
      clearAuth()
      dispatch('unauthorized')
    }
    throw new Error(errMsg)
  }
  if (r.status === 204) return undefined as T
  // 4xx/5xx：解析错误消息并抛出（错误响应约定为 {"error":"..."}，调用方 catch 处理）
  if (r.status >= 400) {
    let msg = `HTTP ${r.status}`
    try {
      const d = await r.json()
      if (d && typeof d.error === 'string' && d.error) msg = d.error
    } catch { /* 非 JSON 错误体（如 http.Error 纯文本），保留 HTTP 状态码 */ }
    throw new Error(msg)
  }
  return r.json()
}

// 远程终止指定主机上的进程
export async function killProcess(agentId: string, pid: number): Promise<{ status: string }> {
  return fetchJSON<{ status: string }>(`${API}/cmd/kill`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ agent_id: agentId, pid }),
  })
}

// ── 文件管理 ──────────────────────────────────────────────

export interface DirEntry {
  name: string
  path: string
  is_dir: boolean
  size: number
  modified: number
}

// 列出远程目录（path 为空 → Windows 驱动器列表 / Linux 根目录）
export async function listDir(agentId: string, path: string): Promise<DirEntry[]> {
  const d = await fetchJSON<{ entries: DirEntry[] }>(`${API}/cmd/list-dir`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ agent_id: agentId, path }),
  })
  return d.entries || []
}

// 远程删除文件/目录（recursive=true 时递归删除目录）
export async function deletePath(
  agentId: string,
  path: string,
  recursive: boolean,
): Promise<{ status: string }> {
  return fetchJSON<{ status: string }>(`${API}/cmd/delete`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ agent_id: agentId, path, recursive }),
  })
}

// 远程下载文件（流式，带进度回调）；totalSize 用于计算百分比（服务端为分块响应无 Content-Length）
export async function downloadFile(
  agentId: string,
  path: string,
  totalSize: number,
  onProgress?: (loaded: number, total: number) => void,
): Promise<{ blob: Blob; filename: string }> {
  const token = localStorage.getItem('token') || ''
  const qs = new URLSearchParams({ agent_id: agentId, path })
  const resp = await fetch(`${API}/file/download?${qs.toString()}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!resp.ok) {
    let msg = `HTTP ${resp.status}`
    try {
      const d = await resp.json()
      if (d && typeof d.error === 'string' && d.error) msg = d.error
    } catch { /* 非 JSON 错误体 */ }
    throw new Error(msg)
  }
  const chunks: Uint8Array[] = []
  let loaded = 0
  const reader = resp.body?.getReader()
  if (reader) {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      if (value) {
        chunks.push(value)
        loaded += value.length
        onProgress?.(loaded, totalSize)
      }
    }
  } else {
    const buf = new Uint8Array(await resp.arrayBuffer())
    chunks.push(buf)
    loaded = buf.length
    onProgress?.(loaded, totalSize)
  }
  // 文件名：优先 Content-Disposition 的 filename*=UTF-8'' 形式
  let filename = path.split(/[\\/]/).pop() || 'download'
  const cd = resp.headers.get('Content-Disposition') || ''
  const m = cd.match(/filename\*=UTF-8''([^;]+)/i)
  if (m) {
    try { filename = decodeURIComponent(m[1]) } catch { /* 保留原名 */ }
  }
  return { blob: new Blob(chunks as BlobPart[]), filename }
}

// 远程上传文件（流式，带进度回调；XHR 才能拿到上传进度）
export function uploadFile(
  agentId: string,
  destPath: string,
  file: File,
  overwrite: boolean,
  onProgress?: (loaded: number, total: number) => void,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const token = localStorage.getItem('token') || ''
    const qs = new URLSearchParams({
      agent_id: agentId,
      path: destPath,
      overwrite: overwrite ? '1' : '0',
      size: String(file.size),
    })
    const xhr = new XMLHttpRequest()
    xhr.open('POST', `${API}/file/upload?${qs.toString()}`)
    xhr.setRequestHeader('Authorization', `Bearer ${token}`)
    xhr.setRequestHeader('Content-Type', 'application/octet-stream')
    xhr.upload.onprogress = e => {
      if (e.lengthComputable) onProgress?.(e.loaded, e.total)
    }
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve()
      } else {
        let msg = `HTTP ${xhr.status}`
        try {
          const d = JSON.parse(xhr.responseText)
          if (d && typeof d.error === 'string' && d.error) msg = d.error
        } catch { /* 非 JSON 错误体 */ }
        reject(new Error(msg))
      }
    }
    xhr.onerror = () => reject(new Error('网络错误，上传失败'))
    xhr.onabort = () => reject(new Error('上传已取消'))
    xhr.send(file)
  })
}

export { API }
