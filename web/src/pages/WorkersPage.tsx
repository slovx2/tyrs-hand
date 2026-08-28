import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { EllipsisVertical, Plus, Server } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router'
import { api } from '../api/client'
import { useUI } from '../state'
import { CreateWorkerDialog, CredentialDialog } from './WorkerDialogs'
import type { Worker, WorkerDefaults } from './workerTypes'
import { confirmAction, visibleWorkerRoles } from './workerHelpers'
import { CapabilityBadges, StatusBadge } from './workerUI'

export type { Worker } from './workerTypes'

export function WorkersPage() {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [createOpen, setCreateOpen] = useState(false)
  const [credential, setCredential] = useState<{
    title: string
    token: string
  } | null>(null)
  const me = useQuery({
    queryKey: ['me'],
    queryFn: () => api<{ role: 'admin' | 'user' }>('/auth/me'),
    retry: false,
  })
  const isAdmin = me.data?.role === 'admin'
  const workers = useQuery({
    queryKey: ['workers'],
    queryFn: () => api<{ items: Worker[] }>('/workers'),
  })
  const defaults = useQuery({
    queryKey: ['worker-defaults'],
    queryFn: () => api<WorkerDefaults>('/settings/workers'),
    enabled: isAdmin,
  })
  const refresh = () => queryClient.invalidateQueries({ queryKey: ['workers'] })
  const action = useMutation({
    mutationFn: async ({ worker, type }: { worker: Worker; type: string }) => {
      if (type === 'enroll') {
        const result = await api<{ enrollmentToken: string }>(
          `/workers/${worker.id}/enrollments`,
          { method: 'POST' },
        )
        return {
          title: `${worker.name} 的一次性注册 Token`,
          token: result.enrollmentToken,
        }
      }
      if (type === 'toggle') {
        await api<void>(`/workers/${worker.id}/enabled`, {
          method: 'PUT',
          body: JSON.stringify({ enabled: !worker.enabled }),
        })
      } else {
        await api<void>(`/workers/${worker.id}`, { method: 'DELETE' })
      }
      return null
    },
    onSuccess: async (result) => {
      if (result) setCredential(result)
      await refresh()
    },
  })
  const saveDefaults = useMutation({
    mutationFn: (value: WorkerDefaults) =>
      api<void>('/settings/workers', {
        method: 'PUT',
        body: JSON.stringify(value),
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['worker-defaults'] })
      showToast('success', '默认 Worker 已保存；已有资源不会迁移')
    },
  })
  const items = workers.data?.items ?? []

  return (
    <section>
      <div className="workers-page-header">
        <div>
          <h1 className="text-3xl font-bold">Worker</h1>
          <p className="muted mt-2">
            选择一台 Worker，进入独立页面管理运行状态、Codex 与 Workspace。
          </p>
        </div>
        {isAdmin && (
          <button
            className="button icon-label-button"
            onClick={() => setCreateOpen(true)}
          >
            <Plus aria-hidden size={16} />
            新增 Worker
          </button>
        )}
      </div>

      {isAdmin && (
        <div className="worker-placement-bar">
          <div>
            <strong>Discord 默认 Worker</strong>
            <p className="muted text-xs">仅影响之后首次分配的新资源。</p>
          </div>
          <WorkerSelect
            workers={items}
            value={defaults.data?.discordWorkerId ?? ''}
            onChange={(value) =>
              saveDefaults.mutate({ discordWorkerId: value || null })
            }
          />
        </div>
      )}

      {workers.isError && (
        <p role="alert" className="error-text mt-6">
          {workers.error.message}
        </p>
      )}
      <div className="worker-list">
        {items.map((worker) => (
          <WorkerListItem
            key={worker.id}
            worker={worker}
            isAdmin={isAdmin}
            action={action}
          />
        ))}
        {!workers.isLoading && items.length === 0 && (
          <div className="workspace-empty">
            <p className="font-semibold">尚无可用 Worker</p>
          </div>
        )}
      </div>

      {createOpen && (
        <CreateWorkerDialog
          onClose={() => setCreateOpen(false)}
          onCreated={async (result) => {
            setCreateOpen(false)
            setCredential({
              title: '新 Worker 的一次性注册 Token',
              token: result.enrollmentToken,
            })
            await refresh()
          }}
        />
      )}
      {credential && (
        <CredentialDialog {...credential} onClose={() => setCredential(null)} />
      )}
    </section>
  )
}

function WorkerListItem({
  worker,
  isAdmin,
  action,
}: {
  worker: Worker
  isAdmin: boolean
  action: {
    mutate: (value: { worker: Worker; type: string }) => void
    isPending: boolean
  }
}) {
  return (
    <article className="worker-list-item">
      <Link className="worker-list-main" to={`/workers/${worker.id}/overview`}>
        <Server aria-hidden size={21} />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-lg font-semibold">{worker.name}</h2>
            <StatusBadge tone={worker.enabled ? 'success' : 'muted'}>
              {worker.enabled ? '启用' : '停用'}
            </StatusBadge>
            <StatusBadge
              tone={worker.status === 'online' ? 'success' : 'muted'}
            >
              {worker.status}
            </StatusBadge>
          </div>
          <p className="muted mt-1 text-sm">
            {visibleWorkerRoles(worker)} · 并发 {worker.maxConcurrentJobs}
          </p>
          <CapabilityBadges worker={worker} />
        </div>
        <span className="worker-list-link">查看详情</span>
      </Link>
      {isAdmin && (
        <details className="worker-more-menu">
          <summary
            className="icon-button"
            role="button"
            aria-label={`${worker.name} 更多操作`}
          >
            <EllipsisVertical aria-hidden size={18} />
          </summary>
          <div className="worker-more-popover">
            <button
              disabled={action.isPending}
              onClick={() => action.mutate({ worker, type: 'enroll' })}
            >
              轮换凭据
            </button>
            <button
              disabled={action.isPending}
              onClick={() =>
                confirmAction(`${worker.enabled ? '停用' : '启用'} Worker？`) &&
                action.mutate({ worker, type: 'toggle' })
              }
            >
              {worker.enabled ? '停用' : '启用'}
            </button>
            <button
              className="is-danger"
              disabled={action.isPending}
              onClick={() =>
                confirmAction('删除后无法恢复，确定删除此 Worker？') &&
                action.mutate({ worker, type: 'delete' })
              }
            >
              删除 Worker
            </button>
          </div>
        </details>
      )}
    </article>
  )
}

function WorkerSelect({
  workers,
  value,
  onChange,
}: {
  workers: Worker[]
  value: string
  onChange: (value: string) => void
}) {
  return (
    <select
      aria-label="Discord 默认 Worker"
      className="field"
      value={value}
      onChange={(event) => onChange(event.target.value)}
    >
      <option value="">未设置</option>
      {workers
        .filter((worker) => worker.enabled && worker.roles.includes('discord'))
        .map((worker) => (
          <option value={worker.id} key={worker.id}>
            {worker.name}
          </option>
        ))}
    </select>
  )
}
