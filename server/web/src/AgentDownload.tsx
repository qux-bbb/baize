import { useState, useEffect } from 'react'
import { API, fetchJSON, clearAuth } from './api'

interface AgentInfo {
  public_addr: string
  agent_binary: string
  binary_exists: boolean
  binary_size: number
  binary_sha256: string
  installer_path: string
  installer_exists: boolean
  installer_size: number
  installer_sha256: string
}

export default function AgentDownload() {
  const [agentInfo, setAgentInfo] = useState<AgentInfo | null>(null)
  const [downloading, setDownloading] = useState(false)
  const [agentMsg, setAgentMsg] = useState('')

  useEffect(() => {
    fetchJSON<AgentInfo>(`${API}/agent/info`).then(setAgentInfo).catch(() => setAgentMsg('加载 Agent 信息失败'))
  }, [])

  const downloadAgent = async () => {
    setDownloading(true)
    setAgentMsg('')
    try {
      const r = await fetch(`${API}/agent/package`, {
        headers: { 'Authorization': `Bearer ${localStorage.getItem('token') || ''}` }
      })
      if (r.status === 401) {
        clearAuth()
        window.dispatchEvent(new CustomEvent('baize-auth', { detail: 'unauthorized' }))
        throw new Error('登录已过期，请重新登录')
      }
      if (!r.ok) {
        const d = await r.json().catch(() => ({}))
        throw new Error((d as any).error || `下载失败 (${r.status})`)
      }
      const blob = await r.blob()
      const cd = r.headers.get('Content-Disposition') || ''
      const m = cd.match(/filename="?([^";]+)"?/)
      const filename = m ? m[1] : `baize-agent_${Date.now()}.zip`
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
      setAgentMsg('安装包已下载，请拷贝到目标终端解压后以管理员身份运行 install.bat')
    } catch (e: any) {
      setAgentMsg(e.message)
    } finally {
      setDownloading(false)
    }
  }

  const downloadInstaller = async () => {
    setDownloading(true)
    setAgentMsg('')
    try {
      const r = await fetch(`${API}/agent/installer`, {
        headers: { 'Authorization': `Bearer ${localStorage.getItem('token') || ''}` }
      })
      if (r.status === 401) {
        clearAuth()
        window.dispatchEvent(new CustomEvent('baize-auth', { detail: 'unauthorized' }))
        throw new Error('登录已过期，请重新登录')
      }
      if (!r.ok) {
        const d = await r.json().catch(() => ({}))
        throw new Error((d as any).error || `下载失败 (${r.status})`)
      }
      const blob = await r.blob()
      const cd = r.headers.get('Content-Disposition') || ''
      const m = cd.match(/filename="?([^";]+)"?/)
      const filename = m ? m[1] : 'baize-agent.msi'
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
      setAgentMsg('MSI 已下载。批量部署: msiexec /i baize-agent.msi /q SERVER_ADDR="<替换为你的地址>"')
    } catch (e: any) {
      setAgentMsg(e.message)
    } finally {
      setDownloading(false)
    }
  }

  return (
    <div className="agent-download">
      <h2>📦 下载 Agent 安装包</h2>

      <div className="form-group">
        <label>Agent 将连接的 Server 地址</label>
        <input readOnly value={agentInfo?.public_addr || '（未配置 --public-addr，将按访问地址推断）'} />
      </div>

      <div className="form-group">
        <label>Agent 二进制</label>
        <div>
          {agentInfo?.binary_exists ? (
            <span style={{ color: '#22c55e' }}>就绪（{(agentInfo.binary_size / 1024).toFixed(0)} KB）</span>
          ) : (
            <span style={{ color: '#fb7185' }}>未就绪 — Server 需配置 --agent-binary 指向 agent.exe</span>
          )}
        </div>
      </div>

      {agentInfo?.binary_sha256 && (
        <div className="form-group">
          <label>SHA256（安装前可校验）</label>
          <div className="mono" style={{ fontSize: '0.7rem', wordBreak: 'break-all' }}>{agentInfo.binary_sha256}</div>
        </div>
      )}

      <button onClick={downloadAgent} disabled={downloading || !agentInfo?.binary_exists}>
        {downloading ? '⏳ 打包中...' : '⬇ 下载安装包 (.zip)'}
      </button>
      <button onClick={downloadInstaller} disabled={downloading || !agentInfo?.installer_exists} style={{ marginLeft: '0.5rem' }}>
        {downloading ? '⏳ 下载中...' : '⬇ 下载 MSI'}
      </button>
      {agentMsg && <span className="msg">{agentMsg}</span>}

      {agentInfo?.installer_exists && (
        <div className="form-group" style={{ marginTop: '0.8rem' }}>
          <label>MSI 静默安装命令（批量部署，SERVER_ADDR 替换为你的地址）</label>
          <code style={{ fontSize: '0.75rem', wordBreak: 'break-all' }}>msiexec /i baize-agent.msi /q SERVER_ADDR="http://10.0.0.1:50051"</code>
          <div style={{ fontSize: '0.75rem', opacity: 0.6, marginTop: '0.3rem' }}>
            不带 SERVER_ADDR 安装后，可手动编辑 C:\Program Files\Baize\agent.conf 并重启服务
          </div>
        </div>
      )}

      <h3>安装步骤</h3>
      <ol style={{ fontSize: '0.8rem', lineHeight: 1.9, opacity: 0.85, paddingLeft: '1.2rem' }}>
        <li>下载 zip 后拷贝到目标终端（Windows）</li>
        <li>解压，右键 <code>install.bat</code> → 以管理员身份运行</li>
        <li>脚本自动安装服务并启动，稍后可在 Dashboard 主机列表看到该主机</li>
      </ol>
    </div>
  )
}
