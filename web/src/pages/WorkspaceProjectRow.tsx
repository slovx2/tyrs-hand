import {
  ChevronDown,
  Folder,
  GitBranch,
  GitCommit,
  MessageSquare,
} from 'lucide-react'
import { useState } from 'react'
import { WorkspaceProjectForums } from './WorkspaceProjectForums'
import type { WorkspaceProject, DiscordMember } from './workspaceTypes'

export function WorkspaceProjectRow({
  project,
  ownerDiscordUserId,
  members,
}: {
  project: WorkspaceProject
  ownerDiscordUserId: string
  members: DiscordMember[]
}) {
  const [expanded, setExpanded] = useState(false)
  const missing = project.availabilityStatus === 'missing'
  const activeForum = project.forums.find(
    (forum) => forum.bindingStatus === 'active',
  )

  return (
    <div
      className={`workspace-project ${missing ? 'is-missing' : ''}`}
      data-testid={`workspace-project-${project.id}`}
    >
      <div className="workspace-project-grid">
        <div className="project-identity" data-label="项目">
          <div className="flex min-w-0 items-center gap-2">
            <Folder aria-hidden size={16} />
            <strong className="truncate" title={project.name}>
              {project.name}
            </strong>
          </div>
          <span className="project-path" title={project.relativePath}>
            {project.projectSource === 'codex_registered' && project.hostPath
              ? project.hostPath
              : project.relativePath}
          </span>
          <span className="project-source">
            {project.projectSource === 'workspace_root'
              ? 'Workspace 根目录'
              : project.projectSource === 'codex_registered'
                ? 'Codex 已注册'
                : 'Workspace 子目录'}
          </span>
        </div>
        <div data-label="类型">
          <span className="project-kind">{project.projectKind}</span>
        </div>
        <div className="project-git-status" data-label="Git 状态">
          {project.projectKind === 'git' ? (
            <>
              <span title={project.branch || '未命名分支'}>
                <GitBranch aria-hidden size={14} />
                {project.branch || '—'}
              </span>
              <span title={project.headSha || '尚无提交'}>
                <GitCommit aria-hidden size={14} />
                {project.headSha ? project.headSha.slice(0, 8) : '—'}
              </span>
              <span className={project.dirty ? 'dirty-indicator' : ''}>
                {project.dirty ? '有修改' : '干净'}
              </span>
            </>
          ) : (
            <span className="muted">普通目录</span>
          )}
          {project.remoteUrl && (
            <span className="project-remote" title={project.remoteUrl}>
              {project.remoteUrl}
            </span>
          )}
        </div>
        <div data-label="可用性">
          <span
            className={`status-badge ${missing ? 'is-danger' : 'is-success'}`}
          >
            {missing ? '缺失' : '可用'}
          </span>
        </div>
        <div data-label="Forum">
          <span
            className={`forum-binding ${activeForum ? 'is-active' : ''}`}
            title={activeForum?.name}
          >
            <MessageSquare aria-hidden size={14} />
            {missing ? '项目不可用' : activeForum ? activeForum.name : '未配对'}
          </span>
        </div>
        <div className="project-actions">
          <button
            type="button"
            className="icon-button"
            title="管理 Forum 配对"
            aria-label={`${project.name} 管理 Forum 配对`}
            aria-expanded={expanded}
            disabled={missing && project.projectSource === 'codex_registered'}
            onClick={() => setExpanded((value) => !value)}
          >
            <ChevronDown
              aria-hidden
              size={18}
              className={expanded ? 'rotate-180' : ''}
            />
          </button>
        </div>
      </div>
      {project.scanError && (
        <p className="error-text project-row-error">{project.scanError}</p>
      )}
      {expanded && (
        <WorkspaceProjectForums
          project={project}
          ownerDiscordUserId={ownerDiscordUserId}
          members={members}
        />
      )}
    </div>
  )
}
