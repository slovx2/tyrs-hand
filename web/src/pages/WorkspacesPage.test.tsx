import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Outlet, Route, Routes } from 'react-router'
import { ToastViewport } from '../components/ToastViewport'
import { server } from '../test/server'
import type { Worker } from './workerTypes'
import { WorkerWorkspacePage } from './WorkspacesPage'

afterEach(cleanup)

const worker: Worker = {
  id: '11111111-1111-1111-1111-111111111111',
  name: 'worker-primary',
  roles: ['discord'],
  enabled: true,
  maxConcurrentJobs: 6,
  protocolVersion: 23,
  status: 'online',
}

const workspace = {
  id: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
  ownerDiscordUserId: '20',
  ownerName: 'Bob',
  workerId: worker.id,
  projectsScannedAt: '2026-07-27T10:00:00Z',
  projects: [
    {
      id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      name: 'atlas',
      relativePath: 'workspaces/atlas',
      projectSource: 'workspace_child',
      projectKind: 'git',
      availabilityStatus: 'available',
      branch: 'main',
      headSha: '0123456789abcdef',
      dirty: true,
      lastSeenAt: '2026-07-27T10:00:00Z',
      forums: [
        {
          id: '22222222-2222-2222-2222-222222222222',
          name: 'bob-atlas',
          discordId: '901',
          bindingStatus: 'active',
          collaborators: [
            {
              forumId: '22222222-2222-2222-2222-222222222222',
              memberId: '40',
              accessLevel: 'readonly',
              administratorBypass: false,
            },
          ],
        },
      ],
    },
    {
      id: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
      name: 'outside',
      relativePath: 'codex/hash',
      hostPath: '/srv/outside',
      projectSource: 'codex_registered',
      projectKind: 'directory',
      availabilityStatus: 'available',
      dirty: false,
      lastSeenAt: '2026-07-27T10:00:00Z',
      forums: [],
    },
    {
      id: 'cccccccc-cccc-cccc-cccc-cccccccccccc',
      name: 'notes',
      relativePath: 'workspaces/notes',
      projectSource: 'workspace_child',
      projectKind: 'directory',
      availabilityStatus: 'available',
      dirty: false,
      lastSeenAt: '2026-07-27T10:00:00Z',
      forums: [],
    },
  ],
}

function membersHandler() {
  return http.get('/api/v1/discord/members', () =>
    HttpResponse.json([
      {
        guildId: 'guild',
        discordUserId: '40',
        username: 'alice',
        displayName: 'Alice',
        bound: false,
        workspaceOwner: false,
      },
      {
        guildId: 'guild',
        discordUserId: '20',
        username: 'bob',
        displayName: 'Bob',
        bound: false,
        workspaceOwner: true,
      },
      {
        guildId: 'guild',
        discordUserId: '30',
        username: 'cara',
        displayName: 'Cara',
        bound: false,
        workspaceOwner: false,
      },
    ]),
  )
}

function scanHandler(onScan?: () => void) {
  return http.post(`/api/v1/workers/${worker.id}/workspace/scan`, () => {
    onScan?.()
    return HttpResponse.json({ workspace })
  })
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/workspace']}>
        <Routes>
          <Route element={<Outlet context={{ worker, isAdmin: true }} />}>
            <Route path="workspace" element={<WorkerWorkspacePage />} />
          </Route>
        </Routes>
      </MemoryRouter>
      <ToastViewport />
    </QueryClientProvider>,
  )
}

describe('WorkerWorkspacePage', () => {
  it('只展示当前 Worker 的 Workspace，Codex 项目默认隐藏', async () => {
    const scan = vi.fn()
    server.use(
      http.get(`/api/v1/workers/${worker.id}/workspace`, () =>
        HttpResponse.json({ workspace }),
      ),
      membersHandler(),
      scanHandler(scan),
    )
    renderPage()
    const user = userEvent.setup()

    expect(await screen.findByText('workspaces/atlas')).toBeInTheDocument()
    await waitFor(() => expect(scan).toHaveBeenCalledTimes(1))
    expect(screen.getByText('Worker 实时扫描')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '刷新' }))
    await waitFor(() => expect(scan).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('Workspace 已刷新')).toBeInTheDocument()
    expect(screen.queryByText('/srv/outside')).not.toBeInTheDocument()
    await user.click(screen.getByLabelText('显示 Codex 项目'))
    expect(screen.getByText('/srv/outside')).toBeInTheDocument()
  })

  it('未绑定时只列出尚未拥有 Workspace 的成员，并固定绑定当前 Worker', async () => {
    const create = vi.fn()
    const scan = vi.fn()
    server.use(
      http.get(`/api/v1/workers/${worker.id}/workspace`, () =>
        HttpResponse.json({ workspace: null }),
      ),
      membersHandler(),
      http.post('/api/v1/workspaces', async ({ request }) => {
        create(await request.json())
        return HttpResponse.json({ id: 'new-workspace' }, { status: 201 })
      }),
      scanHandler(scan),
    )
    renderPage()
    const user = userEvent.setup()

    expect(await screen.findByText('尚未绑定 Workspace')).toBeInTheDocument()
    const select = screen.getByLabelText('Workspace 负责人')
    expect(within(select).queryByRole('option', { name: 'Bob' })).toBeNull()
    await user.selectOptions(select, '30')
    await user.click(screen.getByRole('button', { name: '绑定 Workspace' }))
    expect(create).toHaveBeenCalledWith({
      ownerDiscordUserId: '30',
      workerId: worker.id,
    })
    await waitFor(() => expect(scan).toHaveBeenCalledTimes(1))
    expect(await screen.findByText('workspaces/atlas')).toBeInTheDocument()
  })

  it('Forum 操作只失效当前 Worker Workspace 查询', async () => {
    let workspaceRequests = 0
    const disable = vi.fn()
    server.use(
      http.get(`/api/v1/workers/${worker.id}/workspace`, () => {
        workspaceRequests += 1
        return HttpResponse.json({ workspace })
      }),
      membersHandler(),
      scanHandler(),
      http.post('/api/v1/workspace-forums/:id/disable', ({ params }) => {
        disable(params.id)
        return new HttpResponse(null, { status: 202 })
      }),
    )
    renderPage()
    const user = userEvent.setup()

    await screen.findByText('workspaces/atlas')
    await user.click(
      screen.getByRole('button', { name: 'atlas 管理 Forum 配对' }),
    )
    await user.click(screen.getByRole('button', { name: '停用 Forum' }))
    expect(disable).toHaveBeenCalledWith('22222222-2222-2222-2222-222222222222')
    expect(workspaceRequests).toBeGreaterThan(1)
  })

  it('可在当前 Worker 下创建 Forum 并管理协作者', async () => {
    const pair = vi.fn()
    const grant = vi.fn()
    const remove = vi.fn()
    server.use(
      http.get(`/api/v1/workers/${worker.id}/workspace`, () =>
        HttpResponse.json({ workspace }),
      ),
      membersHandler(),
      scanHandler(),
      http.post(
        '/api/v1/workspace-projects/:id/forums',
        async ({ params, request }) => {
          pair(params.id, await request.json())
          return HttpResponse.json({ id: 'operation' }, { status: 202 })
        },
      ),
      http.put(
        '/api/v1/workspace-projects/:projectId/forums/:forumId/collaborators/:memberId',
        async ({ params, request }) => {
          grant(params.memberId, await request.json())
          return new HttpResponse(null, { status: 204 })
        },
      ),
      http.delete(
        '/api/v1/workspace-projects/:projectId/forums/:forumId/collaborators/:memberId',
        ({ params }) => {
          remove(params.memberId)
          return new HttpResponse(null, { status: 204 })
        },
      ),
    )
    renderPage()
    const user = userEvent.setup()

    await screen.findByText('workspaces/atlas')
    await user.click(
      screen.getByRole('button', { name: 'notes 管理 Forum 配对' }),
    )
    const notes = screen.getByTestId(
      'workspace-project-cccccccc-cccc-cccc-cccc-cccccccccccc',
    )
    await user.type(within(notes).getByRole('textbox'), 'notes-forum')
    await user.click(within(notes).getByRole('button', { name: '创建 Forum' }))
    expect(pair).toHaveBeenCalledWith('cccccccc-cccc-cccc-cccc-cccccccccccc', {
      mode: 'new',
      name: 'notes-forum',
    })

    await user.click(
      screen.getByRole('button', { name: 'atlas 管理 Forum 配对' }),
    )
    await user.selectOptions(screen.getByLabelText('bob-atlas 协作者'), '30')
    await user.selectOptions(
      screen.getByLabelText('bob-atlas 权限'),
      'operator',
    )
    await user.click(screen.getByRole('button', { name: '授权' }))
    await user.click(screen.getByRole('button', { name: '移除 Alice' }))
    expect(grant).toHaveBeenCalledWith('30', { accessLevel: 'operator' })
    expect(remove).toHaveBeenCalledWith('40')
  })

  it('手动刷新再次扫描，实时失败时保留已有项目', async () => {
    let scans = 0
    server.use(
      http.get(`/api/v1/workers/${worker.id}/workspace`, () =>
        HttpResponse.json({ workspace }),
      ),
      membersHandler(),
      http.post(`/api/v1/workers/${worker.id}/workspace/scan`, () => {
        scans += 1
        if (scans === 1) return HttpResponse.json({ workspace })
        return HttpResponse.json(
          { title: '实时扫描 Worker 项目失败', status: 502 },
          { status: 502 },
        )
      }),
    )
    renderPage()
    const user = userEvent.setup()

    expect(await screen.findByText('workspaces/atlas')).toBeInTheDocument()
    await waitFor(() => expect(scans).toBe(1))
    await user.click(screen.getByRole('button', { name: '刷新' }))
    expect(
      await screen.findByText(/当前继续显示上次扫描结果/),
    ).toBeInTheDocument()
    expect(screen.getByText('workspaces/atlas')).toBeInTheDocument()
    expect(scans).toBe(2)
  })
})
