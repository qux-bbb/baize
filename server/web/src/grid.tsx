// BaizeGrid — AG Grid 封装
// 统一暗色主题 / 列宽拖拽 / 排序 / 拖表头重排 / 列显隐面板 / 状态持久化 / 重置视图
import { useEffect, useMemo, useRef, useState } from 'react'
import { AgGridReact } from 'ag-grid-react'
import {
  themeQuartz,
  type ColDef,
  type ColumnState,
  type GridApi,
  type GridState,
  type ICellRendererParams,
  type RowClassRules,
  type RowClickedEvent,
  type ValueFormatterParams,
} from 'ag-grid-community'

// ── 通用工具 ──────────────────────────────────────────
export function formatTime(ts: string): string {
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ts || ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export const SEV: Record<string, string> = {
  critical: '#fb7185', high: '#fbbf24', medium: '#fb923c', low: '#22d3ee', info: '#94a3b8',
}
const SEV_WEIGHT: Record<string, number> = { critical: 5, high: 4, medium: 3, low: 2, info: 1 }

// 严重度排序：critical > high > medium > low > info
export const sevComparator = (a: string, b: string) => (SEV_WEIGHT[a] || 0) - (SEV_WEIGHT[b] || 0)

// ── 暗色主题（对齐 Baize 现有配色：--bg #0b1120 / --card #131c31 / --accent #22d3ee）──
export const baizeTheme = themeQuartz.withParams({
  accentColor: '#22d3ee',
  backgroundColor: '#0b1120',
  dataBackgroundColor: '#0b1120',
  oddRowBackgroundColor: '#0f172a',
  rowBorder: { color: 'rgba(30, 41, 59, 0.5)' },
  columnBorder: { color: '#2e3d55' },
  headerColumnBorder: { color: '#2e3d55' },
  headerRowBorder: { color: '#1e293b' },
  headerColumnResizeHandleColor: '#22d3ee',
  headerColumnResizeHandleWidth: 2,
  rowHeight: 30,
  headerHeight: 36,
  cellFontFamily: "'JetBrains Mono', 'Consolas', monospace",
  cellFontSize: 12,
  cellHorizontalPadding: 10,
  rowHoverColor: 'rgba(34, 211, 238, 0.06)',
  selectedRowBackgroundColor: 'rgba(34, 211, 238, 0.12)',
  cellTextColor: '#e2e8f0',
  foregroundColor: '#e2e8f0',
  headerTextColor: '#64748b',
})

// ── 通用 cell renderer ─────────────────────────────────
// 严重度：彩色圆点 + 文本
export const sevCell = (p: ICellRendererParams) => (
  <span><span className="sev" style={{ color: SEV[p.value] || '#94a3b8' }}>●</span> {p.value}</span>
)

// 详情链接（阻止行点击冒泡）
export const linkCell = (onClick: (row: any) => void) => (p: ICellRendererParams) => (
  <span className="link" onClick={(e) => { e.stopPropagation(); onClick(p.data) }}>详情</span>
)

// 主机状态：在线绿点 / 离线灰点 / 已吊销 badge
export const statusCell = (p: ICellRendererParams) => {
  const d = p.data || {}
  return d.revoked
    ? <><span className="dot gray" /> <span className="badge offline">已吊销</span></>
    : <><span className={`dot ${d.is_online ? 'green' : 'gray'}`} /> <span className={`badge ${d.is_online ? 'online' : 'offline'}`}>{d.is_online ? '在线' : '离线'}</span></>
}

// 时间格式化（ISO 字符串 → yyyy-MM-dd HH:mm:ss）
export const timeFormatter = (p: ValueFormatterParams) => formatTime(p.value)

// 时间列过滤参数：数据是 ISO 字符串，解析为 {day, month, year} 供 agDateColumnFilter 使用
export const dateFilterParams = {
  dateParser: (v: string) => {
    if (!v) return null
    const d = new Date(v)
    return isNaN(d.getTime()) ? null : { day: d.getDate(), month: d.getMonth() + 1, year: d.getFullYear() }
  },
}

// ── 状态持久化 ─────────────────────────────────────────
function loadState(key: string): GridState | undefined {
  try {
    const raw = localStorage.getItem(key)
    return raw ? (JSON.parse(raw) as GridState) : undefined
  } catch { return undefined }
}

// ── BaizeGrid ──────────────────────────────────────────
interface BaizeGridProps<T> {
  columnDefs: ColDef<T>[]
  rowData: T[]
  stateKey: string          // localStorage 保存 key（每个表格唯一）
  height?: number           // 表格最大高度；行数少时自动收缩（默认 520）
  onRowClicked?: (e: RowClickedEvent<T>) => void
  rowClassRules?: RowClassRules<T>
  emptyText?: string        // 空数据提示
}

export function BaizeGrid<T>({ columnDefs, rowData, stateKey, height = 520, onRowClicked, rowClassRules, emptyText }: BaizeGridProps<T>) {
  const apiRef = useRef<GridApi | null>(null)
  const timerRef = useRef<number | undefined>(undefined)
  const initialState = useMemo(() => loadState(stateKey), [stateKey])
  const [colPanelOpen, setColPanelOpen] = useState(false)
  // 全局搜索（Quick Filter）输入
  const [quickFilter, setQuickFilter] = useState('')
  // 列显隐面板的勾选状态（打开面板时从 api 读取，含 localStorage 恢复的显隐）
  const [colStates, setColStates] = useState<{ colId: string; header: string; visible: boolean }[]>([])
  // 可显隐的列（有 headerName 的列；无表头的操作列不参与）
  const toggleable = useMemo(() => columnDefs.filter(c => c.headerName).map(c => ({
    colId: c.colId || (c.field as string), header: c.headerName!,
  })), [columnDefs])

  // 固定视口高度（AG Grid 内部滚动 + 虚拟滚动）；空数据时留出空态提示区
  const gridH = rowData.length === 0 ? 200 : height

  useEffect(() => () => window.clearTimeout(timerRef.current), [])

  const openColPanel = () => {
    const api = apiRef.current
    if (!api) return
    setColStates(api.getColumnState().map((s: ColumnState) => ({
      colId: s.colId,
      header: toggleable.find(t => t.colId === s.colId)?.header || s.colId,
      visible: !s.hide,
    })))
    setColPanelOpen(v => !v)
  }

  const toggleCol = (colId: string) => {
    const api = apiRef.current
    if (!api) return
    const cur = api.getColumnState().find((s: ColumnState) => s.colId === colId)
    if (!cur) return
    const newHide = !cur.hide // 点击 = 切换：hide 取反
    // 至少保留一列可见
    if (newHide && api.getColumnState().filter((s: ColumnState) => !s.hide).length <= 1) return
    api.applyColumnState({ state: [{ colId, hide: newHide }] })
    setColStates(prev => prev.map(c => c.colId === colId ? { ...c, visible: !newHide } : c))
  }

  // 重置视图：列宽/排序/显隐/重排 + 列过滤 + 全局搜索，全部恢复默认
  const resetView = () => {
    apiRef.current?.resetColumnState()
    apiRef.current?.setFilterModel(null)
    setQuickFilter('')
  }

  return (
    <div className={`baize-grid-wrap${onRowClicked ? ' clickable-rows' : ''}`}>
      <div className="grid-tools">
        <div className="grid-search">
          <input
            className="grid-search-input"
            placeholder="🔍 全局搜索…"
            value={quickFilter}
            onChange={(e) => {
              setQuickFilter(e.target.value)
              apiRef.current?.setGridOption('quickFilterText', e.target.value || undefined)
            }}
          />
          {quickFilter && (
            <button className="grid-search-clear" title="清除搜索" onClick={() => {
              setQuickFilter('')
              apiRef.current?.setGridOption('quickFilterText', undefined)
            }}>×</button>
          )}
        </div>
        <button className="grid-tool" title="显示 / 隐藏列" onClick={openColPanel}>≡ 列</button>
        <button className="grid-tool" title="重置列宽 / 排序 / 显隐 / 过滤" onClick={resetView}>↺ 重置视图</button>
        {colPanelOpen && (
          <div className="grid-colpanel">
            <div className="grid-colpanel-title">列</div>
            {colStates.map(c => (
              <label key={c.colId} className="grid-colpanel-item">
                <input type="checkbox" checked={c.visible} onChange={() => toggleCol(c.colId)} /> {c.header}
              </label>
            ))}
          </div>
        )}
      </div>
      <div className="baize-grid" style={{ height: gridH }}>
        <AgGridReact
          theme={baizeTheme}
          columnDefs={columnDefs}
          rowData={rowData}
          defaultColDef={{
            sortable: true,
            resizable: true,
            filter: true,
            floatingFilter: true,
            // 隐藏 floating 行漏斗按钮（窄列防挤）→ 表头 hover 漏斗按钮自动恢复，过滤菜单入口不丢
            suppressFloatingFilterButton: true,
          }}
          initialState={initialState}
          onStateUpdated={(e) => {
            // 状态变化（拖宽/排序/显隐/重排）300ms 防抖后落库
            window.clearTimeout(timerRef.current)
            timerRef.current = window.setTimeout(() => {
              try { localStorage.setItem(stateKey, JSON.stringify(e.state)) } catch { /* 忽略 */ }
            }, 300)
          }}
          onRowClicked={onRowClicked}
          rowClassRules={rowClassRules}
          onGridReady={(e) => { apiRef.current = e.api }}
          overlayNoRowsTemplate={emptyText ? `<span class="baize-empty">${emptyText}</span>` : undefined}
        />
      </div>
    </div>
  )
}
