import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Plus, RefreshCw, X } from 'lucide-react'
import { useMemo, useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'
import type { Worker } from './WorkersPage'
import { WorkspaceSection } from './WorkspaceSection'
import type { WorkspaceList, DiscordMember } from './workspaceTypes'

export function WorkspaceManagement({ workers }: { workers: Worker[] }) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [createOpen, setCreateOpen] = useState(false)
  const workspaces = useQuery({
    queryKey: ['workspaces'],
    queryFn: () => api<WorkspaceList>('/workspaces'),
    refetchInterval: 5_000,
  })
  const members = useQuery({
    queryKey: ['discord-members'],
    queryFn: () => api<DiscordMember[]>('/discord/members'),
  })
  const items = useMemo(
    () => workspaces.data?.items ?? [],
    [workspaces.data?.items],
  )
  const existingOwners = new Set(
    items.map((environment) => environment.ownerDiscordUserId),
  )
  const eligibleMembers = (members.data ?? []).filter(
    (member) => !existingOwners.has(member.discordUserId),
  )
  const abnormalCount = items.filter((workspace) =>
    Boolean(workspace.projectScanError),
  ).length
  const boundWorkerIDs = new Set(
    items.map((workspace) => workspace.workerId).filter(Boolean),
  )
  const availableWorkers = workers.filter(
    (worker) =>
      worker.enabled &&
      worker.roles.includes('discord') &&
      !boundWorkerIDs.has(worker.id),
  )

  const refresh = async () => {
    const result = await Promise.all([workspaces.refetch(), members.refetch()])
    if (result.some((item) => item.error)) {
      showToast('error', '刷新 Workspace 失败')
      return
    }
    showToast('success', 'Workspace 已刷新')
  }

  return (
    <section className="mt-12 border-t pt-10 [border-color:var(--border)]">
      <div className="workspace-page-header">
        <div>
          <h2 className="text-2xl font-bold">Workspace、项目与 Forum</h2>
          <p className="muted mt-2">
            每个 Worker 最多绑定一个逻辑 Workspace；项目来自该 Worker 宿主目录。
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            className="button-secondary icon-label-button"
            title="刷新环境与项目状态"
            disabled={workspaces.isFetching || members.isFetching}
            onClick={() => void refresh()}
          >
            <RefreshCw
              aria-hidden
              size={16}
              className={workspaces.isFetching ? 'spin' : ''}
            />
            刷新
          </button>
          <button
            type="button"
            className="button icon-label-button"
            onClick={() => setCreateOpen(true)}
          >
            <Plus aria-hidden size={16} />
            创建 Workspace
          </button>
        </div>
      </div>

      <div className="workspace-summary">
        <SummaryMetric label="Workspace" value={items.length} />
        <SummaryMetric
          label="异常"
          value={abnormalCount}
          warning={abnormalCount > 0}
        />
        <SummaryMetric
          label="项目"
          value={items.reduce(
            (total, environment) => total + environment.projects.length,
            0,
          )}
        />
      </div>

      {workspaces.isError && (
        <p role="alert" className="error-text mt-6">
          {workspaces.error.message}
        </p>
      )}
      <div className="workspace-list">
        {items.map((workspace) => (
          <WorkspaceSection
            key={workspace.id}
            workspace={workspace}
            members={members.data ?? []}
          />
        ))}
        {!workspaces.isLoading && items.length === 0 && (
          <div className="workspace-empty">
            <p className="font-semibold">尚无 Workspace</p>
            <p className="muted mt-1 text-sm">
              绑定 Worker 后，在宿主 Workspace 根目录中建立项目即可自动发现。
            </p>
          </div>
        )}
      </div>

      {createOpen && (
        <CreateWorkspaceDialog
          members={eligibleMembers}
          workers={availableWorkers}
          onClose={() => setCreateOpen(false)}
          onCreated={async () => {
            setCreateOpen(false)
            await queryClient.invalidateQueries({
              queryKey: ['workspaces'],
            })
          }}
        />
      )}
    </section>
  )
}

function SummaryMetric({
  label,
  value,
  warning = false,
}: {
  label: string
  value: number
  warning?: boolean
}) {
  return (
    <div className={`workspace-metric ${warning ? 'is-warning' : ''}`}>
      {warning && <AlertTriangle aria-hidden size={16} />}
      <span className="muted text-sm">{label}</span>
      <strong>{value}</strong>
    </div>
  )
}

function CreateWorkspaceDialog({
  members,
  workers,
  onClose,
  onCreated,
}: {
  members: DiscordMember[]
  workers: Worker[]
  onClose: () => void
  onCreated: () => Promise<void>
}) {
  const showToast = useUI((state) => state.showToast)
  const [ownerDiscordUserId, setOwnerDiscordUserId] = useState('')
  const [workerId, setWorkerId] = useState('')
  const create = useMutation({
    mutationFn: () =>
      api<{ id: string }>('/workspaces', {
        method: 'POST',
        body: JSON.stringify({ ownerDiscordUserId, workerId }),
      }),
    onSuccess: async () => {
      showToast('success', 'Workspace 已创建')
      await onCreated()
    },
  })

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={onClose}>
      <div
        className="modal-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="create-workspace-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between gap-3">
          <div>
            <h2 id="create-workspace-title" className="text-lg font-semibold">
              创建 Workspace
            </h2>
            <p className="muted mt-1 text-sm">
              每位成员和每个 Worker 都只能绑定一个 Workspace。
            </p>
          </div>
          <button
            type="button"
            className="icon-button"
            title="关闭"
            aria-label="关闭"
            onClick={onClose}
          >
            <X aria-hidden size={18} />
          </button>
        </div>
        <label className="mt-5 block text-sm">
          Discord 成员
          <select
            className="field mt-1"
            value={ownerDiscordUserId}
            onChange={(event) => setOwnerDiscordUserId(event.target.value)}
          >
            <option value="">选择尚无环境的活跃成员</option>
            {members.map((member) => (
              <option key={member.discordUserId} value={member.discordUserId}>
                {member.displayName || member.username}
              </option>
            ))}
          </select>
        </label>
        <label className="mt-4 block text-sm">
          Worker
          <select
            className="field mt-1"
            value={workerId}
            onChange={(event) => setWorkerId(event.target.value)}
          >
            <option value="">选择尚未绑定 Workspace 的 Worker</option>
            {workers.map((worker) => (
              <option key={worker.id} value={worker.id}>
                {worker.name}
              </option>
            ))}
          </select>
        </label>
        {members.length === 0 && (
          <p className="muted mt-3 text-sm">没有可绑定的活跃成员。</p>
        )}
        <div className="mt-6 flex justify-end gap-2">
          <button type="button" className="button-secondary" onClick={onClose}>
            取消
          </button>
          <button
            type="button"
            className="button icon-label-button"
            disabled={!ownerDiscordUserId || !workerId || create.isPending}
            onClick={() => create.mutate()}
          >
            <Plus aria-hidden size={16} />
            {create.isPending ? '创建中…' : '创建 Workspace'}
          </button>
        </div>
      </div>
    </div>
  )
}
