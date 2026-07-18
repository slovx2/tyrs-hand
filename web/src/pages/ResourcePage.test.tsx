import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { server } from '../test/server'
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
})
