import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterEach, expect, it } from 'vitest'
import { server } from '../test/server'
import { WorkerRuntimes } from './WorkerRuntimes'

afterEach(cleanup)

it('单引擎故障保留另一个入口的独立地址与指纹', async () => {
  server.use(
    http.get('/api/v1/workers/worker/runtimes', () =>
      HttpResponse.json([
        {
          workerId: 'worker',
          engine: 'codex',
          enabled: true,
          status: 'running',
          sshListenAddress: ':2222',
          sshHostKeyFingerprint: 'codex-key',
          protocolVersion: '0.147.0',
          build: { cliBuild: '0.153.4' },
          capabilities: [],
          modelCatalog: null,
          releaseReady: true,
          heartbeatAt: null,
        },
        {
          workerId: 'worker',
          engine: 'claude-code',
          enabled: true,
          status: 'unavailable',
          sshListenAddress: ':3333',
          sshHostKeyFingerprint: 'claude-key',
          protocolVersion: '0.147.0',
          build: { cliBuild: '2.1.282' },
          capabilities: [],
          modelCatalog: null,
          releaseReady: false,
          heartbeatAt: null,
        },
      ]),
    ),
  )
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <WorkerRuntimes workerId="worker" />
    </QueryClientProvider>,
  )
  expect(await screen.findByRole('status')).toHaveTextContent(
    '部分运行时不可用',
  )
  const codex = within(screen.getByLabelText('Codex 入口'))
  expect(codex.getByText(':2222')).toBeInTheDocument()
  expect(codex.getByText('codex-key')).toBeInTheDocument()
  expect(codex.getByText('运行中')).toBeInTheDocument()
  const claude = within(screen.getByLabelText('Claude Code 入口'))
  expect(claude.getByText(':3333')).toBeInTheDocument()
  expect(claude.getByText('claude-key')).toBeInTheDocument()
  expect(claude.getByText('暂不可用')).toBeInTheDocument()
})
