import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ToastViewport } from '../components/ToastViewport'
import { server } from '../test/server'
import { DevicesPage } from './DevicesPage'

afterEach(cleanup)

function renderPage() {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <DevicesPage />
      <ToastViewport />
    </QueryClientProvider>,
  )
}

describe('DevicesPage', () => {
  it('选择 Worker 生成只读二维码并由管理员确认', async () => {
    const create = vi.fn()
    const approve = vi.fn()
    server.use(
      http.get('/api/v1/workers', () =>
        HttpResponse.json({
          items: [
            {
              id: '11111111-1111-1111-1111-111111111111',
              name: 'song-ubuntu',
              roles: ['discord'],
              enabled: true,
              maxConcurrentJobs: 4,
              protocolVersion: 28,
              status: 'online',
              sshHostKeyFingerprint:
                'SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
            },
          ],
        }),
      ),
      http.get('/api/v1/client-devices', () =>
        HttpResponse.json({ items: [] }),
      ),
      http.post('/api/v1/client-device-pairings', async ({ request }) => {
        create(await request.json())
        return HttpResponse.json(
          {
            id: '22222222-2222-2222-2222-222222222222',
            status: 'waiting_scan',
            workerId: '11111111-1111-1111-1111-111111111111',
            workerName: 'song-ubuntu',
            sshHostKeyFingerprint:
              'SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
            expiresAt: '2026-08-15T02:00:00Z',
            qrDataUrl: 'data:image/png;base64,dGVzdA==',
          },
          { status: 201 },
        )
      }),
      http.get('/api/v1/client-device-pairings/:id', () =>
        HttpResponse.json({
          id: '22222222-2222-2222-2222-222222222222',
          status: 'waiting_confirmation',
          deviceName: 'Pixel E2E',
          platform: 'android',
          workerId: '11111111-1111-1111-1111-111111111111',
          workerName: 'song-ubuntu',
          sshHostKeyFingerprint:
            'SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
          expiresAt: '2026-08-15T02:00:00Z',
        }),
      ),
      http.post('/api/v1/client-device-pairings/:id/approve', ({ params }) => {
        approve(params.id)
        return HttpResponse.json({
          id: '33333333-3333-3333-3333-333333333333',
          name: 'Pixel E2E',
          platform: 'android',
          machines: [],
        })
      }),
    )

    renderPage()
    expect(
      await screen.findByText(/扫码只允许移动端查看所选 Worker 的定时任务/),
    ).toBeInTheDocument()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: '生成二维码' }))
    expect(create).toHaveBeenCalledWith({
      workerId: '11111111-1111-1111-1111-111111111111',
    })
    expect(await screen.findByText('Pixel E2E')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '确认授权' }))
    expect(approve).toHaveBeenCalledWith('22222222-2222-2222-2222-222222222222')
    expect(
      await screen.findByText('设备已获得这台 Worker 的定时任务只读权限'),
    ).toBeInTheDocument()
  })

  it('展示每台设备获准查看的机器', async () => {
    server.use(
      http.get('/api/v1/workers', () => HttpResponse.json({ items: [] })),
      http.get('/api/v1/client-devices', () =>
        HttpResponse.json({
          items: [
            {
              id: '33333333-3333-3333-3333-333333333333',
              name: 'iPhone',
              platform: 'ios',
              approvedAt: '2026-08-15T01:00:00Z',
              machines: [
                {
                  workerId: '11111111-1111-1111-1111-111111111111',
                  name: 'song-ubuntu',
                  sshHostKeyFingerprint:
                    'SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
                  status: 'online',
                  approvedAt: '2026-08-15T01:00:00Z',
                },
              ],
            },
          ],
        }),
      ),
    )
    renderPage()
    expect(
      await screen.findByRole('heading', { name: 'iPhone' }),
    ).toBeInTheDocument()
    expect(screen.getByText('song-ubuntu')).toBeInTheDocument()
    expect(screen.getByText('ios · 1 台机器')).toBeInTheDocument()
  })
})
