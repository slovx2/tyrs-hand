import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '../test/server'
import { GitHubAgentSettingsPage } from './GitHubAgentSettingsPage'

describe('GitHubAgentSettingsPage', () => {
  it('展示继承值并保存仓库覆盖', async () => {
    const saved = vi.fn()
    server.use(
      http.get('/api/v1/settings/github-agent', () =>
        HttpResponse.json({
          models: [
            {
              id: 'gpt-5.6-sol',
              supportedReasoningEfforts: [
                { reasoningEffort: 'low' },
                { reasoningEffort: 'medium' },
                { reasoningEffort: 'high' },
                { reasoningEffort: 'xhigh' },
              ],
              defaultReasoningEffort: 'low',
              serviceTiers: [{ id: 'priority' }],
              additionalSpeedTiers: ['fast'],
              isDefault: true,
            },
            {
              id: 'gpt-5.6-terra',
              supportedReasoningEfforts: [{ reasoningEffort: 'medium' }],
              defaultReasoningEffort: 'medium',
              serviceTiers: [],
              additionalSpeedTiers: [],
              isDefault: false,
            },
          ],
          items: [
            {
              id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
              owner: 'example-org',
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
            },
          ],
        }),
      ),
      http.put(
        '/api/v1/settings/github-agent/repositories/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
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
        <GitHubAgentSettingsPage />
      </QueryClientProvider>,
    )
    await screen.findByText('example-org/tyrs-hand')
    const user = userEvent.setup()
    await user.selectOptions(screen.getByLabelText('服务等级'), 'fast')
    await user.click(screen.getByRole('button', { name: '保存设置' }))
    expect(saved).toHaveBeenCalledWith(
      expect.objectContaining({
        serviceTier: 'fast',
        model: null,
        reasoningEffort: null,
      }),
    )
  })
})
