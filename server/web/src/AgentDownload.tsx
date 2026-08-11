import { useState, useEffect, type MouseEvent } from 'react'
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
  masked?: string // 部分掩码（前 6 + 后 4），列表展示用
}

// 本地掩码（与后端 ListTokens 的 masked 一致）：保留前 6 + 后 4 位，中间打点，等长 42 字符
const MASK_DOTS = '••••••••••••••••••••••••••'
const maskPlain = (p: string): string =>
  p && p.length >= 20 ? p.slice(0, 12) + MASK_DOTS + p.slice(-4) : ''

export default function AgentDownload() {
  const [agentInfo, setAgentInfo] = useState<AgentInfo | null>(null)
  const [tokens, setTokens] = useState<TokenRec[]>([])
  const [selectedToken, setSelectedToken] = useState('')
  const [tokenName, setTokenName] = useState('')
  // plainCache: token id → 明文（仅组件内存，用于复制/嵌入命令，不落 localStorage）
  const [plainCache, setPlainCache] = useState<Record<string, string>>({})
  const [plainMissing, setPlainMissing] = useState(false) // 选中 token 无法还原明文（旧版仅存哈希）
  const [creating, setCreating] = useState(false)
  const [agentMsg, setAgentMsg] = useState('')

  useEffect(() => {
    fetchJSON<AgentInfo>(`${API}/agent/info`).then(setAgentInfo).catch(() => setAgentMsg('加载 Agent 信息失败'))
    loadTokens()
  }, [])

  const loadTokens = async () => {
    try {
      const d = await fetchJSON<{ tokens: TokenRec[] }>(`${API}/enrollment-tokens`)
      setTokens(d.tokens || [])
    } catch (e: any) {
      setAgentMsg('加载 token 失败: ' + e.message)
    }
  }

  // 取 token 明文（缓存，失败=旧版仅存哈希）
  const fetchPlain = async (id: string): Promise<string | null> => {
    if (plainCache[id]) return plainCache[id]
    try {
      const d = await fetchJSON<{ token: string }>(`${API}/enrollment-tokens/${id}`)
      setPlainCache(prev => ({ ...prev, [id]: d.token }))
      return d.token
    } catch (e: any) {
      setAgentMsg('获取 token 明文失败: ' + e.message)
      return null
    }
  }

  const createToken = async () => {
    setCreating(true)
    try {
      const d = await fetchJSON<{ token: string; id: string }>(`${API}/enrollment-tokens`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: tokenName.trim() || '默认' }),
      })
      // 缓存明文并自动设为当前使用（安装命令嵌入）
      setPlainCache(prev => ({ ...prev, [d.id]: d.token }))
      setSelectedToken(d.id)
      setPlainMissing(false)
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

  // 复制完整 token（服务端解密返回，仅内存传递）
  const copyToken = async (id: string, e: MouseEvent) => {
    e.stopPropagation()
    const plain = await fetchPlain(id)
    if (plain) {
      copyText(plain, 'token ')
    } else {
      setAgentMsg('该 token 为旧版仅存哈希，无法还原明文，可吊销重建')
    }
  }

  // 选中 = 设为当前使用（安装命令嵌入该 token），并预取明文
  const selectToken = async (t: TokenRec) => {
    setSelectedToken(t.id)
    setPlainMissing(false)
    const plain = await fetchPlain(t.id)
    if (!plain) setPlainMissing(true) // 旧版仅存哈希，无法还原明文
  }

  // 安装命令：目标机（Windows）以管理员身份在 PowerShell 执行，一条命令完成 下载→解压→安装。
  // 展示用掩码 token，复制命令时才带完整明文（避免屏幕上/截图泄露明文）。
  const selectedRec = tokens.find(t => t.id === selectedToken)
  const tokenPlain = plainCache[selectedToken] || '' // 明文（复制命令用）
  const tokenMasked = tokenPlain ? maskPlain(tokenPlain) : (selectedRec?.masked || '') // 掩码（展示用）
  const tokenDisplay = tokenMasked || '' // 无可用掩码时留空，绝不显示占位符
  const buildInstallCmd = () => {
    const origin = window.location.origin
    const addr = agentInfo?.public_addr || '<Server地址>'
    return `curl.exe -k -o baize.zip "${origin}/api/agent/package"; Expand-Archive baize.zip -Force; .\\baize\\install.bat ${addr} ${tokenPlain}`
  }

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
      {tokens.length > 0 && (
        <table className="table" style={{ fontSize: '0.75rem', marginBottom: '0.8rem' }}>
          <thead><tr><th>名称</th><th>Token</th><th>创建时间</th><th>已注册</th><th>状态</th><th></th></tr></thead>
          <tbody>
            {tokens.map(t => (
              <tr key={t.id} style={{ cursor: 'pointer' }} onClick={() => selectToken(t)}>
                <td>{t.name} {selectedToken === t.id && '✓'}</td>
                <td className="mono" style={{ wordBreak: 'break-all' }}>
                  {t.masked || '••••••••••••••••••••'}
                  <button
                    title="复制完整 token"
                    style={{ marginLeft: '0.4rem', background: 'none', border: 'none', cursor: 'pointer', fontSize: '0.85rem' }}
                    onClick={e => copyToken(t.id, e)}
                  >📋</button>
                </td>
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

      {/* ── 安装命令（仅在选中 token 后展示，避免无选中时出现占位符/坏命令） ── */}
      <h3>🚀 一条命令安装（目标终端 PowerShell 执行）</h3>
      {selectedRec ? (
        !tokenPlain && plainMissing ? (
          <div className="empty" style={{ fontSize: '0.75rem', margin: '0.4rem 0' }}>
            该 token 为旧版仅存哈希，无法生成安装命令，可吊销重建
          </div>
        ) : (
          <>
            <pre className="mono" style={{ background: '#0f172a', padding: '0.6rem', borderRadius: '6px', fontSize: '0.72rem', lineHeight: 1.7, overflowX: 'auto', whiteSpace: 'pre-wrap' }}>
{`curl.exe -k -o baize.zip "${window.location.origin}/api/agent/package"; Expand-Archive baize.zip -Force; .\\baize\\install.bat ${agentInfo?.public_addr || '<Server地址>'} ${tokenDisplay}`}
            </pre>
            <div style={{ display: 'flex', gap: '0.5rem', marginTop: '0.4rem', flexWrap: 'wrap' }}>
              <button onClick={() => copyText(buildInstallCmd(), '安装命令')}>复制命令</button>
              <span style={{ fontSize: '0.72rem', opacity: 0.6, alignSelf: 'center' }}>
                命令中的 token 展示为掩码，点「复制命令」获取的才是完整 token（请勿手动选中文本复制）。curl -k 忽略自签证书校验；包内 SHA256SUMS.txt 可校验完整性
              </span>
            </div>
          </>
        )
      ) : (
        <div className="empty" style={{ fontSize: '0.75rem', margin: '0.4rem 0' }}>
          ⬆ 请先在上方选择一个 token，将自动生成安装命令
        </div>
      )}

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
