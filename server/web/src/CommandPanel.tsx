// 主机远程命令执行 — 通过解释器执行命令/脚本，并展示 Agent 回传的输出
//
// 交互约定（对齐 Defender Live Response / Elastic Response Console / CrowdStrike RTR）：
//   - 单行输入 + Enter 执行（终端语义；中文输入法组合期间"上屏"的 Enter 不会触发执行）；
//   - ↑/↓ 翻命令历史（localStorage 保留最近 20 条）；
//   - 执行按钮上标明目标主机，避免误点；
//   - 可填"理由"，随指令下发并写入服务端日志（理由留痕取代二次确认）；
//   - stdout + stderr 合并回显，命令失败时同样展示报错输出。
import { useEffect, useState } from 'react'
import { ExecResult, Interpreter, execCommand } from './api'

const INTERPRETERS: { value: Interpreter; label: string }[] = [
  { value: 'powershell', label: 'PowerShell (Windows)' },
  { value: 'cmd', label: 'cmd (Windows)' },
  { value: 'bash', label: 'bash (Linux/macOS)' },
  { value: 'python', label: 'python' },
]

const TIMEOUTS = [10, 30, 60, 300]

/** 命令历史：localStorage 键 + 上限 */
const HIST_KEY = 'baize.cmd.history'
const HIST_MAX = 20

interface HistItem {
  script: string
  reason: string
  at: number
}

function loadHistory(): HistItem[] {
  try {
    const raw = localStorage.getItem(HIST_KEY)
    if (!raw) return []
    const arr = JSON.parse(raw)
    if (!Array.isArray(arr)) return []
    return arr
      .filter((x: any) => x && typeof x.script === 'string' && x.script.trim() !== '')
      .slice(0, HIST_MAX)
  } catch {
    return []
  }
}

/** 按主机 OS 选默认解释器（Agent 上报的 os_type 为 windows / linux / macos） */
function defaultInterpreter(osType?: string): Interpreter {
  return (osType || '').toLowerCase().includes('win') ? 'powershell' : 'bash'
}

const selectStyle: React.CSSProperties = {
  background: 'var(--card)',
  border: '1px solid var(--border)',
  color: 'var(--text)',
  padding: '0.4rem 0.6rem',
  borderRadius: '6px',
  fontFamily: 'var(--font)',
  fontSize: '0.8rem',
}

const outputStyle: React.CSSProperties = {
  margin: 0,
  padding: '0.6rem 0.7rem',
  maxHeight: '360px',
  overflow: 'auto',
  fontFamily: 'monospace',
  fontSize: '0.8rem',
  lineHeight: 1.45,
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
  color: '#e2e8f0',
}

export default function CommandPanel({
  agentId,
  hostname,
  osType,
}: {
  agentId: string
  hostname: string
  osType?: string
}) {
  const [interpreter, setInterpreter] = useState<Interpreter>(defaultInterpreter(osType))
  const [timeoutSecs, setTimeoutSecs] = useState(30)
  const [script, setScript] = useState('')
  const [reason, setReason] = useState('')
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<ExecResult | null>(null)
  const [err, setErr] = useState('')

  const [history, setHistory] = useState<HistItem[]>(loadHistory)
  /** 当前浏览到历史的第几条；-1 = 不在历史浏览中 */
  const [histIdx, setHistIdx] = useState(-1)
  /** 进入历史浏览前用户已输入的草稿，↓ 翻回底部时恢复 */
  const [draft, setDraft] = useState('')

  // 切换主机时清空状态（否则会残留上一台主机的命令与执行结果）
  useEffect(() => {
    setInterpreter(defaultInterpreter(osType))
    setScript('')
    setReason('')
    setResult(null)
    setErr('')
    setHistIdx(-1)
    setDraft('')
  }, [agentId, osType])

  const canRun = !running && script.trim().length > 0

  const pushHistory = (s: string, r: string) => {
    setHistory(prev => {
      // 与最近一条相同则不重复插入，只把它提到最前
      const rest = prev.length > 0 && prev[0].script === s ? prev.slice(1) : prev
      const next = [{ script: s, reason: r, at: Date.now() }, ...rest].slice(0, HIST_MAX)
      try {
        localStorage.setItem(HIST_KEY, JSON.stringify(next))
      } catch {
        /* localStorage 不可用时忽略（历史仅是本地方便功能） */
      }
      return next
    })
  }

  const doRun = async () => {
    if (!canRun) return
    const cmd = script.trim()
    const why = reason.trim()
    setRunning(true)
    setErr('')
    setResult(null)
    pushHistory(cmd, why)
    setHistIdx(-1)
    try {
      const r = await execCommand(agentId, cmd, interpreter, timeoutSecs, why)
      setResult(r)
    } catch (e: any) {
      setErr(`执行失败: ${e.message}`)
    }
    setRunning(false)
  }

  /** ↑/↓ 翻历史：delta=+1 往旧，-1 往新 */
  const navHistory = (delta: number) => {
    if (history.length === 0) return
    const next = histIdx + delta
    if (next < 0) {
      // 回到最新（恢复草稿）
      setHistIdx(-1)
      setScript(draft)
      return
    }
    if (next >= history.length) return
    if (histIdx === -1) setDraft(script)
    setHistIdx(next)
    setScript(history[next].script)
    setReason(history[next].reason)
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      // 中文等输入法组合期间的 Enter 是"上屏/选词"，不能当作执行
      if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return
      e.preventDefault()
      doRun()
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      navHistory(1)
      return
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      navHistory(-1)
    }
  }

  return (
    <div>
      {/* 工具条：解释器 / 超时 / 理由 */}
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', marginBottom: '0.6rem', flexWrap: 'wrap' }}>
        <select
          value={interpreter}
          onChange={e => setInterpreter(e.target.value as Interpreter)}
          disabled={running}
          style={selectStyle}
          title="命令解释器"
        >
          {INTERPRETERS.map(i => (
            <option key={i.value} value={i.value}>
              {i.label}
            </option>
          ))}
        </select>
        <select
          value={timeoutSecs}
          onChange={e => setTimeoutSecs(Number(e.target.value))}
          disabled={running}
          style={selectStyle}
          title="超时时间；超时后 Agent 会终止该命令"
        >
          {TIMEOUTS.map(t => (
            <option key={t} value={t}>
              超时 {t}s
            </option>
          ))}
        </select>
        <input
          className="input"
          style={{ flex: 1, maxWidth: 'none', minWidth: '200px', fontSize: '0.8rem' }}
          value={reason}
          onChange={e => setReason(e.target.value)}
          disabled={running}
          placeholder="理由（可选，会记入服务端日志）"
        />
      </div>

      {/* 命令输入：单行 + Enter 执行（终端语义） */}
      <input
        className="input"
        style={{
          width: '100%',
          maxWidth: 'none',
          fontFamily: 'monospace',
          fontSize: '0.85rem',
          marginBottom: '0.6rem',
        }}
        value={script}
        onChange={e => {
          setScript(e.target.value)
          setHistIdx(-1)
        }}
        onKeyDown={onKeyDown}
        disabled={running}
        spellCheck={false}
        autoComplete="off"
        placeholder={
          interpreter === 'powershell'
            ? '例如: Get-Process | Sort-Object CPU -Descending | Select-Object -First 10'
            : interpreter === 'cmd'
              ? '例如: ipconfig /all'
              : '例如: ps aux --sort=-%cpu | head'
        }
      />

      {/* 执行 */}
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.7rem', marginBottom: '0.8rem', flexWrap: 'wrap' }}>
        <button className="btn" onClick={doRun} disabled={!canRun}>
          {running ? '⏳ 执行中...' : `▶ 在 ${hostname} 上执行`}
        </button>
        <span style={{ fontSize: '0.75rem', color: '#94a3b8' }}>
          Enter 执行 · ↑/↓ 历史{history.length > 0 ? `（${history.length}）` : ''} · Agent 以系统权限运行，命令会真实生效
        </span>
      </div>

      {err && (
        <div className="error" style={{ marginBottom: '0.5rem' }}>
          {err}
        </div>
      )}

      {/* 输出 */}
      {result && (
        <div style={{ border: '1px solid var(--border)', borderRadius: '6px', overflow: 'hidden' }}>
          <div
            style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              gap: '0.5rem',
              padding: '0.4rem 0.7rem',
              background: 'rgba(15,23,42,0.6)',
              borderBottom: '1px solid var(--border)',
              fontSize: '0.78rem',
              flexWrap: 'wrap',
            }}
          >
            <span style={{ color: result.success ? '#22c55e' : '#fb7185', fontWeight: 600 }}>
              {result.success ? '✓ 执行成功' : `✗ 执行失败${result.error_message ? ` · ${result.error_message}` : ''}`}
            </span>
            <span style={{ color: '#94a3b8' }}>耗时 {(result.elapsed_ms / 1000).toFixed(2)}s</span>
          </div>
          <pre style={outputStyle}>
            {result.output || (result.success ? '（命令执行成功，无输出）' : '（无输出；若 Agent 版本较旧，可能未回传输出）')}
          </pre>
        </div>
      )}
    </div>
  )
}
