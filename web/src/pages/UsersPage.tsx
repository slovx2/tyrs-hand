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

interface Invitation {
  id: string
  username: string
  expiresAt: string
  createdAt: string
  status: 'pending' | 'expired' | 'revoked' | 'accepted'
}

const invitationStatus: Record<Invitation['status'], string> = {
  pending: '待接受',
  expired: '已过期',
  revoked: '已撤销',
  accepted: '已接受',
}

export function UsersPage() {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [username, setUsername] = useState('')
  const [invite, setInvite] = useState<{ token: string; url: string; expiresAt: string }>()
  const users = useQuery({
    queryKey: ['users'],
    queryFn: () => api<{ items: User[] }>('/users'),
  })
  const invitations = useQuery({
    queryKey: ['invitations'],
    queryFn: () => api<{ items: Invitation[] }>('/auth/invitations'),
  })
  const create = useMutation({
    mutationFn: () =>
      api<{ token: string; expiresAt: string }>('/auth/invitations', {
        method: 'POST',
        body: JSON.stringify({ username }),
    }),
    onSuccess: async (result) => {
      setInvite({
        ...result,
        url: `${window.location.origin}/invite?token=${encodeURIComponent(result.token)}`,
      })
      setUsername('')
      await queryClient.invalidateQueries({ queryKey: ['users'] })
      await queryClient.invalidateQueries({ queryKey: ['invitations'] })
      showToast('success', '邀请已创建，请安全发送给同事')
    },
  })
  const revoke = useMutation({
    mutationFn: (invitation: Invitation) =>
      api<void>(`/auth/invitations/${invitation.id}`, { method: 'DELETE' }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['invitations'] })
      showToast('success', '邀请已撤销')
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
          <div className="font-medium">邀请链接（发送给同事）</div>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <code className="min-w-0 flex-1 break-all select-all">{invite.url}</code>
            <button
              type="button"
              className="button-secondary"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(invite.url)
                  showToast('success', '邀请链接已复制')
                } catch {
                  showToast('error', '复制失败，请手动复制邀请链接')
                }
              }}
            >
              复制链接
            </button>
          </div>
          <p className="muted mt-2 text-sm">有效期至：{new Date(invite.expiresAt).toLocaleString()}</p>
          <details className="mt-3 text-sm">
            <summary className="cursor-pointer">查看 Token（手工填写时使用）</summary>
            <code className="mt-2 block break-all select-all">{invite.token}</code>
          </details>
          <button type="button" className="button-secondary mt-3" onClick={() => setInvite(undefined)}>
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
      <section className="mt-6">
        <div className="mb-3 flex items-baseline justify-between gap-3">
          <div>
            <h2 className="text-xl font-semibold">待接受的邀请</h2>
            <p className="muted mt-1 text-sm">邀请被接受前会显示在这里；待接受邀请可以撤销。</p>
          </div>
        </div>
        <div className="grid gap-3">
          {(invitations.data?.items ?? []).filter((invitation) => invitation.status !== 'accepted').map((invitation) => {
            const canRevoke = invitation.status === 'pending'
            return (
              <article className="panel flex flex-wrap items-center justify-between gap-3" key={invitation.id}>
                <div>
                  <strong>{invitation.username}</strong>
                  <p className="muted text-sm">
                    创建于 {new Date(invitation.createdAt).toLocaleString()} ·
                    {' '}有效期至 {new Date(invitation.expiresAt).toLocaleString()}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <span className={`status-badge ${canRevoke ? 'is-success' : 'is-danger'}`}>
                    {invitationStatus[invitation.status]}
                  </span>
                  {canRevoke && (
                    <button
                      type="button"
                      className="button-danger"
                      disabled={revoke.isPending}
                      onClick={() => {
                        if (window.confirm(`确定撤销发送给“${invitation.username}”的邀请吗？撤销后链接将立即失效。`)) {
                          revoke.mutate(invitation)
                        }
                      }}
                    >
                      撤销邀请
                    </button>
                  )}
                </div>
              </article>
            )
          })}
          {invitations.isSuccess && (invitations.data?.items ?? []).every((invitation) => invitation.status === 'accepted') && (
            <p className="muted panel">当前没有待接受的邀请。</p>
          )}
        </div>
      </section>
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
