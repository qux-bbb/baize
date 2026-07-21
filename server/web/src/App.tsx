import { useState, useEffect, useCallback } from 'react'
import ConfigPage from './ConfigPage'
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

type Route = { page: 'hosts' } | { page: 'alerts' } | { page: 'events'; host?: string; q?: string } | { page: 'host-detail'; agentId: string } | { page: 'config' } | { page: 'config' }

const API = '/api'

async function fetchJSON<T>(url: string): Promise<T> {
  const r = await fetch(url)
  if (!r.ok) throw new Error(`HTTP ${r.status}`)
  return r.json()
}

function timeAgo(ts: string): string {
  const s = Math.floor((Date.now() - new Date(ts).getTime()) / 1000)
  if (s < 60) return '刚刚'
  if (s < 3600) return `${Math.floor(s / 60)}分钟前`
  if (s < 86400) return `${Math.floor(s / 3600)}小时前`
  return `${Math.floor(s / 86400)}天前`
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
  if (path === 'events') return { page: 'events', host: params.get('host') || undefined }
  return { page: 'hosts' }
}

function navigate(hash: string) { location.hash = '#' + hash }

export default function App() {
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
  const [sysLoading, setSysLoading] = useState(false)
  const [searchQ, setSearchQ] = useState('')
  const [hostInput, setHostInput] = useState('')

  useEffect(() => {
    const onHash = () => { const r = parseHash(); setRoute(r); setSearchQ((r as any).q || ''); setHostInput((r as any).host || '') }
    onHash()
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])

  const load = useCallback(async () => {
    if (route.page === 'config') return
    setLoading(true); setErr('')
    try {
      if (route.page === 'hosts') {
        const d = await fetchJSON<{ hosts: Host[] }>(`${API}/hosts`)
        setHosts(d.hosts)
      } else if (route.page === 'alerts') {
        const d = await fetchJSON<{ alerts: Alert[] }>(`${API}/alerts`)
        setAlerts(d.alerts)
      } else if (route.page === 'events') {
        let params = new URLSearchParams()
        if (route.host) params.set('hostname', route.host)
        if ((route as any).q) params.set('q', (route as any).q)
        const qs = params.toString()
        const d = await fetchJSON<{ events: EventItem[] }>(`${API}/events${qs ? '?' + qs : ''}`)
        setEvents(d.events)
      } else if (route.page === 'host-detail') {
        // 系统信息通过点击刷新获取，不由 load 自动加载
      }
    } catch (e: any) { setErr(e.message) }
    setLoading(false)
  }, [route.page, route.page === 'events' ? ((route as any).host + '|' + ((route as any).q || '')) : undefined,
    route.page === 'host-detail' ? (route as any).agentId : undefined])

  // 初始加载时 fetch 一次主机列表，之后页面切换不重置
  useEffect(() => {
    fetchJSON<{ hosts: Host[] }>(`${API}/hosts`).then(d => setHosts(d.hosts)).catch(() => {})
  }, [])

  // 加载系统信息（进程/网络）
  const loadSysInfo = useCallback(async () => {
    if (route.page !== 'host-detail') return
    setSysLoading(true)
    try {
      const d = await fetchJSON<any>(`${API}/systeminfo?agent_id=${(route as any).agentId}`)
      setProcs(d.processes || [])
      setConns((d.tcp_connections || []).concat(d.udp_endpoints || []))
    } catch (e: any) {
      setErr(e.message)
    }
    setSysLoading(false)
  }, [route.page, (route as any).agentId])

  // 搜索函数：读 DOM、更新 hash、调 API
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

  useEffect(() => { load() }, [load])

  async function showAlertDetail(alertID: string) {
    try {
      const d = await fetchJSON<AlertDetail>(`${API}/alert?alert_id=${alertID}`)
      setDetail(d)
    } catch { setErr('加载告警详情失败') }
  }

  const host = route.page === 'host-detail' ? hosts.find(h => h.agent_id === route.agentId) : null

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
            </div>
          )}
        </div>
      </header>

      <main className="main">
        {err && <div className="error">{err}</div>}
        {loading && <div className="loading">加载中...</div>}

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
                  <div className="ago" style={{marginTop:'0.3rem'}}>最后活跃: {timeAgo(h.last_seen)}</div>
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
                  <thead><tr><th>PID</th><th>名称</th><th>CPU%</th><th>内存</th></tr></thead>
                  <tbody>
                    {procs.map((p,i) => (
                      <tr key={i}><td className="mono">{p.pid}</td><td className="summary">{p.name}</td><td>{p.cpu?.toFixed(1)}</td><td>{(p.memory / 1024).toFixed(0)}KB</td></tr>
                    ))}
                    {procs.length === 0 && <tr><td colSpan={4} className="empty">点击刷新获取进程信息</td></tr>}
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
                  <td className="ago">{timeAgo(a['@timestamp'])}</td>
                </tr>
              ))}
              {alerts.length === 0 && <tr><td colSpan={5} className="empty">暂无告警</td></tr>}
            </tbody>
          </table>
        )}

        {!loading && route.page === 'events' && (
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
              <input value={(route as any).q || searchQ} onChange={e => setSearchQ(e.target.value)} onBlur={e => {
                const v = e.target.value
                const params = new URLSearchParams()
                if ((route as any).host) params.set('hostname', (route as any).host)
                if (v) params.set('q', v)
                const qs = params.toString()
                navigate(qs ? 'events?' + qs : 'events')
              }} onKeyDown={e => { if (e.key === 'Enter') { (e.target as HTMLInputElement).blur() } }} placeholder="搜索 (PID, IP, 域名, 文件名)..." className="input" style={{width:'auto',flex:2}} />
              <button onClick={doSearch} className="btn">查询</button>
            </div>
            <table className="table">
              <thead><tr><th>时间</th><th>主机</th><th>类型</th><th>摘要</th><th></th></tr></thead>
              <tbody>
                {events.map((e, i) => (
                  <tr key={i}>
                    <td className="ago">{timeAgo(e['@timestamp'])}</td>
                    <td>{e.hostname}</td>
                    <td className="mono">{e.event_type}{e.event_action ? '/' + e.event_action : ''}</td>
                    <td className="summary">{e.summary}</td>
                    <td className="action"><span className="link" onClick={() => setEventDetail(e)}>详情</span></td>
                  </tr>
                ))}
                {events.length === 0 && <tr><td colSpan={5} className="empty">暂无事件</td></tr>}
              </tbody>
            </table>
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
    </div>
  )
}
