import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link2, RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'
import { useWorkerDetail } from './workerDetailContext'
import { WorkspaceSection } from './WorkspaceSection'
import type { DiscordMember, Workspace } from './workspaceTypes'

interface WorkerWorkspaceResponse {
  workspace: Workspace | null
}

export function WorkerWorkspacePage() {
  const { worker } = useWorkerDetail()
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [ownerDiscordUserId, setOwnerDiscordUserId] = useState('')
  const [showCodexProjects, setShowCodexProjects] = useState(false)
  const workspace = useQuery({
    queryKey: ['worker-workspace', worker.id],
    queryFn: () =>
      api<WorkerWorkspaceResponse>(`/workers/${worker.id}/workspace`),
    refetchInterval: 5_000,
  })
  const members = useQuery({
    queryKey: ['discord-members'],
    queryFn: () => api<DiscordMember[]>('/discord/members'),
  })
  const create = useMutation({
    mutationFn: () =>
      api<{ id: string }>('/workspaces', {
        method: 'POST',
        body: JSON.stringify({ ownerDiscordUserId, workerId: worker.id }),
      }),
    onSuccess: async () => {
      setOwnerDiscordUserId('')
      await queryClient.invalidateQueries({
        queryKey: ['worker-workspace', worker.id],
      })
      await queryClient.invalidateQueries({ queryKey: ['discord-members'] })
      showToast('success', 'Workspace 已绑定')
    },
  })
  const eligibleMembers = (members.data ?? []).filter(
    (member) => !member.workspaceOwner,
  )

  const refresh = async () => {
    const result = await Promise.all([workspace.refetch(), members.refetch()])
    if (result.some((item) => item.error)) {
      showToast('error', '刷新 Workspace 失败')
      return
    }
    showToast('success', 'Workspace 已刷新')
  }

  if (workspace.isError) {
    return (
      <p role="alert" className="error-text">
        {workspace.error.message}
      </p>
    )
  }

  return (
    <div className="worker-detail-stack">
      <section className="panel">
        <div className="workspace-page-header">
          <div>
            <h2 className="text-xl font-semibold">Workspace</h2>
            <p className="muted mt-1 text-sm">
              这里只管理 {worker.name} 绑定的 Workspace、项目和 Forum。
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            {workspace.data?.workspace && (
              <label className="button-secondary inline-flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={showCodexProjects}
                  onChange={(event) =>
                    setShowCodexProjects(event.target.checked)
                  }
                />
                显示 Codex 项目
              </label>
            )}
            <button
              type="button"
              className="button-secondary icon-label-button"
              disabled={workspace.isFetching || members.isFetching}
              onClick={() => void refresh()}
            >
              <RefreshCw
                aria-hidden
                size={16}
                className={workspace.isFetching ? 'spin' : ''}
              />
              刷新
            </button>
          </div>
        </div>
      </section>

      {workspace.isLoading ? (
        <section className="panel muted">正在读取 Workspace…</section>
      ) : workspace.data?.workspace ? (
        <WorkspaceSection
          workerId={worker.id}
          workspace={workspace.data.workspace}
          members={members.data ?? []}
          showCodexProjects={showCodexProjects}
        />
      ) : (
        <section className="panel workspace-unbound">
          <div>
            <h2 className="text-xl font-semibold">尚未绑定 Workspace</h2>
            <p className="muted mt-1 text-sm">
              选择一位尚未拥有 Workspace 的活跃 Discord 成员，将其绑定到当前
              Worker。
            </p>
          </div>
          <div className="workspace-bind-form">
            <label>
              <span className="label">Workspace 负责人</span>
              <select
                className="field mt-1"
                value={ownerDiscordUserId}
                onChange={(event) => setOwnerDiscordUserId(event.target.value)}
              >
                <option value="">选择 Discord 成员</option>
                {eligibleMembers.map((member) => (
                  <option
                    key={member.discordUserId}
                    value={member.discordUserId}
                  >
                    {member.displayName || member.username}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              className="button icon-label-button"
              disabled={!ownerDiscordUserId || create.isPending}
              onClick={() => create.mutate()}
            >
              <Link2 aria-hidden size={16} />
              {create.isPending ? '绑定中…' : '绑定 Workspace'}
            </button>
          </div>
          {!members.isLoading && eligibleMembers.length === 0 && (
            <p className="muted text-sm">没有可绑定的活跃 Discord 成员。</p>
          )}
        </section>
      )}
    </div>
  )
}
