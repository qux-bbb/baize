import { useState, useEffect, type MouseEvent } from 'react'
import { API, fetchJSON } from './api'

interface AgentInfo {
  public_addr: string
  agent_binary: string
  binary_exists: boolean
  binary_size: number
  binary_sha256: string
  tls_enabled?: boolean
}

interface TokenRec {
  id: string
  name: string
  created_at: string
  revoked?: boolean
  used_count?: number
  masked?: string // 部分掩码（前 12 + 后 4），列表展示用
}

// 本地掩码与后端 registry.go maskToken 逐位一致：前 12 + 中间 24 点 + 后 4 = 40 字符
const MASK_DOTS = '••••••••••••••••••••••••••'
const maskPlain = (p: string): string =>
  p && p.length >= 20 ? p.slice(0, 12) + MASK_DOTS + p.slice(-4) : ''

// Server 地址三选一：Server 上报值 / 按当前访问地址推断 / 自定义
type AddrMode = 'server' | 'inferred' | 'custom'

export default function AgentDownload() {
  const [agentInfo, setAgentInfo] = useState<AgentInfo | null>(null)
  const [infoErr, setInfoErr] = useState('')
  const [tokens, setTokens] = useState<TokenRec[]>([])
  const [selectedToken, setSelectedToken] = useState('')
  const [tokenName, setTokenName] = useState('')
  // plainCache: token id → 明文（仅组件内存，用于复制/嵌入命令，不落 localStorage）
  const [plainCache, setPlainCache] = useState<Record<string, string>>({})
  const [plainMissing, setPlainMissing] = useState(false) // 选中 token 无法还原明文（旧版仅存哈希）
  const [plainLoading, setPlainLoading] = useState(false)
  const [creating, setCreating] = useState(false)
  const [tokenMsg, setTokenMsg] = useState('')
  const [cmdMsg, setCmdMsg] = useState('')

  // ── 第 1 步：Server 地址 ──
  const [addrMode, setAddrMode] = useState<AddrMode>('server')
  const [customAddr, setCustomAddr] = useState('')

  useEffect(() => {
    fetchJSON<AgentInfo>(`${API}/agent/info`)
      .then(info => {
        setAgentInfo(info)
        // 未配置 --public-addr 时，Server 会按请求 Host 推断；这种情况直接切到"按访问地址推断"
        if (!info.public_addr) setAddrMode('inferred')
      })
      .catch(() => setInfoErr('加载 Agent 信息失败'))
    loadTokens()
  }, [])

  const loadTokens = async () => {
    try {
      const d = await fetchJSON<{ tokens: TokenRec[] }>(`${API}/enrollment-tokens`)
      setTokens(d.tokens || [])
    } catch (e: any) {
      setTokenMsg('加载 token 失败: ' + e.message)
    }
  }

  // 命令内容变了就清掉上一次的复制反馈，避免"已复制/复制失败"指向的其实是旧命令
  useEffect(() => { setCmdMsg('') }, [selectedToken, addrMode, customAddr])

  // 取 token 明文（缓存；失败=旧版仅存哈希）
  const fetchPlain = async (id: string): Promise<string | null> => {
    if (plainCache[id]) return plainCache[id]
    try {
      const d = await fetchJSON<{ token: string }>(`${API}/enrollment-tokens/${id}`)
      setPlainCache(prev => ({ ...prev, [id]: d.token }))
      return d.token
    } catch {
      return null
    }
  }

  const createToken = async () => {
    const name = tokenName.trim() || '默认'
    setCreating(true)
    try {
      const d = await fetchJSON<{ token: string; id: string }>(`${API}/enrollment-tokens`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name }),
      })
      // 缓存明文并自动设为当前使用（安装命令嵌入）
      setPlainCache(prev => ({ ...prev, [d.id]: d.token }))
      setSelectedToken(d.id)
      setPlainMissing(false)
      setTokenName('')
      setTokenMsg(`已生成 token「${name}」并设为当前使用`)
      loadTokens()
    } catch (e: any) {
      setTokenMsg('创建 token 失败: ' + e.message)
    }
    setCreating(false)
  }

  const revokeToken = async (id: string) => {
    if (!window.confirm('吊销后该 token 不能再注册新 Agent（已注册的不受影响）。确认吊销？')) return
    try {
      await fetchJSON(`${API}/enrollment-tokens/${id}`, { method: 'DELETE' })
      // 明文从内存缓存里清掉：已吊销的 token 不应还能生成安装命令
      setPlainCache(prev => {
        const next = { ...prev }
        delete next[id]
        return next
      })
      if (id === selectedToken) setPlainMissing(false) // 置灰由 revoked 判定，不再借用 plainMissing
      setTokenMsg('已吊销该 token')
      loadTokens()
    } catch (e: any) {
      setTokenMsg('吊销失败: ' + e.message)
    }
  }

  const copyText = (text: string, label: string, setMsg: (s: string) => void) => {
    navigator.clipboard?.writeText(text).then(
      () => setMsg(`${label}已复制到剪贴板`),
      () => setMsg('复制失败，请手动选择复制')
    )
  }

  // 复制完整 token（服务端解密返回，仅内存传递）
  const copyToken = async (id: string, e: MouseEvent) => {
    e.stopPropagation()
    const plain = await fetchPlain(id)
    if (plain) copyText(plain, 'token ', setTokenMsg)
    else setTokenMsg('该 token 为旧版仅存哈希，无法还原明文，可吊销重建')
  }

  // 选中 = 设为当前使用（安装命令嵌入该 token），并预取明文
  const selectToken = async (t: TokenRec) => {
    setSelectedToken(t.id)
    setPlainMissing(false)
    setTokenMsg('')
    if (t.revoked) return // 已吊销：不预取明文，第 3 步会因 revoked 置灰
    if (plainCache[t.id]) return
    setPlainLoading(true)
    const plain = await fetchPlain(t.id)
    setPlainLoading(false)
    if (!plain) {
      setPlainMissing(true)
      setTokenMsg('该 token 为旧版仅存哈希，无法还原明文，可吊销重建')
    }
  }

  // ── 第 1 步状态 ──
  const scheme = agentInfo?.tls_enabled ? 'https://' : 'http://'
  const inferredAddr = `${scheme}${window.location.hostname}:50051`
  const serverAddr =
    addrMode === 'server' ? (agentInfo?.public_addr || '')
      : addrMode === 'inferred' ? inferredAddr
        : customAddr.trim()
  const step1Done = !!serverAddr
  const addrMismatch = !!serverAddr && !!agentInfo?.public_addr && serverAddr !== agentInfo.public_addr

  // ── 第 2 步状态 ──
  const selectedRec = tokens.find(t => t.id === selectedToken)
  const tokenPlain = plainCache[selectedToken] || '' // 明文（复制命令用）
  const tokenMasked = tokenPlain ? maskPlain(tokenPlain) : (selectedRec?.masked || '') // 掩码（展示用）
  const tokenDisplay = tokenMasked || '' // 无可用掩码时留空，绝不显示占位符
  const step2Done = !!selectedRec && !selectedRec.revoked && !plainMissing && !!tokenPlain

  // ── 第 3 步状态 ──
  const step3Locked = !step1Done || !step2Done
  const lockReason = !step1Done ? '需先完成第 1 步' : '需先完成第 2 步'

  const stepClass = (n: 1 | 2 | 3) => {
    if (n === 1) return step1Done ? 'done' : 'act'
    if (n === 2) return !step1Done ? 'locked' : step2Done ? 'done' : 'act'
    return step3Locked ? 'locked' : 'act'
  }
  const stepMark = (n: 1 | 2 | 3) => {
    const c = stepClass(n)
    if (c === 'done') return '✓'
    if (c === 'locked') return '🔒'
    return String(n)
  }

  // 安装命令：目标机（Windows）以 PowerShell 执行，一条命令完成 下载→解压→安装。
  // 展示用掩码 token，复制命令时才带完整明文（避免屏幕上/截图泄露明文）。
  // ⚠ 单一来源：展示与复制都走这个函数，不再各写一份命令文本。
  const buildInstallCmd = (token: string) =>
    `curl.exe -k -o baize.zip "${window.location.origin}/api/agent/package"; Expand-Archive baize.zip -Force; .\\baize\\install.bat ${serverAddr} ${token}`

  return (
    <div className="agent-download">
      <h2>安装 Agent</h2>
      <div className="page-lead">按 <b>1 → 2 → 3</b> 依次完成；上一步没配好，下一步会置灰不可用。</div>

      {/* ── 步骤条 ── */}
      <div className="steps">
        <div className={`s ${stepClass(1)}`}><span className="n">{stepMark(1)}</span><span className="t">Server 地址</span></div>
        <div className="line" />
        <div className={`s ${stepClass(2)}`}><span className="n">{stepMark(2)}</span><span className="t">选择凭据</span></div>
        <div className="line" />
        <div className={`s ${stepClass(3)}`}><span className="n">{stepMark(3)}</span><span className="t">复制命令</span></div>
      </div>

      {/* ── 第 1 步：Server 地址 ── */}
      <div className="card">
        <div className="step-head">
          <span className="num">1</span>
          <span className="t">Server 地址</span>
          <span className="s">Agent 将要连接的地址</span>
          <span className="sp" />
          {agentInfo?.tls_enabled && <span className="ok-note">🔒 TLS 已启用</span>}
        </div>
        <select className="inp" style={{ width: '100%' }} value={addrMode}
          onChange={e => setAddrMode(e.target.value as AddrMode)}>
          <option value="server">
            {(agentInfo?.public_addr || '（Server 未上报地址）')}　（Server 上报的 --public-addr，推荐）
          </option>
          <option value="inferred">{inferredAddr}　（按当前访问地址推断）</option>
          <option value="custom">自定义…</option>
        </select>
        {addrMode === 'custom' && (
          <input className="inp" style={{ width: '100%', marginTop: '0.4rem' }}
            placeholder="https://<域名或 IP>:50051"
            value={customAddr}
            onChange={e => setCustomAddr(e.target.value)} />
        )}
        {addrMismatch && (
          <div className="warn-box">
            ⚠ 该地址与 Server 上报的 --public-addr 不一致；TLS 已启用时会导致 Agent 证书校验失败。
            改动只影响下方命令文本，不改 Server 配置。
          </div>
        )}
        {infoErr && <div className="warn-box">⚠ {infoErr}</div>}
      </div>

      {/* ── 第 2 步：选择凭据 ── */}
      <div className={`card${step1Done ? '' : ' locked'}`}>
        <div className="step-head">
          <span className="num">2</span>
          <span className="t">选择凭据</span>
          <span className="s">点一行即选用</span>
          <span className="sp" />
          {!step1Done && <span className="lock-note">🔒 {lockReason}</span>}
        </div>
        <div className="token-new">
          <input className="inp" style={{ flex: 1 }}
            placeholder="token 名称（如：测试机 / 生产-财务部）"
            value={tokenName}
            onChange={e => setTokenName(e.target.value)}
            disabled={!step1Done} />
          <button onClick={createToken} disabled={creating || !step1Done}>
            {creating ? '创建中...' : '生成 token'}
          </button>
        </div>
        <div style={{ fontSize: '0.72rem', color: 'var(--muted)', marginBottom: '0.5rem' }}>
          Agent 首次启动凭 token 注册换取身份密钥。token 可重复使用，吊销后不能注册新 Agent。
        </div>
        {tokens.length > 0 ? (
          <table className="table">
            <thead><tr><th>名称</th><th>凭据</th><th>操作</th></tr></thead>
            <tbody>
              {tokens.map(t => (
                <tr key={t.id} className={selectedToken === t.id ? 'sel' : ''}
                  onClick={() => step1Done && selectToken(t)}>
                  <td>
                    <div className="name">
                      {t.name}
                      {selectedToken === t.id && <span className="chip">使用中</span>}
                      {t.revoked && <span className="chip rev">已吊销</span>}
                    </div>
                    <div className="meta">
                      {t.created_at?.slice(0, 16).replace('T', ' ')} · 已注册 {t.used_count ?? 0} 台
                    </div>
                  </td>
                  <td>
                    <span className="mask">{t.masked || '—'}</span>
                    <button className="icon-btn" title="复制完整 token"
                      disabled={!t.masked || !step1Done}
                      onClick={e => copyToken(t.id, e)}>📋</button>
                  </td>
                  <td>
                    {!t.revoked && (
                      <span className="link" onClick={e => { e.stopPropagation(); revokeToken(t.id) }}>吊销</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <div className="hint">还没有注册 token，先在上面生成一个</div>
        )}
        {tokenMsg && <div className="msg-box">{tokenMsg}</div>}
      </div>

      {/* ── 第 3 步：安装命令 ── */}
      <div className={`card${step3Locked ? ' locked' : ''}`}>
        <div className="step-head">
          <span className="num">3</span>
          <span className="t">安装命令</span>
          <span className="s">目标机 PowerShell 执行（脚本自动提权）</span>
          <span className="sp" />
          {step3Locked && <span className="lock-note">🔒 {lockReason}</span>}
        </div>
        {!step1Done ? (
          <div className="hint">先在第 1 步选择 Server 地址</div>
        ) : selectedRec?.revoked ? (
          <div className="hint">该 token 已吊销，无法生成安装命令，请另选一个</div>
        ) : plainMissing ? (
          <div className="hint">该 token 为旧版仅存哈希，无法生成安装命令，可吊销重建</div>
        ) : plainLoading ? (
          <div className="hint">正在读取凭据…</div>
        ) : !step2Done ? (
          <div className="hint">先在第 2 步选择一个有效 token</div>
        ) : (
          <>
            <div className="use">
              将使用：<b style={{ color: 'var(--text)' }}>{selectedRec?.name}</b> ·
              地址 <span className="mask">{serverAddr}</span> ·
              token 在命令中显示为掩码，点「复制命令」才是完整明文（请勿手动选中文本复制）
            </div>
            <pre className="cmd-pre">{buildInstallCmd(tokenDisplay)}</pre>
            <div className="cmd-actions">
              <button onClick={() => copyText(buildInstallCmd(tokenPlain), '安装命令', setCmdMsg)}>复制命令</button>
              <span className="note">需要换 token？点上表任意行即可切换；包内 SHA256SUMS.txt 可校验完整性</span>
            </div>
            {cmdMsg && <div className="msg-box">{cmdMsg}</div>}
          </>
        )}
      </div>

      {/* ── 折叠：安装包校验信息（Server 地址与 TLS 已在第 1 步常显，这里不重复） ── */}
      <div className="card">
        <details>
          <summary>安装包校验（agent.exe / SHA256）</summary>
          <div className="kv">
            <div className="k">agent.exe</div>
            <div className="v">
              {agentInfo?.binary_exists
                ? <span className="ok-note">就绪（{(agentInfo.binary_size / 1024).toFixed(0)} KB）</span>
                : <span className="bad-note">未就绪 — Server 需配置 --agent-binary 指向 agent.exe</span>}
            </div>
            <div className="k">SHA256</div>
            <div className="v mask">{agentInfo?.binary_sha256 || '—'}</div>
            <div className="k">校验说明</div>
            <div className="v">即包内 SHA256SUMS.txt 首行（baize-agent.exe），安装前可校验</div>
          </div>
        </details>
      </div>
    </div>
  )
}
