import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Plus, RefreshCw, X } from 'lucide-react'
import { useMemo, useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'
import { DevelopmentEnvironmentSection } from './DevelopmentEnvironmentSection'
import type {
  DevelopmentEnvironmentList,
  DiscordMember,
} from './developmentEnvironmentTypes'

export function DevelopmentEnvironmentsPage() {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [createOpen, setCreateOpen] = useState(false)
  const environments = useQuery({
    queryKey: ['development-environments'],
    queryFn: () => api<DevelopmentEnvironmentList>('/development-environments'),
    refetchInterval: 5_000,
  })
  const members = useQuery({
    queryKey: ['discord-members'],
    queryFn: () => api<DiscordMember[]>('/discord/members'),
  })
  const items = useMemo(
    () => environments.data?.items ?? [],
    [environments.data?.items],
  )
  const existingOwners = new Set(
    items.map((environment) => environment.ownerDiscordUserId),
  )
  const eligibleMembers = (members.data ?? []).filter(
    (member) => !existingOwners.has(member.discordUserId),
  )
  const abnormalCount = items.filter(
    (environment) =>
      !['ready', 'running'].includes(environment.status) ||
      Boolean(environment.error || environment.projectScanError),
  ).length

  const refresh = async () => {
    const result = await Promise.all([
      environments.refetch(),
      members.refetch(),
    ])
    if (result.some((item) => item.error)) {
      showToast('error', '刷新开发环境失败')
      return
    }
    showToast('success', '开发环境已刷新')
  }

  return (
    <section>
      <div className="development-page-header">
        <div>
          <h1 className="text-3xl font-bold">开发环境</h1>
          <p className="muted mt-2">
            管理个人长期容器、自动发现的项目与 Discord Forum 配对。
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            className="button-secondary icon-label-button"
            title="刷新环境与项目状态"
            disabled={environments.isFetching || members.isFetching}
            onClick={() => void refresh()}
          >
            <RefreshCw
              aria-hidden
              size={16}
              className={environments.isFetching ? 'spin' : ''}
            />
            刷新
          </button>
          <button
            type="button"
            className="button icon-label-button"
            onClick={() => setCreateOpen(true)}
          >
            <Plus aria-hidden size={16} />
            创建环境
          </button>
        </div>
      </div>

      <div className="development-summary">
        <SummaryMetric label="环境" value={items.length} />
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

      {environments.isError && (
        <p role="alert" className="error-text mt-6">
          {environments.error.message}
        </p>
      )}
      <div className="development-environment-list">
        {items.map((environment) => (
          <DevelopmentEnvironmentSection
            key={environment.id}
            environment={environment}
            members={members.data ?? []}
          />
        ))}
        {!environments.isLoading && items.length === 0 && (
          <div className="development-empty">
            <p className="font-semibold">尚无长期开发环境</p>
            <p className="muted mt-1 text-sm">
              创建环境后，直接在容器的 workspaces 目录中建立项目。
            </p>
          </div>
        )}
      </div>

      {createOpen && (
        <CreateEnvironmentDialog
          members={eligibleMembers}
          onClose={() => setCreateOpen(false)}
          onCreated={async () => {
            setCreateOpen(false)
            await queryClient.invalidateQueries({
              queryKey: ['development-environments'],
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
    <div className={`development-metric ${warning ? 'is-warning' : ''}`}>
      {warning && <AlertTriangle aria-hidden size={16} />}
      <span className="muted text-sm">{label}</span>
      <strong>{value}</strong>
    </div>
  )
}

function CreateEnvironmentDialog({
  members,
  onClose,
  onCreated,
}: {
  members: DiscordMember[]
  onClose: () => void
  onCreated: () => Promise<void>
}) {
  const showToast = useUI((state) => state.showToast)
  const [ownerDiscordUserId, setOwnerDiscordUserId] = useState('')
  const create = useMutation({
    mutationFn: () =>
      api<{ id: string; operationId: string }>('/development-environments', {
        method: 'POST',
        body: JSON.stringify({ ownerDiscordUserId }),
      }),
    onSuccess: async () => {
      showToast('info', '开发环境创建已排队')
      await onCreated()
    },
  })

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={onClose}>
      <div
        className="modal-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="create-environment-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between gap-3">
          <div>
            <h2 id="create-environment-title" className="text-lg font-semibold">
              创建长期开发环境
            </h2>
            <p className="muted mt-1 text-sm">每位成员只能拥有一个环境。</p>
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
        {members.length === 0 && (
          <p className="muted mt-3 text-sm">没有可创建环境的活跃成员。</p>
        )}
        <div className="mt-6 flex justify-end gap-2">
          <button type="button" className="button-secondary" onClick={onClose}>
            取消
          </button>
          <button
            type="button"
            className="button icon-label-button"
            disabled={!ownerDiscordUserId || create.isPending}
            onClick={() => create.mutate()}
          >
            <Plus aria-hidden size={16} />
            {create.isPending ? '创建中…' : '创建环境'}
          </button>
        </div>
      </div>
    </div>
  )
}
