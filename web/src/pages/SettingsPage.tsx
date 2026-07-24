import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm, useWatch } from 'react-hook-form'
import { api } from '../api/client'
import { useUI, type Locale } from '../state'

interface ChatGPTAccount {
  configured: boolean
  email?: string
  planType?: string
}

interface ProviderSettings {
  modelSource: 'chatgpt' | 'provider'
  baseUrl?: string
  model?: string
  reasoningEffort?: string
  serviceTier?: string
  proxyUrl?: string
  providerConfigured: boolean
  chatgptConfigured: boolean
  chatgptAuthRevision: number
  configSignature: string
  chatgptAccount: ChatGPTAccount
}

interface ProviderInput {
  modelSource: 'chatgpt' | 'provider'
  baseUrl?: string
  apiKey?: string
  model?: string
  reasoningEffort?: string
  serviceTier?: string
  proxyUrl?: string
}

interface ChatGPTLoginOperation {
  id: string
  status: 'pending' | 'awaiting_user' | 'completed' | 'failed' | 'canceled'
  authUrl?: string
  email?: string
  planType?: string
  error?: string
  expiresAt?: string
}

export function SettingsPage() {
  const { locale, theme, setLocale, setTheme, showToast } = useUI()
  const queryClient = useQueryClient()
  const [loginID, setLoginID] = useState<string>()
  const settings = useQuery({
    queryKey: ['settings'],
    queryFn: () => api<ProviderSettings>('/settings/agent-provider'),
  })
  const globalAgents = useQuery({
    queryKey: ['global-agents'],
    queryFn: () => api<{ content: string }>('/settings/global-agents'),
  })
  const login = useQuery({
    queryKey: ['chatgpt-login', loginID],
    queryFn: () =>
      api<ChatGPTLoginOperation>(
        `/settings/agent-provider/chatgpt/login/${loginID}`,
      ),
    enabled: Boolean(loginID),
    refetchInterval: (query) => {
      const status = query.state.data?.status
      return status === 'pending' || status === 'awaiting_user' ? 1000 : false
    },
  })
  const form = useForm<ProviderInput>({
    values: settings.data
      ? {
          modelSource: settings.data.modelSource,
          baseUrl: settings.data.baseUrl ?? '',
          model: settings.data.model ?? '',
          reasoningEffort: settings.data.reasoningEffort ?? '',
          serviceTier: settings.data.serviceTier ?? '',
          proxyUrl: settings.data.proxyUrl ?? '',
          apiKey: '',
        }
      : undefined,
  })
  const modelSource = useWatch({ control: form.control, name: 'modelSource' })
  const saveProvider = useMutation({
    mutationFn: (value: ProviderInput) =>
      api<void>('/settings/agent-provider', {
        method: 'PUT',
        body: JSON.stringify(value),
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['settings'] })
      showToast('success', '模型设置已保存')
    },
    onError: (error) => showToast('error', error.message),
  })
  const startLogin = useMutation({
    mutationFn: () =>
      api<ChatGPTLoginOperation>('/settings/agent-provider/chatgpt/login', {
        method: 'POST',
      }),
    onSuccess: (operation) => {
      setLoginID(operation.id)
      queryClient.setQueryData(['chatgpt-login', operation.id], operation)
    },
    onError: (error) => showToast('error', error.message),
  })
  const cancelLogin = useMutation({
    mutationFn: (id: string) =>
      api<void>(`/settings/agent-provider/chatgpt/login/${id}`, {
        method: 'DELETE',
      }),
    onSuccess: () => {
      setLoginID(undefined)
      showToast('info', '已取消 ChatGPT 登录')
    },
    onError: (error) => showToast('error', error.message),
  })
  const logout = useMutation({
    mutationFn: () =>
      api<void>('/settings/agent-provider/chatgpt/account', {
        method: 'DELETE',
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['settings'] })
      showToast('success', '已退出全局 ChatGPT 账号')
    },
    onError: (error) => showToast('error', error.message),
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
    onError: (error) => showToast('error', error.message),
  })

  useEffect(() => {
    if (login.data?.status !== 'completed') return
    showToast('success', 'ChatGPT 全局账号登录成功')
    void queryClient
      .invalidateQueries({ queryKey: ['settings'] })
      .then(() => setLoginID(undefined))
  }, [login.data?.status, queryClient, showToast])

  const account = settings.data?.chatgptAccount
  const loginOperation = login.data
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
            {(['light', 'dark'] as const).map((value) => (
              <button
                className={`theme-option ${theme === value ? 'theme-option-active' : ''}`}
                type="button"
                aria-pressed={theme === value}
                onClick={() => setTheme(value)}
                key={value}
              >
                {value === 'light' ? '亮色' : '暗色'}
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="panel mt-6">
        <h2 className="text-xl font-semibold">全局 ChatGPT 账号</h2>
        <p className="muted mt-2 text-sm">
          账号登录态会安全下发到所有 Codex Runtime，用于插件市场和 ChatGPT
          模型调用。
        </p>
        <div className="mt-4 rounded-xl border border-[color:var(--border)] p-4">
          <p className="font-medium">
            {account?.configured
              ? account.email || 'ChatGPT 账号已登录'
              : '尚未登录 ChatGPT'}
          </p>
          {account?.planType && (
            <p className="muted mt-1 text-sm">套餐：{account.planType}</p>
          )}
          <div className="mt-4 flex flex-wrap gap-3">
            <button
              className="button"
              type="button"
              disabled={
                startLogin.isPending ||
                loginOperation?.status === 'pending' ||
                loginOperation?.status === 'awaiting_user'
              }
              onClick={() => startLogin.mutate()}
            >
              {account?.configured ? '重新登录' : '登录 ChatGPT'}
            </button>
            {account?.configured && (
              <button
                className="button-secondary"
                type="button"
                disabled={
                  logout.isPending ||
                  loginOperation?.status === 'pending' ||
                  loginOperation?.status === 'awaiting_user' ||
                  settings.data?.modelSource === 'chatgpt'
                }
                title={
                  settings.data?.modelSource === 'chatgpt'
                    ? '请先切换到 Provider 模式'
                    : undefined
                }
                onClick={() => logout.mutate()}
              >
                退出登录
              </button>
            )}
          </div>
        </div>
        {loginOperation && (
          <div className="danger-note mt-4" role="status">
            {loginOperation.status === 'awaiting_user' && (
              <>
                <p>后台登录已启动，请在官方页面完成授权。</p>
                {loginOperation.authUrl && (
                  <a
                    className="button mt-3 inline-flex"
                    href={loginOperation.authUrl}
                    target="_blank"
                    rel="noreferrer"
                  >
                    打开 ChatGPT 授权页面
                  </a>
                )}
                {loginOperation.expiresAt && (
                  <p className="mt-2 text-xs">
                    此登录流程将在{' '}
                    {new Date(loginOperation.expiresAt).toLocaleString()} 过期。
                  </p>
                )}
                <button
                  className="button-secondary mt-3"
                  type="button"
                  disabled={cancelLogin.isPending}
                  onClick={() => cancelLogin.mutate(loginOperation.id)}
                >
                  取消登录
                </button>
              </>
            )}
            {loginOperation.status === 'pending' && <p>正在启动后台登录…</p>}
            {loginOperation.status === 'failed' && (
              <p>登录失败：{loginOperation.error || '未知错误'}</p>
            )}
            {loginOperation.status === 'canceled' && <p>登录已取消。</p>}
          </div>
        )}
      </div>

      <form
        className="panel mt-6"
        onSubmit={form.handleSubmit((value) => saveProvider.mutate(value))}
      >
        <h2 className="text-xl font-semibold">Codex 模型来源</h2>
        <div className="danger-note mt-4">
          切换模型来源会生成新的配置签名。已有 Thread
          将在管理员确认后通过摘要交接并重建。
        </div>
        <label className="mt-5 block text-sm">
          模型调用方式
          <select className="field mt-1" {...form.register('modelSource')}>
            <option value="chatgpt">ChatGPT 全局账号</option>
            <option value="provider">Provider（Base URL + App Key）</option>
          </select>
        </label>
        {!account?.configured && modelSource === 'chatgpt' && (
          <p className="danger-note mt-2 text-sm">
            请先在上方完成 ChatGPT 全局账号登录。
          </p>
        )}
        <fieldset className="mt-5 rounded-xl border border-[color:var(--border)] p-4">
          <legend className="px-2 font-medium">Provider 配置</legend>
          <p className="muted text-sm">
            未启用时也会保留。Base URL 留空时使用 https://api.openai.com/v1。
          </p>
          <label className="mt-4 block text-sm">
            Base URL
            <input
              className="field mt-1"
              placeholder="https://api.openai.com/v1"
              {...form.register('baseUrl')}
            />
          </label>
          <label className="mt-4 block text-sm">
            App Key
            <input
              className="field mt-1"
              type="password"
              autoComplete="off"
              placeholder={
                settings.data?.providerConfigured
                  ? '已配置；留空保持不变'
                  : '尚未配置'
              }
              {...form.register('apiKey')}
            />
          </label>
          <label className="mt-4 block text-sm">
            代理 URL
            <input className="field mt-1" {...form.register('proxyUrl')} />
          </label>
        </fieldset>
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
        <button className="button mt-5" disabled={saveProvider.isPending}>
          {saveProvider.isPending ? '保存中…' : '保存模型设置'}
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
