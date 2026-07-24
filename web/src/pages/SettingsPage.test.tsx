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
  it('即时切换主题并保存模型与全局指令设置', async () => {
    useUI.getState().setTheme('light')
    const saved = vi.fn()
    const savedAgents = vi.fn()
    server.use(
      http.get('/api/v1/settings/agent-provider', () =>
        HttpResponse.json({
          modelSource: 'chatgpt',
          baseUrl: '',
          model: 'gpt-5',
          reasoningEffort: 'high',
          serviceTier: 'priority',
          proxyUrl: '',
          providerConfigured: true,
          chatgptConfigured: true,
          chatgptAuthRevision: 3,
          configSignature: 'signature',
          chatgptAccount: {
            configured: true,
            email: 'admin@example.com',
            planType: 'plus',
          },
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
    await user.click(screen.getByRole('button', { name: '保存模型设置' }))

    expect(saved).toHaveBeenCalledWith(
      expect.objectContaining({
        model: 'gpt-5.6',
        modelSource: 'chatgpt',
      }),
    )
    expect(await screen.findByText('模型设置已保存')).toBeInTheDocument()

    const agents = screen.getByLabelText('全局 AGENTS.md')
    await user.clear(agents)
    await user.type(agents, '# Shared rules')
    await user.click(screen.getByRole('button', { name: '保存全局 AGENTS.md' }))
    expect(savedAgents).toHaveBeenCalledWith({ content: '# Shared rules' })
    expect(await screen.findByText('全局 AGENTS.md 已保存')).toBeInTheDocument()
  })

  it('发起并取消后台 ChatGPT 登录', async () => {
    const canceled = vi.fn()
    const operation = {
      id: '92b45cb9-aa2c-4438-b194-50181736df94',
      status: 'awaiting_user',
      verificationUrl: 'https://auth.openai.com/codex/device',
      userCode: 'ABCD-1234',
      expiresAt: '2026-07-24T10:00:00Z',
    }
    server.use(
      http.get('/api/v1/settings/agent-provider', () =>
        HttpResponse.json({
          modelSource: 'provider',
          baseUrl: '',
          model: 'gpt-5',
          reasoningEffort: 'high',
          serviceTier: 'priority',
          proxyUrl: '',
          providerConfigured: true,
          chatgptConfigured: false,
          chatgptAuthRevision: 0,
          configSignature: 'signature',
          chatgptAccount: { configured: false },
        }),
      ),
      http.get('/api/v1/settings/global-agents', () =>
        HttpResponse.json({ content: '' }),
      ),
      http.post('/api/v1/settings/agent-provider/chatgpt/login', () =>
        HttpResponse.json(operation, { status: 202 }),
      ),
      http.get(
        `/api/v1/settings/agent-provider/chatgpt/login/${operation.id}`,
        () => HttpResponse.json(operation),
      ),
      http.delete(
        `/api/v1/settings/agent-provider/chatgpt/login/${operation.id}`,
        () => {
          canceled()
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
    await user.click(
      await screen.findByRole('button', { name: '登录 ChatGPT' }),
    )
    const link = await screen.findByRole('link', {
      name: '打开 ChatGPT 设备授权页面',
    })
    expect(screen.getByLabelText('设备授权代码')).toHaveTextContent(
      operation.userCode,
    )
    expect(link).toHaveAttribute('href', operation.verificationUrl)
    await user.click(screen.getByRole('button', { name: '取消登录' }))
    expect(canceled).toHaveBeenCalledOnce()
    expect(
      (await screen.findAllByText('已取消 ChatGPT 登录')).length,
    ).toBeGreaterThan(0)
  })
})
