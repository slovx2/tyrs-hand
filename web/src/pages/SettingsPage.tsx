import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { api } from '../api/client'
import { useUI, type Locale, type Theme } from '../state'

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
  const { locale, theme, setLocale, setTheme } = useUI()
  const queryClient = useQueryClient()
  const settings = useQuery({
    queryKey: ['settings'],
    queryFn: () => api<ProviderSettings>('/settings/agent-provider'),
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
    onSuccess: () =>
      void queryClient.invalidateQueries({ queryKey: ['settings'] }),
  })
  return (
    <section className="max-w-3xl">
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
        <label className="text-sm">
          主题
          <select
            className="field mt-1"
            value={theme}
            onChange={(event) => setTheme(event.target.value as Theme)}
          >
            <option value="light">浅色</option>
            <option value="dark">深色</option>
          </select>
        </label>
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
        {mutation.error && (
          <p role="alert" className="mt-4 text-red-700">
            {mutation.error.message}
          </p>
        )}
        <button className="button mt-5" disabled={mutation.isPending}>
          保存 Provider 设置
        </button>
      </form>
    </section>
  )
}
