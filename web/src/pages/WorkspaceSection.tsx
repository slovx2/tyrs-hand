import { Server } from 'lucide-react'
import { WorkspaceProjectRow } from './WorkspaceProjectRow'
import type { Workspace, DiscordMember } from './workspaceTypes'

export function WorkspaceSection({
  workerId,
  workspace,
  members,
  showCodexProjects,
}: {
  workerId: string
  workspace: Workspace
  members: DiscordMember[]
  showCodexProjects: boolean
}) {
  const scanTime = workspace.projectsScannedAt
    ? new Date(workspace.projectsScannedAt).toLocaleString('zh-CN')
    : '尚未扫描'
  const projects = workspace.projects.filter(
    (project) =>
      showCodexProjects || project.projectSource !== 'codex_registered',
  )

  return (
    <article className="workspace-card">
      <header className="workspace-card-header">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <Server aria-hidden size={19} />
            <h3 className="text-lg font-semibold">{workspace.ownerName}</h3>
          </div>
          <dl className="workspace-facts">
            <div>
              <dt>Worker</dt>
              <dd className="font-mono">{workspace.workerId || '待绑定'}</dd>
            </div>
            <div>
              <dt>项目扫描</dt>
              <dd>{scanTime}</dd>
            </div>
          </dl>
        </div>
      </header>

      {workspace.projectScanError && (
        <div role="alert" className="workspace-alert">
          {workspace.projectScanError}
        </div>
      )}
      <div className="project-table">
        <div className="project-table-head" aria-hidden="true">
          <span>项目</span>
          <span>类型</span>
          <span>Git 状态</span>
          <span>可用性</span>
          <span>Forum</span>
          <span />
        </div>
        {projects.map((project) => (
          <WorkspaceProjectRow
            key={project.id}
            workerId={workerId}
            project={project}
            ownerDiscordUserId={workspace.ownerDiscordUserId}
            members={members}
          />
        ))}
        {projects.length === 0 && (
          <div className="project-empty">
            <p className="font-semibold">未发现项目</p>
            <p className="muted mt-1 text-sm">
              {showCodexProjects
                ? '在 Worker 配置的 Workspace 根目录中创建一级目录后会自动出现。'
                : '当前仅显示 Workspace 项目；打开“显示 Codex 项目”可查看已注册项目。'}
            </p>
          </div>
        )}
      </div>
    </article>
  )
}
