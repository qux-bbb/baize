// 规则管理页 — Sigma 规则：列表 / 启停 / 新建 / 编辑 / 上传 / 删除 / 重载
// 规则真源是服务端 data/rules/*.yml；启停状态落 data/rules.json（重启保持）
import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ChangeEvent } from 'react'
import type { ColDef, ICellRendererParams } from 'ag-grid-community'
import { API, fetchJSON } from './api'
import { BaizeGrid, sevCell, sevComparator } from './grid'

interface RuleMeta {
  file: string
  title: string
  id: string
  level: string
  category: string
  description: string
  tags: string[] | null
  builtin: boolean
  enabled: boolean
}

interface RuleLoadError {
  file: string
  error: string
}

interface RulesResp {
  rules: RuleMeta[]
  total: number
  enabled: number
  errors: RuleLoadError[]
}

const RULE_TEMPLATE = `title: 新规则
description: 
level: medium
logsource:
  category: process
detection:
  selection:
    image_path: '*evil.exe'
  condition: selection
`

export default function RulesPage({ onChanged }: { onChanged?: () => void }) {
  const [rules, setRules] = useState<RuleMeta[]>([])
  const [errors, setErrors] = useState<RuleLoadError[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState('') // 正在启停/删除的规则文件名
  const [reloading, setReloading] = useState(false)

  // 编辑 / 新建弹窗
  const [editing, setEditing] = useState<{ file: string; yaml: string; isNew: boolean } | null>(null)
  const [saving, setSaving] = useState(false)
  const [saveErr, setSaveErr] = useState('')
  // 查看弹窗 / 删除确认
  const [viewing, setViewing] = useState<{ file: string; yaml: string } | null>(null)
  const [delConfirm, setDelConfirm] = useState<RuleMeta | null>(null)

  const load = useCallback(async () => {
    setErr('')
    try {
      const d = await fetchJSON<RulesResp>(`${API}/rules`)
      setRules(d.rules || [])
      setErrors(d.errors || [])
    } catch (e: any) {
      setErr(e.message)
    }
    setLoading(false)
  }, [])

  useEffect(() => { load() }, [load])

  const flash = (m: string) => {
    setNotice(m)
    window.setTimeout(() => setNotice(''), 3000)
  }

  // 规则变更后：刷新本页 + 通知外层更新侧栏计数
  const refresh = useCallback(async () => {
    await load()
    onChanged?.()
  }, [load, onChanged])

  const toggle = useCallback(async (rule: RuleMeta) => {
    setBusy(rule.file)
    setErr('')
    try {
      await fetchJSON(`${API}/rules/toggle`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ file: rule.file, enabled: !rule.enabled }),
      })
      await refresh()
    } catch (e: any) {
      setErr(e.message)
      await load() // 失败回滚显示
    }
    setBusy('')
  }, [refresh, load])

  const doReload = async () => {
    setReloading(true)
    setErr('')
    try {
      await fetchJSON(`${API}/rules/reload`, { method: 'POST' })
      await refresh()
      flash('已从磁盘重新加载规则')
    } catch (e: any) {
      setErr(e.message)
    }
    setReloading(false)
  }

  const openNew = () => {
    setSaveErr('')
    setEditing({ file: '', yaml: RULE_TEMPLATE, isNew: true })
  }

  const openYAML = useCallback(async (rule: RuleMeta, mode: 'view' | 'edit') => {
    setErr('')
    try {
      const d = await fetchJSON<{ file: string; yaml: string }>(`${API}/rules/yaml?file=${encodeURIComponent(rule.file)}`)
      if (mode === 'view') {
        setViewing({ file: rule.file, yaml: d.yaml })
      } else {
        setSaveErr('')
        setEditing({ file: rule.file, yaml: d.yaml, isNew: false })
      }
    } catch (e: any) {
      setErr(e.message)
    }
  }, [])

  // 上传规则文件：读取内容填入编辑弹窗（确认后才保存，避免误传直接生效）
  const onUpload = (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0]
    e.target.value = '' // 允许重复选择同一文件
    if (!f) return
    const reader = new FileReader()
    reader.onload = () => {
      setSaveErr('')
      setEditing({ file: f.name, yaml: String(reader.result || ''), isNew: true })
    }
    reader.onerror = () => setErr('读取规则文件失败')
    reader.readAsText(f)
  }

  const save = async () => {
    if (!editing) return
    setSaving(true)
    setSaveErr('')
    try {
      await fetchJSON(`${API}/rules`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ file: editing.file.trim(), yaml: editing.yaml }),
      })
      setEditing(null)
      await refresh()
      flash('规则已保存并立即生效')
    } catch (e: any) {
      setSaveErr(e.message) // 后端校验错误（YAML 语法 / 缺字段 / 文件名非法）
    }
    setSaving(false)
  }

  const doDelete = async () => {
    if (!delConfirm) return
    const file = delConfirm.file
    setDelConfirm(null)
    setBusy(file)
    setErr('')
    try {
      await fetchJSON(`${API}/rules?file=${encodeURIComponent(file)}`, { method: 'DELETE' })
      await refresh()
      flash(`已删除规则 ${file}`)
    } catch (e: any) {
      setErr(e.message)
    }
    setBusy('')
  }

  const ruleCols = useMemo<ColDef<RuleMeta>[]>(() => [
    {
      headerName: '状态', width: 80,
      cellRenderer: (p: ICellRendererParams<RuleMeta>) => {
        const r = p.data!
        return (
          <label className="toggle-switch rule-toggle" title={r.enabled ? '点击停用' : '点击启用'}>
            <input type="checkbox" checked={r.enabled} disabled={busy === r.file} onChange={() => toggle(r)} />
            <span className="toggle-slider"></span>
          </label>
        )
      },
    },
    { headerName: '规则名', field: 'title', flex: 1.5, tooltipField: 'title' },
    { headerName: '级别', field: 'level', width: 110, cellRenderer: sevCell, comparator: sevComparator },
    { headerName: '类别', field: 'category', width: 130 },
    { headerName: '来源', width: 92, valueGetter: p => (p.data?.builtin ? '内置' : '自定义') },
    { headerName: '规则 ID', field: 'id', width: 210, cellClass: 'mono' },
    { headerName: '描述', field: 'description', flex: 1.4, tooltipField: 'description' },
    {
      headerName: '', colId: 'action', width: 150, sortable: false, resizable: false, filter: false, floatingFilter: false,
      cellRenderer: (p: ICellRendererParams<RuleMeta>) => {
        const r = p.data!
        return (
          <span className="rule-actions">
            <span className="link" onClick={e => { e.stopPropagation(); openYAML(r, 'view') }}>查看</span>
            <span className="link" onClick={e => { e.stopPropagation(); openYAML(r, 'edit') }}>编辑</span>
            {!r.builtin && <span className="link danger" onClick={e => { e.stopPropagation(); setDelConfirm(r) }}>删除</span>}
          </span>
        )
      },
    },
  ], [busy, toggle, openYAML])

  const enabledCount = rules.filter(r => r.enabled).length

  return (
    <div className="rules-page">
      <div className="rules-bar">
        <span className="rules-stat">
          启用 <strong className="accent">{enabledCount}</strong> / 共 <strong>{rules.length}</strong>
        </span>
        <span className="rules-bar-spacer" />
        <button className="btn" onClick={openNew}>＋ 新建规则</button>
        <label className="btn upload-btn">
          ⬆ 上传规则
          <input type="file" accept=".yml,.yaml" onChange={onUpload} style={{ display: 'none' }} />
        </label>
        <button className="btn" onClick={doReload} disabled={reloading}>{reloading ? '重载中...' : '↻ 重载'}</button>
      </div>

      {notice && <div className="notice">{notice}</div>}
      {err && <div className="error" style={{ padding: '0.6rem' }}>{err}</div>}
      {errors.length > 0 && (
        <div className="error" style={{ padding: '0.6rem', textAlign: 'left' }}>
          规则解析失败（已跳过，修复后可点「↻ 重载」）：
          <ul style={{ margin: '0.4rem 0 0 1.2rem' }}>
            {errors.map(e => <li key={e.file}><span className="mono">{e.file}</span> — {e.error}</li>)}
          </ul>
        </div>
      )}

      {loading
        ? <div className="loading">加载中...</div>
        : <BaizeGrid columnDefs={ruleCols} rowData={rules} stateKey="baize.grid.rules" height={560} emptyText="暂无规则" />}

      {/* 新建 / 编辑规则 */}
      {editing && (
        <div className="overlay" onClick={() => !saving && setEditing(null)}>
          <div className="modal modal-wide" onClick={e => e.stopPropagation()}>
            <div className="modal-header">
              <strong>{editing.isNew ? '新建规则' : '编辑规则'}</strong>
              <span className="mono" style={{ color: '#64748b', marginLeft: '0.6rem' }}>{editing.file || '未命名'}</span>
              <button className="close" onClick={() => !saving && setEditing(null)}>×</button>
            </div>
            <div className="modal-body">
              <div className="field" style={{ alignItems: 'center' }}>
                <label>文件名</label>
                <input
                  className="input" style={{ flex: 1 }} value={editing.file}
                  disabled={!editing.isNew}
                  placeholder="my-rule.yml（后缀须为 .yml 或 .yaml）"
                  onChange={e => setEditing({ ...editing, file: e.target.value })}
                />
              </div>
              <textarea
                className="yaml-editor" spellCheck={false} value={editing.yaml}
                onChange={e => setEditing({ ...editing, yaml: e.target.value })}
              />
              {saveErr && <div className="error" style={{ padding: '0.6rem' }}>{saveErr}</div>}
            </div>
            <div className="modal-actions">
              <button className="btn" onClick={() => setEditing(null)} disabled={saving}>取消</button>
              <button className="btn" onClick={save} disabled={saving}>{saving ? '保存中...' : '保存并生效'}</button>
            </div>
          </div>
        </div>
      )}

      {/* 查看规则原文 */}
      {viewing && (
        <div className="overlay" onClick={() => setViewing(null)}>
          <div className="modal modal-wide" onClick={e => e.stopPropagation()}>
            <div className="modal-header">
              <strong>查看规则</strong>
              <span className="mono" style={{ color: '#64748b', marginLeft: '0.6rem' }}>{viewing.file}</span>
              <button className="close" onClick={() => setViewing(null)}>×</button>
            </div>
            <div className="modal-body">
              <pre className="yaml-editor yaml-view">{viewing.yaml}</pre>
            </div>
            <div className="modal-actions">
              <button className="btn" onClick={() => setViewing(null)}>关闭</button>
            </div>
          </div>
        </div>
      )}

      {/* 删除确认 */}
      {delConfirm && (
        <div className="overlay" onClick={() => setDelConfirm(null)}>
          <div className="modal" onClick={e => e.stopPropagation()} style={{ maxWidth: '440px' }}>
            <div className="modal-header">
              <strong>删除规则</strong>
              <button className="close" onClick={() => setDelConfirm(null)}>×</button>
            </div>
            <div className="modal-body">
              <div className="field"><label>规则</label><span>{delConfirm.title}</span></div>
              <div className="field"><label>文件</label><span className="mono">{delConfirm.file}</span></div>
              <div className="field"><label>后果</label><span>规则文件将从规则目录删除，该规则不再参与检测。此操作不可恢复。</span></div>
            </div>
            <div className="modal-actions">
              <button className="btn" onClick={() => setDelConfirm(null)}>取消</button>
              <button className="btn btn-danger" onClick={doDelete}>确认删除</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
