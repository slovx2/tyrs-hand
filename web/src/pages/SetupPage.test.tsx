import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { server } from '../test/server'
import { SetupPage } from './SetupPage'

describe('SetupPage', () => {
  it('创建管理员后立即显示 TOTP 和恢复资料', async () => {
    server.use(
      http.get('/api/v1/setup/status', () =>
        HttpResponse.json({ setupRequired: true }),
      ),
      http.post('/api/v1/setup/admin', () =>
        HttpResponse.json(
          {
            totpSecret: 'totp-secret',
            provisioningUri: 'otpauth://totp/tyrs-hand',
            recoveryCodes: ['recovery-1', 'recovery-2'],
          },
          { status: 201 },
        ),
      ),
    )
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <MemoryRouter>
          <SetupPage />
        </MemoryRouter>
      </QueryClientProvider>,
    )
    const user = userEvent.setup()
    await user.type(
      screen.getByLabelText('一次性 Setup Token'),
      'setup-token-long-enough',
    )
    await user.type(screen.getByLabelText('管理员用户名'), 'admin')
    await user.type(screen.getByLabelText('管理员密码'), 'a-strong-password')
    await user.click(screen.getByRole('button', { name: '创建管理员' }))

    expect(
      await screen.findByRole('heading', { name: '管理员创建完成' }),
    ).toBeInTheDocument()
    expect(screen.getByText('totp-secret')).toBeInTheDocument()
    expect(screen.getByText('recovery-1')).toBeInTheDocument()
  })
})
