import { useState, useEffect } from 'react'
import { API, fetchJSON } from './api'

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
  tls_enabled?: boolean
}

interface TokenRec {
  id: string
  name: string
  created_at: string
  revoked?: boolean
  used_count?: number
}

export default function AgentDownload() {
  const [agentInfo, setAgentInfo] = useState<AgentInfo | null>(null)
  const [tokens, setTokens] = useState<TokenRec[]>([])
  const [selectedToken, setSelectedToken] = useState('')
  const [tokenName, setTokenName] = useState('')
  const [newToken, setNewToken] = useState<{ token: string; id: string } | null>(null)
  const [creating, setCreating] = useState(false)
  const [agentMsg, setAgentMsg] = useState('')

  useEffect(() => {
    fetchJSON<AgentInfo>(`${API}/agent/info`).then(setAgentInfo).catch(() => setAgentMsg('加载 Agent 信息失败'))
    loadTokens()
  }, [])

  const loadTokens = async () => {
    try {
      const d = await fetchJSON<{ tokens: TokenRec[] }>(`${API}/enrollment-tokens`)
      const list = d.tokens || []
      setTokens(list)
      // 默认选中第一个未吊销 token
      if (!list.some(t => t.id === selectedToken)) {
        const active = list.find(t => !t.revoked)
        setSelectedToken(active ? active.id : '')
      }
    } catch (e: any) {
      setAgentMsg('加载 token 失败: ' + e.message)
    }
  }

  const createToken = async () => {
    setCreating(true)
    setNewToken(null)
    try {
      const d = await fetchJSON<{ token: string; id: string }>(`${API}/enrollment-tokens`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: tokenName.trim() || '默认' }),
      })
      setNewToken(d)
      setTokenName('')
      loadTokens()
    } catch (e: any) {
      setAgentMsg('创建 token 失败: ' + e.message)
    }
    setCreating(false)
  }

  const revokeToken = async (id: string) => {
    if (!window.confirm('吊销后该 token 不能再注册新 Agent（已注册的不受影响）。确认吊销？')) return
    try {
      await fetchJSON(`${API}/enrollment-tokens/${id}`, { method: 'DELETE' })
      loadTokens()
    } catch (e: any) {
      setAgentMsg('吊销失败: ' + e.message)
    }
  }

  const copyText = (text: string, label: string) => {
    navigator.clipboard?.writeText(text).then(
      () => setAgentMsg(`${label}已复制到剪贴板`),
      () => setAgentMsg('复制失败，请手动选择复制')
    )
  }

  // 安装命令：目标机（Windows）以管理员身份执行。
  // token 明文只在创建时返回一次：刚创建时自动嵌入，否则用占位符提示手动填写。
  const tokenPlain = newToken ? newToken.token : '<注册token>'
  const buildInstallCmd = () => {
    const origin = window.location.origin
    const addr = agentInfo?.public_addr || '<Server地址>'
    return [
      `curl.exe -k -o baize.zip "${origin}/api/agent/package"`,
      'Expand-Archive baize.zip -Force',
      `.\\baize\\install.bat ${addr} ${tokenPlain}`,
    ].join('\n')
  }

  // token 明文只在创建时返回一次（列表里只有 id/hash，不展示明文）

  return (
    <div className="agent-download">
      <h2>📦 下载 Agent 安装包</h2>

      <div className="form-group">
        <label>Server 地址（Agent 将连接）</label>
        <input readOnly value={agentInfo?.public_addr || '（未配置 --public-addr）'} />
        {agentInfo?.tls_enabled && (
          <div style={{ fontSize: '0.75rem', color: '#22c55e', marginTop: '0.25rem' }}>
            🔒 TLS 已启用（安装包内置 ca.crt，Agent 使用 https:// 加密连接）
          </div>
        )}
      </div>

      {/* ── 注册 token 管理 ── */}
      <h3>🔑 注册 token</h3>
      <div style={{ fontSize: '0.75rem', opacity: 0.7, marginBottom: '0.5rem' }}>
        Agent 首次启动凭 token 注册换取身份密钥（对标 Wazuh authd）。token 可重复使用，吊销后不能注册新 Agent。
      </div>
      <div style={{ display: 'flex', gap: '0.4rem', marginBottom: '0.5rem' }}>
        <input
          placeholder="token 名称（如：测试机 / 生产-财务部）"
          value={tokenName}
          onChange={e => setTokenName(e.target.value)}
          style={{ flex: 1 }}
        />
        <button onClick={createToken} disabled={creating}>{creating ? '创建中...' : '生成 token'}</button>
      </div>
      {newToken && (
        <div className="form-group" style={{ border: '1px solid #fbbf24', padding: '0.6rem', borderRadius: '6px', marginBottom: '0.5rem' }}>
          <label>新 token（只显示一次，请立即复制）</label>
          <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
            <code className="mono" style={{ flex: 1, wordBreak: 'break-all', fontSize: '0.8rem' }}>{newToken.token}</code>
            <button onClick={() => copyText(newToken.token, 'token ')}>复制</button>
          </div>
        </div>
      )}
      {tokens.length > 0 && (
        <table className="table" style={{ fontSize: '0.75rem', marginBottom: '0.8rem' }}>
          <thead><tr><th>名称</th><th>创建时间</th><th>已注册</th><th>状态</th><th></th></tr></thead>
          <tbody>
            {tokens.map(t => (
              <tr key={t.id} style={{ cursor: 'pointer' }} onClick={() => setSelectedToken(t.id)}>
                <td>{t.name} {selectedToken === t.id && '✓'}</td>
                <td>{t.created_at?.slice(0, 16).replace('T', ' ')}</td>
                <td>{t.used_count ?? 0}</td>
                <td>{t.revoked ? <span style={{ color: '#fb7185' }}>已吊销</span> : <span style={{ color: '#22c55e' }}>有效</span>}</td>
                <td>
                  {!t.revoked && (
                    <span className="link" onClick={e => { e.stopPropagation(); revokeToken(t.id) }}>吊销</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {/* ── 安装命令 ── */}
      <h3>🚀 一条命令安装（目标终端执行）</h3>
      <div style={{ fontSize: '0.75rem', opacity: 0.7, marginBottom: '0.4rem' }}>
        {newToken
          ? <span style={{ color: '#fbbf24' }}>已自动嵌入新创建的 token（只显示一次，请尽快复制命令）</span>
          : <>命令里的 <code style={{ background: '#1e293b', padding: '0 4px' }}>&lt;注册token&gt;</code> 请用上方创建 token 后复制的明文替换</>}
      </div>
      <pre className="mono" style={{ background: '#0f172a', padding: '0.6rem', borderRadius: '6px', fontSize: '0.72rem', lineHeight: 1.7, overflowX: 'auto', whiteSpace: 'pre-wrap' }}>
{`curl.exe -k -o baize.zip "${window.location.origin}/api/agent/package"
Expand-Archive baize.zip -Force
.\\baize\\install.bat ${agentInfo?.public_addr || '<Server地址>'} ${tokenPlain}`}
      </pre>
      <div style={{ display: 'flex', gap: '0.5rem', marginTop: '0.4rem', flexWrap: 'wrap' }}>
        <button onClick={() => copyText(buildInstallCmd(), '安装命令')}>复制命令</button>
        <span style={{ fontSize: '0.72rem', opacity: 0.6, alignSelf: 'center' }}>（curl -k 忽略自签证书校验；包内 SHA256SUMS.txt 可校验完整性）</span>
      </div>

      {/* ── 二进制与校验 ── */}
      <div className="form-group" style={{ marginTop: '0.8rem' }}>
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

      {agentMsg && <div className="msg" style={{ marginTop: '0.5rem' }}>{agentMsg}</div>}
    </div>
  )
}
