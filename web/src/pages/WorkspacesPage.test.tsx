import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ToastViewport } from '../components/ToastViewport'
import { server } from '../test/server'
import { WorkspaceManagement } from './WorkspacesPage'

afterEach(cleanup)

const workspace = {
  id: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
  ownerDiscordUserId: '20',
  ownerName: 'Bob',
  workerId: '11111111-1111-1111-1111-111111111111',
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
    http.get('/api/v1/workspaces', () =>
      HttpResponse.json({ items: [workspace] }),
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
      <WorkspaceManagement
        workers={[
          {
            id: '44444444-4444-4444-4444-444444444444',
            name: 'worker-2',
            roles: ['discord'],
            enabled: true,
            maxConcurrentJobs: 2,
            protocolVersion: 22,
            status: 'online',
          },
        ]}
      />
      <ToastViewport />
    </QueryClientProvider>,
  )
}

describe('WorkspaceManagement', () => {
  it('按 Workspace 展示自动发现项目并绑定空闲 Worker', async () => {
    commonHandlers()
    const create = vi.fn()
    server.use(
      http.post('/api/v1/workspaces', async ({ request }) => {
        create(await request.json())
        return HttpResponse.json(
          { id: 'dddddddd-dddd-dddd-dddd-dddddddddddd' },
          { status: 201 },
        )
      }),
    )
    renderPage()
    const user = userEvent.setup()

    expect(await screen.findByText('workspaces/atlas')).toBeInTheDocument()
    expect(screen.getByText('workspaces/notes')).toBeInTheDocument()
    expect(screen.getByText('缺失')).toBeInTheDocument()
    expect(screen.queryByLabelText('普通项目名称')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '创建 Workspace' }))
    const memberSelect = screen.getByLabelText('Discord 成员')
    expect(
      within(memberSelect).queryByRole('option', { name: 'Bob' }),
    ).not.toBeInTheDocument()
    await user.selectOptions(memberSelect, '10')
    await user.selectOptions(
      screen.getByLabelText('Worker'),
      '44444444-4444-4444-4444-444444444444',
    )
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: '创建 Workspace',
      }),
    )
    expect(create).toHaveBeenCalledWith({
      ownerDiscordUserId: '10',
      workerId: '44444444-4444-4444-4444-444444444444',
    })
  })

  it('管理 Forum 配对、历史恢复、协作者和缺失态', async () => {
    commonHandlers()
    const disable = vi.fn()
    const pair = vi.fn()
    const grant = vi.fn()
    server.use(
      http.post('/api/v1/workspace-forums/:id/disable', ({ params }) => {
        disable(params.id)
        return new HttpResponse(null, { status: 202 })
      }),
      http.post(
        '/api/v1/workspace-projects/:id/forums',
        async ({ params, request }) => {
          pair(params.id, await request.json())
          return new HttpResponse(null, { status: 202 })
        },
      ),
      http.put(
        '/api/v1/workspace-projects/:projectId/forums/:forumId/collaborators/:memberId',
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
      'workspace-project-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
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
      'workspace-project-cccccccc-cccc-cccc-cccc-cccccccccccc',
    )
    expect(within(archive).getByRole('button', { name: '恢复' })).toBeDisabled()
    expect(
      within(archive).getByRole('button', { name: '创建 Forum' }),
    ).toBeDisabled()
  })
})
