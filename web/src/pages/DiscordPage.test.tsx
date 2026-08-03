import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ToastViewport } from '../components/ToastViewport'
import { server } from '../test/server'
import { DiscordPage } from './DiscordPage'

afterEach(cleanup)

function renderPage() {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <DiscordPage />
      <ToastViewport />
    </QueryClientProvider>,
  )
}

function commonHandlers() {
  server.use(
    http.get('/api/v1/settings/discord', () =>
      HttpResponse.json({
        guildId: '123',
        enabled: true,
        communityEnabled: true,
        applicationId: '456',
        botUserId: '789',
        tokenConfigured: true,
      }),
    ),
    http.get('/api/v1/discord/status', () =>
      HttpResponse.json({
        configured: true,
        enabled: true,
        gatewayStatus: 'connected',
        pendingOutbox: 2,
        failedOutbox: 0,
        pendingInitializationOperations: 1,
      }),
    ),
  )
}

describe('DiscordPage', () => {
  it('只展示连接、状态和 Server 初始化', async () => {
    commonHandlers()
    renderPage()

    expect(await screen.findByText('connected')).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { name: '连接设置' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { name: 'Server 初始化' }),
    ).toBeInTheDocument()
    expect(screen.queryByText('普通项目')).not.toBeInTheDocument()
    expect(screen.queryByText('成员与开发 Forum')).not.toBeInTheDocument()
  })

  it('执行全新初始化预检并要求精确确认', async () => {
    commonHandlers()
    const initialize = vi.fn()
    server.use(
      http.post(
        '/api/v1/discord/initializations/preflight',
        async ({ request }) => {
          const body = (await request.json()) as { mode: string }
          expect(body.mode).toBe('fresh')
          return HttpResponse.json({
            guildId: '123',
            mode: 'fresh',
            creates: ['系统'],
            updates: [],
            deletes: ['旧频道'],
            conflicts: [],
            missingPermissions: [],
            channelCount: 1,
            safe: true,
          })
        },
      ),
      http.post('/api/v1/discord/initializations', async ({ request }) => {
        initialize(await request.json())
        return HttpResponse.json(
          { id: '22222222-2222-2222-2222-222222222222' },
          { status: 202 },
        )
      }),
    )
    renderPage()
    const user = userEvent.setup()

    await screen.findByText('connected')
    await user.click(screen.getByRole('button', { name: '全新初始化' }))
    await user.type(
      screen.getByLabelText(/输入确认指令/),
      'DELETE ALL CHANNELS 123',
    )
    await user.click(screen.getByRole('button', { name: '执行预检' }))
    expect(await screen.findByText('预检通过')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '开始初始化' }))

    expect(initialize).toHaveBeenCalledWith({
      mode: 'fresh',
      confirmation: 'DELETE ALL CHANNELS 123',
    })
    expect(
      await screen.findByText('初始化请求已提交，状态会自动刷新'),
    ).toBeInTheDocument()
  })
})
