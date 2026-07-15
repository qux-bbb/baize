import { useState, useEffect, useCallback } from 'react'

// ── Types ──────────────────────────────────────────────

interface Host {
  agent_id: string
  hostname: string
  os_type: string
  os_version: string
  event_count: number
  last_seen: string
  ips?: string[]
}

interface Alert {
  alert_id: string
  rule_name: string
  severity: string
  hostname: string
  description: string
  event_type: string
  '@timestamp': string
  tags?: string[]
}

interface Event {
  '@timestamp': string
  event_type: string
  event_action?: string
  summary: string
  pid?: number
  hostname: string
}

// ── API 工具 ──────────────────────────────────────────────

const API = '/api'

async function fetchJSON<T>(url: string): Promise<T> {
  const res = await fetch(url)
  if (!res.ok) throw new Error(`HTTP ${res.status}`)
  return res.json()
}

function timeAgo(ts: string): string {
  const sec = Math.floor((Date.now() - new Date(ts).getTime()) / 1000)
  if (sec < 60) return '刚刚'
  if (sec < 3600) return `${Math.floor(sec / 60)}分钟前`
  if (sec < 86400) return `${Math.floor(sec / 3600)}小时前`
  return `${Math.floor(sec / 86400)}天前`
}

const SEV_COLORS: Record<string, string> = {
  critical: '#fb7185',
  high: '#fbbf24',
  medium: '#fb923c',
  low: '#22d3ee',
  info: '#94a3b8',
}

// ── 单页 App ──────────────────────────────────────────────

export default function App() {
  const [tab, setTab] = useState<'hosts' | 'alerts' | 'events'>('hosts')
  const [hosts, setHosts] = useState<Host[]>([])
  const [alerts, setAlerts] = useState<Alert[]>([])
  const [events, setEvents] = useState<Event[]>([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [hostFilter, setHostFilter] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setErr('')
    try {
      if (tab === 'hosts') {
        const d = await fetchJSON<{ hosts: Host[] }>(`${API}/hosts`)
        setHosts(d.hosts)
      } else if (tab === 'alerts') {
        const d = await fetchJSON<{ alerts: Alert[] }>(`${API}/alerts`)
        setAlerts(d.alerts)
      } else {
        const q = hostFilter ? `?hostname=${hostFilter}` : ''
        const d = await fetchJSON<{ events: Event[] }>(`${API}/events${q}`)
        setEvents(d.events)
      }
    } catch (e: any) {
      setErr(e.message)
    }
    setLoading(false)
  }, [tab, hostFilter])

  useEffect(() => { load() }, [load])

  return (
    <div className="app">
      <header className="header">
        <div className="header-inner">
          <h1><span className="logo">◆</span> Baize 白泽 <span className="subtitle">EDR Dashboard</span></h1>
          <div className="tabs">
            <button onClick={() => setTab('hosts')} className={tab === 'hosts' ? 'active' : ''}>🖥 主机 ({hosts.length})</button>
            <button onClick={() => setTab('alerts')} className={tab === 'alerts' ? 'active' : ''}>🚨 告警 ({alerts.length})</button>
            <button onClick={() => setTab('events')} className={tab === 'events' ? 'active' : ''}>📋 事件</button>
          </div>
        </div>
      </header>

      <main className="main">
        {err && <div className="error">连接失败: {err}</div>}
        {loading && <div className="loading">加载中...</div>}

        {!loading && tab === 'hosts' && (
          <div className="grid">
            {hosts.map(h => (
              <div key={h.agent_id} className="card host-card" onClick={() => { setHostFilter(h.hostname); setTab('events') }}>
                <div className="card-header">
                  <span className="dot green" />
                  <strong>{h.hostname}</strong>
                </div>
                <div className="card-body">
                  <div>OS: {h.os_type} {h.os_version}</div>
                  <div>事件: {h.event_count.toLocaleString()}</div>
                  <div className="ago">{timeAgo(h.last_seen)}</div>
                </div>
              </div>
            ))}
            {hosts.length === 0 && <div className="empty">暂无在线主机</div>}
          </div>
        )}

        {!loading && tab === 'alerts' && (
          <table className="table">
            <thead>
              <tr><th>严重度</th><th>规则</th><th>主机</th><th>类型</th><th>时间</th></tr>
            </thead>
            <tbody>
              {alerts.map(a => (
                <tr key={a.alert_id}>
                  <td><span className="sev" style={{ color: SEV_COLORS[a.severity] || '#94a3b8' }}>●</span> {a.severity}</td>
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

        {!loading && tab === 'events' && (
          <>
            <div className="filter-bar">
              <input value={hostFilter} onChange={e => setHostFilter(e.target.value)} placeholder="按主机名过滤..." className="input" />
              <button onClick={load} className="btn">查询</button>
            </div>
            <table className="table">
              <thead>
                <tr><th>时间</th><th>主机</th><th>类型</th><th>摘要</th></tr>
              </thead>
              <tbody>
                {events.map((e, i) => (
                  <tr key={i}>
                    <td className="ago">{timeAgo(e['@timestamp'])}</td>
                    <td>{e.hostname}</td>
                    <td className="mono">{e.event_type}{e.event_action ? '/' + e.event_action : ''}</td>
                    <td className="summary">{e.summary}</td>
                  </tr>
                ))}
                {events.length === 0 && <tr><td colSpan={4} className="empty">暂无事件</td></tr>}
              </tbody>
            </table>
          </>
        )}
      </main>
    </div>
  )
}
