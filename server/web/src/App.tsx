import { useState, useEffect, useCallback, useMemo } from 'react'
import type { ColDef, ICellRendererParams } from 'ag-grid-community'
import ConfigPage from './ConfigPage'
import AgentDownload from './AgentDownload'
import LoginPage from './LoginPage'
import ChangePasswordPage from './ChangePasswordPage'
import ChangePasswordModal from './ChangePasswordModal'
import { API, fetchJSON, initAuth, setAuth, clearAuth, isMustChangePassword } from './api'
import { BaizeGrid, SEV, formatTime, sevCell, linkCell, statusCell, timeFormatter, sevComparator, dateFilterParams } from './grid'
import './config.css'

interface Host {
  agent_id: string; hostname: string; os_type: string; os_version: string;
  arch: string; agent_version: string; event_count: number; last_seen: string;
  ips?: string[]; is_online: boolean; revoked?: boolean;
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

type Route = { page: 'hosts' } | { page: 'alerts' } | { page: 'events'; host?: string; q?: string } | { page: 'host-detail'; agentId: string } | { page: 'config' } | { page: 'agent' }

// ── 工具 ──────────────────────────────────────────────

// 系统状态连接格式转换：{local_ip, local_port, remote_ip, remote_port} → {local:"ip:port", remote:"ip:port"}
// （状态数据连接字段是分开的 IP/端口，实时查询是拼接好的字符串，统一成展示结构）
function convConn(c: any) {
  const local = `${c.local_ip || '0.0.0.0'}:${c.local_port}`
  const remote = c.remote_ip && c.remote_ip !== '0.0.0.0' ? `${c.remote_ip}:${c.remote_port}` : ''
  return { pid: c.pid, local, remote, state: c.state || '' }
}

function parseHash(): Route {
  const hash = location.hash.slice(1)
  const parts = hash.split('?')
  const path = parts[0]
  const params = parts[1] ? new URLSearchParams(parts[1]) : new URLSearchParams()

  if (path.startsWith('hosts/')) return { page: 'host-detail', agentId: path.slice(6) }
  if (path === 'alerts') return { page: 'alerts' }
  if (path === 'config') return { page: 'config' }
  if (path === 'agent') return { page: 'agent' }
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
  const [procDetail, setProcDetail] = useState<any | null>(null)
  // 系统状态展示（主机详情页）：默认 = 系统状态基线（首次上线/刷新时 Agent 上报落库的最新一份）；
  // 点"实时刷新" = 实时查询当前状态（Agent 收到指令会同时上报新状态，Server 覆盖落库）
  const [sysView, setSysView] = useState<{
    source: 'state' | 'live'
    capturedAt: string   // state 模式：Agent 采集时间
    receivedAt: string   // state 模式：Server 收到时间
    liveAt: string       // live 模式：查询时间
    procs: any[]
    tcp: any[]
    udp: any[]
  } | null>(null)
  const [sysLoading, setSysLoading] = useState(false)
  const [sysRefreshing, setSysRefreshing] = useState(false)
  const [sysErr, setSysErr] = useState('')
  // 系统状态标签页：进程 / TCP 连接 / UDP 监听（默认进程）
  const [sysTab, setSysTab] = useState<'procs' | 'tcp' | 'udp'>('procs')
  const [searchQ, setSearchQ] = useState('')
  const [hostInput, setHostInput] = useState('')
  const [showChangePwd, setShowChangePwd] = useState(false)
  const [exportConfirm, setExportConfirm] = useState<{ kind: 'events' | 'alerts' } | null>(null)
  const [exporting, setExporting] = useState(false)
  // 主机行操作菜单（⋯ 按钮 → 查看详情 / 移除 Agent）+ 移除确认
  const [rowMenu, setRowMenu] = useState<{ agentId: string; hostname: string; x: number; y: number } | null>(null)
  const [removeConfirm, setRemoveConfirm] = useState<{ agentId: string; hostname: string } | null>(null)
  const [removing, setRemoving] = useState(false)
  const [notice, setNotice] = useState('')

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

  // 行操作菜单：点击页面其他位置关闭
  useEffect(() => {
    const close = () => setRowMenu(null)
    window.addEventListener('click', close)
    return () => window.removeEventListener('click', close)
  }, [])

  // 数据加载
  const load = useCallback(async (overrideToken?: string) => {
    if (route.page === 'config' || route.page === 'agent') return
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

  // 进入主机详情页自动加载系统状态（基线：首次上线/刷新时 Agent 上报落库的最新一份）
  const loadSystemState = useCallback(async () => {
    if (route.page !== 'host-detail') return
    setSysLoading(true); setSysErr('')
    try {
      const d = await fetchJSON<any>(`${API}/system-state?agent_id=${(route as any).agentId}`)
      setSysView({
        source: 'state',
        capturedAt: d.captured_at || '',
        receivedAt: d.received_at || '',
        liveAt: '',
        procs: d.processes || [],
        tcp: (d.tcp_connections || []).map(convConn),
        udp: (d.udp_endpoints || []).map(convConn),
      })
    } catch (e: any) {
      setSysView(null)
      if (e.message !== '该主机暂无系统状态') setSysErr(e.message)
    }
    setSysLoading(false)
  }, [route.page, (route as any).agentId])

  useEffect(() => { loadSystemState() }, [loadSystemState])

  // 实时刷新：查询当前状态并展示；Agent 收到指令会同时上报新系统状态（Server 覆盖落库 = 刷新即更新基线）
  const refreshSysInfo = useCallback(async () => {
    if (route.page !== 'host-detail') return
    setSysRefreshing(true); setSysErr('')
    try {
      const d = await fetchJSON<any>(`${API}/systeminfo?agent_id=${(route as any).agentId}`)
      setSysView({
        source: 'live',
        capturedAt: '', receivedAt: '',
        liveAt: new Date().toISOString(),
        procs: d.processes || [],
        tcp: (d.tcp_connections || []).map((c: any) => ({ pid: c.pid, local: c.local, remote: c.remote || '', state: c.state })),
        udp: (d.udp_endpoints || []).map((c: any) => ({ pid: c.pid, local: c.local, remote: '', state: c.state })),
      })
    } catch (e: any) { setSysErr(e.message) }
    setSysRefreshing(false)
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

  // 导出：导航式下载（浏览器原生下载；Firefox 对 fetch+blob+a.click 的异步下载支持不可靠），
  // token 经 URL query 传递（仅下载端点支持，短时有效）
  function doExport(kind: 'events' | 'alerts') {
    setExporting(true)
    const params = new URLSearchParams()
    if (kind === 'events') {
      if ((route as any).host) params.set('hostname', (route as any).host)
      if ((route as any).q) params.set('q', (route as any).q)
    }
    const qs = params.toString()
    const token = localStorage.getItem('token') || ''
    const a = document.createElement('a')
    a.href = `${API}/export/${kind}?token=${encodeURIComponent(token)}${qs ? '&' + qs : ''}`
    document.body.appendChild(a)
    a.click()
    a.remove()
    setTimeout(() => setExporting(false), 1500)
  }

  // 移除 Agent：删注册记录 → key 失效 → 在线连接被断（对标 Wazuh remove agent）
  async function doRemoveAgent() {
    if (!removeConfirm) return
    setRemoving(true); setErr(''); setNotice('')
    try {
      await fetchJSON(`${API}/agents/${removeConfirm.agentId}`, { method: 'DELETE' })
      setNotice(`已移除 ${removeConfirm.hostname}`)
      setRemoveConfirm(null)
      setTimeout(() => setNotice(''), 5000)
      load() // 刷新主机列表（该主机从列表消失）
    } catch (e: any) {
      setErr('移除失败: ' + (e.message || e))
    } finally {
      setRemoving(false)
    }
  }

  const host = route.page === 'host-detail' ? hosts.find(h => h.agent_id === route.agentId) : null

  // ── 表格列定义（AG Grid；列宽/排序/显隐状态按 stateKey 持久化）──

  const hostCols = useMemo<ColDef<Host>[]>(() => [
    { headerName: '主机名', field: 'hostname', flex: 1.2, cellStyle: { fontWeight: 600 }, tooltipField: 'hostname' },
    { headerName: '状态', colId: 'status', width: 110, valueGetter: p => (p.data!.revoked ? 2 : p.data!.is_online ? 1 : 0), cellRenderer: statusCell, filter: true, filterValueGetter: p => (p.data!.revoked ? '已吊销' : p.data!.is_online ? '在线' : '离线') },
    { headerName: 'OS', colId: 'os', flex: 1, valueGetter: p => `${p.data!.os_type} ${p.data!.os_version}` },
    { headerName: '架构', field: 'arch', width: 90 },
    { headerName: 'Agent', field: 'agent_version', width: 110 },
    { headerName: '事件数', field: 'event_count', width: 95, valueFormatter: p => Number(p.value).toLocaleString(), filter: 'agNumberColumnFilter' },
    { headerName: 'IP', field: 'ips', width: 190, valueFormatter: p => (p.value || []).join(', ') },
    { headerName: '最后活跃', field: 'last_seen', width: 165, valueFormatter: timeFormatter, filter: 'agDateColumnFilter', filterParams: dateFilterParams },
    { headerName: '', colId: 'action', width: 80, sortable: false, resizable: false, filter: false, floatingFilter: false, pinned: 'right', cellRenderer: (p: ICellRendererParams) => (
      <button
        className="row-menu-btn"
        title="操作"
        onClick={(e) => {
          e.stopPropagation()
          const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
          setRowMenu({ agentId: p.data!.agent_id, hostname: p.data!.hostname, x: r.right - 132, y: r.bottom + 4 })
        }}
      >⋯</button>
    ) },
  ], [])

  const alertCols = useMemo<ColDef<Alert>[]>(() => [
    { headerName: '严重度', field: 'severity', width: 110, cellRenderer: sevCell, comparator: sevComparator, filter: true },
    { headerName: '规则', field: 'rule_name', flex: 1.4, tooltipField: 'rule_name' },
    { headerName: '主机', field: 'hostname', width: 140 },
    { headerName: '类型', field: 'event_type', width: 130 },
    { headerName: '时间', field: '@timestamp', width: 165, valueFormatter: timeFormatter, sort: 'desc', filter: 'agDateColumnFilter', filterParams: dateFilterParams },
  ], [])

  const eventCols = useMemo<ColDef<EventItem>[]>(() => [
    { headerName: '时间', field: '@timestamp', width: 165, valueFormatter: timeFormatter, sort: 'desc', filter: 'agDateColumnFilter', filterParams: dateFilterParams },
    { headerName: '主机', field: 'hostname', width: 140 },
    { headerName: '类型', field: 'event_type', width: 150, valueFormatter: p => p.data!.event_type + (p.data!.event_action ? '/' + p.data!.event_action : ''), filterValueGetter: p => p.data!.event_type + (p.data!.event_action ? '/' + p.data!.event_action : '') },
    { headerName: '摘要', field: 'summary', flex: 1, tooltipField: 'summary', cellClass: 'summary-cell' },
    { headerName: '', colId: 'action', width: 64, sortable: false, resizable: false, filter: false, floatingFilter: false, cellRenderer: linkCell(setEventDetail) },
  ], [])

  const procCols = useMemo<ColDef<any>[]>(() => [
    { headerName: 'PID', field: 'pid', width: 80, filter: 'agNumberColumnFilter' },
    { headerName: '名称', field: 'name', flex: 1, tooltipField: 'name' },
    { headerName: '路径', field: 'exe', flex: 2.4, tooltipField: 'exe', cellClass: 'summary-cell' },
    { headerName: 'CPU%', field: 'cpu', width: 80, valueFormatter: p => (p.value != null ? Number(p.value).toFixed(1) : '-'), filter: 'agNumberColumnFilter' },
    { headerName: '内存', field: 'memory', width: 90, valueFormatter: p => (p.value != null ? (Number(p.value) / 1024).toFixed(0) + 'KB' : '-'), filter: 'agNumberColumnFilter' },
    { headerName: '', colId: 'action', width: 64, sortable: false, resizable: false, filter: false, floatingFilter: false, cellRenderer: linkCell(setProcDetail) },
  ], [])

  const tcpCols = useMemo<ColDef<any>[]>(() => [
    { headerName: 'PID', field: 'pid', width: 80, filter: 'agNumberColumnFilter' },
    { headerName: '本地', field: 'local', flex: 1, tooltipField: 'local' },
    { headerName: '远程', field: 'remote', flex: 1, tooltipField: 'remote' },
    { headerName: '状态', field: 'state', width: 110, filter: true },
  ], [])

  const udpCols = useMemo<ColDef<any>[]>(() => [
    { headerName: 'PID', field: 'pid', width: 80, filter: 'agNumberColumnFilter' },
    { headerName: '本地', field: 'local', flex: 1, tooltipField: 'local' },
    { headerName: '状态', field: 'state', width: 110, filter: true },
  ], [])

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
            ) : route.page === 'agent' ? (
              <><span className="back" onClick={() => navigate('hosts')}>←</span> <span className="logo">◆</span> 下载 Agent</>
            ) : (
              <><span className="logo">◆</span> Baize 白泽 <span className="subtitle">Dashboard</span></>
            )}
          </h1>
          {route.page !== 'host-detail' && route.page !== 'agent' && (
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
        {notice && <div className="notice">{notice}</div>}
        {loading && route.page !== 'events' && <div className="loading">加载中...</div>}
        {exporting && <div className="loading">⏳ 正在生成导出文件，请稍候…（最多 50,000 条，数据量大时可能需要一些时间）</div>}

        {!loading && route.page === 'hosts' && hosts && (
          <>
            <button
              onClick={() => navigate('agent')}
              style={{ marginBottom: '0.8rem' }}
            >
              📦 下载 Agent
            </button>
            <BaizeGrid
              columnDefs={hostCols}
              rowData={hosts}
              stateKey="baize.grid.hosts"
              onRowClicked={e => {
                // 点行尾操作按钮（⋯）不跳详情页：React 合成事件 stopPropagation 拦不住
                // AG Grid 的原生行点击监听（其监听器比 React 事件委托更靠内层），这里显式排除
                const t = e.event && (e.event.target as HTMLElement)
                if (t && t.closest('.row-menu-btn')) return
                navigate('hosts/' + e.data!.agent_id)
              }}
              rowClassRules={{ 'row-revoked': p => !!p.data?.revoked }}
              emptyText="暂无在线主机"
            />
          </>
        )}

        {!loading && route.page === 'config' && <ConfigPage />}

        {route.page === 'agent' && <AgentDownload />}

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
              <div style={{display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:'0.5rem', flexWrap:'wrap', gap:'0.5rem'}}>
                <span style={{fontSize:'0.9rem', fontWeight:600}}>
                  系统信息
                  {sysView && (
                    sysView.source === 'state'
                      ? <span style={{fontWeight:'normal', fontSize:'0.75rem', color:'#94a3b8', marginLeft:'0.5rem'}}>采集于 {formatTime(sysView.capturedAt)} · 收到于 {formatTime(sysView.receivedAt)}</span>
                      : <span style={{fontWeight:'normal', fontSize:'0.75rem', color:'#22d3ee', marginLeft:'0.5rem'}}>查询于 {formatTime(sysView.liveAt)}</span>
                  )}
                </span>
                <button className="btn" onClick={refreshSysInfo} disabled={sysRefreshing} style={{fontSize:'0.75rem'}}>{sysRefreshing ? '刷新中...' : '刷新'}</button>
              </div>
              {sysErr && <div className="error" style={{marginBottom:'0.5rem'}}>{sysErr}</div>}
              {sysLoading ? <div className="loading">加载系统状态...</div> : (
              sysView ? (
                <div>
                  <div className="tabs" style={{ marginBottom: '0.6rem' }}>
                    <button className={sysTab === 'procs' ? 'active' : ''} onClick={() => setSysTab('procs')}>进程 ({sysView.procs.length})</button>
                    <button className={sysTab === 'tcp' ? 'active' : ''} onClick={() => setSysTab('tcp')}>TCP 连接 ({sysView.tcp.length})</button>
                    <button className={sysTab === 'udp' ? 'active' : ''} onClick={() => setSysTab('udp')}>UDP 监听 ({sysView.udp.length})</button>
                  </div>
                  {sysTab === 'procs' && (
                    <BaizeGrid columnDefs={procCols} rowData={sysView.procs} stateKey="baize.grid.procs" height={400} emptyText="无进程数据" />
                  )}
                  {sysTab === 'tcp' && (
                    <BaizeGrid columnDefs={tcpCols} rowData={sysView.tcp} stateKey="baize.grid.tcp" height={400} emptyText="无 TCP 连接" />
                  )}
                  {sysTab === 'udp' && (
                    <BaizeGrid columnDefs={udpCols} rowData={sysView.udp} stateKey="baize.grid.udp" height={400} emptyText="无 UDP 监听" />
                  )}
                </div>
              ) : (
                <div className="empty">该主机暂无系统状态（Agent 未上线或版本过旧），可点击"刷新"获取当前状态</div>
              )
              )}
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
            <BaizeGrid
              columnDefs={alertCols}
              rowData={alerts}
              stateKey="baize.grid.alerts"
              height={600}
              onRowClicked={e => showAlertDetail(e.data!.alert_id)}
              emptyText="暂无告警"
            />
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
              <BaizeGrid
                columnDefs={eventCols}
                rowData={events}
                stateKey="baize.grid.events"
                height={600}
                emptyText="暂无事件"
              />
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
      {/* 主机行操作菜单（⋯ → 查看详情 / 移除 Agent） */}
      {rowMenu && (
        <div className="row-menu" style={{ left: rowMenu.x, top: rowMenu.y }} onClick={e => e.stopPropagation()}>
          <div className="row-menu-item" onClick={() => { setRowMenu(null); navigate('hosts/' + rowMenu.agentId) }}>查看详情</div>
          <div className="row-menu-item danger" onClick={() => { setRemoveConfirm({ agentId: rowMenu.agentId, hostname: rowMenu.hostname }); setRowMenu(null) }}>移除 Agent</div>
        </div>
      )}
      {/* 移除 Agent 确认弹窗 */}
      {removeConfirm && (
        <div className="overlay" onClick={() => !removing && setRemoveConfirm(null)}>
          <div className="modal" onClick={e => e.stopPropagation()} style={{ maxWidth: '440px' }}>
            <div className="modal-header">
              <strong>移除 Agent</strong>
              <button className="close" onClick={() => !removing && setRemoveConfirm(null)}>×</button>
            </div>
            <div className="modal-body">
              <div className="field"><label>主机</label><span>{removeConfirm.hostname}</span></div>
              <div className="field"><label>Agent ID</label><span className="mono">{removeConfirm.agentId}</span></div>
              <div className="field"><label>后果</label><span>移除后该 Agent 将无法连接 Server，需重新安装才能接入。此操作不可恢复。</span></div>
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem', padding: '0 1rem 1rem' }}>
              <button className="btn" onClick={() => setRemoveConfirm(null)} disabled={removing}>取消</button>
              <button className="btn btn-danger" onClick={doRemoveAgent} disabled={removing}>{removing ? '移除中...' : '确认移除'}</button>
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
                    ['CPU', procDetail.cpu != null ? procDetail.cpu.toFixed(1) + '%' : '-'],
                    ['内存', procDetail.memory != null ? (procDetail.memory / 1024).toFixed(0) + ' KB' : '-']].map(([k,v]) => (
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
