import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { server } from '../test/server'
import { ToastViewport } from '../components/ToastViewport'
import { DiscordPage } from './DiscordPage'

afterEach(cleanup)

function renderPage() {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <DiscordPage />
      <ToastViewport />
    </QueryClientProvider>,
  )
}

function commonHandlers() {
  server.use(
    http.get('/api/v1/settings/discord', () =>
      HttpResponse.json({
        guildId: '123',
        enabled: true,
        communityEnabled: true,
        applicationId: '456',
        botUserId: '789',
        tokenConfigured: true,
      }),
    ),
    http.get('/api/v1/discord/status', () =>
      HttpResponse.json({
        configured: true,
        enabled: true,
        gatewayStatus: 'connected',
        pendingOutbox: 2,
        failedOutbox: 0,
        pendingInitializationOperations: 1,
      }),
    ),
    http.get('/api/v1/discord/members', () =>
      HttpResponse.json([
        {
          guildId: '123',
          discordUserId: '10',
          username: 'alice',
          displayName: 'Alice',
          bound: true,
          githubLogin: 'alice',
        },
        {
          guildId: '123',
          discordUserId: '20',
          username: 'bob',
          displayName: 'Bob',
          bound: true,
          githubLogin: 'bob',
        },
      ]),
    ),
    http.get('/api/v1/repositories', () =>
      HttpResponse.json({
        items: [
          {
            id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
            owner: 'datawake-ai',
            name: 'tyrs-hand',
            enabled: true,
          },
        ],
      }),
    ),
    http.get('/api/v1/discord/development-environments', () =>
      HttpResponse.json([
        {
          id: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
          ownerDiscordUserId: '20',
          ownerName: 'Bob',
          buildRepositoryId: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
          buildRepository: 'datawake-ai/tyrs-hand',
          status: 'running',
          runtimeUser: 'vscode',
          lastUsedAt: '2026-07-21T00:00:00Z',
          sshPublicKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest',
          sshFingerprint: 'SHA256:test',
          sshPort: 2222,
          sshDiscordUserId: '20',
          sshDisplayName: 'Bob',
          sshConfigRevision: 2,
          sshAppliedRevision: 2,
          daemonStatus: 'running',
          appServerStatus: 'running',
          sshStatus: 'running',
          relayStatus: 'running',
          forums: [
            {
              id: '11111111-1111-1111-1111-111111111111',
              name: 'bob-dev',
              discordId: '999',
              repositoryId: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
              repository: 'datawake-ai/tyrs-hand',
              status: 'ready',
              branch: 'tyrs-hand/discord/bob',
              dirty: false,
            },
          ],
        },
      ]),
    ),
  )
}

describe('DiscordPage', () => {
  it('执行全新初始化预检并要求精确确认', async () => {
    commonHandlers()
    const initialize = vi.fn()
    server.use(
      http.post(
        '/api/v1/discord/initializations/preflight',
        async ({ request }) => {
          const body = (await request.json()) as { mode: string }
          expect(body.mode).toBe('fresh')
          return HttpResponse.json({
            guildId: '123',
            mode: 'fresh',
            creates: ['系统'],
            updates: [],
            deletes: ['旧频道'],
            conflicts: [],
            missingPermissions: [],
            channelCount: 1,
            safe: true,
          })
        },
      ),
      http.post('/api/v1/discord/initializations', async ({ request }) => {
        initialize(await request.json())
        return HttpResponse.json(
          { id: '22222222-2222-2222-2222-222222222222' },
          { status: 202 },
        )
      }),
    )
    renderPage()
    const user = userEvent.setup()
    expect(await screen.findByText('connected')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '全新初始化' }))
    const confirmation = screen.getByLabelText(/输入确认指令/)
    await user.type(confirmation, 'DELETE ALL CHANNELS 123')
    await user.click(screen.getByRole('button', { name: '执行预检' }))
    expect(await screen.findByText('预检通过')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '开始初始化' }))
    expect(await screen.findByText(/初始化操作已创建/)).toBeInTheDocument()
    expect(
      await screen.findByText('初始化请求已提交，状态会自动刷新'),
    ).toBeInTheDocument()
    expect(initialize).toHaveBeenCalledWith({
      mode: 'fresh',
      confirmation: 'DELETE ALL CHANNELS 123',
    })
  })

  it('选择仓库创建开发 Forum 并配置 operator 权限', async () => {
    commonHandlers()
    const createForum = vi.fn()
    const grant = vi.fn()
    server.use(
      http.post('/api/v1/discord/members/10/forum', async ({ request }) => {
        createForum(await request.json())
        return HttpResponse.json(
          { id: '33333333-3333-3333-3333-333333333333' },
          { status: 202 },
        )
      }),
      http.put(
        '/api/v1/discord/forums/:forumId/access/:memberId',
        async ({ params, request }) => {
          grant(params, await request.json())
          return new HttpResponse(null, { status: 204 })
        },
      ),
    )
    renderPage()
    const user = userEvent.setup()
    expect((await screen.findAllByText('Alice')).length).toBeGreaterThan(0)
    await user.selectOptions(
      screen.getByLabelText('Alice 开发仓库'),
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
    )
    await user.type(screen.getByLabelText('Alice Forum 名称'), 'alice-api')
    await user.click(
      screen.getAllByRole('button', { name: '创建开发 Forum' })[0],
    )
    expect(createForum).toHaveBeenCalledWith({
      repositoryId: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      name: 'alice-api',
    })
    expect(
      await screen.findByText('开发 Forum 创建请求已提交，列表会自动刷新'),
    ).toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText('bob-dev 授权成员'), '10')
    await user.selectOptions(screen.getByLabelText('bob-dev 权限'), 'operator')
    await user.click(screen.getByRole('button', { name: '授权' }))
    expect(grant).toHaveBeenCalledWith(
      expect.objectContaining({
        forumId: '11111111-1111-1111-1111-111111111111',
        memberId: '10',
      }),
      { accessLevel: 'operator' },
    )
    expect(await screen.findByText('Forum 访问权限已更新')).toBeInTheDocument()
  })

  it('重建用户级环境并强确认删除最后一个 Forum', async () => {
    commonHandlers()
    const rebuild = vi.fn()
    const remove = vi.fn()
    vi.spyOn(window, 'prompt').mockReturnValue(
      'DELETE 11111111-1111-1111-1111-111111111111',
    )
    server.use(
      http.post(
        '/api/v1/discord/development-environments/eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee/rebuild',
        () => {
          rebuild()
          return new HttpResponse(null, { status: 202 })
        },
      ),
      http.get(
        '/api/v1/discord/development-forums/11111111-1111-1111-1111-111111111111/delete-preflight',
        () =>
          HttpResponse.json({
            forumId: '11111111-1111-1111-1111-111111111111',
            dirty: true,
            unpushed: true,
            active: false,
            deletesEnvironment: true,
            confirmation: 'DELETE 11111111-1111-1111-1111-111111111111',
          }),
      ),
      http.post(
        '/api/v1/discord/development-forums/11111111-1111-1111-1111-111111111111/delete',
        async ({ request }) => {
          remove(await request.json())
          return HttpResponse.json(
            { id: 'dddddddd-dddd-dddd-dddd-dddddddddddd' },
            { status: 202 },
          )
        },
      ),
    )
    renderPage()
    const user = userEvent.setup()
    await screen.findByText('bob-dev')
    await user.click(screen.getByRole('button', { name: '下次运行前重建环境' }))
    expect(rebuild).toHaveBeenCalledOnce()
    expect(
      await screen.findByText('环境已标记为重建，将在下次运行前生效'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '删除' }))
    expect(window.prompt).toHaveBeenCalledWith(
      expect.stringContaining('最后一个 Forum'),
    )
    expect(remove).toHaveBeenCalledWith({
      confirmation: 'DELETE 11111111-1111-1111-1111-111111111111',
    })
    expect(
      await screen.findByText('Forum 删除请求已提交，列表会自动刷新'),
    ).toBeInTheDocument()
  })

  it('保存和停用环境 SSH 配置', async () => {
    commonHandlers()
    const save = vi.fn()
    const disable = vi.fn()
    server.use(
      http.put(
        '/api/v1/discord/development-environments/eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee/ssh',
        async ({ request }) => {
          save(await request.json())
          return new HttpResponse(null, { status: 202 })
        },
      ),
      http.delete(
        '/api/v1/discord/development-environments/eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee/ssh',
        () => {
          disable()
          return new HttpResponse(null, { status: 202 })
        },
      ),
    )
    renderPage()
    const user = userEvent.setup()
    const key = await screen.findByLabelText('Bob SSH 公钥')
    expect(screen.getByText(/身份 Bob/)).toBeInTheDocument()
    expect(screen.getByLabelText('Bob Desktop 发言身份')).toHaveValue('20')
    await user.clear(key)
    await user.type(key, 'ssh-ed25519 AAAAC3NzaNew')
    const port = screen.getByLabelText('Bob SSH 端口')
    await user.clear(port)
    await user.type(port, '2200')
    const identity = screen.getByLabelText('Bob Desktop 发言身份')
    await user.selectOptions(identity, '10')
    await user.click(screen.getByRole('button', { name: '保存 SSH' }))
    expect(save).toHaveBeenCalledWith({
      publicKey: 'ssh-ed25519 AAAAC3NzaNew',
      port: 2200,
      discordUserId: '10',
    })
    expect(await screen.findByText('SSH 配置已排队生效')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '停用 SSH' }))
    expect(disable).toHaveBeenCalledOnce()
  })
})
