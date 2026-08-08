import { useState } from 'react'

const API = '/api'

interface Props {
  onLogin: (token: string, username: string, mustChangePassword: boolean) => void
}

export default function LoginPage({ onLogin }: Props) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!username || !password) { setError('请输入用户名和密码'); return }
    setLoading(true)
    setError('')
    try {
      const r = await fetch(`${API}/login`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      })
      const data = await r.json()
      if (!r.ok) {
        setError(data.error || '登录失败')
        setLoading(false)
        return
      }
      onLogin(data.token, data.username, data.must_change_password)
    } catch {
      setError('无法连接到服务器')
    }
    setLoading(false)
  }

  return (
    <div className="auth-page">
      <div className="auth-card">
        <div className="auth-logo">◆</div>
        <h2 className="auth-title">Baize 白泽</h2>
        <p className="auth-subtitle">Dashboard</p>
        <form onSubmit={handleSubmit} className="auth-form">
          <input
            value={username}
            onChange={e => setUsername(e.target.value)}
            placeholder="用户名"
            className="input"
            autoFocus
            disabled={loading}
          />
          <input
            type="password"
            value={password}
            onChange={e => setPassword(e.target.value)}
            placeholder="密码"
            className="input"
            disabled={loading}
          />
          {error && <div className="auth-error">{error}</div>}
          <button type="submit" className="auth-btn" disabled={loading}>
            {loading ? '登录中...' : '登 录'}
          </button>
        </form>
      </div>
    </div>
  )
}
