import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
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
              <span className="label">名称 <span className="required-mark">*</span></span>
              <input
                className="field mt-1"
                aria-label="名称"
                value={name}
                onChange={(event) => setName(event.target.value)}
                required
              />
            </label>
            <label>
              <span className="label">并发上限 <span className="required-mark">*</span></span>
              <input
                className="field mt-1"
                aria-label="并发上限"
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

      <div className="mt-6 grid gap-5">
        {workerItems.map((worker) => (
          <WorkerCard key={worker.id} worker={worker} isAdmin={isAdmin} action={action} />
        ))}
      </div>
      <WorkspaceManagement workers={workerItems} />
    </section>
  )
}

function WorkerCard({ worker, isAdmin, action }: { worker: Worker; isAdmin: boolean; action: { mutate: (variables: { worker: Worker; type: string }) => void } }) {
  const [tab, setTab] = useState<'overview' | 'codex' | 'workspace' | 'users'>('overview')
  return (
    <article className="panel worker-card">
      <div className="worker-card-header">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-lg font-semibold">{worker.name}</h2>
            <StatusBadge tone={worker.enabled ? 'success' : 'muted'}>{worker.enabled ? '启用' : '停用'}</StatusBadge>
            <StatusBadge tone={worker.status === 'online' ? 'success' : 'muted'}>{worker.status}</StatusBadge>
          </div>
          <p className="muted mt-1 text-sm">{worker.roles.includes('discord') ? 'Discord' : '无可用角色'} · 并发 {worker.maxConcurrentJobs}</p>
        </div>
        {isAdmin && (
          <div className="worker-actions">
            <button className="button-secondary" onClick={() => action.mutate({ worker, type: 'enroll' })}>轮换凭据</button>
            <button className="button-danger" onClick={() => confirmAction(`${worker.enabled ? '停用' : '启用'} Worker？`) && action.mutate({ worker, type: 'toggle' })}>{worker.enabled ? '停用' : '启用'}</button>
            <button className="button-danger" onClick={() => confirmAction('删除后无法恢复，确定删除此 Worker？') && action.mutate({ worker, type: 'delete' })}>删除</button>
          </div>
        )}
      </div>
      <div className="worker-badges">
        <StatusBadge tone={worker.heartbeatAt ? 'success' : 'muted'}>心跳 {worker.heartbeatAt ? '正常' : '暂无'}</StatusBadge>
        <StatusBadge tone={worker.metadata?.ssh?.status === 'online' ? 'success' : 'muted'}>SSH {worker.metadata?.ssh?.status ?? '未知'}</StatusBadge>
        <StatusBadge tone={worker.metadata?.host?.appServer === 'online' ? 'success' : 'muted'}>Codex {worker.metadata?.host?.appServer ?? '未知'}</StatusBadge>
        <StatusBadge tone={worker.metadata?.browser?.status === 'online' ? 'success' : 'muted'}>Chrome {worker.metadata?.browser?.status ?? '未知'}</StatusBadge>
      </div>
      <nav className="worker-tabs" aria-label={`${worker.name} 设置`}>
        {((isAdmin ? [['overview', '概览'], ['codex', 'Codex 配置'], ['workspace', 'Workspace'], ['users', '用户分配']] : [['overview', '概览'], ['codex', 'Codex 配置'], ['workspace', 'Workspace']]) as [typeof tab, string][]).map(([id, label]) => (
          <button key={id} className={tab === id ? 'is-active' : ''} onClick={() => setTab(id)}>{label}</button>
        ))}
      </nav>
      <div className="worker-tab-panel">
        {tab === 'overview' && <><p className="muted text-sm">最近心跳：{worker.heartbeatAt ?? '尚未连接'}</p>{worker.workerVersion && <p className="muted text-sm">Worker 版本：{worker.workerVersion}</p>}{worker.lastError && <p className="error-text mt-2">{worker.lastError}</p>}<CapabilityStatus worker={worker} /></>}
        {tab === 'codex' && <WorkerConfigPanel worker={worker} />}
        {tab === 'workspace' && <div className="muted text-sm">Workspace、项目与 Forum 管理位于本页下方，可按 Worker 过滤操作。</div>}
        {tab === 'users' && isAdmin && <WorkerUsersPanel worker={worker} />}
      </div>
    </article>
  )
}

function StatusBadge({ children, tone = 'muted' }: { children: ReactNode; tone?: 'success' | 'muted' }) {
  return <span className={`status-badge ${tone === 'success' ? 'is-success' : ''}`}>{children}</span>
}

function confirmAction(message: string): boolean {
  try {
    if (typeof window.confirm !== 'function') return true
    return window.confirm(message) !== false
  } catch {
    return true
  }
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
  const [baseUrl, setBaseUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [showKey, setShowKey] = useState(false)
  const config = useQuery({
    queryKey: ['worker-config', worker.id],
    queryFn: () =>
      api<{
        revision: string
        agents: string
        baseUrl: string
        envKey: string
        apiKeyConfigured: boolean
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
    onError: (error: Error) => showToast('error', error.message.includes('冲突') ? '配置已变化，请重新读取' : error.message),
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
        body: JSON.stringify({ revision, baseUrl, apiKey }),
      }),
    onSuccess: (result) => {
      setRevision(result.revision)
      setApiKey('')
      void config.refetch()
      showToast('success', 'Model Provider 已保存')
    },
    onError: (error: Error) => showToast('error', error.message.includes('冲突') ? '配置已变化，请重新读取' : error.message),
  })
  const clearAPIKey = useMutation<{ revision: string }, Error, void>({
    mutationFn: () => api<{ revision: string }>(`/workers/${worker.id}/config/provider`, { method: 'PUT', body: JSON.stringify({ revision, baseUrl, clearApiKey: true }) }),
    onSuccess: (result: { revision: string }) => { setRevision(result.revision); void config.refetch(); showToast('success', 'API Key 已清除') },
    onError: (error: Error) => showToast('error', error.message.includes('冲突') ? '配置已变化，请重新读取' : error.message),
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
      setBaseUrl(config.data.baseUrl ?? '')
      setApiKey('')
    }
  }, [config.data])
  return (
    <div className="grid gap-5">
      <div>
        <h3 className="font-semibold">Model Provider</h3>
        <p className="muted mt-1 text-sm">模型请求只使用此处配置的非 ChatGPT Provider。配置内容只保存到 Worker，Control 不保存、不回显。</p>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <label><span className="label">Base URL <span className="required-mark">*</span></span><input className="field mt-1" type="url" required value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://api.example.com/v1" /></label>
        <label><span className="label">API Key <span className="required-mark">*</span></span><div className="input-with-action mt-1"><input className="field" type={showKey ? 'text' : 'password'} value={apiKey} onChange={(event) => setApiKey(event.target.value)} placeholder={config.data?.apiKeyConfigured ? '留空保持原值' : '首次配置必填'} /><button type="button" className="button-ghost" onClick={() => setShowKey((value) => !value)}>{showKey ? '隐藏' : '显示'}</button></div>{config.data?.apiKeyConfigured && <span className="muted mt-1 block text-xs">当前状态：********（{config.data.envKey}）</span>}</label>
      </div>
      <div className="flex flex-wrap gap-2">
        <button className="button" onClick={() => saveProvider.mutate()} disabled={saveProvider.isPending || !config.data || !baseUrl || (!config.data.apiKeyConfigured && !apiKey)}>保存 Provider</button>
        {config.data?.apiKeyConfigured && <button className="button-danger" onClick={() => confirmAction('清除后模型请求将无法认证，确定继续？') && clearAPIKey.mutate()} disabled={clearAPIKey.isPending}>清除 API Key</button>}
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
          className="button-danger"
          onClick={() =>
            confirmAction('重启会影响当前 Codex 会话，继续吗？') &&
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
