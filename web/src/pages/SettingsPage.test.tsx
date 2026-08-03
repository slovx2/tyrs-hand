import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { ToastViewport } from '../components/ToastViewport'
import { server } from '../test/server'
import { SettingsPage } from './SettingsPage'

describe('SettingsPage', () => {
  it('只保存 GitHub Agent 指令，不提供 Control Codex Provider 配置', async () => {
    const saved = vi.fn()
    server.use(
      http.get('/api/v1/settings/github-agent-instructions', () =>
        HttpResponse.json({ content: '# Existing\n', revision: 3 }),
      ),
      http.put(
        '/api/v1/settings/github-agent-instructions',
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
        <SettingsPage />
        <ToastViewport />
      </QueryClientProvider>,
    )
    const user = userEvent.setup()
    expect(
      await screen.findByText(/Codex Provider.*仅从 Worker/),
    ).toBeInTheDocument()
    expect(screen.queryByText('登录 ChatGPT')).not.toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByLabelText('GitHub Agent 指令')).toHaveValue(
        '# Existing\n',
      ),
    )
    const agents = screen.getByLabelText('GitHub Agent 指令')
    await user.clear(agents)
    await user.type(agents, '# Shared rules')
    await user.click(
      screen.getByRole('button', { name: '保存 GitHub Agent 指令' }),
    )
    expect(saved).toHaveBeenCalledWith({ content: '# Shared rules' })
    expect(
      await screen.findByText('GitHub Agent 指令已保存'),
    ).toBeInTheDocument()
  })
})
