import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { Link, useLocation, useNavigate } from 'react-router'
import { z } from 'zod'
import { api } from '../api/client'
import { useUI } from '../state'

const schema = z.object({
  username: z.string().min(1, '请输入用户名'),
  password: z.string().min(1, '请输入密码'),
  totp: z.string().regex(/^\d{6}$/, '请输入 6 位验证码'),
})

type FormData = z.infer<typeof schema>

export function LoginPage() {
  const navigate = useNavigate()
  const location = useLocation()
  const queryClient = useQueryClient()
  const setCSRFToken = useUI((state) => state.setCSRFToken)
  const form = useForm<FormData>({ resolver: zodResolver(schema) })
  const mutation = useMutation({
    mutationFn: (values: FormData) =>
      api<{ csrfToken: string }>('/auth/login', {
        method: 'POST',
        body: JSON.stringify(values),
      }),
    onSuccess: async (result) => {
      setCSRFToken(result.csrfToken)
      await queryClient.invalidateQueries({ queryKey: ['me'] })
      const from = (location.state as { from?: { pathname?: string } } | null)
        ?.from?.pathname
      navigate(from || '/', { replace: true })
    },
  })
  return (
    <main className="grid min-h-screen place-items-center p-5">
      <form
        className="panel w-full max-w-md"
        onSubmit={form.handleSubmit((values) => mutation.mutate(values))}
      >
        <h1 className="text-2xl font-bold">登录 tyrs-hand</h1>
        <label className="mt-6 block text-sm">
          用户名
          <input
            className="field mt-1"
            autoComplete="username"
            {...form.register('username')}
          />
        </label>
        <FieldError message={form.formState.errors.username?.message} />
        <label className="mt-4 block text-sm">
          密码
          <input
            className="field mt-1"
            type="password"
            autoComplete="current-password"
            {...form.register('password')}
          />
        </label>
        <FieldError message={form.formState.errors.password?.message} />
        <label className="mt-4 block text-sm">
          TOTP 验证码
          <input
            className="field mt-1"
            inputMode="numeric"
            autoComplete="one-time-code"
            {...form.register('totp')}
          />
        </label>
        <FieldError message={form.formState.errors.totp?.message} />
        <button className="button mt-6 w-full" disabled={mutation.isPending}>
          {mutation.isPending ? '登录中…' : '登录'}
        </button>
        <Link className="text-link mt-4 block text-center text-sm" to="/setup">
          首次安装？
        </Link>
      </form>
    </main>
  )
}

function FieldError({ message }: { message?: string }) {
  return message ? <p className="error-text mt-1 text-xs">{message}</p> : null
}
