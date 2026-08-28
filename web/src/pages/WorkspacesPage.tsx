import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link2, RefreshCw } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
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
  })
  const members = useQuery({
    queryKey: ['discord-members'],
    queryFn: () => api<DiscordMember[]>('/discord/members'),
  })
  const scannedWorkspace = useRef<string | null>(null)
  const scan = useMutation({
    mutationFn: () =>
      api<WorkerWorkspaceResponse>(`/workers/${worker.id}/workspace/scan`, {
        method: 'POST',
      }),
    onSuccess: (result) => {
      if (result.workspace) scannedWorkspace.current = result.workspace.id
      queryClient.setQueryData(['worker-workspace', worker.id], result)
    },
  })
  const create = useMutation({
    mutationFn: () =>
      api<{ id: string }>('/workspaces', {
        method: 'POST',
        body: JSON.stringify({ ownerDiscordUserId, workerId: worker.id }),
      }),
    onSuccess: async (created) => {
      setOwnerDiscordUserId('')
      scannedWorkspace.current = created.id
      const result = await scan.mutateAsync().catch(() => null)
      if (!result) {
        await queryClient.invalidateQueries({
          queryKey: ['worker-workspace', worker.id],
        })
      }
      await queryClient.invalidateQueries({ queryKey: ['discord-members'] })
      showToast('success', 'Workspace 已绑定')
    },
  })
  const eligibleMembers = (members.data ?? []).filter(
    (member) => !member.workspaceOwner,
  )
  const scanWorkspace = scan.mutate

  useEffect(() => {
    const workspaceId = workspace.data?.workspace?.id
    if (!workspaceId || scannedWorkspace.current === workspaceId) return
    scannedWorkspace.current = workspaceId
    scanWorkspace()
  }, [scanWorkspace, workspace.data?.workspace?.id])

  const refresh = async () => {
    const workspaceRefresh = workspace.data?.workspace
      ? scan.mutateAsync()
      : workspace.refetch().then((result) => {
          if (result.error) throw result.error
          return result
        })
    const membersRefresh = members.refetch().then((result) => {
      if (result.error) throw result.error
      return result
    })
    const results = await Promise.allSettled([workspaceRefresh, membersRefresh])
    if (results.some((item) => item.status === 'rejected')) {
      showToast('error', '刷新 Workspace 失败')
      return
    }
    const scanned = results[0]
    if (
      workspace.data?.workspace &&
      scanned.status === 'fulfilled' &&
      'workspace' in scanned.value &&
      scanned.value.workspace?.projectScanError
    ) {
      showToast('error', 'Worker 项目扫描失败')
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
              disabled={
                workspace.isFetching || scan.isPending || members.isFetching
              }
              onClick={() => void refresh()}
            >
              <RefreshCw
                aria-hidden
                size={16}
                className={workspace.isFetching || scan.isPending ? 'spin' : ''}
              />
              刷新
            </button>
          </div>
        </div>
      </section>

      {workspace.isLoading ? (
        <section className="panel muted">正在读取 Workspace…</section>
      ) : workspace.data?.workspace ? (
        <>
          {scan.isError && (
            <div role="alert" className="workspace-alert">
              实时扫描失败：{scan.error.message}。当前继续显示上次扫描结果。
            </div>
          )}
          <WorkspaceSection
            workerId={worker.id}
            workspace={workspace.data.workspace}
            members={members.data ?? []}
            showCodexProjects={showCodexProjects}
          />
        </>
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
