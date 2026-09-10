// 主机文件管理 — 目录浏览 / 删除 / 上传 / 下载
// 列目录与删除走同步指令通道；上传/下载经独立流式通道（真流式，不限大小）。
import { useCallback, useEffect, useRef, useState } from 'react'
import { DirEntry, deletePath, downloadFile, listDir, uploadFile } from './api'

function formatSize(n: number): string {
  if (!n || n < 0) return n === 0 ? '0 B' : '-'
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[i]}`
}

function formatDate(ts: number): string {
  if (!ts) return '-'
  const d = new Date(ts * 1000)
  const p = (x: number) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function joinPath(dir: string, name: string): string {
  if (!dir) return name
  const sep = dir.includes('\\') ? '\\' : '/'
  return dir.replace(/[\\/]+$/, '') + sep + name
}

const thStyle: React.CSSProperties = {
  textAlign: 'left',
  padding: '0.4rem 0.6rem',
  borderBottom: '1px solid #1e293b',
  color: '#94a3b8',
  fontWeight: 600,
  fontSize: '0.78rem',
}
const tdStyle: React.CSSProperties = { padding: '0.4rem 0.6rem', fontSize: '0.82rem' }

export default function FileManager({ agentId }: { agentId: string }) {
  const [cwd, setCwd] = useState('')
  const [pathInput, setPathInput] = useState('')
  const [entries, setEntries] = useState<DirEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [notice, setNotice] = useState('')

  // 删除确认
  const [delConfirm, setDelConfirm] = useState<DirEntry | null>(null)
  const [delRecursive, setDelRecursive] = useState(false)
  const [deleting, setDeleting] = useState(false)

  // 上传覆盖确认
  const [pendingUpload, setPendingUpload] = useState<{ file: File; dest: string } | null>(null)

  // 传输进度（上传/下载）
  const [transfer, setTransfer] = useState<{
    kind: 'upload' | 'download'
    name: string
    loaded: number
    total: number
  } | null>(null)

  const fileInput = useRef<HTMLInputElement>(null)

  const load = useCallback(
    async (p: string) => {
      setLoading(true)
      setErr('')
      setNotice('')
      try {
        const es = await listDir(agentId, p)
        setEntries(es)
        setCwd(p)
        setPathInput(p)
      } catch (e: any) {
        setErr(`读取目录失败: ${e.message}`)
        setEntries([])
      }
      setLoading(false)
    },
    [agentId],
  )

  useEffect(() => {
    load('')
  }, [load])

  const goUp = () => {
    const norm = cwd.replace(/[\\/]+$/, '')
    if (!norm) return
    const idx = Math.max(norm.lastIndexOf('\\'), norm.lastIndexOf('/'))
    if (idx <= 0) {
      load('')
      return
    }
    let parent = norm.slice(0, idx)
    if (/^[A-Za-z]:$/.test(parent)) parent += '\\'
    load(parent)
  }

  const doDownload = async (e: DirEntry) => {
    setErr('')
    setNotice('')
    setTransfer({ kind: 'download', name: e.name, loaded: 0, total: e.size })
    try {
      const { blob, filename } = await downloadFile(agentId, e.path, e.size, (loaded, total) =>
        setTransfer(t => (t ? { ...t, loaded, total } : t)),
      )
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
      setNotice(`已下载: ${filename}`)
    } catch (e2: any) {
      setErr(`下载失败: ${e2.message}`)
    }
    setTransfer(null)
  }

  const doUpload = async (file: File, dest: string, overwrite: boolean) => {
    setPendingUpload(null)
    setErr('')
    setNotice('')
    setTransfer({ kind: 'upload', name: file.name, loaded: 0, total: file.size })
    try {
      await uploadFile(agentId, dest, file, overwrite, (loaded, total) =>
        setTransfer(t => (t ? { ...t, loaded, total } : t)),
      )
      setNotice(`已上传: ${file.name}`)
      await load(cwd)
    } catch (e: any) {
      setErr(`上传失败: ${e.message}`)
    }
    setTransfer(null)
  }

  const onPickFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0]
    e.target.value = ''
    if (!f) return
    const dest = joinPath(cwd, f.name)
    const exists = entries.some(en => !en.is_dir && en.name.toLowerCase() === f.name.toLowerCase())
    if (exists) {
      setPendingUpload({ file: f, dest })
      return
    }
    doUpload(f, dest, false)
  }

  const doDelete = async () => {
    if (!delConfirm) return
    setDeleting(true)
    setErr('')
    setNotice('')
    try {
      await deletePath(agentId, delConfirm.path, delConfirm.is_dir ? delRecursive : false)
      setNotice(`已删除: ${delConfirm.name}`)
      setDelConfirm(null)
      setDelRecursive(false)
      await load(cwd)
    } catch (e: any) {
      setErr(`删除失败: ${e.message}`)
    }
    setDeleting(false)
  }

  const pct = transfer && transfer.total > 0 ? Math.min(100, Math.round((transfer.loaded / transfer.total) * 100)) : null

  return (
    <div>
      {/* 工具条 */}
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', marginBottom: '0.6rem', flexWrap: 'wrap' }}>
        <button className="btn" onClick={goUp} disabled={!cwd} title="上级目录" style={{ fontSize: '0.78rem' }}>
          ↑ 上级
        </button>
        <button className="btn" onClick={() => load(cwd)} disabled={loading} style={{ fontSize: '0.78rem' }}>
          {loading ? '加载中...' : '刷新'}
        </button>
        <input
          className="input"
          style={{ flex: 1, minWidth: '220px', fontFamily: 'monospace', fontSize: '0.8rem' }}
          value={pathInput}
          onChange={e => setPathInput(e.target.value)}
          onKeyDown={e => {
            if (e.key === 'Enter') load(pathInput.trim())
          }}
          placeholder="输入路径后回车跳转（留空 = 驱动器/根目录）"
        />
        <button
          className="btn"
          onClick={() => fileInput.current?.click()}
          disabled={!!transfer || !cwd}
          title={cwd ? '上传文件到当前目录' : '请先进入一个目录'}
          style={{ fontSize: '0.78rem' }}
        >
          ⬆ 上传到当前目录
        </button>
        <input ref={fileInput} type="file" style={{ display: 'none' }} onChange={onPickFile} />
      </div>

      {err && <div className="error" style={{ marginBottom: '0.5rem' }}>{err}</div>}
      {notice && <div className="notice" style={{ marginBottom: '0.5rem' }}>{notice}</div>}

      {/* 传输进度 */}
      {transfer && (
        <div style={{ marginBottom: '0.6rem', padding: '0.5rem 0.7rem', background: '#0f172a', border: '1px solid #1e293b', borderRadius: '6px' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '0.78rem', marginBottom: '0.35rem' }}>
            <span>
              {transfer.kind === 'upload' ? '⬆ 上传中' : '⬇ 下载中'}: <span className="mono">{transfer.name}</span>
            </span>
            <span style={{ color: '#94a3b8' }}>
              {formatSize(transfer.loaded)} / {formatSize(transfer.total)}
              {pct != null ? ` (${pct}%)` : ''}
            </span>
          </div>
          <div style={{ height: '6px', background: '#1e293b', borderRadius: '3px', overflow: 'hidden' }}>
            <div
              style={{
                height: '100%',
                width: pct != null ? `${pct}%` : '30%',
                background: transfer.kind === 'upload' ? '#22c55e' : '#22d3ee',
                transition: 'width 0.15s',
              }}
            />
          </div>
        </div>
      )}

      {/* 目录列表 */}
      {loading && !entries.length ? (
        <div className="loading">加载目录...</div>
      ) : (
        <div style={{ border: '1px solid #1e293b', borderRadius: '6px', overflow: 'hidden' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse' }}>
            <thead>
              <tr>
                <th style={thStyle}>名称</th>
                <th style={{ ...thStyle, width: '110px' }}>大小</th>
                <th style={{ ...thStyle, width: '170px' }}>修改时间</th>
                <th style={{ ...thStyle, width: '150px', textAlign: 'right' }}>操作</th>
              </tr>
            </thead>
            <tbody>
              {entries.length === 0 && (
                <tr>
                  <td style={{ ...tdStyle, color: '#94a3b8' }} colSpan={4}>
                    空目录
                  </td>
                </tr>
              )}
              {entries.map(en => (
                <tr
                  key={en.path}
                  onDoubleClick={() => en.is_dir && load(en.path)}
                  style={{ borderBottom: '1px solid #1e293b' }}
                >
                  <td style={tdStyle}>
                    <span
                      className={en.is_dir ? 'link' : undefined}
                      style={{ cursor: en.is_dir ? 'pointer' : 'default' }}
                      onClick={() => en.is_dir && load(en.path)}
                      title={en.path}
                    >
                      {en.is_dir ? '📁' : '📄'} {en.name}
                    </span>
                  </td>
                  <td style={{ ...tdStyle, color: '#94a3b8' }}>{en.is_dir ? '-' : formatSize(en.size)}</td>
                  <td style={{ ...tdStyle, color: '#94a3b8' }}>{en.is_dir ? '-' : formatDate(en.modified)}</td>
                  <td style={{ ...tdStyle, textAlign: 'right', whiteSpace: 'nowrap' }}>
                    {!en.is_dir && (
                      <button
                        className="btn"
                        style={{ fontSize: '0.72rem', padding: '0.15rem 0.5rem', marginRight: '0.35rem' }}
                        disabled={!!transfer}
                        onClick={() => doDownload(en)}
                      >
                        下载
                      </button>
                    )}
                    <button
                      className="btn"
                      style={{
                        fontSize: '0.72rem',
                        padding: '0.15rem 0.5rem',
                        color: '#fff',
                        background: '#dc2626',
                        borderColor: '#dc2626',
                      }}
                      disabled={deleting}
                      onClick={() => {
                        setDelConfirm(en)
                        setDelRecursive(false)
                      }}
                    >
                      删除
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* 删除确认弹窗 */}
      {delConfirm && (
        <div className="overlay" onClick={() => !deleting && setDelConfirm(null)}>
          <div className="modal" onClick={e => e.stopPropagation()} style={{ maxWidth: '440px' }}>
            <div className="modal-header">
              <strong>删除{delConfirm.is_dir ? '目录' : '文件'}</strong>
              <button className="close" onClick={() => setDelConfirm(null)} disabled={deleting}>
                ×
              </button>
            </div>
            <div className="modal-body">
              <p style={{ margin: '0 0 0.6rem' }}>
                即将<span style={{ color: '#dc2626', fontWeight: 600 }}>永久删除</span>（不可恢复）：
              </p>
              <div className="mono" style={{ fontSize: '0.78rem', wordBreak: 'break-all', marginBottom: '0.8rem' }}>
                {delConfirm.path}
              </div>
              {delConfirm.is_dir && (
                <label style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', fontSize: '0.82rem' }}>
                  <input
                    type="checkbox"
                    checked={delRecursive}
                    onChange={e => setDelRecursive(e.target.checked)}
                  />
                  <span style={{ color: '#dc2626' }}>递归删除目录及其全部内容</span>
                </label>
              )}
              <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end', marginTop: '1rem' }}>
                <button className="btn" onClick={() => setDelConfirm(null)} disabled={deleting}>
                  取消
                </button>
                <button
                  className="btn"
                  onClick={doDelete}
                  disabled={deleting || (delConfirm.is_dir && !delRecursive)}
                  style={{ color: '#fff', background: '#dc2626', borderColor: '#dc2626' }}
                >
                  {deleting ? '删除中...' : '确认删除'}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* 上传覆盖确认弹窗 */}
      {pendingUpload && (
        <div className="overlay" onClick={() => setPendingUpload(null)}>
          <div className="modal" onClick={e => e.stopPropagation()} style={{ maxWidth: '440px' }}>
            <div className="modal-header">
              <strong>目标已存在</strong>
              <button className="close" onClick={() => setPendingUpload(null)}>
                ×
              </button>
            </div>
            <div className="modal-body">
              <p style={{ margin: '0 0 0.6rem' }}>目标路径已存在同名文件，是否覆盖？</p>
              <div className="mono" style={{ fontSize: '0.78rem', wordBreak: 'break-all', marginBottom: '0.8rem' }}>
                {pendingUpload.dest}
              </div>
              <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
                <button className="btn" onClick={() => setPendingUpload(null)}>
                  取消
                </button>
                <button
                  className="btn"
                  onClick={() => doUpload(pendingUpload.file, pendingUpload.dest, true)}
                  style={{ color: '#fff', background: '#dc2626', borderColor: '#dc2626' }}
                >
                  覆盖上传
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
