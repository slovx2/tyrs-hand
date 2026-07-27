import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Container, FolderGit2, RefreshCw, Terminal } from 'lucide-react'
import { useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'
import { DevelopmentEnvironmentRuntime } from './DevelopmentEnvironmentRuntime'
import { DevelopmentProjectRow } from './DevelopmentProjectRow'
import type {
  DevelopmentEnvironment,
  DiscordMember,
} from './developmentEnvironmentTypes'

export function DevelopmentEnvironmentSection({
  environment,
  members,
}: {
  environment: DevelopmentEnvironment
  members: DiscordMember[]
}) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [tab, setTab] = useState<'projects' | 'runtime'>('projects')
  const rebase = useMutation({
    mutationFn: () =>
      api<void>(`/development-environments/${environment.id}/rebase`, {
        method: 'POST',
      }),
    onSuccess: async () => {
      showToast('info', '环境 Rebase 已排队')
      await queryClient.invalidateQueries({
        queryKey: ['development-environments'],
      })
    },
  })
  const healthy = ['ready', 'running'].includes(environment.status)
  const scanTime = environment.projectsScannedAt
    ? new Date(environment.projectsScannedAt).toLocaleString('zh-CN')
    : '尚未扫描'

  return (
    <article className="development-environment">
      <header className="development-environment-header">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <Container aria-hidden size={19} />
            <h2 className="text-lg font-semibold">{environment.ownerName}</h2>
            <span
              className={`status-badge ${healthy ? 'is-success' : 'is-warning'}`}
            >
              {environment.status}
            </span>
          </div>
          <dl className="environment-facts">
            <div>
              <dt>镜像</dt>
              <dd title={environment.imageRef}>
                {environment.imageRef || '—'}
              </dd>
            </div>
            <div>
              <dt>Codex</dt>
              <dd>
                {environment.codexVersion || '待上报'}
                {environment.codexUserOverride ? ' · 个人覆盖' : ''}
              </dd>
            </div>
            <div>
              <dt>项目扫描</dt>
              <dd>{scanTime}</dd>
            </div>
          </dl>
        </div>
        <button
          type="button"
          className="button-secondary icon-label-button"
          title="保留 Home 与项目，重建到当前官方镜像"
          disabled={rebase.isPending}
          onClick={() => rebase.mutate()}
        >
          <RefreshCw
            aria-hidden
            size={16}
            className={rebase.isPending ? 'spin' : ''}
          />
          Rebase
        </button>
      </header>

      {(environment.error || environment.projectScanError) && (
        <div role="alert" className="environment-alert">
          {environment.error || environment.projectScanError}
        </div>
      )}

      <div className="environment-tabs" role="tablist" aria-label="环境视图">
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'projects'}
          className={tab === 'projects' ? 'is-active' : ''}
          onClick={() => setTab('projects')}
        >
          <FolderGit2 aria-hidden size={16} />
          项目
          <span>{environment.projects.length}</span>
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'runtime'}
          className={tab === 'runtime' ? 'is-active' : ''}
          onClick={() => setTab('runtime')}
        >
          <Terminal aria-hidden size={16} />
          运行与 SSH
        </button>
      </div>

      {tab === 'projects' ? (
        <div role="tabpanel" className="project-table">
          <div className="project-table-head" aria-hidden="true">
            <span>项目</span>
            <span>类型</span>
            <span>Git 状态</span>
            <span>可用性</span>
            <span>Forum</span>
            <span />
          </div>
          {environment.projects.map((project) => (
            <DevelopmentProjectRow
              key={project.id}
              project={project}
              ownerDiscordUserId={environment.ownerDiscordUserId}
              members={members}
            />
          ))}
          {environment.projects.length === 0 && (
            <div className="project-empty">
              <p className="font-semibold">未发现项目</p>
              <p className="muted mt-1 text-sm">
                在容器的 /var/lib/tyrs-hand/workspaces
                下创建一级目录后会自动出现。
              </p>
            </div>
          )}
        </div>
      ) : (
        <DevelopmentEnvironmentRuntime
          key={`${environment.id}:${environment.sshConfigRevision}:${environment.sshAppliedRevision}`}
          environment={environment}
          members={members}
        />
      )}
    </article>
  )
}
