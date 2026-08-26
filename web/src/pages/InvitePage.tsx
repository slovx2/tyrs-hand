import { useState } from 'react'
import { api } from '../api/client'

export function InvitePage() {
  const [token, setToken] = useState(() => new URLSearchParams(window.location.search).get('token') ?? '')
  const [password, setPassword] = useState('')
  const [result, setResult] = useState<{ totpSecret: string; provisioningUri: string; recoveryCodes: string[] }>()
  const [error, setError] = useState('')
  return (
    <main className="grid min-h-screen place-items-center p-8">
      <form className="panel w-full max-w-md" onSubmit={async (event) => {
        event.preventDefault()
        setError('')
        try {
          setResult(await api<{ totpSecret: string; provisioningUri: string; recoveryCodes: string[] }>('/auth/invitations/accept', { method: 'POST', body: JSON.stringify({ token, password }) }))
        } catch (err) {
          setError((err as Error).message)
        }
      }}>
        <h1 className="text-2xl font-bold">加入 Tyrs Hand</h1>
        <label className="mt-5 block"><span className="label">邀请 Token</span><input value={token} onChange={(event) => setToken(event.target.value)} required /></label>
        <label className="mt-4 block"><span className="label">设置密码</span><input type="password" value={password} onChange={(event) => setPassword(event.target.value)} minLength={8} required /></label>
        <button className="button mt-5">完成注册</button>
        {error && <p className="error-text mt-3">{error}</p>}
        {result && <div className="danger-note mt-5"><p>请立即在验证器中添加以下 TOTP 密钥：</p><code className="mt-2 block select-all">{result.totpSecret}</code><p className="mt-3">恢复码：</p><code className="mt-2 block break-all select-all">{result.recoveryCodes.join(' ')}</code><p className="muted mt-3 text-sm">完成后返回登录页，使用用户名和 TOTP 登录。</p></div>}
      </form>
    </main>
  )
}
