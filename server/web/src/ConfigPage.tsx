import { useState, useEffect } from 'react'

const API = '/api'

interface Host {
  agent_id: string
  hostname: string
}

interface Configs {
  _global: string[]
  [key: string]: string[]
}

const EVENT_TYPES = ['process', 'file', 'network', 'dns', 'registry', 'task', 'yara'] as const

const EVENT_LABELS: Record<string, string> = {
  process: '进程创建/终止',
  file: '文件创建/修改/删除',
  network: '网络连接',
  dns: 'DNS 查询',
  registry: '注册表变更',
  task: '计划任务',
  yara: 'YARA 匹配',
}

export default function ConfigPage() {
  const [configs, setConfigs] = useState<Configs | null>(null)
  const [hosts, setHosts] = useState<Host[]>([])
  const [dirs, setDirs] = useState('')
  const [host, setHost] = useState('')
  const [msg, setMsg] = useState('')

  // 事件类型开关
  const [eventTypes, setEventTypes] = useState<Record<string, boolean> | null>(null)
  const [etHost, setEtHost] = useState('') // ''=全局
  const [etMsg, setEtMsg] = useState('')

  // 加载配置
  useEffect(() => {
    Promise.all([
      fetch(`${API}/config/file-watch`).then(r => r.json()),
      fetch(`${API}/hosts`).then(r => r.json()),
      fetch(`${API}/config/event-types`).then(r => r.json()),
    ]).then(([cfg, hd, et]) => {
      setConfigs(cfg)
      setHosts(hd.hosts || [])
      // 初始化事件类型状态
      const globalET = et._global || {}
      const init: Record<string, boolean> = {}
      for (const t of EVENT_TYPES) {
        init[t] = globalET[t] ?? false
      }
      setEventTypes(init)
    }).catch(() => setMsg('加载失败'))
  }, [])

  // 选择主机时加载该主机的文件监控配置
  const selectHost = (aid: string) => {
    setHost(aid)
    if (configs && configs[aid]) {
      setDirs(configs[aid].join(', '))
    } else if (!aid) {
      setDirs(configs?._global?.join(', ') || '')
    }
  }

  // 选择主机时加载该主机的事件类型配置
  const selectEtHost = (aid: string) => {
    setEtHost(aid)
    fetch(`${API}/config/event-types`).then(r => r.json()).then(et => {
      const cfg = aid ? (et.per_agent?.[aid]) : et._global
      const init: Record<string, boolean> = {}
      for (const t of EVENT_TYPES) {
        init[t] = cfg?.[t] ?? false
      }
      setEventTypes(init)
    }).catch(() => {})
  }

  const save = async () => {
    const dirList = dirs.split(',').map(s => s.trim()).filter(Boolean)
    try {
      const r = await fetch(`${API}/config/file-watch`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ agent_id: host || '', dirs: dirList }),
      })
      const d = await r.json()
      if (d.status === 'ok') {
        setMsg('已保存，已推送到在线主机')
        fetch(`${API}/config/file-watch`).then(r => r.json()).then(setConfigs)
      } else {
        setMsg('保存失败')
      }
    } catch { setMsg('保存失败') }
  }

  const saveEventTypes = async () => {
    if (!eventTypes) return
    try {
      const r = await fetch(`${API}/config/event-types`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ agent_id: etHost || '', categories: eventTypes }),
      })
      const d = await r.json()
      if (d.status === 'ok') {
        setEtMsg('已保存，已推送到在线主机')
      } else {
        setEtMsg('保存失败')
      }
    } catch { setEtMsg('保存失败') }
  }

  const toggleType = (key: string) => {
    if (!eventTypes) return
    setEventTypes({ ...eventTypes, [key]: !eventTypes[key] })
  }

  return (
    <div className="config-page">
      <h2>⚙ 文件监控配置</h2>

      <div className="form-group">
        <label>监控目录（逗号分隔）</label>
        <input value={dirs} onChange={e => setDirs(e.target.value)} placeholder="C:\Temp,D:\Downloads" />
      </div>

      <div className="form-group">
        <label>目标主机（留空 = 全局配置）</label>
        <select value={host} onChange={e => selectHost(e.target.value)}>
          <option value="">— 全局配置 —</option>
          {hosts.map(h => (
            <option key={h.agent_id} value={h.agent_id}>{h.hostname} ({h.agent_id.slice(0, 8)}...)</option>
          ))}
        </select>
      </div>

      <button onClick={save}>保存</button>
      {msg && <span className="msg">{msg}</span>}

      {configs && (
        <div className="config-list">
          <h3>当前配置</h3>
          <table className="table">
            <thead><tr><th>目标</th><th>目录</th></tr></thead>
            <tbody>
              <tr><td className="mono"><strong>全局</strong></td><td className="mono">{(configs._global || []).join(', ') || '-'}</td></tr>
              {Object.entries(configs).filter(([k]) => k !== '_global').map(([agent, dirList]) => (
                <tr key={agent}><td className="mono">{agent.slice(0, 19)}...</td><td className="mono">{dirList.join(', ')}</td></tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="event-types">
        <h3>◆ 事件类型开关</h3>

        <div className="form-group">
          <label>目标主机（留空 = 全局配置）</label>
          <select value={etHost} onChange={e => selectEtHost(e.target.value)}>
            <option value="">— 全局配置 —</option>
            {hosts.map(h => (
              <option key={h.agent_id} value={h.agent_id}>{h.hostname} ({h.agent_id.slice(0, 8)}...)</option>
            ))}
          </select>
        </div>

        <div className="toggle-grid">
          {EVENT_TYPES.map(t => (
            <div key={t} className="toggle-row">
              <label className="toggle-switch">
                <input type="checkbox" checked={eventTypes?.[t] ?? false} onChange={() => toggleType(t)} />
                <span className="toggle-slider"></span>
              </label>
              <span className="label">{EVENT_LABELS[t]}</span>
              <span className="desc">{t}</span>
            </div>
          ))}
        </div>

        <div style={{ marginTop: '1rem' }}>
          <button onClick={saveEventTypes}>保存</button>
          {etMsg && <span className="msg">{etMsg}</span>}
        </div>
      </div>
    </div>
  )
}
