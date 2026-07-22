import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { api } from '../api/client'
import { useUI, type Locale } from '../state'

interface ProviderSettings {
  providerType: 'device-code' | 'api-key'
  baseUrl?: string
  model?: string
  reasoningEffort?: string
  serviceTier?: string
  proxyUrl?: string
  configured: boolean
}

export function SettingsPage() {
  const { locale, theme, setLocale, setTheme, showToast } = useUI()
  const queryClient = useQueryClient()
  const settings = useQuery({
    queryKey: ['settings'],
    queryFn: () => api<ProviderSettings>('/settings/agent-provider'),
  })
  const globalAgents = useQuery({
    queryKey: ['global-agents'],
    queryFn: () => api<{ content: string }>('/settings/global-agents'),
  })
  const form = useForm<ProviderSettings & { apiKey?: string }>({
    values: settings.data,
  })
  const mutation = useMutation({
    mutationFn: (value: ProviderSettings & { apiKey?: string }) =>
      api<void>('/settings/agent-provider', {
        method: 'PUT',
        body: JSON.stringify(value),
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['settings'] })
      showToast('success', 'Provider 设置已保存')
    },
  })
  const globalAgentsMutation = useMutation({
    mutationFn: (content: string) =>
      api<void>('/settings/global-agents', {
        method: 'PUT',
        body: JSON.stringify({ content }),
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['global-agents'] })
      showToast('success', '全局 AGENTS.md 已保存')
    },
  })
  return (
    <section className="mx-auto max-w-4xl">
      <h1 className="text-3xl font-bold">系统设置</h1>
      <div className="panel mt-8 grid gap-4 sm:grid-cols-2">
        <label className="text-sm">
          语言
          <select
            className="field mt-1"
            value={locale}
            onChange={(event) => setLocale(event.target.value as Locale)}
          >
            <option value="zh-CN">中文</option>
            <option value="en-US">English</option>
          </select>
        </label>
        <div className="text-sm">
          主题
          <div className="theme-toggle mt-1" role="group" aria-label="主题">
            <button
              className={`theme-option ${theme === 'light' ? 'theme-option-active' : ''}`}
              type="button"
              aria-pressed={theme === 'light'}
              onClick={() => setTheme('light')}
            >
              亮色
            </button>
            <button
              className={`theme-option ${theme === 'dark' ? 'theme-option-active' : ''}`}
              type="button"
              aria-pressed={theme === 'dark'}
              onClick={() => setTheme('dark')}
            >
              暗色
            </button>
          </div>
        </div>
      </div>
      <form
        className="panel mt-6"
        onSubmit={form.handleSubmit((value) => mutation.mutate(value))}
      >
        <h2 className="text-xl font-semibold">Codex Provider</h2>
        <div className="danger-note mt-4">
          变更 Provider 会生成新的配置签名。已有 Thread
          将在管理员确认后通过摘要交接并重建。
        </div>
        <label className="mt-5 block text-sm">
          认证方式
          <select className="field mt-1" {...form.register('providerType')}>
            <option value="device-code">共享账号 Device Code</option>
            <option value="api-key">兼容 API Key</option>
          </select>
        </label>
        <label className="mt-4 block text-sm">
          兼容 API Base URL
          <input
            className="field mt-1"
            placeholder="https://api.openai.com/v1"
            {...form.register('baseUrl')}
          />
        </label>
        <label className="mt-4 block text-sm">
          API Key
          <input
            className="field mt-1"
            type="password"
            autoComplete="off"
            {...form.register('apiKey')}
          />
        </label>
        <label className="mt-4 block text-sm">
          代理 URL
          <input className="field mt-1" {...form.register('proxyUrl')} />
        </label>
        <div className="mt-4 grid gap-4 sm:grid-cols-3">
          <label className="text-sm">
            默认模型
            <input className="field mt-1" {...form.register('model')} />
          </label>
          <label className="text-sm">
            推理级别
            <input
              className="field mt-1"
              {...form.register('reasoningEffort')}
            />
          </label>
          <label className="text-sm">
            Service Tier
            <input className="field mt-1" {...form.register('serviceTier')} />
          </label>
        </div>
        <button className="button mt-5" disabled={mutation.isPending}>
          {mutation.isPending ? '保存中…' : '保存 Provider 设置'}
        </button>
      </form>
      <form
        className="panel mt-6"
        onSubmit={(event) => {
          event.preventDefault()
          const formData = new FormData(event.currentTarget)
          globalAgentsMutation.mutate(String(formData.get('content') ?? ''))
        }}
      >
        <h2 className="text-xl font-semibold">全局 AGENTS.md</h2>
        <textarea
          className="field mt-4 min-h-80 font-mono text-sm"
          name="content"
          maxLength={262144}
          defaultValue={globalAgents.data?.content ?? ''}
          key={globalAgents.data?.content ?? ''}
          spellCheck={false}
          aria-label="全局 AGENTS.md"
        />
        <button
          className="button mt-5"
          disabled={globalAgentsMutation.isPending}
        >
          {globalAgentsMutation.isPending ? '保存中…' : '保存全局 AGENTS.md'}
        </button>
      </form>
    </section>
  )
}
