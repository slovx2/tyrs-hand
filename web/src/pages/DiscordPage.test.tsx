import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
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
    http.get('/api/v1/discord/members', () =>
      HttpResponse.json([
        {
          guildId: '123',
          discordUserId: '10',
          username: 'alice',
          displayName: 'Alice',
          bound: true,
          githubLogin: 'alice',
        },
        {
          guildId: '123',
          discordUserId: '20',
          username: 'bob',
          displayName: 'Bob',
          bound: true,
          githubLogin: 'bob',
          forumId: '11111111-1111-1111-1111-111111111111',
        },
      ]),
    ),
  )
}

describe('DiscordPage', () => {
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
    expect(await screen.findByText('connected')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '全新初始化' }))
    const confirmation = screen.getByLabelText(/输入确认指令/)
    await user.type(confirmation, 'DELETE ALL CHANNELS 123')
    await user.click(screen.getByRole('button', { name: '执行预检' }))
    expect(await screen.findByText('预检通过')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '开始初始化' }))
    expect(await screen.findByText(/初始化操作已创建/)).toBeInTheDocument()
    expect(initialize).toHaveBeenCalledWith({
      mode: 'fresh',
      confirmation: 'DELETE ALL CHANNELS 123',
    })
  })

  it('创建个人 Forum 并配置 operator 权限', async () => {
    commonHandlers()
    const createForum = vi.fn()
    const grant = vi.fn()
    server.use(
      http.post('/api/v1/discord/members/10/forum', () => {
        createForum()
        return HttpResponse.json(
          { id: '33333333-3333-3333-3333-333333333333' },
          { status: 202 },
        )
      }),
      http.put(
        '/api/v1/discord/forums/:forumId/access/:memberId',
        async ({ params, request }) => {
          grant(params, await request.json())
          return new HttpResponse(null, { status: 204 })
        },
      ),
    )
    renderPage()
    const user = userEvent.setup()
    expect((await screen.findAllByText('Alice')).length).toBeGreaterThan(0)
    await user.click(screen.getByRole('button', { name: '创建个人 Forum' }))
    expect(createForum).toHaveBeenCalled()

    await user.selectOptions(screen.getByLabelText('Bob 授权成员'), '10')
    await user.selectOptions(screen.getByLabelText('Bob 权限'), 'operator')
    await user.click(screen.getByRole('button', { name: '授权' }))
    expect(grant).toHaveBeenCalledWith(
      expect.objectContaining({
        forumId: '11111111-1111-1111-1111-111111111111',
        memberId: '10',
      }),
      { accessLevel: 'operator' },
    )
  })
})
