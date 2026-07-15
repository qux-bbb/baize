import { useState, useEffect, useCallback } from 'react'

interface Host {
  agent_id: string; hostname: string; os_type: string; os_version: string
  agent_version?: string; arch?: string; event_count: number
  last_seen: string; ips?: string[]
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

export default function App() {
  const [tab, setTab] = useState<'hosts' | 'alerts' | 'events'>('hosts')
  const [hosts, setHosts] = useState<Host[]>([])
  const [alerts, setAlerts] = useState<Alert[]>([])
  const [events, setEvents] = useState<EventItem[]>([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [hostFilter, setHostFilter] = useState('')
  const [detail, setDetail] = useState<AlertDetail | null>(null)

  const load = useCallback(async () => {
    setLoading(true); setErr(''); setDetail(null)
    try {
      if (tab === 'hosts') {
        const d = await fetchJSON<{ hosts: Host[] }>(`${API}/hosts`)
        setHosts(d.hosts)
      } else if (tab === 'alerts') {
        const d = await fetchJSON<{ alerts: Alert[] }>(`${API}/alerts`)
        setAlerts(d.alerts)
      } else {
        const q = hostFilter ? `?hostname=${hostFilter}` : ''
        const d = await fetchJSON<{ events: EventItem[] }>(`${API}/events${q}`)
        setEvents(d.events)
      }
    } catch (e: any) { setErr(e.message) }
    setLoading(false)
  }, [tab, hostFilter])

  useEffect(() => { load() }, [load])

  async function showDetail(alertID: string) {
    try {
      const d = await fetchJSON<AlertDetail>(`${API}/alert?alert_id=${alertID}`)
      setDetail(d)
    } catch { setErr('加载告警详情失败') }
  }

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
        {err && <div className="error">{err}</div>}
        {loading && <div className="loading">加载中...</div>}

        {!loading && tab === 'hosts' && (
          <div className="grid">
            {hosts.map(h => (
              <div key={h.agent_id} className="card" onClick={() => { setHostFilter(h.hostname); setTab('events') }}>
                <div className="card-header"><span className="dot green" /><strong>{h.hostname}</strong></div>
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

        {!loading && tab === 'alerts' && (
          <table className="table">
            <thead>
              <tr><th>严重度</th><th>规则</th><th>主机</th><th>类型</th><th>时间</th></tr>
            </thead>
            <tbody>
              {alerts.map(a => (
                <tr key={a.alert_id} onClick={() => showDetail(a.alert_id)} className="clickable">
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

        {!loading && tab === 'events' && (
          <>
            <div className="filter-bar">
              <input value={hostFilter} onChange={e => setHostFilter(e.target.value)} placeholder="按主机名过滤..." className="input" />
              <button onClick={load} className="btn">查询</button>
            </div>
            <table className="table">
              <thead><tr><th>时间</th><th>主机</th><th>类型</th><th>摘要</th></tr></thead>
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

      {/* 告警详情弹窗 */}
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
                        <tr key={k}>
                          <td className="mono">{k}</td>
                          <td className="mono">{String(v)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
