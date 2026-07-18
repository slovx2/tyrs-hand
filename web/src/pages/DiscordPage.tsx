import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { api } from '../api/client'

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
  forumId?: string
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
  const settings = useQuery({
    queryKey: ['discord-settings'],
    queryFn: () => api<DiscordSettings>('/settings/discord'),
  })
  const status = useQuery({
    queryKey: ['discord-status'],
    queryFn: () => api<DiscordStatus>('/discord/status'),
    refetchInterval: 60_000,
  })
  const members = useQuery({
    queryKey: ['discord-members'],
    queryFn: () => api<DiscordMember[]>('/discord/members'),
    enabled: settings.data?.tokenConfigured === true,
  })
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
    },
  })

  return (
    <section className="max-w-5xl">
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
              autoComplete="off"
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
        {save.error && <p className="error-text mt-3">{save.error.message}</p>}
        <button className="button mt-5" disabled={save.isPending}>
          保存 Discord 设置
        </button>
      </form>

      <InitializationPanel
        key={settings.data?.guildId ?? ''}
        guildId={settings.data?.guildId ?? ''}
      />

      <div className="panel mt-6">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2 className="text-xl font-semibold">成员与个人 Forum</h2>
            <p className="muted mt-1 text-sm">
              个人 Forum 只由管理员创建，不会自动创建。
            </p>
          </div>
          <button
            type="button"
            className="button-secondary"
            onClick={() => void members.refetch()}
          >
            刷新成员
          </button>
        </div>
        <div className="danger-note mt-4">
          拥有 Discord Administrator
          权限的成员可以绕过频道权限覆盖。后台授权无法限制这种平台级能力。
        </div>
        <div className="mt-5 grid gap-4">
          {(members.data ?? []).map((member) => (
            <MemberRow
              key={member.discordUserId}
              member={member}
              members={members.data ?? []}
            />
          ))}
          {members.data?.length === 0 && (
            <p className="muted text-sm">暂无已同步成员</p>
          )}
        </div>
      </div>
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
  const [mode, setMode] = useState<'incremental' | 'fresh'>('incremental')
  const [confirmation, setConfirmation] = useState('')
  const [preflight, setPreflight] = useState<Preflight>()
  const check = useMutation({
    mutationFn: () =>
      api<Preflight>('/discord/initializations/preflight', {
        method: 'POST',
        body: JSON.stringify({ mode }),
      }),
    onSuccess: setPreflight,
  })
  const initialize = useMutation({
    mutationFn: () =>
      api<{ id: string }>('/discord/initializations', {
        method: 'POST',
        body: JSON.stringify({ mode, confirmation }),
      }),
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
          执行预检
        </button>
        <button
          type="button"
          className="button"
          onClick={() => initialize.mutate()}
          disabled={
            !preflight?.safe || !confirmationValid || initialize.isPending
          }
        >
          开始初始化
        </button>
      </div>
      {(check.error || initialize.error) && (
        <p className="error-text mt-3">
          {(check.error ?? initialize.error)?.message}
        </p>
      )}
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
  members,
}: {
  member: DiscordMember
  members: DiscordMember[]
}) {
  const queryClient = useQueryClient()
  const [target, setTarget] = useState('')
  const [level, setLevel] = useState<'readonly' | 'operator'>('readonly')
  const createForum = useMutation({
    mutationFn: () =>
      api<{ id: string }>(`/discord/members/${member.discordUserId}/forum`, {
        method: 'POST',
      }),
    onSuccess: () =>
      void queryClient.invalidateQueries({ queryKey: ['discord-members'] }),
  })
  const access = useMutation({
    mutationFn: (method: 'PUT' | 'DELETE') =>
      api<void>(`/discord/forums/${member.forumId}/access/${target}`, {
        method,
        body:
          method === 'PUT' ? JSON.stringify({ accessLevel: level }) : undefined,
      }),
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
            {member.bound ? `GitHub: ${member.githubLogin}` : '未绑定 GitHub'} ·{' '}
            {member.forumId ? '已创建 Forum' : '未创建 Forum'}
          </p>
        </div>
        {!member.forumId && (
          <button
            type="button"
            className="button-secondary"
            disabled={createForum.isPending}
            onClick={() => createForum.mutate()}
          >
            创建个人 Forum
          </button>
        )}
      </div>
      {member.forumId && (
        <div className="mt-3 grid gap-2 sm:grid-cols-[1fr_150px_auto_auto]">
          <select
            className="field"
            aria-label={`${member.displayName} 授权成员`}
            value={target}
            onChange={(event) => setTarget(event.target.value)}
          >
            <option value="">选择其他成员</option>
            {members
              .filter(
                (candidate) => candidate.discordUserId !== member.discordUserId,
              )
              .map((candidate) => (
                <option
                  key={candidate.discordUserId}
                  value={candidate.discordUserId}
                >
                  {candidate.displayName || candidate.username}
                </option>
              ))}
          </select>
          <select
            className="field"
            aria-label={`${member.displayName} 权限`}
            value={level}
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
            授权
          </button>
          <button
            type="button"
            className="button-secondary"
            disabled={!target || access.isPending}
            onClick={() => access.mutate('DELETE')}
          >
            移除
          </button>
        </div>
      )}
      {(createForum.error || access.error) && (
        <p className="error-text mt-2 text-sm">
          {(createForum.error ?? access.error)?.message}
        </p>
      )}
    </div>
  )
}
