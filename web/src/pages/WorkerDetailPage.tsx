import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Box, FileCog, LayoutDashboard, Users } from 'lucide-react'
import { Link, Navigate, NavLink, Outlet, useParams } from 'react-router'
import { api } from '../api/client'
import type { Worker } from './workerTypes'
import {
  useWorkerDetail,
  type WorkerDetailContext,
} from './workerDetailContext'
import { visibleWorkerRoles } from './workerHelpers'
import { CapabilityBadges, CapabilityStatus, StatusBadge } from './workerUI'

export function WorkerDetailPage() {
  const { workerId = '' } = useParams()
  const me = useQuery({
    queryKey: ['me'],
    queryFn: () => api<{ role: 'admin' | 'user' }>('/auth/me'),
    retry: false,
  })
  const worker = useQuery({
    queryKey: ['worker', workerId],
    queryFn: () => api<Worker>(`/workers/${workerId}`),
    enabled: Boolean(workerId),
  })
  if (worker.isLoading || me.isLoading) {
    return <p className="muted">正在读取 Worker…</p>
  }
  if (worker.isError) {
    return (
      <section>
        <Link className="detail-back-link" to="/workers">
          <ArrowLeft aria-hidden size={16} />
          返回 Worker
        </Link>
        <p role="alert" className="error-text mt-6">
          {worker.error.message}
        </p>
      </section>
    )
  }
  if (!worker.data) return <Navigate to="/workers" replace />
  const isAdmin = me.data?.role === 'admin'
  const tabs = [
    { to: 'overview', label: '概览', icon: LayoutDashboard },
    { to: 'codex', label: 'Codex 配置', icon: FileCog },
    { to: 'workspace', label: 'Workspace', icon: Box },
    ...(isAdmin ? [{ to: 'users', label: '用户分配', icon: Users }] : []),
  ]

  return (
    <section>
      <Link className="detail-back-link" to="/workers">
        <ArrowLeft aria-hidden size={16} />
        返回 Worker
      </Link>
      <header className="worker-detail-header">
        <div className="min-w-0">
          <p className="detail-eyebrow">WORKER / {worker.data.id}</p>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <h1 className="text-3xl font-bold">{worker.data.name}</h1>
            <StatusBadge tone={worker.data.enabled ? 'success' : 'muted'}>
              {worker.data.enabled ? '启用' : '停用'}
            </StatusBadge>
            <StatusBadge
              tone={worker.data.status === 'online' ? 'success' : 'muted'}
            >
              {worker.data.status}
            </StatusBadge>
          </div>
          <p className="muted mt-2 text-sm">
            {visibleWorkerRoles(worker.data)} · 并发{' '}
            {worker.data.maxConcurrentJobs}
          </p>
          <CapabilityBadges worker={worker.data} />
        </div>
      </header>
      <nav
        className="worker-detail-nav"
        aria-label={`${worker.data.name} 管理`}
      >
        {tabs.map((tab) => (
          <NavLink
            key={tab.to}
            to={tab.to}
            className={({ isActive }) => (isActive ? 'is-active' : '')}
          >
            <tab.icon aria-hidden size={16} />
            {tab.label}
          </NavLink>
        ))}
      </nav>
      <div className="worker-detail-content">
        <Outlet
          context={
            { worker: worker.data, isAdmin } satisfies WorkerDetailContext
          }
        />
      </div>
    </section>
  )
}

export function WorkerOverviewPage() {
  const { worker } = useWorkerDetail()
  return (
    <div className="worker-detail-stack">
      <section className="panel">
        <div className="worker-overview-heading">
          <div>
            <h2 className="text-xl font-semibold">运行状态</h2>
            <p className="muted mt-1 text-sm">
              当前 Worker 上报的宿主能力与版本信息。
            </p>
          </div>
          <StatusBadge tone={worker.status === 'online' ? 'success' : 'danger'}>
            {worker.status}
          </StatusBadge>
        </div>
        <CapabilityStatus worker={worker} />
      </section>
      <section className="panel">
        <h2 className="text-xl font-semibold">版本与心跳</h2>
        <dl className="runtime-status-grid mt-5">
          <div>
            <dt>Worker 版本</dt>
            <dd>{worker.workerVersion || '尚未上报'}</dd>
          </div>
          <div>
            <dt>协议版本</dt>
            <dd>{worker.protocolVersion}</dd>
          </div>
          <div>
            <dt>最近心跳</dt>
            <dd>
              {worker.heartbeatAt
                ? new Date(worker.heartbeatAt).toLocaleString('zh-CN')
                : '尚未连接'}
            </dd>
          </div>
          <div>
            <dt>SSH 指纹</dt>
            <dd className="font-mono">
              {worker.sshHostKeyFingerprint || '尚未上报'}
            </dd>
          </div>
        </dl>
        {worker.lastError && (
          <p role="alert" className="error-text mt-5">
            {worker.lastError}
          </p>
        )}
      </section>
    </div>
  )
}

export function WorkerUsersPage() {
  const { worker, isAdmin } = useWorkerDetail()
  const queryClient = useQueryClient()
  const users = useQuery({
    queryKey: ['users'],
    queryFn: () =>
      api<{
        items: {
          id: string
          username: string
          role: 'admin' | 'user'
          enabled: boolean
        }[]
      }>('/users'),
    enabled: isAdmin,
  })
  const assigned = useQuery({
    queryKey: ['worker-users', worker.id],
    queryFn: () =>
      api<{ items: { id: string; username: string }[] }>(
        `/workers/${worker.id}/users`,
      ),
    enabled: isAdmin,
  })
  const update = useMutation({
    mutationFn: ({ userId, remove }: { userId: string; remove: boolean }) =>
      api<void>(`/workers/${worker.id}/users/${userId}`, {
        method: remove ? 'DELETE' : 'PUT',
      }),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ['worker-users', worker.id] }),
  })
  if (!isAdmin) return <Navigate to="../overview" replace />
  const assignedIDs = new Set(
    (assigned.data?.items ?? []).map((user) => user.id),
  )
  return (
    <section className="panel">
      <h2 className="text-xl font-semibold">用户分配</h2>
      <p className="muted mt-1 text-sm">
        被分配的普通用户可以管理此 Worker 的 Codex、Workspace 和 Forum。
      </p>
      <div className="worker-user-list mt-5">
        {(users.data?.items ?? [])
          .filter((user) => user.role === 'user' && user.username)
          .map((user) => {
            const isAssigned = assignedIDs.has(user.id)
            return (
              <div className="worker-user-row" key={user.id}>
                <div>
                  <strong>{user.username}</strong>
                  <p className="muted text-xs">
                    {user.enabled ? '账号启用' : '账号停用'}
                  </p>
                </div>
                <button
                  className={isAssigned ? 'button-danger' : 'button-secondary'}
                  onClick={() =>
                    update.mutate({ userId: user.id, remove: isAssigned })
                  }
                  disabled={update.isPending}
                >
                  {isAssigned ? '移除' : '分配'}
                </button>
              </div>
            )
          })}
      </div>
    </section>
  )
}
