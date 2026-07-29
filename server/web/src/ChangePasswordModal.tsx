import { useState } from 'react'
import { getToken } from './api'

const API = '/api'

interface Props {
  token: string
  onDone: (newToken: string) => void
  onClose: () => void
}

export default function ChangePasswordModal({ token, onDone, onClose }: Props) {
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
      setLoading(false)
      setTimeout(() => onDone(data.token), 1200)
    } catch {
      setError('无法连接到服务器')
      setLoading(false)
    }
  }

  return (
    <>
      <div className="modal-header">
        <strong>修改密码</strong>
        <button className="close" onClick={onClose}>×</button>
      </div>
      <div className="modal-body" style={{textAlign: 'center'}}>
        {success ? (
          <div style={{padding: '1.5rem 0'}}>
            <div style={{fontSize: '2rem', marginBottom: '0.5rem'}}>✅</div>
            <div style={{color: '#34d399', fontWeight: 600, fontSize: '1rem'}}>密码修改成功</div>
            <div style={{color: '#64748b', fontSize: '0.8rem', marginTop: '0.4rem'}}>正在刷新凭证...</div>
          </div>
        ) : (
          <form onSubmit={handleSubmit} className="auth-form">
            <input type="password" value={oldPwd} onChange={e => setOldPwd(e.target.value)}
              placeholder="当前密码" className="input" autoFocus disabled={loading} />
            <input type="password" value={newPwd} onChange={e => setNewPwd(e.target.value)}
              placeholder="新密码" className="input" disabled={loading} />
            <input type="password" value={confirmPwd} onChange={e => setConfirmPwd(e.target.value)}
              placeholder="确认新密码" className="input" disabled={loading} />
            {error && <div className="auth-error" style={{textAlign: 'center'}}>{error}</div>}
            <button type="submit" className="auth-btn" disabled={loading}>
              {loading ? '修改中...' : '确认修改'}
            </button>
          </form>
        )}
      </div>
    </>
  )
}
