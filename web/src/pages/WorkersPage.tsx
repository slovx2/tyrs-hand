import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
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
  githubWorkerId?: string | null
  discordWorkerId?: string | null
}

export function WorkersPage() {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [name, setName] = useState('')
  const [githubRole, setGitHubRole] = useState(true)
  const [discordRole, setDiscordRole] = useState(true)
  const [capacity, setCapacity] = useState(6)
  const [token, setToken] = useState('')
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
          roles: [githubRole && 'github', discordRole && 'discord'].filter(
            Boolean,
          ),
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

      <div className="panel mt-8">
        <h2 className="text-xl font-semibold">默认 Placement</h2>
        <div className="mt-4 grid gap-4 sm:grid-cols-2">
          <WorkerSelect
            label="GitHub 默认 Worker"
            role="github"
            workers={workerItems}
            value={defaults.data?.githubWorkerId ?? ''}
            onChange={(value) =>
              saveDefaults.mutate({
                githubWorkerId: value || null,
                discordWorkerId: defaults.data?.discordWorkerId ?? null,
              })
            }
          />
          <WorkerSelect
            label="Discord 默认 Worker"
            role="discord"
            workers={workerItems}
            value={defaults.data?.discordWorkerId ?? ''}
            onChange={(value) =>
              saveDefaults.mutate({
                githubWorkerId: defaults.data?.githubWorkerId ?? null,
                discordWorkerId: value || null,
              })
            }
          />
        </div>
      </div>

      <form
        className="panel mt-6"
        onSubmit={(event) => {
          event.preventDefault()
          create.mutate()
        }}
      >
        <h2 className="text-xl font-semibold">注册新 Worker</h2>
        <div className="mt-4 grid gap-4 sm:grid-cols-4">
          <label>
            <span className="label">名称</span>
            <input
              value={name}
              onChange={(event) => setName(event.target.value)}
              required
            />
          </label>
          <label>
            <span className="label">并发上限</span>
            <input
              type="number"
              min={1}
              value={capacity}
              onChange={(event) => setCapacity(Number(event.target.value))}
            />
          </label>
          <label className="flex items-center gap-2 pt-7">
            <input
              type="checkbox"
              checked={githubRole}
              onChange={(e) => setGitHubRole(e.target.checked)}
            />
            GitHub
          </label>
          <label className="flex items-center gap-2 pt-7">
            <input
              type="checkbox"
              checked={discordRole}
              onChange={(e) => setDiscordRole(e.target.checked)}
            />
            Discord
          </label>
        </div>
        <button
          className="button mt-5"
          disabled={create.isPending || (!githubRole && !discordRole)}
        >
          创建并生成 Token
        </button>
      </form>

      <div className="mt-6 grid gap-4">
        {workerItems.map((worker) => (
          <article className="panel" key={worker.id}>
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div>
                <h2 className="text-lg font-semibold">{worker.name}</h2>
                <p className="muted mt-1 text-sm">
                  {worker.roles.join(' + ')} · 并发 {worker.maxConcurrentJobs} ·{' '}
                  {worker.status}
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
            </div>
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
      <select value={value} onChange={(event) => onChange(event.target.value)}>
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
