import { useState, useEffect, useCallback } from 'react'
import ConfigPage from './ConfigPage'
import LoginPage from './LoginPage'
import ChangePasswordPage from './ChangePasswordPage'
import ChangePasswordModal from './ChangePasswordModal'
import { API, fetchJSON, initAuth, setAuth, clearAuth, isMustChangePassword } from './api'
import './config.css'

interface Host {
  agent_id: string; hostname: string; os_type: string; os_version: string;
  arch: string; agent_version: string; event_count: number; last_seen: string;
  ips?: string[]; is_online: boolean;
}

interface Alert {
  alert_id: string; rule_name: string; severity: string; hostname: string
  description: string; event_type: string; '@timestamp': string; tags?: string[]
}

interface AlertDetail {
  alert_id: string; rule_name: string; rule_id: string; severity: string
  hostname: string; description: string; '@timestamp': string; tags?: string[]
  event_type: string; source_event?: Record<string, string>
}

interface EventItem {
  '@timestamp': string; event_type: string; event_action?: string
  summary: string; pid?: number; hostname: string
}

type Route = { page: 'hosts' } | { page: 'alerts' } | { page: 'events'; host?: string; q?: string } | { page: 'host-detail'; agentId: string } | { page: 'config' }

// ── 工具 ──────────────────────────────────────────────

function formatTime(ts: string): string {
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ts || ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

const SEV: Record<string, string> = {
  critical: '#fb7185', high: '#fbbf24', medium: '#fb923c', low: '#22d3ee', info: '#94a3b8',
}

function parseHash(): Route {
  const hash = location.hash.slice(1)
  const parts = hash.split('?')
  const path = parts[0]
  const params = parts[1] ? new URLSearchParams(parts[1]) : new URLSearchParams()

  if (path.startsWith('hosts/')) return { page: 'host-detail', agentId: path.slice(6) }
  if (path === 'alerts') return { page: 'alerts' }
  if (path === 'config') return { page: 'config' }
  if (path === 'events') {
    // 兼容两种参数名：主机详情页跳转用 host=，事件页过滤框用 hostname=
    return { page: 'events', host: params.get('host') || params.get('hostname') || undefined, q: params.get('q') || undefined }
  }
  return { page: 'hosts' }
}

function navigate(hash: string) { location.hash = '#' + hash }

export default function App() {
  // ── 所有 Hooks 必须在顶部（不能放在条件返回之后） ─────

  const [authState, setAuthState] = useState<{ token: string; username: string; mustChange: boolean } | null>(() => {
    initAuth()
    const t = localStorage.getItem('token')
    if (t) return { token: t, username: localStorage.getItem('username') || '', mustChange: isMustChangePassword() }
    return null
  })

  const [route, setRoute] = useState<Route>(parseHash)
  const [hosts, setHosts] = useState<Host[]>([])
  const [alerts, setAlerts] = useState<Alert[]>([])
  const [events, setEvents] = useState<EventItem[]>([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [detail, setDetail] = useState<AlertDetail | null>(null)
  const [eventDetail, setEventDetail] = useState<any | null>(null)
  const [procs, setProcs] = useState<any[]>([])
  const [conns, setConns] = useState<any[]>([])
  const [procDetail, setProcDetail] = useState<any | null>(null)
  const [sysLoading, setSysLoading] = useState(false)
  const [searchQ, setSearchQ] = useState('')
  const [hostInput, setHostInput] = useState('')
  const [showChangePwd, setShowChangePwd] = useState(false)
  const [exportConfirm, setExportConfirm] = useState<{ kind: 'events' | 'alerts' } | null>(null)
  const [exporting, setExporting] = useState(false)

  // 认证事件监听
  useEffect(() => {
    const handler = (e: Event) => {
      const evt = e as CustomEvent
      if (evt.detail === 'logout') {
        setAuthState(null)
      } else if (evt.detail === 'must_change_password') {
        setAuthState(prev => prev ? { ...prev, mustChange: true } : null)
      } else if (evt.detail === 'unauthorized') {
        // token 失效（过期/吊销/密钥变更）→ 回到登录页
        setAuthState(null)
      }
    }
    window.addEventListener('baize-auth', handler)
    return () => window.removeEventListener('baize-auth', handler)
  }, [])

  // hash 变更监听
  useEffect(() => {
    const onHash = () => { const r = parseHash(); setRoute(r); setSearchQ((r as any).q || ''); setHostInput((r as any).host || '') }
    onHash()
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])

  // 数据加载
  const load = useCallback(async (overrideToken?: string) => {
    if (route.page === 'config') return
    setLoading(true); setErr('')
    try {
      const auth = overrideToken || localStorage.getItem('token')
      const opts = auth ? { headers: { 'Authorization': `Bearer ${auth}` } as Record<string, string> } : undefined
      if (route.page === 'hosts') {
        const d = await fetchJSON<{ hosts: Host[] }>(`${API}/hosts`, opts)
        setHosts(d.hosts)
      } else if (route.page === 'alerts') {
        const d = await fetchJSON<{ alerts: Alert[] }>(`${API}/alerts`, opts)
        setAlerts(d.alerts)
      } else if (route.page === 'events') {
        let params = new URLSearchParams()
        if (route.host) params.set('hostname', route.host)
        if ((route as any).q) params.set('q', (route as any).q)
        const qs = params.toString()
        const d = await fetchJSON<{ events: EventItem[] }>(`${API}/events${qs ? '?' + qs : ''}`, opts)
        setEvents(d.events)
      }
    } catch (e: any) {
      console.warn('[Baize] load error:', e.message, '| route:', route.page, '| auth:', !!localStorage.getItem('token'))
      setErr(e.message)
    }
    setLoading(false)
  }, [route.page, route.page === 'events' ? ((route as any).host + '|' + ((route as any).q || '')) : undefined,
    route.page === 'host-detail' ? (route as any).agentId : undefined])

  useEffect(() => { load() }, [load])

  // 初始加载主机列表（authState 就绪时触发）
  useEffect(() => {
    if (authState && !authState.mustChange) {
      load(authState.token)
    }
  }, [authState, load])

  const doLogout = useCallback(async () => {
    try {
      const token = localStorage.getItem('token')
      if (token) {
        await fetch(`${API}/logout`, { method: 'POST', headers: { 'Authorization': `Bearer ${token}` } })
      }
    } catch {}
    clearAuth()
    setAuthState(null)
    location.hash = ''
  }, [])

  const loadSysInfo = useCallback(async () => {
    if (route.page !== 'host-detail') return
    setSysLoading(true)
    try {
      const d = await fetchJSON<any>(`${API}/systeminfo?agent_id=${(route as any).agentId}`)
      setProcs(d.processes || [])
      setConns((d.tcp_connections || []).concat(d.udp_endpoints || []))
    } catch (e: any) { setErr(e.message) }
    setSysLoading(false)
  }, [route.page, (route as any).agentId])

  const doSearch = useCallback(async () => {
    const inputs = document.querySelectorAll('.filter-bar .input') as unknown as HTMLInputElement[]
    const hostVal = inputs[0]?.value || ''
    const searchVal = inputs[1]?.value || ''
    const params = new URLSearchParams()
    if (hostVal) params.set('hostname', hostVal)
    if (searchVal) params.set('q', searchVal)
    const qs = params.toString()
    const url = `${API}/events${qs ? '?' + qs : ''}`
    try {
      const d = await fetchJSON<{ events: EventItem[] }>(url)
      setEvents(d.events)
    } catch (e: any) { setErr(e.message) }
  }, [])

  async function showAlertDetail(alertID: string) {
    try {
      const d = await fetchJSON<AlertDetail>(`${API}/alert?alert_id=${alertID}`)
      setDetail(d)
    } catch { setErr('加载告警详情失败') }
  }

  // 导出：带 token 请求导出端点，拿 blob 触发浏览器下载
  async function doExport(kind: 'events' | 'alerts') {
    setExporting(true)
    try {
      const params = new URLSearchParams()
      if (kind === 'events') {
        if ((route as any).host) params.set('hostname', (route as any).host)
        if ((route as any).q) params.set('q', (route as any).q)
      }
      const qs = params.toString()
      const r = await fetch(`${API}/export/${kind}${qs ? '?' + qs : ''}`, {
        headers: { 'Authorization': `Bearer ${localStorage.getItem('token') || ''}` }
      })
      if (r.status === 401) {
        clearAuth()
        window.dispatchEvent(new CustomEvent('baize-auth', { detail: 'unauthorized' }))
        throw new Error('登录已过期，请重新登录')
      }
      if (!r.ok) {
        const d = await r.json().catch(() => ({}))
        throw new Error((d as any).error || `导出失败 (${r.status})`)
      }
      const blob = await r.blob()
      const cd = r.headers.get('Content-Disposition') || ''
      const m = cd.match(/filename="?([^";]+)"?/)
      const filename = m ? m[1] : `${kind}_${Date.now()}.csv`
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
    } catch (e: any) {
      setErr(e.message)
    } finally {
      setExporting(false)
    }
  }

  const host = route.page === 'host-detail' ? hosts.find(h => h.agent_id === route.agentId) : null

  // ── 判断当前应该渲染哪个页面 ──────────────────────────

  // 未登录 → 登录页
  if (!authState) {
    return (
      <LoginPage
        onLogin={(token, username, mustChange) => {
          setAuth(token, mustChange)
          localStorage.setItem('username', username)
          setAuthState({ token, username, mustChange })
          navigate('hosts')
        }}
      />
    )
  }

  // 已登录但需改密码
  if (authState.mustChange) {
    return (
      <ChangePasswordPage
        username={authState.username}
        token={authState.token}
        onPasswordChanged={(token) => {
          setAuth(token, false)
          setAuthState({ ...authState, token, mustChange: false })
          navigate('hosts')
        }}
        onLogout={() => {
          clearAuth()
          setAuthState(null)
        }}
      />
    )
  }

  // ── Dashboard ──────────────────────────────────────────

  return (
    <div className="app">
      <header className="header">
        <div className="header-inner">
          <h1>
            {route.page === 'host-detail' ? (
              <><span className="back" onClick={() => navigate('hosts')}>←</span> <span className="logo">◆</span> {host?.hostname || '主机详情'}</>
            ) : (
              <><span className="logo">◆</span> Baize 白泽 <span className="subtitle">EDR Dashboard</span></>
            )}
          </h1>
          {route.page !== 'host-detail' && (
            <div className="tabs">
              <button onClick={() => navigate('hosts')} className={route.page === 'hosts' ? 'active' : ''}>🖥 主机 ({hosts.length})</button>
              <button onClick={() => navigate('alerts')} className={route.page === 'alerts' ? 'active' : ''}>🚨 告警 ({alerts.length})</button>
              <button onClick={() => navigate('events')} className={route.page === 'events' ? 'active' : ''}>📋 事件</button>
              <button onClick={() => navigate('config')} className={route.page === 'config' ? 'active' : ''}>⚙ 配置</button>
              <button onClick={() => setShowChangePwd(true)} className="change-pwd-btn">🔑 密码</button>
              <button onClick={doLogout} className="logout-btn">退出</button>
            </div>
          )}
        </div>
      </header>

      <main className="main">
        {err && <div className="error">{err}</div>}
        {loading && route.page !== 'events' && <div className="loading">加载中...</div>}
        {exporting && <div className="loading">⏳ 正在生成导出文件，请稍候…（最多 50,000 条，数据量大时可能需要一些时间）</div>}

        {!loading && route.page === 'hosts' && hosts && (
          <div className="grid">
            {hosts.map(h => (
              <div key={h.agent_id} className="card" onClick={() => navigate('hosts/' + h.agent_id)}>
                <div className="card-header"><span className={`dot ${h.is_online ? 'green' : 'gray'}`} /><strong>{h.hostname}</strong><span className={`badge ${h.is_online ? 'online' : 'offline'}`}>{h.is_online ? '在线' : '离线'}</span></div>
                <div className="card-body">
                  <div className="row"><span className="label">OS</span><span>{h.os_type} {h.os_version}</span></div>
                  <div className="row"><span className="label">架构</span><span>{h.arch || '-'}</span></div>
                  <div className="row"><span className="label">Agent</span><span className="mono">{h.agent_version || '-'}</span></div>
                  <div className="row"><span className="label">事件</span><span>{h.event_count.toLocaleString()}</span></div>
                  {h.ips && h.ips.length > 0 && (
                    <div className="row"><span className="label">IP</span><span className="mono">{h.ips.join(', ')}</span></div>
                  )}
                  <div className="row"><span className="label">ID</span><span className="mono" style={{fontSize:'0.65rem'}}>{h.agent_id.slice(0,19)}...</span></div>
                  <div className="ago" style={{marginTop:'0.3rem'}}>最后活跃: {formatTime(h.last_seen)}</div>
                </div>
              </div>
            ))}
            {hosts.length === 0 && <div className="empty">暂无在线主机</div>}
          </div>
        )}

        {!loading && route.page === 'config' && <ConfigPage />}

        {!loading && route.page === 'host-detail' && host && (
          <div className="host-detail">
            <div className="detail-grid">
              <div className="detail-card">
                <div className="detail-label">Agent ID</div>
                <div className="detail-value mono">{host.agent_id}</div>
              </div>
              <div className="detail-card">
                <div className="detail-label">系统</div>
                <div className="detail-value">{host.os_type} {host.os_version}</div>
              </div>
              <div className="detail-card">
                <div className="detail-label">架构</div>
                <div className="detail-value">{host.arch || '-'}</div>
              </div>
              <div className="detail-card">
                <div className="detail-label">Agent 版本</div>
                <div className="detail-value mono">{host.agent_version || '-'}</div>
              </div>
              <div className="detail-card">
                <div className="detail-label">IP 地址</div>
                <div className="detail-value mono">{host.ips?.join(', ') || '-'}</div>
              </div>
              <div className="detail-card">
                <div className="detail-label">最后活跃</div>
                <div className="detail-value">{host.last_seen}</div>
              </div>
            </div>
            <div className="detail-actions">
              <button className="btn" onClick={() => navigate('events?host=' + host.hostname)}>查看事件</button>
            </div>
            <div style={{marginTop:'1rem'}}>
              <div style={{display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:'0.5rem'}}>
                <span style={{fontSize:'0.9rem', fontWeight:600}}>系统信息</span>
                <button className="btn" onClick={loadSysInfo} disabled={sysLoading} style={{fontSize:'0.75rem'}}>{sysLoading ? '加载中...' : '刷新'}</button>
              </div>
              <div style={{display:'flex', gap:'1rem'}}>
              <div style={{flex:1}}>
                <h3 style={{margin:'0 0 0.5rem'}}>进程 ({procs.length})</h3>
                <div className="scroll-table">
                <table className="table">
                  <thead><tr><th>PID</th><th>名称</th><th>CPU%</th><th>内存</th><th></th></tr></thead>
                  <tbody>
                    {procs.map((p,i) => (
                      <tr key={i}><td className="mono">{p.pid}</td><td>{p.name}</td><td>{p.cpu?.toFixed(1)}</td><td>{(p.memory / 1024).toFixed(0)}KB</td><td className="action"><span className="link" onClick={() => setProcDetail(p)}>详情</span></td></tr>
                    ))}
                    {procs.length === 0 && <tr><td colSpan={5} className="empty">点击刷新获取进程信息</td></tr>}
                  </tbody>
                </table>
                </div>
              </div>
              <div style={{flex:1}}>
                <h3 style={{margin:'0 0 0.5rem'}}>网络连接 ({conns.length})</h3>
                <div className="scroll-table">
                <table className="table">
                  <thead><tr><th>PID</th><th>本地</th><th>远程</th><th>状态</th></tr></thead>
                  <tbody>
                    {conns.map((c,i) => (
                      <tr key={i}><td className="mono">{c.pid}</td><td className="mono">{c.local}</td><td className="mono">{c.remote || '-'}</td><td>{c.state}</td></tr>
                    ))}
                    {conns.length === 0 && <tr><td colSpan={4} className="empty">点击刷新获取连接信息</td></tr>}
                  </tbody>
                </table>
                </div>
              </div>
            </div>
            </div>
          </div>
        )}
        {!loading && route.page === 'host-detail' && !host && (
          <div className="empty">主机不存在或尚未加载</div>
        )}

        {!loading && route.page === 'alerts' && (
          <>
            <div className="filter-bar" style={{ justifyContent: 'flex-end' }}>
              <button onClick={() => setExportConfirm({ kind: 'alerts' })} className="btn" disabled={exporting}>{exporting ? '⏳ 导出中...' : '⬇ 导出'}</button>
            </div>
          <table className="table">
            <thead>
              <tr><th>严重度</th><th>规则</th><th>主机</th><th>类型</th><th>时间</th></tr>
            </thead>
            <tbody>
              {alerts.map(a => (
                <tr key={a.alert_id} onClick={() => showAlertDetail(a.alert_id)} className="clickable">
                  <td><span className="sev" style={{ color: SEV[a.severity] || '#94a3b8' }}>●</span> {a.severity}</td>
                  <td>{a.rule_name}</td>
                  <td>{a.hostname}</td>
                  <td className="mono">{a.event_type}</td>
                  <td className="ago">{formatTime(a['@timestamp'])}</td>
                </tr>
              ))}
              {alerts.length === 0 && <tr><td colSpan={5} className="empty">暂无告警</td></tr>}
            </tbody>
          </table>
          </>
        )}

        {route.page === 'events' && (
          <>
            <div className="filter-bar">
              <input value={hostInput} onChange={e => setHostInput(e.target.value)} onBlur={e => {
                const v = e.target.value
                const params = new URLSearchParams()
                if (v) params.set('hostname', v)
                if ((route as any).q) params.set('q', (route as any).q)
                const qs = params.toString()
                navigate(qs ? 'events?' + qs : 'events')
              }} onKeyDown={e => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur() }} placeholder="按主机名过滤..." className="input" style={{width:'auto',flex:1}} />
              <input value={searchQ} onChange={e => setSearchQ(e.target.value)} onBlur={e => {
                const v = e.target.value
                const params = new URLSearchParams()
                if ((route as any).host) params.set('hostname', (route as any).host)
                if (v) params.set('q', v)
                const qs = params.toString()
                navigate(qs ? 'events?' + qs : 'events')
              }} onKeyDown={e => { if (e.key === 'Enter') { (e.target as HTMLInputElement).blur() } }} placeholder="搜索 (PID, IP, 域名, 文件名)..." className="input" style={{width:'auto',flex:2}} />
              <button onClick={doSearch} className="btn">查询</button>
              <button onClick={() => setExportConfirm({ kind: 'events' })} className="btn" disabled={exporting}>{exporting ? '⏳ 导出中...' : '⬇ 导出'}</button>
            </div>
            {loading ? <div className="loading">加载中...</div> : (
            <table className="table">
              <thead><tr><th>时间</th><th>主机</th><th>类型</th><th>摘要</th><th></th></tr></thead>
              <tbody>
                {events.map((e, i) => (
                  <tr key={i}>
                    <td className="ago">{formatTime(e['@timestamp'])}</td>
                    <td>{e.hostname}</td>
                    <td className="mono">{e.event_type}{e.event_action ? '/' + e.event_action : ''}</td>
                    <td className="summary">{e.summary}</td>
                    <td className="action"><span className="link" onClick={() => setEventDetail(e)}>详情</span></td>
                  </tr>
                ))}
                {events.length === 0 && <tr><td colSpan={5} className="empty">暂无事件</td></tr>}
              </tbody>
            </table>
            )}
          </>
        )}
      </main>

      {detail && (
        <div className="overlay" onClick={() => setDetail(null)}>
          <div className="modal" onClick={e => e.stopPropagation()}>
            <div className="modal-header">
              <span className="sev" style={{ color: SEV[detail.severity] }}>●</span>
              <strong>{detail.rule_name}</strong>
              <button className="close" onClick={() => setDetail(null)}>×</button>
            </div>
            <div className="modal-body">
              <div className="field"><label>告警 ID</label><span className="mono">{detail.alert_id}</span></div>
              <div className="field"><label>规则 ID</label><span className="mono">{detail.rule_id}</span></div>
              <div className="field"><label>严重度</label><span>{detail.severity}</span></div>
              <div className="field"><label>主机</label><span>{detail.hostname}</span></div>
              <div className="field"><label>描述</label><span>{detail.description || '-'}</span></div>
              <div className="field"><label>事件类型</label><span className="mono">{detail.event_type}</span></div>
              <div className="field"><label>时间</label><span>{detail['@timestamp']}</span></div>
              {detail.tags && <div className="field"><label>标签</label><span>{detail.tags.join(', ')}</span></div>}
              {detail.source_event && (
                <div className="source-section">
                  <div className="section-title">触发事件的字段</div>
                  <table className="kv-table">
                    <tbody>
                      {Object.entries(detail.source_event).map(([k, v]) => (
                        <tr key={k}><td className="mono">{k}</td><td className="mono">{String(v)}</td></tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          </div>
        </div>
      )}

      {eventDetail && (
        <div className="overlay" onClick={() => setEventDetail(null)}>
          <div className="modal modal-wide" onClick={e => e.stopPropagation()}>
            <div className="modal-header">
              <strong>{eventDetail.event_type}{eventDetail.event_action ? '/' + eventDetail.event_action : ''}</strong>
              <span className="ago" style={{marginLeft:'0.6rem'}}>{eventDetail['@timestamp']}</span>
              <button className="close" onClick={() => setEventDetail(null)}>×</button>
            </div>
            <div className="modal-body">
              <table className="kv-table">
                <tbody>
                  {eventDetail.raw && Object.entries(eventDetail.raw).filter(([k]) => !k.startsWith('_')).sort().map(([k, v]) => (
                    <tr key={k}><td className="mono">{k}</td><td className="mono">{String(v)}</td></tr>
                  ))}
                  {!eventDetail.raw && <tr><td colSpan={2} className="empty">无详细数据</td></tr>}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      )}
      {/* 导出确认弹窗 */}
      {exportConfirm && (
        <div className="overlay" onClick={() => setExportConfirm(null)}>
          <div className="modal" onClick={e => e.stopPropagation()} style={{ maxWidth: '440px' }}>
            <div className="modal-header">
              <strong>确认导出</strong>
              <button className="close" onClick={() => setExportConfirm(null)}>×</button>
            </div>
            <div className="modal-body">
              <div className="field">
                <label>导出内容</label>
                <span>{exportConfirm.kind === 'events' ? '事件记录（当前过滤条件下，最多 50,000 条）' : '全部告警记录（最多 50,000 条）'}</span>
              </div>
              {exportConfirm.kind === 'events' && ((route as any).host || (route as any).q) && (
                <div className="field">
                  <label>当前过滤</label>
                  <span>{[(route as any).host ? `主机: ${(route as any).host}` : '', (route as any).q ? `搜索: ${(route as any).q}` : ''].filter(Boolean).join('　')}</span>
                </div>
              )}
              <div className="field"><label>文件格式</label><span>CSV（UTF-8，Excel/WPS 可直接打开）</span></div>
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem', padding: '0 1rem 1rem' }}>
              <button className="btn" onClick={() => setExportConfirm(null)}>取消</button>
              <button className="btn" onClick={() => { const k = exportConfirm.kind; setExportConfirm(null); doExport(k) }}>确认导出</button>
            </div>
          </div>
        </div>
      )}
      {procDetail && (
        <div className="overlay" onClick={() => setProcDetail(null)}>
          <div className="modal" onClick={e => e.stopPropagation()}>
            <div className="modal-header">
              <strong>进程详情 · PID {procDetail.pid}</strong>
              <span className="mono" style={{marginLeft:'0.6rem'}}>{procDetail.name}</span>
              <button className="close" onClick={() => setProcDetail(null)}>×</button>
            </div>
            <div className="modal-body">
              <table className="kv-table">
                <tbody>
                  {[['PID', procDetail.pid], ['名称', procDetail.name], ['路径', procDetail.exe],
                    ['CPU', procDetail.cpu?.toFixed(1) + '%'], ['内存', (procDetail.memory / 1024).toFixed(0) + ' KB']].map(([k,v]) => (
                    <tr key={k as string}><td className="mono">{k as string}</td><td className="mono">{String(v)}</td></tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      )}

      {/* 修改密码弹窗 */}
      {showChangePwd && authState && (
        <div className="overlay" onClick={() => setShowChangePwd(false)}>
          <div className="modal" onClick={e => e.stopPropagation()} style={{maxWidth:'420px'}}>
            <ChangePasswordModal
              token={authState.token}
              onDone={(newToken) => {
                setAuth(newToken, false)
                setAuthState({ ...authState, token: newToken, mustChange: false })
                setShowChangePwd(false)
                navigate('hosts')
              }}
              onClose={() => setShowChangePwd(false)}
            />
          </div>
        </div>
      )}
    </div>
  )
}
