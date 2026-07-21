import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'

interface ExecutionNode {
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
}

interface Defaults {
  githubNodeId?: string | null
  discordNodeId?: string | null
}

export function ExecutionNodesPage() {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [name, setName] = useState('')
  const [githubRole, setGitHubRole] = useState(true)
  const [discordRole, setDiscordRole] = useState(true)
  const [capacity, setCapacity] = useState(6)
  const [token, setToken] = useState('')
  const nodes = useQuery({
    queryKey: ['execution-nodes'],
    queryFn: () => api<{ items: ExecutionNode[] }>('/execution-nodes'),
  })
  const defaults = useQuery({
    queryKey: ['execution-defaults'],
    queryFn: () => api<Defaults>('/settings/execution'),
  })
  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['execution-nodes'] }),
      queryClient.invalidateQueries({ queryKey: ['execution-defaults'] }),
    ])
  }
  const create = useMutation({
    mutationFn: () =>
      api<{ enrollmentToken: string }>('/execution-nodes', {
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
      showToast('success', '执行节点已创建')
    },
  })
  const action = useMutation({
    mutationFn: async ({
      node,
      type,
    }: {
      node: ExecutionNode
      type: string
    }) => {
      if (type === 'enroll') {
        const result = await api<{ enrollmentToken: string }>(
          `/execution-nodes/${node.id}/enrollments`,
          { method: 'POST' },
        )
        setToken(result.enrollmentToken)
      } else if (type === 'toggle') {
        await api<void>(`/execution-nodes/${node.id}/enabled`, {
          method: 'PUT',
          body: JSON.stringify({ enabled: !node.enabled }),
        })
      } else {
        await api<void>(`/execution-nodes/${node.id}`, { method: 'DELETE' })
      }
    },
    onSuccess: refresh,
  })
  const saveDefaults = useMutation({
    mutationFn: (value: Defaults) =>
      api<void>('/settings/execution', {
        method: 'PUT',
        body: JSON.stringify(value),
      }),
    onSuccess: async () => {
      await refresh()
      showToast('success', '默认执行节点已保存；已有资源不会迁移')
    },
  })
  const nodeItems = nodes.data?.items ?? []

  return (
    <section>
      <h1 className="text-3xl font-bold">执行节点</h1>
      <p className="muted mt-2">
        Worker 主动通过 HTTPS 领取任务。默认节点只在新建开发环境或 Work Item
        首次产生任务时冻结。
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
          <NodeSelect
            label="GitHub 默认执行节点"
            role="github"
            nodes={nodeItems}
            value={defaults.data?.githubNodeId ?? ''}
            onChange={(value) =>
              saveDefaults.mutate({
                githubNodeId: value || null,
                discordNodeId: defaults.data?.discordNodeId ?? null,
              })
            }
          />
          <NodeSelect
            label="Discord 默认执行节点"
            role="discord"
            nodes={nodeItems}
            value={defaults.data?.discordNodeId ?? ''}
            onChange={(value) =>
              saveDefaults.mutate({
                githubNodeId: defaults.data?.githubNodeId ?? null,
                discordNodeId: value || null,
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
        <h2 className="text-xl font-semibold">注册新节点</h2>
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
          className="button-primary mt-5"
          disabled={create.isPending || (!githubRole && !discordRole)}
        >
          创建并生成 Token
        </button>
      </form>

      <div className="mt-6 grid gap-4">
        {nodeItems.map((node) => (
          <article className="panel" key={node.id}>
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div>
                <h2 className="text-lg font-semibold">{node.name}</h2>
                <p className="muted mt-1 text-sm">
                  {node.roles.join(' + ')} · 并发 {node.maxConcurrentJobs} ·{' '}
                  {node.status}
                  {node.workerVersion ? ` · Worker ${node.workerVersion}` : ''}
                </p>
                <p className="muted mt-1 text-xs">
                  最近心跳：{node.heartbeatAt ?? '尚未连接'}
                </p>
                {node.lastError && (
                  <p className="error-text mt-2">{node.lastError}</p>
                )}
              </div>
              <div className="flex flex-wrap gap-2">
                <button
                  className="button-secondary"
                  onClick={() => action.mutate({ node, type: 'enroll' })}
                >
                  轮换凭据
                </button>
                <button
                  className="button-secondary"
                  onClick={() => action.mutate({ node, type: 'toggle' })}
                >
                  {node.enabled ? '停用' : '启用'}
                </button>
                <button
                  className="button-secondary"
                  onClick={() => action.mutate({ node, type: 'delete' })}
                >
                  删除
                </button>
              </div>
            </div>
          </article>
        ))}
      </div>
    </section>
  )
}

function NodeSelect({
  label,
  role,
  nodes,
  value,
  onChange,
}: {
  label: string
  role: string
  nodes: ExecutionNode[]
  value: string
  onChange: (value: string) => void
}) {
  return (
    <label>
      <span className="label">{label}</span>
      <select value={value} onChange={(event) => onChange(event.target.value)}>
        <option value="">未设置</option>
        {nodes
          .filter((node) => node.enabled && node.roles.includes(role))
          .map((node) => (
            <option value={node.id} key={node.id}>
              {node.name}
            </option>
          ))}
      </select>
    </label>
  )
}
