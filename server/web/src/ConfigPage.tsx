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

export default function ConfigPage() {
  const [configs, setConfigs] = useState<Configs | null>(null)
  const [hosts, setHosts] = useState<Host[]>([])
  const [dirs, setDirs] = useState('')
  const [host, setHost] = useState('')
  const [msg, setMsg] = useState('')

  useEffect(() => {
    Promise.all([
      fetch(`${API}/config/file-watch`).then(r => r.json()),
      fetch(`${API}/hosts`).then(r => r.json()),
    ]).then(([cfg, hd]) => {
      setConfigs(cfg)
      setHosts(hd.hosts || [])
      setDirs(cfg._global?.join(', ') || '')
    }).catch(() => setMsg('加载失败'))
  }, [])

  // 选中主机时加载该主机的配置
  const selectHost = (aid: string) => {
    setHost(aid)
    if (configs && configs[aid]) {
      setDirs(configs[aid].join(', '))
    } else if (!aid) {
      setDirs(configs?._global?.join(', ') || '')
    }
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
        // 刷新配置列表
        fetch(`${API}/config/file-watch`).then(r => r.json()).then(setConfigs)
      } else {
        setMsg('保存失败')
      }
    } catch { setMsg('保存失败') }
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
    </div>
  )
}
