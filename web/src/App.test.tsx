import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'
import { App } from './App'
import { useUI } from './state'
import { server } from './test/server'

afterEach(cleanup)

describe('App', () => {
  it('从当前登录会话恢复 CSRF token', async () => {
    useUI.getState().setCSRFToken(undefined)
    server.use(
      http.get('/api/v1/setup/status', () =>
        HttpResponse.json({ setupRequired: false, githubConfigured: true }),
      ),
      http.get('/api/v1/auth/me', () =>
        HttpResponse.json({
          id: '11111111-1111-1111-1111-111111111111',
          username: 'admin',
          csrfToken: 'restored-csrf',
          expiresAt: '2026-08-01T00:00:00Z',
        }),
      ),
      ...['work-items', 'jobs', 'workers'].map((resource) =>
        http.get(`/api/v1/${resource}`, () => HttpResponse.json({ items: [] })),
      ),
    )

    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <MemoryRouter>
          <App />
        </MemoryRouter>
      </QueryClientProvider>,
    )

    expect(await screen.findByText('控制面概览')).toBeInTheDocument()
    expect(useUI.getState().csrfToken).toBe('restored-csrf')
  })
})
