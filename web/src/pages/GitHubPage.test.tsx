import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { ToastViewport } from '../components/ToastViewport'
import { server } from '../test/server'
import { GitHubPage } from './GitHubPage'

describe('GitHubPage', () => {
  it('保存 GitHub App 后刷新状态并显示成功通知', async () => {
    const saved = vi.fn()
    server.use(
      http.get('/api/v1/github/app', () =>
        HttpResponse.json({ configured: false }),
      ),
      http.get('/api/v1/github/app/manifest', () =>
        HttpResponse.json({ url: '', manifest: '' }),
      ),
      http.put('/api/v1/github/app', async ({ request }) => {
        saved(await request.json())
        return new HttpResponse(null, { status: 204 })
      }),
    )
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <GitHubPage />
        <ToastViewport />
      </QueryClientProvider>,
    )
    const user = userEvent.setup()
    expect(await screen.findByText('尚未配置')).toBeInTheDocument()
    await user.type(screen.getByLabelText('App ID'), '4329880')
    await user.type(screen.getByLabelText('Client ID'), 'client-id')
    await user.type(screen.getByLabelText('App Slug'), 'tyrshand')
    await user.type(screen.getByLabelText('Private Key（PEM）'), 'private-key')
    await user.type(screen.getByLabelText('Webhook Secret'), 'secret')
    await user.click(screen.getByRole('button', { name: '保存 GitHub App' }))

    expect(saved).toHaveBeenCalledWith({
      appId: 4329880,
      clientId: 'client-id',
      appSlug: 'tyrshand',
      privateKey: 'private-key',
      webhookSecret: 'secret',
    })
    expect(await screen.findByText('GitHub App 设置已保存')).toBeInTheDocument()
  })
})
