import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { server } from '../test/server'
import { useUI } from '../state'
import { ResourcePage } from './ResourcePage'

describe('ResourcePage', () => {
  it('显示从管理 API 返回的资源', async () => {
    server.use(
      http.get('/api/v1/work-items', () =>
        HttpResponse.json({
          items: [{ id: 'item-1', kind: 'issue', number: 42, state: 'open' }],
        }),
      ),
    )
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <ResourcePage resource="work-items" title="工作项" />
      </QueryClientProvider>,
    )
    expect(screen.getByRole('heading', { name: '工作项' })).toBeInTheDocument()
    expect(await screen.findByText('item-1')).toBeInTheDocument()
    expect(screen.getByText('42')).toBeInTheDocument()
  })

  it('显示空状态', async () => {
    server.use(
      http.get('/api/v1/workers', () => HttpResponse.json({ items: [] })),
    )
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <ResourcePage resource="workers" title="Worker" />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('暂无数据')).toBeInTheDocument()
  })

  it('允许管理员对 error Control 执行对账和重置', async () => {
    const user = userEvent.setup()
    let reconcileCount = 0
    let resetCount = 0
    server.use(
      http.get('/api/v1/threads', () =>
        HttpResponse.json({
          items: [
            { id: 'control-error', status: 'error' },
            { id: 'control-idle', status: 'idle' },
          ],
        }),
      ),
      http.post('/api/v1/controls/control-error/reconcile', () => {
        reconcileCount += 1
        return new HttpResponse(null, { status: 204 })
      }),
      http.post('/api/v1/controls/control-error/reset', () => {
        resetCount += 1
        return new HttpResponse(null, { status: 204 })
      }),
    )
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <ResourcePage resource="threads" title="Codex Controls" />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('control-error')).toBeInTheDocument()
    expect(screen.getAllByText('—')).not.toHaveLength(0)
    await user.click(screen.getByRole('button', { name: '对账' }))
    await waitFor(() => expect(reconcileCount).toBe(1))
    await waitFor(() =>
      expect(useUI.getState().toasts.at(-1)?.message).toBe('对账已完成'),
    )
    await user.click(screen.getByRole('button', { name: '重置' }))
    await waitFor(() => expect(resetCount).toBe(1))
    await waitFor(() =>
      expect(useUI.getState().toasts.at(-1)?.message).toBe('重置已完成'),
    )
  })
})
