import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'
import type { Worker } from './workerTypes'
import { useWorkerDetail } from './workerDetailContext'
import { confirmAction } from './workerHelpers'

interface WorkerConfig {
  revision: string
  agents: string
  baseUrl: string
  envKey: string
  apiKeyConfigured: boolean
}

export function WorkerConfigRoute() {
  const { worker } = useWorkerDetail()
  return <WorkerConfigPage worker={worker} />
}

export function WorkerConfigPage({ worker }: { worker: Worker }) {
  const config = useQuery({
    queryKey: ['worker-config', worker.id],
    queryFn: () => api<WorkerConfig>(`/workers/${worker.id}/config`),
  })
  if (config.isError) {
    return (
      <p role="alert" className="error-text">
        {config.error.message}
      </p>
    )
  }
  if (!config.data) return <p className="muted">正在读取 Codex 配置…</p>
  return (
    <WorkerConfigEditor
      key={config.data.revision}
      worker={worker}
      initialConfig={config.data}
      refetchConfig={() => config.refetch()}
    />
  )
}

function WorkerConfigEditor({
  worker,
  initialConfig,
  refetchConfig,
}: {
  worker: Worker
  initialConfig: WorkerConfig
  refetchConfig: () => Promise<unknown>
}) {
  const showToast = useUI((state) => state.showToast)
  const [agents, setAgents] = useState(initialConfig.agents)
  const [revision, setRevision] = useState(initialConfig.revision)
  const [baseUrl, setBaseUrl] = useState(initialConfig.baseUrl ?? '')
  const [apiKey, setApiKey] = useState('')
  const [showKey, setShowKey] = useState(false)
  const oauth = useQuery({
    queryKey: ['worker-oauth', worker.id],
    queryFn: () =>
      api<{ status: string; userCode?: string; verificationUrl?: string }>(
        `/workers/${worker.id}/codex/oauth/devices`,
      ),
    refetchInterval: (query) =>
      query.state.data?.status === 'pending' ? 2_000 : false,
  })
  const saveAgents = useMutation({
    mutationFn: () =>
      api<{ revision: string }>(`/workers/${worker.id}/config/agents`, {
        method: 'PUT',
        body: JSON.stringify({ revision, content: agents }),
      }),
    onSuccess: (result) => {
      setRevision(result.revision)
      showToast('success', 'AGENTS.md 已保存')
    },
    onError: (error: Error) => showConfigError(showToast, error),
  })
  const saveProvider = useMutation<{ revision: string }, Error, boolean>({
    mutationFn: (clearApiKey) =>
      api<{ revision: string }>(`/workers/${worker.id}/config/provider`, {
        method: 'PUT',
        body: JSON.stringify({
          revision,
          baseUrl,
          ...(clearApiKey ? { clearApiKey: true } : { apiKey }),
        }),
      }),
    onSuccess: (result) => {
      setRevision(result.revision)
      setApiKey('')
      void refetchConfig()
      showToast('success', 'Model Provider 已保存')
    },
    onError: (error: Error) => showConfigError(showToast, error),
  })
  const restart = useMutation({
    mutationFn: () =>
      api(`/workers/${worker.id}/codex/restart`, { method: 'POST' }),
    onSuccess: () => showToast('success', '已请求重启 Codex'),
  })
  const startOAuth = useMutation({
    mutationFn: () =>
      api(`/workers/${worker.id}/codex/oauth/devices`, { method: 'POST' }),
    onSuccess: () => oauth.refetch(),
  })

  return (
    <div className="worker-detail-stack">
      <section className="panel worker-config-section">
        <div>
          <h2 className="text-xl font-semibold">Model Provider</h2>
          <p className="muted mt-1 text-sm">
            模型请求只使用此处配置的非 ChatGPT Provider。配置只保存到 Worker。
          </p>
        </div>
        <div className="mt-5 grid gap-4 sm:grid-cols-2">
          <label>
            <span className="label">
              Base URL <span className="required-mark">*</span>
            </span>
            <input
              className="field mt-1"
              type="url"
              required
              value={baseUrl}
              onChange={(event) => setBaseUrl(event.target.value)}
              placeholder="https://api.example.com/v1"
            />
          </label>
          <label>
            <span className="label">
              API Key <span className="required-mark">*</span>
            </span>
            <div className="input-with-action mt-1">
              <input
                className="field"
                type={showKey ? 'text' : 'password'}
                value={apiKey}
                onChange={(event) => setApiKey(event.target.value)}
                placeholder={
                  initialConfig.apiKeyConfigured
                    ? '留空保持原值'
                    : '首次配置必填'
                }
              />
              <button
                type="button"
                className="button-ghost"
                onClick={() => setShowKey((value) => !value)}
              >
                {showKey ? '隐藏' : '显示'}
              </button>
            </div>
            {initialConfig.apiKeyConfigured && (
              <span className="muted mt-1 block text-xs">
                当前状态：********（{initialConfig.envKey}）
              </span>
            )}
          </label>
        </div>
        <div className="mt-4 flex flex-wrap gap-2">
          <button
            className="button"
            onClick={() => saveProvider.mutate(false)}
            disabled={
              saveProvider.isPending ||
              !baseUrl ||
              (!initialConfig.apiKeyConfigured && !apiKey)
            }
          >
            保存 Provider
          </button>
          {initialConfig.apiKeyConfigured && (
            <button
              className="button-danger"
              onClick={() =>
                confirmAction('清除后模型请求将无法认证，确定继续？') &&
                saveProvider.mutate(true)
              }
              disabled={saveProvider.isPending}
            >
              清除 API Key
            </button>
          )}
        </div>
      </section>

      <section className="panel worker-config-section">
        <h2 className="text-xl font-semibold">AGENTS.md</h2>
        <p className="muted mt-1 text-sm">
          内容直接读取和写入当前 Worker 的 Codex Home。
        </p>
        <textarea
          className="field mt-4 min-h-52 font-mono text-xs leading-5"
          value={agents}
          onChange={(event) => setAgents(event.target.value)}
        />
        <div className="mt-4 flex flex-wrap gap-2">
          <button
            className="button-secondary"
            onClick={() => saveAgents.mutate()}
            disabled={saveAgents.isPending}
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
        </div>
      </section>

      <section className="panel worker-config-section">
        <h2 className="text-xl font-semibold">ChatGPT 账号</h2>
        <p className="muted mt-1 text-sm">
          OAuth 只用于账号登录，不参与模型 Provider 请求。
        </p>
        <button
          className="button-secondary mt-4"
          onClick={() => startOAuth.mutate()}
          disabled={startOAuth.isPending}
        >
          登录 ChatGPT 账号
        </button>
        {oauth.data?.status === 'pending' && oauth.data.userCode && (
          <div className="danger-note mt-4">
            请打开{' '}
            <a
              href={oauth.data.verificationUrl}
              target="_blank"
              rel="noreferrer"
            >
              {oauth.data.verificationUrl}
            </a>
            ，输入设备码 <code>{oauth.data.userCode}</code>。
          </div>
        )}
        {oauth.data?.status === 'authenticated' && (
          <p className="muted mt-4 text-sm">ChatGPT OAuth 已登录。</p>
        )}
      </section>
    </div>
  )
}

function showConfigError(
  showToast: (tone: 'success' | 'error' | 'info', message: string) => void,
  error: Error,
) {
  showToast(
    'error',
    error.message.includes('冲突') ? '配置已变化，请重新读取' : error.message,
  )
}
