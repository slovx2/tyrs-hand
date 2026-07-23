import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ToastViewport } from '../components/ToastViewport'
import { server } from '../test/server'
import { SSHPage } from './SSHPage'

afterEach(cleanup)

function renderPage() {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <SSHPage />
      <ToastViewport />
    </QueryClientProvider>,
  )
}

describe('SSHPage', () => {
  it('手工录入凭证和主机节点分配', async () => {
    const createCredential = vi.fn()
    const createHost = vi.fn()
    server.use(
      http.get('/api/v1/ssh/credentials', () =>
        HttpResponse.json({
          items: [
            {
              id: '11111111-1111-1111-1111-111111111111',
              name: 'production',
              publicKey: 'ssh-ed25519 AAAA',
              fingerprint: 'SHA256:fingerprint',
              enabled: true,
              version: 1,
              hostCount: 0,
              updatedAt: '2026-07-22T00:00:00Z',
            },
          ],
        }),
      ),
      http.get('/api/v1/ssh/hosts', () => HttpResponse.json({ items: [] })),
      http.get('/api/v1/execution-nodes', () =>
        HttpResponse.json({
          items: [
            {
              id: '22222222-2222-2222-2222-222222222222',
              name: 'song-ubuntu',
              roles: ['github', 'discord'],
              enabled: true,
              maxConcurrentJobs: 6,
              protocolVersion: 1,
              status: 'online',
            },
          ],
        }),
      ),
      http.post('/api/v1/ssh/credentials', async ({ request }) => {
        createCredential(await request.json())
        return HttpResponse.json({}, { status: 201 })
      }),
      http.post('/api/v1/ssh/hosts', async ({ request }) => {
        createHost(await request.json())
        return HttpResponse.json({}, { status: 201 })
      }),
    )

    renderPage()
    const user = userEvent.setup()
    expect(await screen.findByText('SHA256:fingerprint')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '添加凭证' }))
    await user.type(screen.getByLabelText('名称'), 'backup')
    await user.type(
      screen.getByLabelText('私钥'),
      '-----BEGIN PRIVATE KEY-----',
    )
    await user.click(screen.getByRole('button', { name: '保存凭证' }))
    expect(createCredential).toHaveBeenCalledWith({
      name: 'backup',
      privateKey: '-----BEGIN PRIVATE KEY-----',
      passphrase: '',
      enabled: true,
    })

    await user.click(screen.getByRole('button', { name: '添加主机' }))
    await user.type(screen.getByLabelText('SSH 别名'), 'production-host')
    await user.type(screen.getByLabelText('主机地址'), '192.0.2.10')
    await user.clear(screen.getByLabelText('用户名'))
    await user.type(screen.getByLabelText('用户名'), 'ubuntu')
    await user.selectOptions(
      screen.getByLabelText('使用凭证'),
      '11111111-1111-1111-1111-111111111111',
    )
    await user.click(screen.getByLabelText('song-ubuntu'))
    await user.click(screen.getByRole('button', { name: '保存主机' }))
    expect(createHost).toHaveBeenCalledWith({
      alias: 'production-host',
      hostname: '192.0.2.10',
      port: 22,
      username: 'ubuntu',
      credentialId: '11111111-1111-1111-1111-111111111111',
      proxyJumpHostId: null,
      executionNodeIds: ['22222222-2222-2222-2222-222222222222'],
      enabled: true,
    })
  })

  it('按 SSH config 一次导入多个主机', async () => {
    const importHosts = vi.fn()
    const credentialID = '11111111-1111-1111-1111-111111111111'
    const nodeID = '22222222-2222-2222-2222-222222222222'
    server.use(
      http.get('/api/v1/ssh/credentials', () =>
        HttpResponse.json({
          items: [
            {
              id: credentialID,
              name: 'production',
              publicKey: 'ssh-ed25519 AAAA',
              fingerprint: 'SHA256:fingerprint',
              enabled: true,
              version: 1,
              hostCount: 0,
              updatedAt: '2026-07-22T00:00:00Z',
            },
          ],
        }),
      ),
      http.get('/api/v1/ssh/hosts', () => HttpResponse.json({ items: [] })),
      http.get('/api/v1/execution-nodes', () =>
        HttpResponse.json({
          items: [
            {
              id: nodeID,
              name: 'song-ubuntu',
              enabled: true,
              status: 'online',
            },
          ],
        }),
      ),
      http.post('/api/v1/ssh/hosts/import', async ({ request }) => {
        importHosts(await request.json())
        return HttpResponse.json({ items: [{}, {}] }, { status: 201 })
      }),
    )

    renderPage()
    const user = userEvent.setup()
    expect(await screen.findByText('SHA256:fingerprint')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '导入 SSH config' }))
    await user.type(
      screen.getByLabelText('SSH config'),
      `Host jump
  HostName 192.0.2.10
  User ubuntu
  IdentityFile ~/.ssh/id_ed25519

Host production
  HostName 10.0.0.8
  User deploy
  Port 2222
  ProxyJump jump
  ServerAliveInterval 30`,
    )
    expect(screen.getByText('将导入 2 台主机')).toBeInTheDocument()
    expect(
      screen.getByText(
        'config 中的 IdentityFile 只是本机路径，页面不会读取；以下选择的托管凭证会用于全部主机。',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText((_, element) => {
        return (
          element?.tagName === 'P' &&
          element.textContent === '未导入的指令：ServerAliveInterval'
        )
      }),
    ).toBeInTheDocument()
    await user.click(screen.getByLabelText('批量导入到 song-ubuntu'))
    await user.click(screen.getByRole('button', { name: '导入 2 台主机' }))

    expect(importHosts).toHaveBeenCalledWith({
      credentialId: credentialID,
      executionNodeIds: [nodeID],
      enabled: true,
      hosts: [
        {
          alias: 'jump',
          hostname: '192.0.2.10',
          port: 22,
          username: 'ubuntu',
        },
        {
          alias: 'production',
          hostname: '10.0.0.8',
          port: 2222,
          username: 'deploy',
          proxyJumpAlias: 'jump',
        },
      ],
    })
    expect(await screen.findByText('已导入 2 台 SSH 主机')).toBeInTheDocument()
  })

  it('轮换凭证并编辑、删除已有主机', async () => {
    const updateCredential = vi.fn()
    const deleteCredential = vi.fn()
    const updateHost = vi.fn()
    const deleteHost = vi.fn()
    const credentialID = '11111111-1111-1111-1111-111111111111'
    const spareCredentialID = '33333333-3333-3333-3333-333333333333'
    const jumpID = '44444444-4444-4444-4444-444444444444'
    const targetID = '55555555-5555-5555-5555-555555555555'
    const firstNodeID = '22222222-2222-2222-2222-222222222222'
    const secondNodeID = '66666666-6666-6666-6666-666666666666'
    server.use(
      http.get('/api/v1/ssh/credentials', () =>
        HttpResponse.json({
          items: [
            {
              id: credentialID,
              name: 'production',
              publicKey: 'ssh-ed25519 AAAA',
              fingerprint: 'SHA256:production',
              enabled: true,
              version: 2,
              hostCount: 2,
              updatedAt: '2026-07-22T00:00:00Z',
            },
            {
              id: spareCredentialID,
              name: 'unused',
              publicKey: 'ssh-ed25519 BBBB',
              fingerprint: 'SHA256:unused',
              enabled: false,
              version: 1,
              hostCount: 0,
              updatedAt: '2026-07-22T00:00:00Z',
            },
          ],
        }),
      ),
      http.get('/api/v1/ssh/hosts', () =>
        HttpResponse.json({
          items: [
            {
              id: jumpID,
              alias: 'jump',
              hostname: '192.0.2.1',
              port: 22,
              username: 'ubuntu',
              credentialId: credentialID,
              credentialName: 'production',
              executionNodeIds: [firstNodeID],
              enabled: true,
              updatedAt: '2026-07-22T00:00:00Z',
            },
            {
              id: targetID,
              alias: 'target',
              hostname: '192.0.2.2',
              port: 22,
              username: 'root',
              credentialId: credentialID,
              credentialName: 'production',
              proxyJumpHostId: jumpID,
              proxyJumpAlias: 'jump',
              executionNodeIds: [firstNodeID],
              enabled: false,
              updatedAt: '2026-07-22T00:00:00Z',
            },
          ],
        }),
      ),
      http.get('/api/v1/execution-nodes', () =>
        HttpResponse.json({
          items: [
            { id: firstNodeID, name: 'song-ubuntu', enabled: true },
            { id: secondNodeID, name: 'backup-node', enabled: true },
          ],
        }),
      ),
      http.put(
        `/api/v1/ssh/credentials/${credentialID}`,
        async ({ request }) => {
          updateCredential(await request.json())
          return HttpResponse.json({})
        },
      ),
      http.delete(`/api/v1/ssh/credentials/${spareCredentialID}`, () => {
        deleteCredential()
        return new HttpResponse(null, { status: 204 })
      }),
      http.put(`/api/v1/ssh/hosts/${targetID}`, async ({ request }) => {
        updateHost(await request.json())
        return HttpResponse.json({})
      }),
      http.delete(`/api/v1/ssh/hosts/${targetID}`, () => {
        deleteHost()
        return new HttpResponse(null, { status: 204 })
      }),
    )

    renderPage()
    const user = userEvent.setup()
    const productionCard = (
      await screen.findByText('SHA256:production')
    ).closest('article')
    expect(productionCard).not.toBeNull()
    const linkedDelete = within(productionCard!).getByRole('button', {
      name: '删除',
    })
    expect(linkedDelete).toBeDisabled()
    expect(linkedDelete).toHaveAttribute('title', '请先删除关联主机')
    await user.click(
      within(productionCard!).getByRole('button', { name: '编辑' }),
    )
    await user.type(
      screen.getByLabelText('私钥'),
      '-----BEGIN PRIVATE KEY-----',
    )
    await user.type(screen.getByLabelText('私钥口令（可选）'), 'secret')
    await user.click(screen.getByLabelText('启用凭证'))
    await user.click(screen.getByRole('button', { name: '保存凭证' }))
    expect(updateCredential).toHaveBeenCalledWith({
      name: 'production',
      privateKey: '-----BEGIN PRIVATE KEY-----',
      passphrase: 'secret',
      enabled: false,
    })

    const unusedCard = screen.getByText('SHA256:unused').closest('article')
    expect(unusedCard).not.toBeNull()
    await user.click(within(unusedCard!).getByRole('button', { name: '删除' }))
    expect(deleteCredential).toHaveBeenCalledOnce()

    const targetCard = screen
      .getByRole('heading', { name: 'target' })
      .closest('article')
    expect(targetCard).not.toBeNull()
    await user.click(within(targetCard!).getByRole('button', { name: '编辑' }))
    await user.clear(screen.getByLabelText('端口'))
    await user.type(screen.getByLabelText('端口'), '2222')
    expect(screen.getByLabelText('backup-node')).toBeDisabled()
    await user.selectOptions(screen.getByLabelText('ProxyJump（可选）'), '')
    expect(screen.getByLabelText('backup-node')).toBeEnabled()
    await user.click(screen.getByLabelText('song-ubuntu'))
    await user.click(screen.getByLabelText('backup-node'))
    await user.click(screen.getByLabelText('启用主机'))
    await user.click(screen.getByRole('button', { name: '保存主机' }))
    expect(updateHost).toHaveBeenCalledWith({
      alias: 'target',
      hostname: '192.0.2.2',
      port: 2222,
      username: 'root',
      credentialId: credentialID,
      proxyJumpHostId: null,
      executionNodeIds: [secondNodeID],
      enabled: true,
    })

    const refreshedTarget = screen
      .getByRole('heading', { name: 'target' })
      .closest('article')
    await user.click(
      within(refreshedTarget!).getByRole('button', { name: '删除' }),
    )
    expect(deleteHost).toHaveBeenCalledOnce()
  })
})
