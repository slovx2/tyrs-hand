import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'
import { WorkspaceManagement } from './WorkspacesPage'

export interface Worker {
  id: string
  name: string
  roles: string[]
  enabled: boolean
  maxConcurrentJobs: number
  protocolVersion: number
  workerVersion?: string
  status: string
  heartbeatAt?: string
  lastError?: string
  sshHostKeyFingerprint?: string
  metadata?: {
    ssh?: {
      status?: string
      listenAddress?: string
    }
    outboundSSH?: {
      status?: string
      revision?: string
      credentialCount?: number
      hostCount?: number
      lastError?: string
    }
    host?: {
      home?: string
      codexHome?: string
      workspaceRoot?: string
      appServer?: string
    }
    browser?: {
      status?: string
      bridgeVersion?: string
      extensionVersion?: string
      chromeVersion?: string
      profile?: string
      tabCount?: number
      lastError?: string
    }
  }
}

interface Defaults {
  discordWorkerId?: string | null
}

export function WorkersPage() {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [name, setName] = useState('')
  const [capacity, setCapacity] = useState(6)
  const [token, setToken] = useState('')
  const me = useQuery({
    queryKey: ['me'],
    queryFn: () => api<{ role: 'admin' | 'user' }>('/auth/me'),
    retry: false,
  })
  const isAdmin = me.data?.role !== 'user'
  const workers = useQuery({
    queryKey: ['workers'],
    queryFn: () => api<{ items: Worker[] }>('/workers'),
  })
  const defaults = useQuery({
    queryKey: ['worker-defaults'],
    queryFn: () => api<Defaults>('/settings/workers'),
  })
  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['workers'] }),
      queryClient.invalidateQueries({ queryKey: ['worker-defaults'] }),
    ])
  }
  const create = useMutation({
    mutationFn: () =>
      api<{ enrollmentToken: string }>('/workers', {
        method: 'POST',
        body: JSON.stringify({
          name,
          roles: ['discord'],
          maxConcurrentJobs: capacity,
        }),
      }),
    onSuccess: async (result) => {
      setToken(result.enrollmentToken)
      setName('')
      await refresh()
      showToast('success', 'Worker 已创建')
    },
  })
  const action = useMutation({
    mutationFn: async ({ worker, type }: { worker: Worker; type: string }) => {
      if (type === 'enroll') {
        const result = await api<{ enrollmentToken: string }>(
          `/workers/${worker.id}/enrollments`,
          { method: 'POST' },
        )
        setToken(result.enrollmentToken)
      } else if (type === 'toggle') {
        await api<void>(`/workers/${worker.id}/enabled`, {
          method: 'PUT',
          body: JSON.stringify({ enabled: !worker.enabled }),
        })
      } else {
        await api<void>(`/workers/${worker.id}`, { method: 'DELETE' })
      }
    },
    onSuccess: refresh,
  })
  const saveDefaults = useMutation({
    mutationFn: (value: Defaults) =>
      api<void>('/settings/workers', {
        method: 'PUT',
        body: JSON.stringify(value),
      }),
    onSuccess: async () => {
      await refresh()
      showToast('success', '默认 Worker 已保存；已有资源不会迁移')
    },
  })
  const workerItems = workers.data?.items ?? []

  return (
    <section>
      <h1 className="text-3xl font-bold">Worker</h1>
      <p className="muted mt-2">
        每个 Worker 绑定一个宿主用户和真实 Codex Home，并主动通过 HTTPS
        领取任务。默认 Worker 只在新建项目或 Work Item 首次产生任务时冻结。
      </p>

      {token && (
        <div className="danger-note mt-6">
          <div className="font-medium">一次性注册 Token（15 分钟内有效）</div>
          <code className="mt-2 block break-all select-all">{token}</code>
          <button
            className="button-secondary mt-3"
            onClick={() => setToken('')}
          >
            我已保存
          </button>
        </div>
      )}

      {isAdmin && (
        <div className="panel mt-8">
          <h2 className="text-xl font-semibold">默认 Placement</h2>
          <div className="mt-4 grid gap-4 sm:grid-cols-1">
            <WorkerSelect
              label="Discord 默认 Worker"
              role="discord"
              workers={workerItems}
              value={defaults.data?.discordWorkerId ?? ''}
              onChange={(value) =>
                saveDefaults.mutate({
                  discordWorkerId: value || null,
                })
              }
            />
          </div>
        </div>
      )}

      {isAdmin && (
        <form
          className="panel mt-6"
          onSubmit={(event) => {
            event.preventDefault()
            create.mutate()
          }}
        >
          <h2 className="text-xl font-semibold">注册新 Worker</h2>
          <div className="mt-4 grid gap-4 sm:grid-cols-2">
            <label>
              <span className="label">名称</span>
              <input
                className="field mt-1"
                value={name}
                onChange={(event) => setName(event.target.value)}
                required
              />
            </label>
            <label>
              <span className="label">并发上限</span>
              <input
                className="field mt-1"
                type="number"
                min={1}
                value={capacity}
                onChange={(event) => setCapacity(Number(event.target.value))}
              />
            </label>
            <p className="muted pt-7 text-sm">角色：Discord</p>
          </div>
          <button className="button mt-5" disabled={create.isPending}>
            创建并生成 Token
          </button>
        </form>
      )}

      <div className="mt-6 grid gap-4">
        {workerItems.map((worker) => (
          <article className="panel" key={worker.id}>
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div>
                <h2 className="text-lg font-semibold">{worker.name}</h2>
                <p className="muted mt-1 text-sm">
                  {worker.roles.includes('discord') ? 'Discord' : '无可用角色'}{' '}
                  · 并发 {worker.maxConcurrentJobs} · {worker.status}
                  {worker.workerVersion
                    ? ` · Worker ${worker.workerVersion}`
                    : ''}
                </p>
                <p className="muted mt-1 text-xs">
                  最近心跳：{worker.heartbeatAt ?? '尚未连接'}
                </p>
                {worker.sshHostKeyFingerprint && (
                  <p className="muted mt-1 break-all text-xs">
                    机器指纹：{worker.sshHostKeyFingerprint}
                  </p>
                )}
                {worker.lastError && (
                  <p className="error-text mt-2">{worker.lastError}</p>
                )}
                <CapabilityStatus worker={worker} />
              </div>
              {isAdmin && (
                <div className="flex flex-wrap gap-2">
                  <button
                    className="button-secondary"
                    onClick={() => action.mutate({ worker, type: 'enroll' })}
                  >
                    轮换凭据
                  </button>
                  <button
                    className="button-secondary"
                    onClick={() => action.mutate({ worker, type: 'toggle' })}
                  >
                    {worker.enabled ? '停用' : '启用'}
                  </button>
                  <button
                    className="button-secondary"
                    onClick={() => action.mutate({ worker, type: 'delete' })}
                  >
                    删除
                  </button>
                </div>
              )}
            </div>
            {isAdmin && <WorkerUsersPanel worker={worker} />}
            <WorkerConfigPanel worker={worker} />
          </article>
        ))}
      </div>
      <WorkspaceManagement workers={workerItems} />
    </section>
  )
}

function CapabilityStatus({ worker }: { worker: Worker }) {
  const ssh = worker.metadata?.ssh
  const outboundSSH = worker.metadata?.outboundSSH
  const host = worker.metadata?.host
  const browser = worker.metadata?.browser
  if (!ssh && !outboundSSH && !host && !browser) return null
  return (
    <div className="mt-3 grid gap-1 text-xs">
      {ssh && (
        <p className="muted">
          内置 SSH：{ssh.status ?? 'unknown'} ·{' '}
          {ssh.listenAddress ?? '未知地址'}
        </p>
      )}
      {host && (
        <p className="muted">
          Codex：{host.appServer ?? 'unknown'} · Home {host.codexHome ?? '未知'}{' '}
          · 工作区 {host.workspaceRoot ?? '未知'}
        </p>
      )}
      {outboundSSH && (
        <p className="muted">
          出站 SSH：{outboundSSH.status ?? 'unknown'} ·{' '}
          {outboundSSH.hostCount ?? 0} 台主机 ·{' '}
          {outboundSSH.credentialCount ?? 0} 份凭证
          {outboundSSH.lastError ? ` · ${outboundSSH.lastError}` : ''}
        </p>
      )}
      {browser && (
        <p className="muted">
          Chrome：{browser.status ?? 'unknown'} ·{' '}
          {browser.profile ?? '未知 Profile'} · {browser.tabCount ?? 0} 个标签页
          {browser.lastError ? ` · ${browser.lastError}` : ''}
        </p>
      )}
    </div>
  )
}

function WorkerSelect({
  label,
  role,
  workers,
  value,
  onChange,
}: {
  label: string
  role: string
  workers: Worker[]
  value: string
  onChange: (value: string) => void
}) {
  return (
    <label>
      <span className="label">{label}</span>
      <select
        className="field mt-1"
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        <option value="">未设置</option>
        {workers
          .filter((worker) => worker.enabled && worker.roles.includes(role))
          .map((worker) => (
            <option value={worker.id} key={worker.id}>
              {worker.name}
            </option>
          ))}
      </select>
    </label>
  )
}

function WorkerConfigPanel({ worker }: { worker: Worker }) {
  const showToast = useUI((state) => state.showToast)
  const [agents, setAgents] = useState('')
  const [revision, setRevision] = useState('')
  const [provider, setProvider] = useState('')
  const config = useQuery({
    queryKey: ['worker-config', worker.id],
    queryFn: () =>
      api<{
        revision: string
        agents: string
        modelProvider: string
        modelProviders?: Record<string, Record<string, unknown>>
      }>(`/workers/${worker.id}/config`),
  })
  const oauth = useQuery({
    queryKey: ['worker-oauth', worker.id],
    queryFn: () =>
      api<{ status: string; userCode?: string; verificationUrl?: string }>(
        `/workers/${worker.id}/codex/oauth/devices`,
      ),
    refetchInterval: (query) =>
      query.state.data?.status === 'pending' ? 2000 : false,
  })
  const save = useMutation({
    mutationFn: () =>
      api<{ revision: string; agents: string }>(
        `/workers/${worker.id}/config/agents`,
        {
          method: 'PUT',
          body: JSON.stringify({ revision, content: agents }),
        },
      ),
    onSuccess: (result) => {
      setRevision(result.revision)
      showToast('success', 'AGENTS.md 已保存')
    },
  })
  const startOAuth = useMutation({
    mutationFn: () =>
      api(`/workers/${worker.id}/codex/oauth/devices`, { method: 'POST' }),
    onSuccess: () => oauth.refetch(),
  })
  const saveProvider = useMutation({
    mutationFn: () =>
      api<{ revision: string }>(`/workers/${worker.id}/config/provider`, {
        method: 'PUT',
        body: JSON.stringify({
          revision,
          modelProvider: provider,
          modelProviders: config.data?.modelProviders ?? {},
        }),
      }),
    onSuccess: (result) => {
      setRevision(result.revision)
      showToast('success', 'Model Provider 已保存')
    },
  })
  const restart = useMutation({
    mutationFn: () =>
      api(`/workers/${worker.id}/codex/restart`, { method: 'POST' }),
    onSuccess: () => showToast('success', '已请求重启 Codex'),
  })
  useEffect(() => {
    if (config.data) {
      setRevision(config.data.revision)
      setAgents(config.data.agents)
      setProvider(config.data.modelProvider)
    }
  }, [config.data])
  return (
    <div className="mt-5 grid gap-3 border-t pt-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="font-semibold">Codex 配置</h3>
        <label className="flex items-center gap-2 text-sm">
          <span className="label">Model Provider</span>
          <input
            className="field"
            value={provider}
            onChange={(event) => setProvider(event.target.value)}
            placeholder="provider-id"
          />
        </label>
      </div>
      <label>
        <span className="label">AGENTS.md（Worker 真相源）</span>
        <textarea
          className="field mt-1 min-h-40 font-mono text-xs leading-5"
          value={agents}
          onChange={(event) => setAgents(event.target.value)}
          rows={5}
        />
      </label>
      <div className="flex flex-wrap gap-2">
        <button
          className="button-secondary"
          onClick={() => save.mutate()}
          disabled={save.isPending || !config.data}
        >
          保存 AGENTS.md
        </button>
        <button
          className="button-secondary"
          onClick={() => saveProvider.mutate()}
          disabled={saveProvider.isPending || !config.data || !provider}
        >
          保存 Provider
        </button>
        <button
          className="button-secondary"
          onClick={() =>
            window.confirm('重启会影响当前 Codex 会话，继续吗？') &&
            restart.mutate()
          }
          disabled={restart.isPending}
        >
          重启 Codex
        </button>
        <button
          className="button-secondary"
          onClick={() => startOAuth.mutate()}
          disabled={startOAuth.isPending}
        >
          登录 ChatGPT 账号
        </button>
      </div>
      {oauth.data?.status === 'pending' && oauth.data.userCode && (
        <div className="danger-note">
          请打开{' '}
          <a href={oauth.data.verificationUrl} target="_blank" rel="noreferrer">
            {oauth.data.verificationUrl}
          </a>
          ，输入设备码 <code>{oauth.data.userCode}</code>。
        </div>
      )}
      {oauth.data?.status === 'authenticated' && (
        <p className="muted text-sm">
          ChatGPT OAuth 已登录；模型请求仍使用上方 Provider。
        </p>
      )}
    </div>
  )
}

function WorkerUsersPanel({ worker }: { worker: Worker }) {
  const users = useQuery({
    queryKey: ['users'],
    queryFn: () => api<{ items: { id: string; username: string }[] }>('/users'),
  })
  const assigned = useQuery({
    queryKey: ['worker-users', worker.id],
    queryFn: () =>
      api<{ items: { id: string; username: string }[] }>(
        `/workers/${worker.id}/users`,
      ),
  })
  const queryClient = useQueryClient()
  const update = useMutation({
    mutationFn: ({ userId, remove }: { userId: string; remove: boolean }) =>
      api<void>(`/workers/${worker.id}/users/${userId}`, {
        method: remove ? 'DELETE' : 'PUT',
      }),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ['worker-users', worker.id] }),
  })
  const assignedIDs = new Set(
    (assigned.data?.items ?? []).map((user) => user.id),
  )
  return (
    <div className="mt-4 border-t pt-4">
      <h3 className="font-semibold">用户分配</h3>
      <div className="mt-2 flex flex-wrap gap-2">
        {(users.data?.items ?? [])
          .filter((user) => user.username)
          .map((user) => {
            const isAssigned = assignedIDs.has(user.id)
            return (
              <button
                key={user.id}
                className="button-secondary"
                onClick={() =>
                  update.mutate({ userId: user.id, remove: isAssigned })
                }
                disabled={update.isPending}
              >
                {isAssigned ? `移除 ${user.username}` : `分配 ${user.username}`}
              </button>
            )
          })}
      </div>
    </div>
  )
}
