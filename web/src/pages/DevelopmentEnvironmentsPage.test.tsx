import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ToastViewport } from '../components/ToastViewport'
import { server } from '../test/server'
import { DevelopmentEnvironmentsPage } from './DevelopmentEnvironmentsPage'

afterEach(cleanup)

const environment = {
  id: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
  ownerDiscordUserId: '20',
  ownerName: 'Bob',
  status: 'running',
  imageRef: 'registry.example.invalid/development@sha256:aaaaaaaa',
  runtimeUser: 'developer',
  codexVersion: 'codex-cli 1.0.0',
  codexUserOverride: false,
  lastUsedAt: '2026-07-21T00:00:00Z',
  sshPublicKey: 'ssh-ed25519 AAAATest',
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
  projectsScannedAt: '2026-07-27T10:00:00Z',
  projects: [
    {
      id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      name: 'atlas',
      relativePath: 'workspaces/atlas',
      projectKind: 'git',
      availabilityStatus: 'available',
      branch: 'main',
      headSha: '0123456789abcdef',
      dirty: true,
      remoteUrl: 'https://example.invalid/team/atlas.git',
      lastSeenAt: '2026-07-27T10:00:00Z',
      forums: [
        {
          id: '11111111-1111-1111-1111-111111111111',
          name: 'bob-atlas',
          discordId: '901',
          bindingStatus: 'active',
          collaborators: [],
        },
      ],
    },
    {
      id: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
      name: 'notes',
      relativePath: 'workspaces/notes',
      projectKind: 'directory',
      availabilityStatus: 'available',
      dirty: false,
      lastSeenAt: '2026-07-27T10:00:00Z',
      forums: [
        {
          id: '22222222-2222-2222-2222-222222222222',
          name: 'bob-notes-old',
          discordId: '902',
          bindingStatus: 'inactive',
          collaborators: [],
        },
      ],
    },
    {
      id: 'cccccccc-cccc-cccc-cccc-cccccccccccc',
      name: 'archive',
      relativePath: 'workspaces/archive',
      projectKind: 'git',
      availabilityStatus: 'missing',
      dirty: false,
      lastSeenAt: '2026-07-20T10:00:00Z',
      forums: [
        {
          id: '33333333-3333-3333-3333-333333333333',
          name: 'bob-archive-old',
          discordId: '903',
          bindingStatus: 'inactive',
          collaborators: [],
        },
      ],
    },
  ],
}

function commonHandlers() {
  server.use(
    http.get('/api/v1/development-environments', () =>
      HttpResponse.json({ items: [environment] }),
    ),
    http.get('/api/v1/discord/members', () =>
      HttpResponse.json([
        {
          guildId: 'guild',
          discordUserId: '10',
          username: 'alice',
          displayName: 'Alice',
          bound: false,
        },
        {
          guildId: 'guild',
          discordUserId: '20',
          username: 'bob',
          displayName: 'Bob',
          bound: false,
        },
        {
          guildId: 'guild',
          discordUserId: '30',
          username: 'cara',
          displayName: 'Cara',
          bound: false,
        },
      ]),
    ),
  )
}

function renderPage() {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <DevelopmentEnvironmentsPage />
      <ToastViewport />
    </QueryClientProvider>,
  )
}

describe('DevelopmentEnvironmentsPage', () => {
  it('按环境展示自动发现项目并只允许无环境成员创建', async () => {
    commonHandlers()
    const create = vi.fn()
    server.use(
      http.post('/api/v1/development-environments', async ({ request }) => {
        create(await request.json())
        return HttpResponse.json(
          {
            id: 'dddddddd-dddd-dddd-dddd-dddddddddddd',
            operationId: 'ffffffff-ffff-ffff-ffff-ffffffffffff',
          },
          { status: 202 },
        )
      }),
    )
    renderPage()
    const user = userEvent.setup()

    expect(await screen.findByText('workspaces/atlas')).toBeInTheDocument()
    expect(screen.getByText('workspaces/notes')).toBeInTheDocument()
    expect(screen.getByText('缺失')).toBeInTheDocument()
    expect(screen.queryByLabelText('普通项目名称')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '创建环境' }))
    const memberSelect = screen.getByLabelText('Discord 成员')
    expect(
      within(memberSelect).queryByRole('option', { name: 'Bob' }),
    ).not.toBeInTheDocument()
    await user.selectOptions(memberSelect, '10')
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: '创建环境',
      }),
    )
    expect(create).toHaveBeenCalledWith({ ownerDiscordUserId: '10' })
  })

  it('管理 Forum 配对、历史恢复、协作者和缺失态', async () => {
    commonHandlers()
    const disable = vi.fn()
    const pair = vi.fn()
    const grant = vi.fn()
    server.use(
      http.post('/api/v1/development-forums/:id/disable', ({ params }) => {
        disable(params.id)
        return new HttpResponse(null, { status: 202 })
      }),
      http.post(
        '/api/v1/development-projects/:id/forums',
        async ({ params, request }) => {
          pair(params.id, await request.json())
          return new HttpResponse(null, { status: 202 })
        },
      ),
      http.put(
        '/api/v1/development-projects/:projectId/forums/:forumId/collaborators/:memberId',
        async ({ params, request }) => {
          grant(params, await request.json())
          return new HttpResponse(null, { status: 204 })
        },
      ),
    )
    renderPage()
    const user = userEvent.setup()

    await screen.findByText('workspaces/atlas')
    await user.click(
      screen.getByRole('button', { name: 'atlas 管理 Forum 配对' }),
    )
    await user.selectOptions(screen.getByLabelText('bob-atlas 协作者'), '10')
    await user.selectOptions(
      screen.getByLabelText('bob-atlas 权限'),
      'operator',
    )
    await user.click(screen.getByRole('button', { name: '授权' }))
    expect(grant).toHaveBeenCalledWith(
      expect.objectContaining({ memberId: '10' }),
      { accessLevel: 'operator' },
    )
    await user.click(screen.getByRole('button', { name: '停用 Forum' }))
    expect(disable).toHaveBeenCalledWith('11111111-1111-1111-1111-111111111111')

    await user.click(
      screen.getByRole('button', { name: 'notes 管理 Forum 配对' }),
    )
    const notes = screen.getByTestId(
      'development-project-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
    )
    await user.click(within(notes).getByRole('button', { name: '恢复' }))
    expect(pair).toHaveBeenCalledWith(
      'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
      expect.objectContaining({
        mode: 'restore',
        forumId: '22222222-2222-2222-2222-222222222222',
      }),
    )
    await user.click(within(notes).getByRole('button', { name: '创建 Forum' }))
    expect(pair).toHaveBeenCalledWith(
      'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
      expect.objectContaining({ mode: 'new' }),
    )

    await user.click(
      screen.getByRole('button', { name: 'archive 管理 Forum 配对' }),
    )
    const archive = screen.getByTestId(
      'development-project-cccccccc-cccc-cccc-cccc-cccccccccccc',
    )
    expect(within(archive).getByRole('button', { name: '恢复' })).toBeDisabled()
    expect(
      within(archive).getByRole('button', { name: '创建 Forum' }),
    ).toBeDisabled()
  })

  it('执行环境 Rebase 并保存和停用 SSH', async () => {
    commonHandlers()
    const rebase = vi.fn()
    const save = vi.fn()
    const disable = vi.fn()
    server.use(
      http.post(
        '/api/v1/development-environments/eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee/rebase',
        () => {
          rebase()
          return new HttpResponse(null, { status: 202 })
        },
      ),
      http.put(
        '/api/v1/development-environments/eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee/ssh',
        async ({ request }) => {
          save(await request.json())
          return new HttpResponse(null, { status: 202 })
        },
      ),
      http.delete(
        '/api/v1/development-environments/eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee/ssh',
        () => {
          disable()
          return new HttpResponse(null, { status: 202 })
        },
      ),
    )
    renderPage()
    const user = userEvent.setup()

    await screen.findByText('workspaces/atlas')
    await user.click(screen.getByRole('button', { name: 'Rebase' }))
    expect(rebase).toHaveBeenCalledOnce()
    await user.click(screen.getByRole('tab', { name: '运行与 SSH' }))

    const key = screen.getByLabelText('Bob SSH 公钥')
    await user.clear(key)
    await user.type(key, 'ssh-ed25519 AAAANew')
    const port = screen.getByLabelText('Bob SSH 端口')
    await user.clear(port)
    await user.type(port, '2200')
    await user.selectOptions(
      screen.getByLabelText('Bob Desktop 发言身份'),
      '10',
    )
    await user.click(screen.getByRole('button', { name: '保存 SSH' }))
    expect(save).toHaveBeenCalledWith({
      publicKey: 'ssh-ed25519 AAAANew',
      port: 2200,
      discordUserId: '10',
    })
    await user.click(screen.getByRole('button', { name: '停用 SSH' }))
    expect(disable).toHaveBeenCalledOnce()
  })
})
