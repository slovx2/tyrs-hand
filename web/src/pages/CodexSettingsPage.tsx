import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'

type ServiceTier = 'standard' | 'fast'
type ReasoningEffort = 'low' | 'medium' | 'high' | 'xhigh'

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

interface ForumSettings {
  id: string
  name: string
  ownerDiscordUserId: string
  settings: Preferences
  effective: EffectivePreferences
}

interface RepositorySettings {
  id: string
  owner: string
  name: string
  settings: Preferences
  effective: EffectivePreferences
  forums: ForumSettings[]
}

interface SettingsResponse {
  items: RepositorySettings[]
  modelOptions: string[]
}

export function CodexSettingsPage() {
  const settings = useQuery({
    queryKey: ['codex-settings'],
    queryFn: () => api<SettingsResponse>('/settings/codex'),
  })
  if (settings.isLoading) return <p className="muted">正在加载 Codex 设置…</p>
  if (settings.isError)
    return <p className="error-text">{(settings.error as Error).message}</p>
  return (
    <section className="mx-auto max-w-6xl">
      <h1 className="text-3xl font-bold">Codex 设置</h1>
      <p className="muted mt-2">
        Forum 逐项继承仓库，仓库继续继承 Agent 与
        Provider。新会话创建时会固化最终参数。
      </p>
      <div className="mt-8 grid gap-6">
        {settings.data?.items.map((repository) => (
          <div className="panel" key={repository.id}>
            <div>
              <h2 className="text-xl font-semibold">
                {repository.owner}/{repository.name}
              </h2>
              <p className="muted mt-1 text-sm">GitHub 仓库默认值</p>
            </div>
            <ScopeEditor
              key={`repository-${repository.id}`}
              endpoint={`/settings/codex/repositories/${repository.id}`}
              value={repository.settings}
              effective={repository.effective}
              models={settings.data.modelOptions}
            />
            {repository.forums.length > 0 && (
              <div className="mt-6 grid gap-4 border-t pt-6 [border-color:var(--border)]">
                {repository.forums.map((forum) => (
                  <div
                    className="rounded-xl border p-4 [border-color:var(--border)]"
                    key={forum.id}
                  >
                    <div className="mb-4">
                      <h3 className="font-semibold">
                        Discord Forum · {forum.name}
                      </h3>
                      <p className="muted mt-1 text-xs">
                        所有者 {forum.ownerDiscordUserId}
                      </p>
                    </div>
                    <ScopeEditor
                      key={`forum-${forum.id}`}
                      endpoint={`/settings/codex/forums/${forum.id}`}
                      value={forum.settings}
                      effective={forum.effective}
                      models={settings.data.modelOptions}
                    />
                  </div>
                ))}
              </div>
            )}
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
  models: string[]
}) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const isPreset = value.model === null || models.includes(value.model)
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
          serviceTier: serviceTier === '__inherit__' ? null : serviceTier,
          reasoningEffort:
            reasoningEffort === '__inherit__' ? null : reasoningEffort,
        }),
      })
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['codex-settings'] })
      showToast('success', 'Codex 设置已保存')
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
              <option value={model} key={model}>
                {model}
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
            value={serviceTier}
            onChange={(event) => setServiceTier(event.target.value)}
          >
            <option value="__inherit__">
              继承（{tierLabel(effective.serviceTier)}）
            </option>
            <option value="standard">标准</option>
            <option value="fast">快速</option>
          </select>
        </label>
        <label className="text-sm">
          思考等级
          <select
            className="field mt-1"
            value={reasoningEffort}
            onChange={(event) => setReasoningEffort(event.target.value)}
          >
            <option value="__inherit__">
              继承（{effortLabel(effective.reasoningEffort)}）
            </option>
            <option value="low">轻</option>
            <option value="medium">中</option>
            <option value="high">高</option>
            <option value="xhigh">极高</option>
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
  if (value === 'low') return '轻'
  if (value === 'medium') return '中'
  if (value === 'high') return '高'
  if (value === 'xhigh') return '极高'
  return 'Codex 默认'
}
