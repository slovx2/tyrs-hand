import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '../test/server'
import { CodexSettingsPage } from './CodexSettingsPage'

describe('CodexSettingsPage', () => {
  it('展示继承值并保存 Forum 覆盖', async () => {
    const saved = vi.fn()
    server.use(
      http.get('/api/v1/settings/codex', () =>
        HttpResponse.json({
          modelOptions: ['gpt-5.6-sol', 'gpt-5.6-terra'],
          items: [
            {
              id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
              owner: 'datawake-ai',
              name: 'tyrs-hand',
              settings: {
                model: null,
                serviceTier: null,
                reasoningEffort: null,
              },
              effective: {
                model: 'gpt-5.6-sol',
                serviceTier: 'standard',
                reasoningEffort: 'medium',
              },
              forums: [
                {
                  id: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
                  name: 'alice-dev',
                  ownerDiscordUserId: '10',
                  settings: {
                    model: null,
                    serviceTier: null,
                    reasoningEffort: null,
                  },
                  effective: {
                    model: 'gpt-5.6-sol',
                    serviceTier: 'standard',
                    reasoningEffort: 'medium',
                  },
                },
              ],
            },
          ],
        }),
      ),
      http.put(
        '/api/v1/settings/codex/forums/bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
        async ({ request }) => {
          saved(await request.json())
          return new HttpResponse(null, { status: 204 })
        },
      ),
    )
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <CodexSettingsPage />
      </QueryClientProvider>,
    )
    const forum = await screen.findByText('Discord Forum · alice-dev')
    const panel = forum.closest('div.rounded-xl') as HTMLElement
    const user = userEvent.setup()
    await user.selectOptions(within(panel).getByLabelText('服务等级'), 'fast')
    await user.click(within(panel).getByRole('button', { name: '保存设置' }))
    expect(saved).toHaveBeenCalledWith(
      expect.objectContaining({
        serviceTier: 'fast',
        model: null,
        reasoningEffort: null,
      }),
    )
  })
})
