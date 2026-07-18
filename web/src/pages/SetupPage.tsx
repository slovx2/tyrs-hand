import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { Link, Navigate } from 'react-router'
import { z } from 'zod'
import { api } from '../api/client'

const schema = z.object({
  setupToken: z.string().min(16, 'Setup Token 长度不足'),
  username: z.string().min(3).max(64),
  password: z.string().min(12, '密码至少 12 位').max(256),
})

type SetupInput = z.infer<typeof schema>
interface SetupResult {
  totpSecret: string
  provisioningUri: string
  recoveryCodes: string[]
}

export function SetupPage() {
  const [result, setResult] = useState<SetupResult>()
  const queryClient = useQueryClient()
  const status = useQuery({
    queryKey: ['setup-status'],
    queryFn: () => api<{ setupRequired: boolean }>('/setup/status'),
  })
  const form = useForm<SetupInput>({ resolver: zodResolver(schema) })
  const mutation = useMutation({
    mutationFn: (values: SetupInput) =>
      api<SetupResult>('/setup/admin', {
        method: 'POST',
        body: JSON.stringify(values),
      }),
    onSuccess: async (value) => {
      setResult(value)
      await queryClient.invalidateQueries({ queryKey: ['setup-status'] })
    },
  })
  if (status.data && !status.data.setupRequired && !result)
    return <Navigate to="/login" replace />
  if (result) {
    return (
      <main className="grid min-h-screen place-items-center p-5">
        <section className="panel w-full max-w-2xl">
          <h1 className="text-2xl font-bold">管理员创建完成</h1>
          <p className="mt-3">
            请立即把 TOTP
            和恢复码保存到安全的密码管理器。离开此页面后不会再次显示。
          </p>
          <dl className="mt-5 space-y-4">
            <div>
              <dt className="text-sm text-slate-500">TOTP Secret</dt>
              <dd className="mt-1 break-all font-mono">{result.totpSecret}</dd>
            </div>
            <div>
              <dt className="text-sm text-slate-500">Provisioning URI</dt>
              <dd className="mt-1 break-all font-mono text-xs">
                {result.provisioningUri}
              </dd>
            </div>
            <div>
              <dt className="text-sm text-slate-500">恢复码</dt>
              <dd className="mt-2 grid grid-cols-2 gap-2 font-mono">
                {result.recoveryCodes.map((code) => (
                  <span key={code}>{code}</span>
                ))}
              </dd>
            </div>
          </dl>
          <Link to="/login" className="button mt-6">
            前往登录
          </Link>
        </section>
      </main>
    )
  }
  return (
    <main className="grid min-h-screen place-items-center p-5">
      <form
        className="panel w-full max-w-lg"
        onSubmit={form.handleSubmit((values) => mutation.mutate(values))}
      >
        <h1 className="text-2xl font-bold">初始化 tyrs-hand</h1>
        <p className="mt-2 text-sm text-slate-500">
          系统只允许创建一位管理员，并强制启用 TOTP。
        </p>
        <label className="mt-6 block text-sm">
          一次性 Setup Token
          <input
            className="field mt-1"
            type="password"
            {...form.register('setupToken')}
          />
        </label>
        <label className="mt-4 block text-sm">
          管理员用户名
          <input
            className="field mt-1"
            autoComplete="username"
            {...form.register('username')}
          />
        </label>
        <label className="mt-4 block text-sm">
          管理员密码
          <input
            className="field mt-1"
            type="password"
            autoComplete="new-password"
            {...form.register('password')}
          />
        </label>
        {Object.values(form.formState.errors).map((error) => (
          <p key={error.message} className="mt-2 text-xs text-red-700">
            {error.message}
          </p>
        ))}
        {mutation.error && (
          <p role="alert" className="mt-4 text-red-700">
            {mutation.error.message}
          </p>
        )}
        <button className="button mt-6 w-full" disabled={mutation.isPending}>
          创建管理员
        </button>
      </form>
    </main>
  )
}
