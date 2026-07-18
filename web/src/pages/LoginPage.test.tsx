import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { MemoryRouter, Route, Routes } from 'react-router'
import { describe, expect, it } from 'vitest'
import { useUI } from '../state'
import { server } from '../test/server'
import { LoginPage } from './LoginPage'

describe('LoginPage', () => {
  it('登录后保存 CSRF 并返回目标页面', async () => {
    server.use(
      http.post('/api/v1/auth/login', async ({ request }) => {
        expect(await request.json()).toEqual({
          username: 'admin',
          password: 'a-safe-password',
          totp: '123456',
        })
        return HttpResponse.json({
          username: 'admin',
          csrfToken: 'csrf-1',
          expiresAt: '2030-01-01T00:00:00Z',
        })
      }),
    )
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <MemoryRouter
          initialEntries={[
            { pathname: '/login', state: { from: { pathname: '/jobs' } } },
          ]}
        >
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/jobs" element={<p>任务页面</p>} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('用户名'), 'admin')
    await user.type(screen.getByLabelText('密码'), 'a-safe-password')
    await user.type(screen.getByLabelText('TOTP 验证码'), '123456')
    await user.click(screen.getByRole('button', { name: '登录' }))
    expect(await screen.findByText('任务页面')).toBeInTheDocument()
    expect(useUI.getState().csrfToken).toBe('csrf-1')
  })
})
