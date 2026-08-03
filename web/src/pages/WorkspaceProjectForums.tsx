import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Ban,
  MessageSquarePlus,
  RotateCcw,
  Trash2,
  UserPlus,
} from 'lucide-react'
import { useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'
import type {
  WorkspaceForum,
  WorkspaceProject,
  DiscordMember,
} from './workspaceTypes'

export function WorkspaceProjectForums({
  project,
  ownerDiscordUserId,
  members,
}: {
  project: WorkspaceProject
  ownerDiscordUserId: string
  members: DiscordMember[]
}) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [name, setName] = useState('')
  const activeForum = project.forums.find(
    (forum) => forum.bindingStatus === 'active',
  )
  const historicalForums = project.forums.filter(
    (forum) => forum.bindingStatus === 'inactive',
  )
  const missing = project.availabilityStatus === 'missing'
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: ['workspace-cards'] })
  const pair = useMutation({
    mutationFn: (input: { mode: 'new' | 'restore'; forumId?: string }) =>
      api<void>(`/workspace-projects/${project.id}/forums`, {
        method: 'POST',
        body: JSON.stringify({ ...input, name: name.trim() }),
      }),
    onSuccess: async (_, input) => {
      setName('')
      showToast(
        'info',
        input.mode === 'new' ? 'Forum 创建已排队' : '历史 Forum 恢复已排队',
      )
      await refresh()
    },
  })
  const disable = useMutation({
    mutationFn: (forumId: string) =>
      api<void>(`/workspace-forums/${forumId}/disable`, { method: 'POST' }),
    onSuccess: async () => {
      showToast('info', 'Forum 已停用并切换为只读')
      await refresh()
    },
  })

  return (
    <div className="project-forum-panel">
      {missing && (
        <p className="missing-message">
          项目目录缺失。历史状态继续保留，恢复目录后才能配对 Forum
          或创建新会话。
        </p>
      )}

      {activeForum ? (
        <div className="active-forum">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <p className="font-semibold">{activeForum.name}</p>
              <p className="muted mt-1 font-mono text-xs">
                Discord {activeForum.discordId}
              </p>
            </div>
            <button
              type="button"
              className="button-secondary icon-label-button"
              disabled={disable.isPending}
              onClick={() => disable.mutate(activeForum.id)}
            >
              <Ban aria-hidden size={16} />
              {disable.isPending ? '停用中…' : '停用 Forum'}
            </button>
          </div>
          <ForumCollaborators
            projectId={project.id}
            forum={activeForum}
            ownerDiscordUserId={ownerDiscordUserId}
            members={members}
          />
        </div>
      ) : (
        <div className="new-forum-form">
          <label className="text-sm">
            新 Forum 名称
            <input
              className="field mt-1"
              value={name}
              placeholder={`${project.name}（可选）`}
              disabled={missing}
              onChange={(event) => setName(event.target.value)}
            />
          </label>
          <button
            type="button"
            className="button icon-label-button"
            disabled={missing || pair.isPending}
            onClick={() => pair.mutate({ mode: 'new' })}
          >
            <MessageSquarePlus aria-hidden size={16} />
            {pair.isPending ? '提交中…' : '创建 Forum'}
          </button>
        </div>
      )}

      {historicalForums.length > 0 && (
        <div className="forum-history">
          <p className="muted text-xs font-semibold uppercase">历史 Forum</p>
          <div className="mt-2 grid gap-2">
            {historicalForums.map((forum) => (
              <div className="forum-history-row" key={forum.id}>
                <div className="min-w-0">
                  <p className="truncate text-sm font-medium">{forum.name}</p>
                  <p className="muted truncate font-mono text-xs">
                    {forum.discordId}
                  </p>
                </div>
                {!activeForum && (
                  <button
                    type="button"
                    className="button-secondary icon-label-button"
                    disabled={missing || pair.isPending}
                    onClick={() =>
                      pair.mutate({ mode: 'restore', forumId: forum.id })
                    }
                  >
                    <RotateCcw aria-hidden size={15} />
                    恢复
                  </button>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function ForumCollaborators({
  projectId,
  forum,
  ownerDiscordUserId,
  members,
}: {
  projectId: string
  forum: WorkspaceForum
  ownerDiscordUserId: string
  members: DiscordMember[]
}) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [memberId, setMemberId] = useState('')
  const [accessLevel, setAccessLevel] = useState<'readonly' | 'operator'>(
    'readonly',
  )
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: ['workspace-cards'] })
  const save = useMutation({
    mutationFn: () =>
      api<void>(
        `/workspace-projects/${projectId}/forums/${forum.id}/collaborators/${memberId}`,
        {
          method: 'PUT',
          body: JSON.stringify({ accessLevel }),
        },
      ),
    onSuccess: async () => {
      setMemberId('')
      showToast('success', 'Forum 协作者权限已更新')
      await refresh()
    },
  })
  const remove = useMutation({
    mutationFn: (targetMemberId: string) =>
      api<void>(
        `/workspace-projects/${projectId}/forums/${forum.id}/collaborators/${targetMemberId}`,
        { method: 'DELETE' },
      ),
    onSuccess: async () => {
      showToast('success', 'Forum 协作者已移除')
      await refresh()
    },
  })
  const availableMembers = members.filter(
    (member) =>
      member.discordUserId !== ownerDiscordUserId &&
      !forum.collaborators.some(
        (collaborator) => collaborator.memberId === member.discordUserId,
      ),
  )

  return (
    <div className="forum-collaborators">
      <p className="text-sm font-semibold">协作者</p>
      <div className="collaborator-form mt-3">
        <select
          className="field"
          aria-label={`${forum.name} 协作者`}
          value={memberId}
          onChange={(event) => setMemberId(event.target.value)}
        >
          <option value="">选择成员</option>
          {availableMembers.map((member) => (
            <option key={member.discordUserId} value={member.discordUserId}>
              {member.displayName || member.username}
            </option>
          ))}
        </select>
        <select
          className="field"
          aria-label={`${forum.name} 权限`}
          value={accessLevel}
          onChange={(event) =>
            setAccessLevel(event.target.value as 'readonly' | 'operator')
          }
        >
          <option value="readonly">只读</option>
          <option value="operator">可操作</option>
        </select>
        <button
          type="button"
          className="button-secondary icon-label-button"
          disabled={!memberId || save.isPending}
          onClick={() => save.mutate()}
        >
          <UserPlus aria-hidden size={15} />
          授权
        </button>
      </div>
      <div className="mt-3 grid gap-2">
        {forum.collaborators.map((collaborator) => {
          const member = members.find(
            (item) => item.discordUserId === collaborator.memberId,
          )
          return (
            <div className="collaborator-row" key={collaborator.memberId}>
              <span className="truncate text-sm">
                {member?.displayName ||
                  member?.username ||
                  collaborator.memberId}
              </span>
              <span className="muted text-xs">
                {collaborator.accessLevel === 'operator' ? '可操作' : '只读'}
              </span>
              <button
                type="button"
                className="icon-button"
                title="移除协作者"
                aria-label={`移除 ${member?.displayName || collaborator.memberId}`}
                disabled={remove.isPending}
                onClick={() => remove.mutate(collaborator.memberId)}
              >
                <Trash2 aria-hidden size={15} />
              </button>
            </div>
          )
        })}
      </div>
    </div>
  )
}
