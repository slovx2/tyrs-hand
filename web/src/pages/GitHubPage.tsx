import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { api } from '../api/client'

interface GitHubStatus {
  configured: boolean
  appId?: number
  clientId?: string
  appSlug?: string
}
interface AppInput {
  appId: number
  clientId: string
  appSlug: string
  privateKey: string
  webhookSecret: string
}

export function GitHubPage() {
  const queryClient = useQueryClient()
  const status = useQuery({
    queryKey: ['github-app'],
    queryFn: () => api<GitHubStatus>('/github/app'),
  })
  const manifest = useQuery({
    queryKey: ['github-manifest'],
    queryFn: () =>
      api<{ url: string; manifest: string }>('/github/app/manifest'),
  })
  const form = useForm<AppInput>()
  const mutation = useMutation({
    mutationFn: (values: AppInput) =>
      api<void>('/github/app', {
        method: 'PUT',
        body: JSON.stringify({ ...values, appId: Number(values.appId) }),
      }),
    onSuccess: () => {
      form.reset()
      void queryClient.invalidateQueries({ queryKey: ['github-app'] })
    },
  })
  const manifestURL = manifest.data?.url
  return (
    <section className="mx-auto max-w-5xl">
      <h1 className="text-3xl font-bold">GitHub App</h1>
      <p className="muted mt-2">
        所有远程操作都使用 Installation Token，不使用普通机器账号。
      </p>
      <div className="panel mt-8">
        <h2 className="text-xl font-semibold">当前状态</h2>
        <p className="mt-3">
          {status.data?.configured
            ? `已连接 ${status.data.appSlug}（App ID ${status.data.appId}）`
            : '尚未配置'}
        </p>
        {manifestURL && (
          <form className="mt-5" method="post" action={manifestURL}>
            <input
              type="hidden"
              name="manifest"
              value={manifest.data?.manifest}
            />
            <button className="button" type="submit">
              通过 Manifest 创建 GitHub App
            </button>
          </form>
        )}
      </div>
      <form
        className="panel mt-6"
        onSubmit={form.handleSubmit((values) => mutation.mutate(values))}
      >
        <h2 className="text-xl font-semibold">手动配置</h2>
        <div className="mt-5 grid gap-4 sm:grid-cols-2">
          <label className="text-sm">
            App ID
            <input
              className="field mt-1"
              type="number"
              required
              {...form.register('appId', { valueAsNumber: true })}
            />
          </label>
          <label className="text-sm">
            Client ID
            <input className="field mt-1" {...form.register('clientId')} />
          </label>
          <label className="text-sm sm:col-span-2">
            App Slug
            <input
              className="field mt-1"
              required
              {...form.register('appSlug')}
            />
          </label>
          <label className="text-sm sm:col-span-2">
            Private Key（PEM）
            <textarea
              className="field mt-1 min-h-36 font-mono text-xs"
              required
              {...form.register('privateKey')}
            />
          </label>
          <label className="text-sm sm:col-span-2">
            Webhook Secret
            <input
              className="field mt-1"
              type="password"
              required
              {...form.register('webhookSecret')}
            />
          </label>
        </div>
        <p className="muted mt-4 text-xs">
          Secret 加密保存，提交后不会回显。重新保存时必须提供新的完整 Secret。
        </p>
        {mutation.error && (
          <p role="alert" className="error-text mt-3">
            {mutation.error.message}
          </p>
        )}
        <button className="button mt-5" disabled={mutation.isPending}>
          保存 GitHub App
        </button>
      </form>
    </section>
  )
}
