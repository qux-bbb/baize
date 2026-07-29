import { useState } from 'react'

const API = '/api'

interface Props {
  username: string
  token: string
  onPasswordChanged: (token: string) => void
  onLogout: () => void
}

export default function ChangePasswordPage({ username, token, onPasswordChanged, onLogout }: Props) {
  const [oldPwd, setOldPwd] = useState('')
  const [newPwd, setNewPwd] = useState('')
  const [confirmPwd, setConfirmPwd] = useState('')
  const [error, setError] = useState('')
  const [success, setSuccess] = useState(false)
  const [loading, setLoading] = useState(false)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!oldPwd || !newPwd || !confirmPwd) { setError('请填写所有字段'); return }
    if (newPwd !== confirmPwd) { setError('两次输入的新密码不一致'); return }
    if (newPwd.length < 6) { setError('密码长度不能少于6位'); return }
    setLoading(true)
    setError('')
    try {
      const r = await fetch(`${API}/change-password`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
        body: JSON.stringify({ old_password: oldPwd, new_password: newPwd }),
      })
      const data = await r.json()
      if (!r.ok) {
        setError(data.error || '修改密码失败')
        setLoading(false)
        return
      }
      setSuccess(true)
      setTimeout(() => onPasswordChanged(data.token), 1200)
    } catch {
      setError('无法连接到服务器')
      setLoading(false)
    }
  }

  if (success) {
    return (
      <div className="auth-page">
        <div className="auth-card">
          <div style={{fontSize: '3rem', marginBottom: '0.8rem'}}>✅</div>
          <h2 className="auth-title">密码修改成功</h2>
          <p className="auth-subtitle" style={{color: '#34d399'}}>正在进入 Dashboard...</p>
        </div>
      </div>
    )
  }

  return (
    <div className="auth-page">
      <div className="auth-card">
        <div className="auth-logo">◆</div>
        <h2 className="auth-title">首次登录·修改密码</h2>
        <p className="auth-subtitle">欢迎，{username}。<br />首次登录请立即修改密码</p>
        <form onSubmit={handleSubmit} className="auth-form">
          <input
            type="password"
            value={oldPwd}
            onChange={e => setOldPwd(e.target.value)}
            placeholder="当前密码"
            className="input"
            autoFocus
            disabled={loading}
          />
          <input
            type="password"
            value={newPwd}
            onChange={e => setNewPwd(e.target.value)}
            placeholder="新密码"
            className="input"
            disabled={loading}
          />
          <input
            type="password"
            value={confirmPwd}
            onChange={e => setConfirmPwd(e.target.value)}
            placeholder="确认新密码"
            className="input"
            disabled={loading}
          />
          {error && <div className="auth-error">{error}</div>}
          <button type="submit" className="auth-btn" disabled={loading}>
            {loading ? '修改中...' : '确认修改'}
          </button>
        </form>
        <div className="auth-footer">
          <span className="link" onClick={onLogout}>返回登录</span>
        </div>
      </div>
    </div>
  )
}
