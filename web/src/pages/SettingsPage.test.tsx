import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { ToastViewport } from '../components/ToastViewport'
import { useUI } from '../state'
import { server } from '../test/server'
import { SettingsPage } from './SettingsPage'

describe('SettingsPage', () => {
  it('即时切换主题并保存 Provider 设置', async () => {
    useUI.getState().setTheme('light')
    const saved = vi.fn()
    const savedAgents = vi.fn()
    server.use(
      http.get('/api/v1/settings/agent-provider', () =>
        HttpResponse.json({
          providerType: 'device-code',
          baseUrl: '',
          model: 'gpt-5',
          reasoningEffort: 'high',
          serviceTier: 'priority',
          proxyUrl: '',
          configured: true,
        }),
      ),
      http.put('/api/v1/settings/agent-provider', async ({ request }) => {
        saved(await request.json())
        return new HttpResponse(null, { status: 204 })
      }),
      http.get('/api/v1/settings/global-agents', () =>
        HttpResponse.json({ content: '# Existing\n' }),
      ),
      http.put('/api/v1/settings/global-agents', async ({ request }) => {
        savedAgents(await request.json())
        return new HttpResponse(null, { status: 204 })
      }),
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
    await screen.findByDisplayValue('gpt-5')
    await user.click(screen.getByRole('button', { name: '暗色' }))
    expect(useUI.getState().theme).toBe('dark')
    await user.clear(screen.getByLabelText('默认模型'))
    await user.type(screen.getByLabelText('默认模型'), 'gpt-5.6')
    await user.click(screen.getByRole('button', { name: '保存 Provider 设置' }))

    expect(saved).toHaveBeenCalledWith(
      expect.objectContaining({
        model: 'gpt-5.6',
        providerType: 'device-code',
      }),
    )
    expect(await screen.findByText('Provider 设置已保存')).toBeInTheDocument()

    const agents = screen.getByLabelText('全局 AGENTS.md')
    await user.clear(agents)
    await user.type(agents, '# Shared rules')
    await user.click(screen.getByRole('button', { name: '保存全局 AGENTS.md' }))
    expect(savedAgents).toHaveBeenCalledWith({ content: '# Shared rules' })
    expect(await screen.findByText('全局 AGENTS.md 已保存')).toBeInTheDocument()
  })
})
