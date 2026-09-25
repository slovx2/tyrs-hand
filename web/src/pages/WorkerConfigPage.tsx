import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../api/client'
import type { components } from '../api/schema'
import { useUI } from '../state'
import type { Worker } from './workerTypes'
import { useWorkerDetail } from './workerDetailContext'
import { confirmAction } from './workerHelpers'

type Engine = 'codex' | 'claude-code'

type WorkerConfig = components['schemas']['WorkerRuntimeConfig']

export function WorkerConfigRoute() {
  const { worker } = useWorkerDetail()
  return <WorkerConfigPage worker={worker} />
}

export function WorkerConfigPage({ worker }: { worker: Worker }) {
  const [engine, setEngine] = useState<Engine>('codex')
  return (
    <div className="worker-detail-stack">
      <label>
        <span className="label">运行时</span>
        <select
          className="field"
          value={engine}
          onChange={(event) => setEngine(event.target.value as Engine)}
        >
          <option value="codex">Codex</option>
          <option value="claude-code">Claude Code</option>
        </select>
      </label>
      <RuntimeConfig key={engine} worker={worker} engine={engine} />
    </div>
  )
}

function RuntimeConfig({ worker, engine }: { worker: Worker; engine: Engine }) {
  const [reload, setReload] = useState(0)
  const config = useQuery({
    queryKey: ['worker-config', worker.id, engine],
    queryFn: () =>
      api<WorkerConfig>(`/workers/${worker.id}/runtimes/${engine}/config`),
  })
  if (config.isError)
    return (
      <p role="alert" className="error-text">
        {config.error.message}
      </p>
    )
  if (!config.data) return <p className="muted">正在读取配置…</p>
  return (
    <>
      <button
        className="button-secondary"
        disabled={config.isFetching}
        onClick={async () => {
          const result = await config.refetch()
          if (result.isSuccess) setReload((value) => value + 1)
        }}
      >
        重新读取配置
      </button>
      <WorkerConfigEditor
        key={`${engine}:${reload}`}
        worker={worker}
        engine={engine}
        initialConfig={config.data}
        refetchConfig={() => config.refetch()}
      />
    </>
  )
}

function WorkerConfigEditor({
  worker,
  engine,
  initialConfig,
  refetchConfig,
}: {
  worker: Worker
  engine: Engine
  initialConfig: WorkerConfig
  refetchConfig: () => Promise<unknown>
}) {
  const claude = engine === 'claude-code'
  const engineLabel = claude ? 'Claude Code' : 'Codex'
  const instructionsFile = claude ? 'CLAUDE.md' : 'AGENTS.md'
  const configURL = `/workers/${worker.id}/runtimes/${engine}/config`
  const [authMethod, setAuthMethod] = useState<string>(
    initialConfig.authMethod || 'api-key',
  )
  const [model, setModel] = useState(initialConfig.model || '')
  const showToast = useUI((state) => state.showToast)
  const [agents, setAgents] = useState(initialConfig.agents)
  const [revision, setRevision] = useState(initialConfig.revision)
  const [baseUrl, setBaseUrl] = useState(initialConfig.baseUrl ?? '')
  const [apiKey, setApiKey] = useState('')
  const [showKey, setShowKey] = useState(false)
  const oauth = useQuery({
    queryKey: ['worker-oauth', worker.id],
    enabled: !claude,
    queryFn: () =>
      api<{ status: string; userCode?: string; verificationUrl?: string }>(
        `/workers/${worker.id}/codex/oauth/devices`,
      ),
    refetchInterval: (query) =>
      query.state.data?.status === 'pending' ? 2_000 : false,
  })
  const saveAgents = useMutation({
    mutationFn: () =>
      api<{ revision: string }>(`${configURL}/agents`, {
        method: 'PUT',
        body: JSON.stringify({ revision, content: agents }),
      }),
    onSuccess: (result) => {
      setRevision(result.revision)
      showToast('success', `${instructionsFile} 已保存`)
      void refetchConfig()
    },
    onError: (error: Error) => showConfigError(showToast, error),
  })
  const saveProvider = useMutation<{ revision: string }, Error, boolean>({
    mutationFn: (clearApiKey) =>
      api<{ revision: string }>(`${configURL}/provider`, {
        method: 'PUT',
        body: JSON.stringify({
          revision,
          baseUrl,
          ...(claude ? { authMethod, model } : {}),
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
      api(`/workers/${worker.id}/runtimes/${engine}/restart`, {
        method: 'POST',
      }),
    onSuccess: () => showToast('success', `已请求重启 ${engineLabel}`),
    onError: (error: Error) => showConfigError(showToast, error),
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
            {claude
              ? '写入 Claude 独立配置目录的 settings.json，与原生 Claude Code 格式一致。保存后可重启以应用于后续会话。'
              : '模型请求只使用此处配置的非 ChatGPT Provider。配置只保存到 Worker。'}
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
        {claude && (
          <div className="mt-4 grid gap-4 sm:grid-cols-2">
            <label>
              <span className="label">认证方式</span>
              <select
                className="field"
                value={authMethod}
                onChange={(event) => setAuthMethod(event.target.value)}
              >
                <option value="api-key">API Key（x-api-key）</option>
                <option value="auth-token">Auth Token（Bearer）</option>
              </select>
            </label>
            <label>
              <span className="label">默认模型</span>
              <input
                className="field"
                value={model}
                onChange={(event) => setModel(event.target.value)}
                placeholder="留空使用 Claude 默认值"
              />
            </label>
          </div>
        )}
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
        <h2 className="text-xl font-semibold">{instructionsFile}</h2>
        <p className="muted mt-1 text-sm">
          {claude
            ? '此指令文件应用于该 Worker 的 Claude 会话。'
            : '内容直接读取和写入当前 Worker 的 Codex Home。'}
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
            保存 {instructionsFile}
          </button>
          <button
            className="button-danger"
            onClick={() =>
              confirmAction(`重启会影响当前 ${engineLabel} 会话，继续吗？`) &&
              restart.mutate()
            }
            disabled={restart.isPending}
          >
            重启 {engineLabel}
          </button>
        </div>
      </section>

      {!claude && (
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
      )}
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
