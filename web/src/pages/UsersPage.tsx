import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'

interface User {
  id: string
  username: string
  role: string
  enabled: boolean
  createdAt: string
}

export function UsersPage() {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [username, setUsername] = useState('')
  const [invite, setInvite] = useState('')
  const users = useQuery({
    queryKey: ['users'],
    queryFn: () => api<{ items: User[] }>('/users'),
  })
  const create = useMutation({
    mutationFn: () =>
      api<{ token: string; expiresAt: string }>('/auth/invitations', {
        method: 'POST',
        body: JSON.stringify({ username }),
      }),
    onSuccess: async (result) => {
      setInvite(result.token)
      setUsername('')
      await queryClient.invalidateQueries({ queryKey: ['users'] })
      showToast('success', '邀请已创建，请安全发送给同事')
    },
  })
  const toggle = useMutation({
    mutationFn: (user: User) =>
      api<void>(`/users/${user.id}/enabled`, {
        method: 'PUT',
        body: JSON.stringify({ enabled: !user.enabled }),
      }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['users'] }),
  })
  return (
    <section>
      <h1 className="text-3xl font-bold">用户管理</h1>
      <p className="muted mt-2">邀请同事使用 Control，并管理账号启用状态。</p>
      {invite && (
        <div className="danger-note mt-6">
          <div className="font-medium">一次性邀请 Token</div>
          <code className="mt-2 block break-all select-all">{invite}</code>
          <button className="button-secondary mt-3" onClick={() => setInvite('')}>
            我已保存
          </button>
        </div>
      )}
      <form
        className="panel mt-6 flex flex-wrap items-end gap-3"
        onSubmit={(event) => {
          event.preventDefault()
          create.mutate()
        }}
      >
        <label>
          <span className="label">用户名</span>
          <input className="field mt-1" value={username} onChange={(event) => setUsername(event.target.value)} required />
        </label>
        <button className="button" disabled={create.isPending}>
          创建邀请
        </button>
      </form>
      <div className="mt-6 grid gap-3">
        {(users.data?.items ?? []).map((user) => (
          <article className="panel flex flex-wrap items-center justify-between gap-3" key={user.id}>
            <div>
              <strong>{user.username}</strong>
              <p className="muted text-sm">{user.role === 'admin' ? '管理员' : '普通用户'} · {user.enabled ? '启用' : '已停用'}</p>
            </div>
            <button className="button-secondary" onClick={() => toggle.mutate(user)} disabled={toggle.isPending}>
              {user.enabled ? '停用' : '启用'}
            </button>
          </article>
        ))}
      </div>
    </section>
  )
}
