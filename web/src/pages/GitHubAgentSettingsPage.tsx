import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'

type ServiceTier = 'standard' | 'fast'
type ReasoningEffort = string

interface Preferences {
  model: string | null
  serviceTier: ServiceTier | null
  reasoningEffort: ReasoningEffort | null
}

interface EffectivePreferences {
  model: string
  serviceTier: ServiceTier
  reasoningEffort: ReasoningEffort | ''
}

interface RepositorySettings {
  id: string
  owner: string
  name: string
  settings: Preferences
  effective: EffectivePreferences
}

interface SettingsResponse {
  items: RepositorySettings[]
  models: ModelOption[]
}

interface ModelOption {
  id: string
  supportedReasoningEfforts: { reasoningEffort: string }[]
  defaultReasoningEffort: string
  additionalSpeedTiers?: string[]
  serviceTiers?: { id: string }[]
  isDefault: boolean
}

export function GitHubAgentSettingsPage() {
  const settings = useQuery({
    queryKey: ['github-agent-settings'],
    queryFn: () => api<SettingsResponse>('/settings/github-agent'),
  })
  if (settings.isLoading)
    return <p className="muted">正在加载 GitHub Agent 设置…</p>
  if (settings.isError)
    return <p className="error-text">{(settings.error as Error).message}</p>
  return (
    <section className="mx-auto max-w-6xl">
      <h1 className="text-3xl font-bold">GitHub Agent 参数</h1>
      <p className="muted mt-2">
        仓库覆盖继承 Agent Profile，并只在 GitHub Work Item 首次执行时固化。
        Provider、登录态和模型目录仍由 Worker 机器的 Codex Home 决定。
      </p>
      <div className="mt-8 grid gap-6">
        {settings.data?.items.map((repository) => (
          <div className="panel" key={repository.id}>
            <div>
              <h2 className="text-xl font-semibold">
                {repository.owner}/{repository.name}
              </h2>
              <p className="muted mt-1 text-sm">仓库任务参数覆盖</p>
            </div>
            <ScopeEditor
              key={`repository-${repository.id}`}
              endpoint={`/settings/github-agent/repositories/${repository.id}`}
              value={repository.settings}
              effective={repository.effective}
              models={settings.data.models}
            />
          </div>
        ))}
        {settings.data?.items.length === 0 && (
          <div className="panel muted">暂无仓库。</div>
        )}
      </div>
    </section>
  )
}

function ScopeEditor({
  endpoint,
  value,
  effective,
  models,
}: {
  endpoint: string
  value: Preferences
  effective: EffectivePreferences
  models: ModelOption[]
}) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const modelIDs = models.map((model) => model.id)
  const isPreset = value.model === null || modelIDs.includes(value.model)
  const [modelMode, setModelMode] = useState(
    value.model === null
      ? '__inherit__'
      : isPreset
        ? value.model
        : '__custom__',
  )
  const [customModel, setCustomModel] = useState(
    isPreset ? '' : value.model || '',
  )
  const [serviceTier, setServiceTier] = useState(
    value.serviceTier || '__inherit__',
  )
  const [reasoningEffort, setReasoningEffort] = useState(
    value.reasoningEffort || '__inherit__',
  )
  const selectedModelID =
    modelMode === '__inherit__'
      ? effective.model
      : modelMode === '__custom__'
        ? customModel.trim()
        : modelMode
  const selectedModel =
    models.find((model) => model.id === selectedModelID) ||
    (selectedModelID ? undefined : models.find((model) => model.isDefault))
  const reasoningEfforts =
    selectedModel?.supportedReasoningEfforts.map(
      (option) => option.reasoningEffort,
    ) || []
  const supportsFast = Boolean(
    selectedModel?.serviceTiers?.some(
      (tier) => tier.id === 'fast' || tier.id === 'priority',
    ) || selectedModel?.additionalSpeedTiers?.includes('fast'),
  )
  const normalizedServiceTier =
    serviceTier === 'fast' && !supportsFast ? 'standard' : serviceTier
  const defaultReasoningEffort = selectedModel?.defaultReasoningEffort || ''
  const normalizedReasoningEffort =
    reasoningEffort !== '__inherit__' &&
    !reasoningEfforts.includes(reasoningEffort)
      ? reasoningEfforts.includes(defaultReasoningEffort)
        ? defaultReasoningEffort
        : '__inherit__'
      : reasoningEffort
  const mutation = useMutation({
    mutationFn: () => {
      const model =
        modelMode === '__inherit__'
          ? null
          : modelMode === '__custom__'
            ? customModel.trim()
            : modelMode
      return api<void>(endpoint, {
        method: 'PUT',
        body: JSON.stringify({
          model,
          serviceTier:
            normalizedServiceTier === '__inherit__'
              ? null
              : normalizedServiceTier,
          reasoningEffort:
            normalizedReasoningEffort === '__inherit__'
              ? null
              : normalizedReasoningEffort,
        }),
      })
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ['github-agent-settings'],
      })
      showToast('success', 'GitHub Agent 参数已保存')
    },
  })
  return (
    <div className="mt-4">
      <div className="grid gap-4 md:grid-cols-3">
        <label className="text-sm">
          模型
          <select
            className="field mt-1"
            value={modelMode}
            onChange={(event) => setModelMode(event.target.value)}
          >
            <option value="__inherit__">
              继承（{effective.model || 'Codex 默认'}）
            </option>
            {models.map((model) => (
              <option value={model.id} key={model.id}>
                {model.id}
              </option>
            ))}
            <option value="__custom__">自定义…</option>
          </select>
          {modelMode === '__custom__' && (
            <input
              className="field mt-2"
              maxLength={128}
              placeholder="输入模型名称"
              value={customModel}
              onChange={(event) => setCustomModel(event.target.value)}
            />
          )}
        </label>
        <label className="text-sm">
          服务等级
          <select
            className="field mt-1"
            value={normalizedServiceTier}
            onChange={(event) => setServiceTier(event.target.value)}
          >
            <option value="__inherit__">
              继承（{tierLabel(effective.serviceTier)}）
            </option>
            <option value="standard">标准</option>
            {supportsFast && <option value="fast">快速</option>}
          </select>
        </label>
        <label className="text-sm">
          思考等级
          <select
            className="field mt-1"
            value={normalizedReasoningEffort}
            onChange={(event) => setReasoningEffort(event.target.value)}
          >
            <option value="__inherit__">
              继承（{effortLabel(effective.reasoningEffort)}）
            </option>
            {reasoningEfforts.map((effort) => (
              <option value={effort} key={effort}>
                {effort}
              </option>
            ))}
          </select>
        </label>
      </div>
      <button
        className="button mt-4"
        type="button"
        disabled={
          mutation.isPending ||
          (modelMode === '__custom__' && !customModel.trim())
        }
        onClick={() => mutation.mutate()}
      >
        {mutation.isPending ? '保存中…' : '保存设置'}
      </button>
    </div>
  )
}

function tierLabel(value: ServiceTier) {
  return value === 'fast' ? '快速' : '标准'
}

function effortLabel(value: EffectivePreferences['reasoningEffort']) {
  return value || 'Codex 默认'
}
