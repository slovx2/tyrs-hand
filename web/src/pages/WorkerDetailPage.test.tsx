import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Navigate, Route, Routes } from 'react-router'
import { server } from '../test/server'
import { WorkerConfigRoute } from './WorkerConfigPage'
import {
  WorkerDetailPage,
  WorkerOverviewPage,
  WorkerUsersPage,
} from './WorkerDetailPage'
import { WorkerWorkspacePage } from './WorkspacesPage'

afterEach(cleanup)

const workerId = '11111111-1111-1111-1111-111111111111'
const worker = {
  id: workerId,
  name: 'worker-primary',
  roles: ['discord'],
  enabled: true,
  maxConcurrentJobs: 4,
  protocolVersion: 23,
  status: 'online',
}

function renderRoute(path: string) {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="workers/:workerId" element={<WorkerDetailPage />}>
            <Route index element={<Navigate to="overview" replace />} />
            <Route path="overview" element={<WorkerOverviewPage />} />
            <Route path="codex" element={<WorkerConfigRoute />} />
            <Route path="workspace" element={<WorkerWorkspacePage />} />
            <Route path="users" element={<WorkerUsersPage />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function commonHandlers(role: 'admin' | 'user' = 'admin') {
  server.use(
    http.get('/api/v1/auth/me', () => HttpResponse.json({ role })),
    http.get(`/api/v1/workers/${workerId}`, () => HttpResponse.json(worker)),
    http.get(`/api/v1/workers/${workerId}/runtimes`, () =>
      HttpResponse.json([]),
    ),
  )
}

describe('WorkerDetailPage', () => {
  it('Claude 配置使用独立路径并隐藏 OpenAI 登录，全局指令两引擎共用一份', async () => {
    commonHandlers()
    const provider = vi.fn()
    const agents = vi.fn()
    const oauth = vi.fn()
    server.use(
      http.get(
        `/api/v1/workers/${workerId}/runtimes/:engine/config`,
        ({ params }) =>
          HttpResponse.json({
            revision: 'rev-1',
            agents: `${params.engine} instructions`,
            baseUrl: 'http://localhost:4321',
            envKey: 'ANTHROPIC_AUTH_TOKEN',
            apiKeyConfigured: true,
            authMethod: 'auth-token',
            model: 'claude-config-model',
          }),
      ),
      http.get(`/api/v1/workers/${workerId}/codex/oauth/devices`, () => {
        oauth()
        return HttpResponse.json({ status: 'idle' })
      }),
      http.put(
        `/api/v1/workers/${workerId}/runtimes/claude-code/config/provider`,
        async ({ request }) => {
          provider(await request.json())
          return HttpResponse.json({ revision: 'rev-2' })
        },
      ),
      http.put(
        `/api/v1/workers/${workerId}/runtimes/claude-code/config/agents`,
        async ({ request }) => {
          agents(await request.json())
          return HttpResponse.json({ revision: 'rev-3' })
        },
      ),
    )
    renderRoute(`/workers/${workerId}/codex`)
    const user = userEvent.setup()
    await screen.findByRole('heading', { name: '全局指令' })
    const oauthCalls = oauth.mock.calls.length
    await user.selectOptions(screen.getByLabelText('运行时'), 'claude-code')
    await screen.findByRole('heading', { name: '重启 Claude Code' })
    expect(screen.queryByText('登录 ChatGPT 账号')).not.toBeInTheDocument()
    expect(oauth).toHaveBeenCalledTimes(oauthCalls)
    expect(screen.getByLabelText('默认模型')).toHaveValue('claude-config-model')
    await user.click(screen.getByRole('button', { name: '保存 Provider' }))
    await vi.waitFor(() =>
      expect(provider).toHaveBeenCalledWith({
        revision: 'rev-1',
        baseUrl: 'http://localhost:4321',
        apiKey: '',
        authMethod: 'auth-token',
        model: 'claude-config-model',
        providerSyncEnabled: true,
      }),
    )
    // Claude 不再单独维护 CLAUDE.md：页面只有一份来自 Codex AGENTS.md 的全局指令。
    expect(screen.getAllByLabelText('全局指令')).toHaveLength(1)
    expect(screen.getByLabelText('全局指令')).toHaveValue('codex instructions')
    expect(
      screen.queryByDisplayValue('claude-code instructions'),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: '保存 CLAUDE.md' }),
    ).not.toBeInTheDocument()
    expect(agents).not.toHaveBeenCalled()
  })

  it('关闭 Claude Provider 同步无需 Key，且不发送表单中的凭据', async () => {
    commonHandlers()
    const provider = vi.fn()
    server.use(
      http.get(`/api/v1/workers/${workerId}/runtimes/:engine/config`, () =>
        HttpResponse.json({
          revision: 'native-1',
          agents: '',
          baseUrl: '',
          envKey: '',
          apiKeyConfigured: false,
          authMethod: 'api-key',
          model: '',
          providerSyncEnabled: true,
        }),
      ),
      http.put(
        `/api/v1/workers/${workerId}/runtimes/claude-code/config/provider`,
        async ({ request }) => {
          provider(await request.json())
          return HttpResponse.json({ revision: 'native-2' })
        },
      ),
    )
    renderRoute(`/workers/${workerId}/codex`)
    const user = userEvent.setup()
    await screen.findByRole('heading', { name: '全局指令' })
    await user.selectOptions(screen.getByLabelText('运行时'), 'claude-code')
    await screen.findByRole('checkbox', { name: '同步 Provider' })
    expect(screen.getByRole('button', { name: '保存 Provider' })).toBeDisabled()
    await user.click(screen.getByRole('checkbox', { name: '同步 Provider' }))
    expect(screen.getByLabelText('认证方式')).toBeDisabled()
    await user.click(screen.getByRole('button', { name: '保存 Provider' }))
    await vi.waitFor(() =>
      expect(provider).toHaveBeenCalledWith({
        revision: 'native-1',
        providerSyncEnabled: false,
      }),
    )
  })

  it('直接访问详情路由并在进入 Codex 页后才读取配置', async () => {
    commonHandlers()
    const configRequest = vi.fn()
    server.use(
      http.get(`/api/v1/workers/${workerId}/runtimes/codex/config`, () => {
        configRequest()
        return HttpResponse.json({
          revision: 'rev-1',
          agents: '',
          baseUrl: 'https://model.example/v1',
          envKey: 'TYRS_HAND_MODEL_API_KEY',
          apiKeyConfigured: true,
        })
      }),
      http.get(`/api/v1/workers/${workerId}/codex/oauth/devices`, () =>
        HttpResponse.json({ status: 'idle' }),
      ),
    )
    renderRoute(`/workers/${workerId}/overview`)
    const user = userEvent.setup()

    expect(await screen.findByText('运行状态')).toBeInTheDocument()
    expect(configRequest).not.toHaveBeenCalled()
    await user.click(screen.getByRole('link', { name: '运行时配置' }))
    expect(await screen.findByText('Model Provider')).toBeInTheDocument()
    expect(configRequest).toHaveBeenCalledTimes(1)
  })

  it('在 Codex 子页更新 Provider、全局指令、OAuth 和重启', async () => {
    commonHandlers()
    const provider = vi.fn()
    const agents = vi.fn()
    const restart = vi.fn()
    const oauth = vi.fn()
    let configRevision = 'rev-1'
    let configBaseUrl = 'https://model.example/v1'
    let oauthPending = false
    server.use(
      http.get(`/api/v1/workers/${workerId}/runtimes/codex/config`, () =>
        HttpResponse.json({
          revision: configRevision,
          agents: 'old agents',
          baseUrl: configBaseUrl,
          envKey: 'TYRS_HAND_MODEL_API_KEY',
          apiKeyConfigured: true,
        }),
      ),
      http.put(
        `/api/v1/workers/${workerId}/runtimes/codex/config/provider`,
        async ({ request }) => {
          const input = (await request.json()) as { baseUrl: string }
          provider(input)
          configBaseUrl = input.baseUrl
          configRevision = 'rev-2'
          return HttpResponse.json({ revision: 'rev-2' })
        },
      ),
      http.put(
        `/api/v1/workers/${workerId}/runtimes/codex/config/agents`,
        async ({ request }) => {
          agents(await request.json())
          return HttpResponse.json({ revision: 'rev-3' })
        },
      ),
      http.post(`/api/v1/workers/${workerId}/runtimes/codex/restart`, () => {
        restart()
        return new HttpResponse(null, { status: 202 })
      }),
      http.get(`/api/v1/workers/${workerId}/codex/oauth/devices`, () =>
        HttpResponse.json(
          oauthPending
            ? {
                status: 'pending',
                userCode: 'ABCD-EFGH',
                verificationUrl: 'https://example.com/device',
              }
            : { status: 'idle' },
        ),
      ),
      http.post(`/api/v1/workers/${workerId}/codex/oauth/devices`, () => {
        oauth()
        oauthPending = true
        return HttpResponse.json({ status: 'pending' })
      }),
    )
    renderRoute(`/workers/${workerId}/codex`)
    const user = userEvent.setup()

    const baseUrl = await screen.findByDisplayValue('https://model.example/v1')
    await user.clear(baseUrl)
    await user.type(baseUrl, 'https://new.example/v1')
    const apiKeyInput = screen.getByPlaceholderText('留空保持原值')
    await user.click(screen.getByRole('button', { name: '显示' }))
    expect(apiKeyInput).toHaveAttribute('type', 'text')
    await user.click(screen.getByRole('button', { name: '隐藏' }))
    await user.click(screen.getByRole('button', { name: '保存 Provider' }))
    expect(provider).toHaveBeenCalledWith({
      revision: 'rev-1',
      baseUrl: 'https://new.example/v1',
      apiKey: '',
    })
    await user.click(screen.getByRole('button', { name: '清除 API Key' }))
    expect(provider).toHaveBeenLastCalledWith({
      revision: 'rev-2',
      baseUrl: 'https://new.example/v1',
      clearApiKey: true,
    })

    const agentsInput = screen.getByDisplayValue('old agents')
    await user.clear(agentsInput)
    await user.type(agentsInput, 'new agents')
    await user.click(screen.getByRole('button', { name: '保存全局指令' }))
    await vi.waitFor(() =>
      expect(agents).toHaveBeenCalledWith({
        revision: 'rev-2',
        content: 'new agents',
      }),
    )
    await user.click(screen.getByRole('button', { name: '重启 Codex' }))
    await user.click(screen.getByRole('button', { name: '登录 ChatGPT 账号' }))
    expect(restart).toHaveBeenCalledOnce()
    expect(oauth).toHaveBeenCalledOnce()
    expect(await screen.findByText('ABCD-EFGH')).toBeInTheDocument()
  })

  it('刷新 Workspace 子路由时只读取当前 Worker Workspace', async () => {
    commonHandlers()
    server.use(
      http.get(`/api/v1/workers/${workerId}/workspace`, () =>
        HttpResponse.json({ workspace: null }),
      ),
      http.get('/api/v1/discord/members', () => HttpResponse.json([])),
      http.post(`/api/v1/workers/${workerId}/workspace/scan`, () =>
        HttpResponse.json({ workspace: null, scan: { projects: [] } }),
      ),
    )
    renderRoute(`/workers/${workerId}/workspace`)

    expect(await screen.findByText('尚未绑定 Workspace')).toBeInTheDocument()
    expect(
      screen.getByText(/管理 worker-primary 的宿主项目/),
    ).toBeInTheDocument()
  })

  it('普通用户不显示用户分配入口，直接访问时返回概览', async () => {
    commonHandlers('user')
    renderRoute(`/workers/${workerId}/users`)

    expect(await screen.findByText('运行状态')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: '用户分配' })).toBeNull()
  })

  it('管理员可在独立子页分配和移除普通用户', async () => {
    commonHandlers()
    const update = vi.fn()
    server.use(
      http.get('/api/v1/users', () =>
        HttpResponse.json({
          items: [
            { id: 'admin', username: 'admin', role: 'admin', enabled: true },
            { id: 'user-a', username: 'alice', role: 'user', enabled: true },
            { id: 'user-b', username: 'bob', role: 'user', enabled: false },
          ],
        }),
      ),
      http.get(`/api/v1/workers/${workerId}/users`, () =>
        HttpResponse.json({ items: [{ id: 'user-a', username: 'alice' }] }),
      ),
      http.delete(`/api/v1/workers/${workerId}/users/:userId`, ({ params }) => {
        update('remove', params.userId)
        return new HttpResponse(null, { status: 204 })
      }),
      http.put(`/api/v1/workers/${workerId}/users/:userId`, ({ params }) => {
        update('add', params.userId)
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderRoute(`/workers/${workerId}/users`)
    const user = userEvent.setup()

    await screen.findByText('alice')
    expect(screen.queryByText('admin')).toBeNull()
    await user.click(screen.getByRole('button', { name: '移除' }))
    await user.click(screen.getByRole('button', { name: '分配' }))
    expect(update).toHaveBeenCalledWith('remove', 'user-a')
    expect(update).toHaveBeenCalledWith('add', 'user-b')
  })
})
