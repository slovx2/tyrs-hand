import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { api } from '../api/client'
import type { ListResponse } from '../api/client'
import { useUI } from '../state'

interface DiscordSettings {
  guildId: string
  enabled: boolean
  communityEnabled: boolean
  applicationId?: string
  botUserId?: string
  tokenConfigured: boolean
}

interface DiscordStatus {
  configured: boolean
  enabled: boolean
  gatewayStatus: string
  gatewayError?: string
  pendingOutbox: number
  failedOutbox: number
  pendingInitializationOperations: number
}

interface DiscordMember {
  guildId: string
  discordUserId: string
  username: string
  displayName: string
  bound: boolean
  githubLogin?: string
}

interface RepositoryRecord {
  id: string
  owner: string
  name: string
  enabled: boolean
}

interface DevelopmentForum {
  id: string
  name: string
  discordId: string
  repositoryId: string
  repository: string
  status: string
  branch: string
  dirty: boolean
  error?: string
}

interface DevelopmentEnvironment {
  id: string
  ownerDiscordUserId: string
  ownerName: string
  status: string
  imageRef: string
  imageId?: string
  runtimeUser?: string
  codexVersion?: string
  codexUserOverride: boolean
  error?: string
  sshPublicKey?: string
  sshFingerprint?: string
  sshPort?: number
  sshDiscordUserId?: string
  sshDisplayName?: string
  sshConfigRevision: number
  sshAppliedRevision: number
  daemonStatus: string
  daemonError?: string
  appServerStatus: string
  sshStatus: string
  relayStatus: string
  forums: DevelopmentForum[]
}

interface Preflight {
  guildId: string
  mode: 'incremental' | 'fresh'
  creates: string[]
  updates: string[]
  deletes: string[]
  conflicts: Array<{ name: string; reason: string }>
  missingPermissions: string[]
  channelCount: number
  safe: boolean
}

type SettingsInput = Pick<
  DiscordSettings,
  'guildId' | 'enabled' | 'applicationId' | 'botUserId'
> & { botToken?: string }

export function DiscordPage() {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const settings = useQuery({
    queryKey: ['discord-settings'],
    queryFn: () => api<DiscordSettings>('/settings/discord'),
  })
  const status = useQuery({
    queryKey: ['discord-status'],
    queryFn: () => api<DiscordStatus>('/discord/status'),
    refetchInterval: (query) =>
      (query.state.data?.pendingInitializationOperations ?? 0) > 0
        ? 2_000
        : 60_000,
  })
  const members = useQuery({
    queryKey: ['discord-members'],
    queryFn: () => api<DiscordMember[]>('/discord/members'),
    enabled: settings.data?.tokenConfigured === true,
  })
  const repositories = useQuery({
    queryKey: ['repositories'],
    queryFn: () => api<ListResponse<RepositoryRecord>>('/repositories'),
  })
  const environments = useQuery({
    queryKey: ['discord-development-environments'],
    queryFn: () =>
      api<DevelopmentEnvironment[]>('/discord/development-environments'),
    enabled: settings.data?.tokenConfigured === true,
    refetchInterval: 5_000,
  })
  useEffect(() => {
    if (status.data?.pendingInitializationOperations === 0) {
      void queryClient.invalidateQueries({
        queryKey: ['discord-development-environments'],
      })
    }
  }, [queryClient, status.data?.pendingInitializationOperations])
  const form = useForm<SettingsInput>({
    values: settings.data
      ? {
          guildId: settings.data.guildId,
          enabled: settings.data.enabled,
          applicationId: settings.data.applicationId,
          botUserId: settings.data.botUserId,
        }
      : undefined,
  })
  const save = useMutation({
    mutationFn: (values: SettingsInput) =>
      api<void>('/settings/discord', {
        method: 'PUT',
        body: JSON.stringify(values),
      }),
    onSuccess: async () => {
      form.setValue('botToken', '')
      await queryClient.invalidateQueries({ queryKey: ['discord-settings'] })
      await queryClient.invalidateQueries({ queryKey: ['discord-status'] })
      showToast('success', 'Discord 设置已保存')
    },
  })

  return (
    <section>
      <h1 className="text-3xl font-bold">Discord</h1>
      <p className="muted mt-2">
        私有 Server、个人 Codex Forum 与 GitHub 任务投影。
      </p>

      <div className="mt-8 grid gap-4 sm:grid-cols-4">
        <StatusMetric
          label="Gateway"
          value={status.data?.gatewayStatus ?? '—'}
        />
        <StatusMetric
          label="Outbox"
          value={status.data?.pendingOutbox ?? '—'}
        />
        <StatusMetric label="失败" value={status.data?.failedOutbox ?? '—'} />
        <StatusMetric
          label="初始化"
          value={status.data?.pendingInitializationOperations ?? '—'}
        />
      </div>
      {status.data?.gatewayError && (
        <p role="alert" className="error-text mt-3">
          {status.data.gatewayError}
        </p>
      )}

      <form
        className="panel mt-6"
        onSubmit={form.handleSubmit((values) => save.mutate(values))}
      >
        <h2 className="text-xl font-semibold">连接设置</h2>
        <div className="mt-5 grid gap-4 sm:grid-cols-2">
          <label className="text-sm">
            Server ID
            <input
              className="field mt-1"
              required
              {...form.register('guildId')}
            />
          </label>
          <label className="text-sm">
            Application ID
            <input className="field mt-1" {...form.register('applicationId')} />
          </label>
          <label className="text-sm">
            Bot User ID
            <input className="field mt-1" {...form.register('botUserId')} />
          </label>
          <label className="text-sm">
            Bot Token
            <input
              className="field mt-1"
              type="password"
              autoComplete="new-password"
              placeholder={
                settings.data?.tokenConfigured ? '已配置，留空则不变' : ''
              }
              {...form.register('botToken')}
            />
          </label>
        </div>
        <label className="mt-4 flex items-center gap-2 text-sm">
          <input type="checkbox" {...form.register('enabled')} />
          启用 Discord 常驻服务
        </label>
        <p className="muted mt-3 text-xs">
          第一阶段只支持启用 Community 的单个私有 Server。
        </p>
        <button className="button mt-5" disabled={save.isPending}>
          {save.isPending ? '保存中…' : '保存 Discord 设置'}
        </button>
      </form>

      <InitializationPanel
        key={settings.data?.guildId ?? ''}
        guildId={settings.data?.guildId ?? ''}
      />

      <div className="panel mt-6">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2 className="text-xl font-semibold">成员与开发 Forum</h2>
            <p className="muted mt-1 text-sm">
              每个 Forum 固定绑定一个仓库；同一成员的多个仓库共享长期开发容器与
              Home。
            </p>
          </div>
          <button
            type="button"
            className="button-secondary"
            disabled={members.isFetching}
            onClick={async () => {
              const result = await members.refetch()
              if (result.error) {
                showToast('error', result.error.message)
                return
              }
              showToast('success', '成员列表已刷新')
            }}
          >
            {members.isFetching ? '刷新中…' : '刷新成员'}
          </button>
        </div>
        <div className="danger-note mt-4">
          拥有 Discord Administrator
          权限的成员可以绕过频道权限覆盖。可操作协作者能够驱动 Agent，且同一
          owner 的容器与 Home 会跨 Forum 复用，因此只应授权给该 owner
          信任的成员。
        </div>
        <div className="mt-5 grid gap-4">
          {(members.data ?? []).map((member) => (
            <MemberRow
              key={member.discordUserId}
              member={member}
              repositories={(repositories.data?.items ?? []).filter(
                (repository) => repository.enabled,
              )}
            />
          ))}
          {members.data?.length === 0 && (
            <p className="muted text-sm">暂无已同步成员</p>
          )}
        </div>
      </div>
      <DevelopmentEnvironmentPanel
        environments={environments.data ?? []}
        members={members.data ?? []}
      />
    </section>
  )
}

function StatusMetric({
  label,
  value,
}: {
  label: string
  value: string | number
}) {
  return (
    <div className="panel">
      <div className="muted text-xs font-medium uppercase">{label}</div>
      <div className="mt-2 truncate text-lg font-semibold">{value}</div>
    </div>
  )
}

function InitializationPanel({ guildId }: { guildId: string }) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [mode, setMode] = useState<'incremental' | 'fresh'>('incremental')
  const [confirmation, setConfirmation] = useState('')
  const [preflight, setPreflight] = useState<Preflight>()
  const check = useMutation({
    mutationFn: () =>
      api<Preflight>('/discord/initializations/preflight', {
        method: 'POST',
        body: JSON.stringify({ mode }),
      }),
    onSuccess: (value) => {
      setPreflight(value)
      showToast(
        value.safe ? 'success' : 'warning',
        value.safe ? '初始化预检已通过' : '初始化预检存在冲突',
      )
    },
  })
  const initialize = useMutation({
    mutationFn: () =>
      api<{ id: string }>('/discord/initializations', {
        method: 'POST',
        body: JSON.stringify({ mode, confirmation }),
      }),
    onSuccess: async () => {
      showToast('info', '初始化请求已提交，状态会自动刷新')
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['discord-status'] }),
        queryClient.invalidateQueries({
          queryKey: ['discord-development-environments'],
        }),
      ])
    },
  })
  const expected = `DELETE ALL CHANNELS ${guildId}`
  const confirmationValid = mode === 'incremental' || confirmation === expected
  return (
    <div className="panel mt-6">
      <h2 className="text-xl font-semibold">Server 初始化</h2>
      <div
        className="theme-toggle mt-4 max-w-sm"
        role="group"
        aria-label="初始化模式"
      >
        {(['incremental', 'fresh'] as const).map((value) => (
          <button
            key={value}
            type="button"
            className={`theme-option ${mode === value ? 'theme-option-active' : ''}`}
            aria-pressed={mode === value}
            onClick={() => {
              setMode(value)
              setPreflight(undefined)
            }}
          >
            {value === 'incremental' ? '增量初始化' : '全新初始化'}
          </button>
        ))}
      </div>
      {mode === 'fresh' && (
        <label className="mt-4 block text-sm">
          输入确认指令 <code>{expected}</code>
          <input
            className="field mt-1 font-mono"
            value={confirmation}
            onChange={(event) => setConfirmation(event.target.value)}
          />
        </label>
      )}
      <div className="mt-4 flex flex-wrap gap-3">
        <button
          type="button"
          className="button-secondary"
          onClick={() => check.mutate()}
          disabled={!guildId || check.isPending}
        >
          {check.isPending ? '预检中…' : '执行预检'}
        </button>
        <button
          type="button"
          className="button"
          onClick={() => initialize.mutate()}
          disabled={
            !preflight?.safe || !confirmationValid || initialize.isPending
          }
        >
          {initialize.isPending ? '提交中…' : '开始初始化'}
        </button>
      </div>
      {initialize.data && (
        <p className="mt-3 text-sm">初始化操作已创建：{initialize.data.id}</p>
      )}
      {preflight && <PreflightResult value={preflight} />}
    </div>
  )
}

function PreflightResult({ value }: { value: Preflight }) {
  return (
    <div
      className="mt-5 border-t pt-4"
      style={{ borderColor: 'var(--border)' }}
    >
      <p className="text-sm font-semibold">
        {value.safe ? '预检通过' : '预检存在冲突'}
      </p>
      <p className="muted mt-1 text-sm">
        创建 {value.creates.length} · 校正 {value.updates.length} · 删除{' '}
        {value.deletes.length} · 当前频道 {value.channelCount}
      </p>
      {value.conflicts.map((conflict) => (
        <p
          className="error-text mt-2 text-sm"
          key={`${conflict.name}-${conflict.reason}`}
        >
          {conflict.name}：{conflict.reason}
        </p>
      ))}
    </div>
  )
}

function MemberRow({
  member,
  repositories,
}: {
  member: DiscordMember
  repositories: RepositoryRecord[]
}) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [repositoryId, setRepositoryId] = useState('')
  const [name, setName] = useState('')
  const createForum = useMutation({
    mutationFn: () =>
      api<{ id: string }>(`/discord/members/${member.discordUserId}/forum`, {
        method: 'POST',
        body: JSON.stringify({ repositoryId, name }),
      }),
    onSuccess: async () => {
      setName('')
      showToast('info', '开发 Forum 创建请求已提交，列表会自动刷新')
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['discord-status'] }),
        queryClient.invalidateQueries({
          queryKey: ['discord-development-environments'],
        }),
      ])
    },
  })
  return (
    <div
      className="border-t pt-4 first:border-t-0 first:pt-0"
      style={{ borderColor: 'var(--border)' }}
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="truncate font-semibold">
            {member.displayName || member.username}
          </p>
          <p className="muted truncate text-xs">
            {member.bound ? `GitHub: ${member.githubLogin}` : '未绑定 GitHub'}
          </p>
        </div>
      </div>
      <div className="mt-3 grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
        <select
          className="field"
          aria-label={`${member.displayName} 开发仓库`}
          value={repositoryId}
          onChange={(event) => setRepositoryId(event.target.value)}
        >
          <option value="">选择仓库</option>
          {repositories.map((repository) => (
            <option key={repository.id} value={repository.id}>
              {repository.owner}/{repository.name}
            </option>
          ))}
        </select>
        <input
          className="field"
          aria-label={`${member.displayName} Forum 名称`}
          placeholder="Forum 名称（可选）"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
        <button
          type="button"
          className="button-secondary"
          disabled={!member.bound || !repositoryId || createForum.isPending}
          onClick={() => createForum.mutate()}
        >
          {createForum.isPending ? '提交中…' : '创建开发 Forum'}
        </button>
      </div>
    </div>
  )
}

function DevelopmentEnvironmentPanel({
  environments,
  members,
}: {
  environments: DevelopmentEnvironment[]
  members: DiscordMember[]
}) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const rebase = useMutation({
    mutationFn: (id: string) =>
      api<void>(`/discord/development-environments/${id}/rebase`, {
        method: 'POST',
      }),
    onSuccess: async () => {
      showToast('info', '环境 Rebase 已排队，将切换到当前官方开发镜像')
      await queryClient.invalidateQueries({
        queryKey: ['discord-development-environments'],
      })
    },
  })
  return (
    <div className="panel mt-6">
      <h2 className="text-xl font-semibold">长期开发环境</h2>
      <p className="muted mt-1 text-sm">
        同一 Discord 用户只有一个容器和 Home；不同仓库、不同 Forum 各自使用独立
        clone。Rebase 会保留 Home 和工作区，但会清除通过 sudo
        安装到系统层的软件。
      </p>
      <div className="mt-5 grid gap-5">
        {environments.map((environment) => (
          <div
            key={environment.id}
            className="border-t pt-4 first:border-t-0 first:pt-0"
            style={{ borderColor: 'var(--border)' }}
          >
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <p className="font-semibold">{environment.ownerName}</p>
                <p className="muted mt-1 text-xs">
                  {environment.status} · 镜像 {environment.imageRef || '待配置'}{' '}
                  · 用户 {environment.runtimeUser || '待启动'} · Codex{' '}
                  {environment.codexVersion || '待上报'}
                  {environment.codexUserOverride
                    ? '（用户覆盖）'
                    : '（镜像内置）'}
                </p>
              </div>
              <button
                type="button"
                className="button-secondary"
                disabled={rebase.isPending}
                onClick={() => rebase.mutate(environment.id)}
              >
                {rebase.isPending ? '提交中…' : 'Rebase 到当前官方镜像'}
              </button>
            </div>
            {environment.error && (
              <p className="error-text mt-2 text-sm">{environment.error}</p>
            )}
            <DevelopmentEnvironmentSSHForm
              key={`${environment.id}:${environment.sshConfigRevision}:${environment.sshAppliedRevision}`}
              environment={environment}
              members={members}
            />
            <div className="mt-3 grid gap-3">
              {environment.forums.map((forum) => (
                <DevelopmentForumRow
                  key={forum.id}
                  forum={forum}
                  ownerUserId={environment.ownerDiscordUserId}
                  members={members}
                />
              ))}
            </div>
          </div>
        ))}
        {environments.length === 0 && (
          <p className="muted text-sm">尚未创建开发 Forum</p>
        )}
      </div>
    </div>
  )
}

function DevelopmentEnvironmentSSHForm({
  environment,
  members,
}: {
  environment: DevelopmentEnvironment
  members: DiscordMember[]
}) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [publicKey, setPublicKey] = useState(environment.sshPublicKey ?? '')
  const [port, setPort] = useState(
    environment.sshPort ? String(environment.sshPort) : '',
  )
  const [discordUserId, setDiscordUserId] = useState(
    environment.sshDiscordUserId ?? '',
  )
  const refresh = async () => {
    await queryClient.invalidateQueries({
      queryKey: ['discord-development-environments'],
    })
  }
  const save = useMutation({
    mutationFn: () =>
      api<void>(`/discord/development-environments/${environment.id}/ssh`, {
        method: 'PUT',
        body: JSON.stringify({
          publicKey: publicKey.trim(),
          port: Number(port),
          discordUserId,
        }),
      }),
    onSuccess: async () => {
      showToast('info', 'SSH 配置已排队生效')
      await refresh()
    },
  })
  const disable = useMutation({
    mutationFn: () =>
      api<void>(`/discord/development-environments/${environment.id}/ssh`, {
        method: 'DELETE',
      }),
    onSuccess: async () => {
      setPublicKey('')
      setPort('')
      setDiscordUserId('')
      showToast('info', 'SSH 停用请求已排队')
      await refresh()
    },
  })
  const pending =
    environment.sshConfigRevision !== environment.sshAppliedRevision
  return (
    <div
      className="mt-3 rounded border p-3"
      style={{ borderColor: 'var(--border)' }}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm font-medium">Codex Desktop SSH</p>
        <p className="muted text-xs">
          总体 {environment.daemonStatus} · App Server{' '}
          {environment.appServerStatus} · Relay {environment.relayStatus} · SSH{' '}
          {environment.sshStatus}
          {pending ? ' · 等待生效' : ''}
          {environment.sshDisplayName
            ? ` · 身份 ${environment.sshDisplayName}`
            : ''}
          {environment.sshFingerprint ? ` · ${environment.sshFingerprint}` : ''}
        </p>
      </div>
      <div className="mt-3 grid gap-2 sm:grid-cols-[minmax(0,1fr)_140px_180px_auto_auto]">
        <label className="text-sm">
          <span className="sr-only">{environment.ownerName} SSH 公钥</span>
          <textarea
            className="field min-h-20"
            aria-label={`${environment.ownerName} SSH 公钥`}
            placeholder="ssh-ed25519 …"
            value={publicKey}
            onChange={(event) => setPublicKey(event.target.value)}
          />
        </label>
        <label className="text-sm">
          <span className="sr-only">{environment.ownerName} SSH 端口</span>
          <input
            className="field"
            aria-label={`${environment.ownerName} SSH 端口`}
            type="number"
            min={1}
            max={65535}
            placeholder="宿主机端口"
            value={port}
            onChange={(event) => setPort(event.target.value)}
          />
        </label>
        <label className="text-sm">
          <span className="sr-only">
            {environment.ownerName} Desktop 发言身份
          </span>
          <select
            className="field"
            aria-label={`${environment.ownerName} Desktop 发言身份`}
            value={discordUserId}
            onChange={(event) => setDiscordUserId(event.target.value)}
          >
            <option value="">选择发言身份</option>
            {members.map((member) => (
              <option key={member.discordUserId} value={member.discordUserId}>
                {member.displayName || member.username}
              </option>
            ))}
          </select>
        </label>
        <button
          type="button"
          className="button-secondary"
          disabled={
            !publicKey.trim() || !port || !discordUserId || save.isPending
          }
          onClick={() => save.mutate()}
        >
          {save.isPending ? '保存中…' : '保存 SSH'}
        </button>
        <button
          type="button"
          className="button-secondary"
          disabled={!environment.sshPort || disable.isPending}
          onClick={() => disable.mutate()}
        >
          {disable.isPending ? '停用中…' : '停用 SSH'}
        </button>
      </div>
      {environment.daemonError && (
        <p className="error-text mt-2 text-xs">{environment.daemonError}</p>
      )}
    </div>
  )
}

function DevelopmentForumRow({
  forum,
  ownerUserId,
  members,
}: {
  forum: DevelopmentForum
  ownerUserId: string
  members: DiscordMember[]
}) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [target, setTarget] = useState('')
  const [level, setLevel] = useState<'readonly' | 'operator'>('readonly')
  const access = useMutation({
    mutationFn: (method: 'PUT' | 'DELETE') =>
      api<void>(`/discord/forums/${forum.id}/access/${target}`, {
        method,
        body:
          method === 'PUT' ? JSON.stringify({ accessLevel: level }) : undefined,
      }),
    onSuccess: (_, method) => {
      showToast(
        'success',
        method === 'PUT' ? 'Forum 访问权限已更新' : 'Forum 访问权限已移除',
      )
    },
  })
  const remove = useMutation({
    mutationFn: async () => {
      const preflight = await api<{
        dirty: boolean
        unpushed: boolean
        active: boolean
        deletesEnvironment: boolean
        confirmation: string
      }>(`/discord/development-forums/${forum.id}/delete-preflight`)
      const warning = `${preflight.active ? '仍有任务排队或运行，当前不能删除。' : ''}${preflight.dirty ? '存在未提交修改。' : ''}${preflight.unpushed ? '存在未推送提交。' : ''}${preflight.deletesEnvironment ? '这也是最后一个 Forum，将删除整个环境和 Home。' : ''}`
      if (preflight.active) return 'blocked' as const
      const confirmation = window.prompt(
        `${warning}\n请输入：${preflight.confirmation}`,
      )
      if (confirmation !== preflight.confirmation) return 'cancelled' as const
      await api<{ id: string }>(
        `/discord/development-forums/${forum.id}/delete`,
        { method: 'POST', body: JSON.stringify({ confirmation }) },
      )
      return 'submitted' as const
    },
    onSuccess: async (result) => {
      if (result === 'blocked') {
        showToast('warning', '该 Forum 仍有任务排队或运行，暂时不能删除')
        return
      }
      if (result !== 'submitted') return
      showToast('info', 'Forum 删除请求已提交，列表会自动刷新')
      await queryClient.invalidateQueries({
        queryKey: ['discord-development-environments'],
      })
    },
  })
  return (
    <div
      className="rounded border p-3"
      style={{ borderColor: 'var(--border)' }}
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <p className="font-medium">{forum.name}</p>
          <p className="muted text-xs">
            {forum.repository} · {forum.status} · {forum.branch}{' '}
            {forum.dirty ? '· 有未提交修改' : ''}
          </p>
        </div>
        <button
          type="button"
          className="button-secondary"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {remove.isPending ? '处理中…' : '删除'}
        </button>
      </div>
      <div className="mt-3 grid gap-2 sm:grid-cols-[1fr_130px_auto_auto]">
        <select
          className="field"
          value={target}
          onChange={(event) => setTarget(event.target.value)}
          aria-label={`${forum.name} 授权成员`}
        >
          <option value="">选择协作者</option>
          {members
            .filter((member) => member.discordUserId !== ownerUserId)
            .map((member) => (
              <option key={member.discordUserId} value={member.discordUserId}>
                {member.displayName || member.username}
              </option>
            ))}
        </select>
        <select
          className="field"
          value={level}
          aria-label={`${forum.name} 权限`}
          onChange={(event) =>
            setLevel(event.target.value as 'readonly' | 'operator')
          }
        >
          <option value="readonly">只读</option>
          <option value="operator">可操作</option>
        </select>
        <button
          type="button"
          className="button-secondary"
          disabled={!target || access.isPending}
          onClick={() => access.mutate('PUT')}
        >
          {access.isPending && access.variables === 'PUT' ? '授权中…' : '授权'}
        </button>
        <button
          type="button"
          className="button-secondary"
          disabled={!target || access.isPending}
          onClick={() => access.mutate('DELETE')}
        >
          {access.isPending && access.variables === 'DELETE'
            ? '移除中…'
            : '移除'}
        </button>
      </div>
    </div>
  )
}
