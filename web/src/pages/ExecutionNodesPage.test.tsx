import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ToastViewport } from '../components/ToastViewport'
import { server } from '../test/server'
import { ExecutionNodesPage } from './ExecutionNodesPage'

afterEach(cleanup)

function renderPage() {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <ExecutionNodesPage />
      <ToastViewport />
    </QueryClientProvider>,
  )
}

describe('ExecutionNodesPage', () => {
  it('创建节点、展示单次 Token 并冻结默认 Placement 配置', async () => {
    const create = vi.fn()
    const saveDefaults = vi.fn()
    server.use(
      http.get('/api/v1/execution-nodes', () =>
        HttpResponse.json({
          items: [
            {
              id: '11111111-1111-1111-1111-111111111111',
              name: 'song-ubuntu',
              roles: ['github', 'discord'],
              enabled: true,
              maxConcurrentJobs: 6,
              protocolVersion: 1,
              workerVersion: '0.1.0',
              status: 'online',
              heartbeatAt: '2026-07-21T00:00:00Z',
            },
          ],
        }),
      ),
      http.get('/api/v1/settings/execution', () =>
        HttpResponse.json({ githubNodeId: null, discordNodeId: null }),
      ),
      http.post('/api/v1/execution-nodes', async ({ request }) => {
        create(await request.json())
        return HttpResponse.json(
          {
            node: {
              id: '22222222-2222-2222-2222-222222222222',
              name: 'home-2',
            },
            enrollmentToken: 'one-time-enrollment-token',
            expiresIn: 900,
          },
          { status: 201 },
        )
      }),
      http.put('/api/v1/settings/execution', async ({ request }) => {
        saveDefaults(await request.json())
        return new HttpResponse(null, { status: 204 })
      }),
    )

    renderPage()
    const user = userEvent.setup()
    expect(
      await screen.findByRole('heading', { name: 'song-ubuntu' }),
    ).toBeInTheDocument()
    await user.selectOptions(
      screen.getByLabelText('GitHub 默认执行节点'),
      '11111111-1111-1111-1111-111111111111',
    )
    expect(saveDefaults).toHaveBeenCalledWith({
      githubNodeId: '11111111-1111-1111-1111-111111111111',
      discordNodeId: null,
    })

    await user.type(screen.getByLabelText('名称'), 'home-2')
    await user.clear(screen.getByLabelText('并发上限'))
    await user.type(screen.getByLabelText('并发上限'), '4')
    await user.click(screen.getByRole('button', { name: '创建并生成 Token' }))
    expect(create).toHaveBeenCalledWith({
      name: 'home-2',
      roles: ['github', 'discord'],
      maxConcurrentJobs: 4,
    })
    expect(
      await screen.findByText('one-time-enrollment-token'),
    ).toBeInTheDocument()
    expect(await screen.findByText('执行节点已创建')).toBeInTheDocument()
  })

  it('轮换凭据与停用节点使用独立管理动作', async () => {
    const rotate = vi.fn()
    const disable = vi.fn()
    server.use(
      http.get('/api/v1/execution-nodes', () =>
        HttpResponse.json({
          items: [
            {
              id: '11111111-1111-1111-1111-111111111111',
              name: 'song-ubuntu',
              roles: ['github', 'discord'],
              enabled: true,
              maxConcurrentJobs: 6,
              protocolVersion: 1,
              status: 'online',
            },
          ],
        }),
      ),
      http.get('/api/v1/settings/execution', () => HttpResponse.json({})),
      http.post('/api/v1/execution-nodes/:id/enrollments', ({ params }) => {
        rotate(params.id)
        return HttpResponse.json(
          { enrollmentToken: 'rotated-token', expiresIn: 900 },
          { status: 201 },
        )
      }),
      http.put(
        '/api/v1/execution-nodes/:id/enabled',
        async ({ params, request }) => {
          disable(params.id, await request.json())
          return new HttpResponse(null, { status: 204 })
        },
      ),
    )

    renderPage()
    const user = userEvent.setup()
    await screen.findByRole('heading', { name: 'song-ubuntu' })
    await user.click(screen.getByRole('button', { name: '轮换凭据' }))
    expect(await screen.findByText('rotated-token')).toBeInTheDocument()
    expect(rotate).toHaveBeenCalledWith('11111111-1111-1111-1111-111111111111')
    await user.click(screen.getByRole('button', { name: '停用' }))
    expect(disable).toHaveBeenCalledWith(
      '11111111-1111-1111-1111-111111111111',
      { enabled: false },
    )
  })
})
