import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
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
    </QueryClientProvider>,
  )
}

describe('DevicesPage', () => {
  it('生成二维码并确认扫码设备', async () => {
    const approve = vi.fn()
    let pairingStatus = 'waiting_scan'
    server.use(
      http.get('/api/v1/client-devices', () =>
        HttpResponse.json({ items: [] }),
      ),
      http.post('/api/v1/client-device-pairings', () =>
        HttpResponse.json(
          {
            id: '11111111-1111-1111-1111-111111111111',
            status: 'waiting_scan',
            expiresAt: '2026-08-02T10:10:00Z',
            pairingUri: 'tyrshand://device-pair?v=1',
            qrDataUrl: 'data:image/png;base64,ZmFrZQ==',
          },
          { status: 201 },
        ),
      ),
      http.get('/api/v1/client-device-pairings/:id', () =>
        HttpResponse.json({
          id: '11111111-1111-1111-1111-111111111111',
          status: pairingStatus,
          deviceId:
            pairingStatus === 'waiting_confirmation' ? 'device-1' : undefined,
          deviceName:
            pairingStatus === 'waiting_confirmation' ? 'Pixel E2E' : undefined,
          platform:
            pairingStatus === 'waiting_confirmation' ? 'android' : undefined,
          expiresAt: '2026-08-02T10:10:00Z',
        }),
      ),
      http.post('/api/v1/client-device-pairings/:id/approve', ({ params }) => {
        approve(params.id)
        return HttpResponse.json({
          id: 'device-1',
          name: 'Pixel E2E',
          platform: 'android',
        })
      }),
    )

    renderPage()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: '添加设备' }))
    expect(
      await screen.findByRole('img', { name: '设备绑定二维码' }),
    ).toBeInTheDocument()

    pairingStatus = 'waiting_confirmation'
    await user.click(screen.getByRole('button', { name: '刷新扫码状态' }))
    expect(await screen.findByText('Pixel E2E')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '确认设备' }))
    expect(approve).toHaveBeenCalledWith('11111111-1111-1111-1111-111111111111')
  })
})
