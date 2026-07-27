import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Play, Save, Search } from 'lucide-react'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { api } from '../api/client'
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
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['discord-settings'] }),
        queryClient.invalidateQueries({ queryKey: ['discord-status'] }),
      ])
      showToast('success', 'Discord 设置已保存')
    },
  })

  return (
    <section>
      <h1 className="text-3xl font-bold">Discord</h1>
      <p className="muted mt-2">管理 Bot 连接、消息投递与 Server 初始化。</p>

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
        <button className="button mt-5 gap-2" disabled={save.isPending}>
          <Save aria-hidden size={16} />
          {save.isPending ? '保存中…' : '保存 Discord 设置'}
        </button>
      </form>

      <InitializationPanel
        key={settings.data?.guildId ?? ''}
        guildId={settings.data?.guildId ?? ''}
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
      await queryClient.invalidateQueries({ queryKey: ['discord-status'] })
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
          className="button-secondary gap-2"
          onClick={() => check.mutate()}
          disabled={!guildId || check.isPending}
        >
          <Search aria-hidden size={16} />
          {check.isPending ? '预检中…' : '执行预检'}
        </button>
        <button
          type="button"
          className="button gap-2"
          onClick={() => initialize.mutate()}
          disabled={
            !preflight?.safe || !confirmationValid || initialize.isPending
          }
        >
          <Play aria-hidden size={16} />
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
