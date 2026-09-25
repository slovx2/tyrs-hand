import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { ControlHarness } from './lib/control.mjs'
import { startModels } from './lib/models.mjs'
import { run } from './lib/process.mjs'
import { SSHProtocolClient } from './lib/ssh-protocol.mjs'
import { WorkerHarness } from './lib/worker.mjs'
import { validateRuntimeWire } from './lib/wire.mjs'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')

test('正式 Worker 共用密钥双 SSH：计划提问、退出执行、完全访问与允许/拒绝审批', { timeout: 240_000 }, async () => {
  const runDir = resolve(repoRoot, '.artifacts/mobile-runtime', String(Date.now()))
  const adapter = resolve(process.env.TYRS_HAND_ADAPTER_ROOT ?? resolve(repoRoot, '../claude-codex'))
  await mkdir(runDir, { recursive: true })
  const control = new ControlHarness({ repoRoot, runDir: resolve(runDir, 'control'), label: 'real-mobile' })
  const clients = [], approvals = []
  let models, worker
  try {
    run('npm', ['run', 'build'], { cwd: adapter })
    models = await startModels(adapter, runDir)
    await control.start()
    const registration = await control.admin.createWorker('real-mobile-dual-engine')
    worker = new WorkerHarness({ repoRoot, runDir: resolve(runDir, 'worker'), control,
      registration, modelURLs: models.urls })
    await worker.start()
    models.setWorkspace(worker.workspace)
    const expected = ['MOBILE_CODEX_CHAT', 'MOBILE_CLAUDE_CHAT', 'MOBILE_CLAUDE_FULL',
      'MOBILE_CLAUDE_APPROVAL', 'MOBILE_CLAUDE_DENY', 'MOBILE_CLAUDE_PLAN']
    const threads = { codex: [], 'claude-code': [] }
    for (const engine of ['codex', 'claude-code']) {
      let currentMarker
      const client = new SSHProtocolClient(worker, engine, (request) => {
        approvals.push({ marker: currentMarker, method: request.method })
        if (request.method === 'item/tool/requestUserInput') return {
          answers: Object.fromEntries(request.params.questions.map((question) => [question.id, {
            answers: [question.id === 'execute_plan' ? '执行计划' : 'Blue'],
          }])),
        }
        assert.equal(request.method, 'item/fileChange/requestApproval')
        assert.ok(['MOBILE_CLAUDE_APPROVAL', 'MOBILE_CLAUDE_DENY'].includes(currentMarker))
        return { decision: currentMarker.endsWith('_DENY') ? 'decline' : 'accept' }
      })
      clients.push(client)
      await client.open()
      for (const marker of expected.filter((item) => item.includes(engine === 'codex' ? '_CODEX_' : '_CLAUDE_'))) {
        currentMarker = marker
        const { thread } = await client.request('thread/start', { cwd: worker.workspace,
          approvalPolicy: /_APPROVAL$|_DENY$/.test(marker) ? 'on-request' : 'never',
          sandbox: 'danger-full-access' })
        threads[engine].push(thread.id)
        const params = { threadId: thread.id, input: [{ type: 'text', text: marker, text_elements: [] }] }
        if (marker.endsWith('_PLAN')) params.collaborationMode = { mode: 'plan', settings: {
          model: 'mock-claude', reasoning_effort: null, developer_instructions: null,
        } }
        const { turn } = await client.request('turn/start', params)
        const event = await client.waitFor('turn/completed', (value) => value.threadId === thread.id && value.turn.id === turn.id)
        assert.equal(event.params.turn.status, 'completed', JSON.stringify(event.params.turn.error))
        const history = await client.request('thread/read', { threadId: thread.id, includeTurns: true })
        assert.match(JSON.stringify(history), new RegExp(marker + '_OK'))
      }
    }
    for (const client of clients) {
      const other = client.engine === 'codex' ? 'claude-code' : 'codex'
      const listed = JSON.stringify(await client.request('thread/list', { limit: 100 }))
      for (const threadId of threads[other]) assert.ok(!listed.includes(threadId), '另一引擎会话不能出现在列表')
      for (const threadId of threads[other]) await assert.rejects(client.request('thread/read', { threadId }))
    }
    assert.equal(approvals.filter((item) => item.method === 'item/tool/requestUserInput').length, 2)
    assert.equal(approvals.filter((item) => item.method === 'item/fileChange/requestApproval').length, 2)
    assert.ok(!approvals.some((item) => item.marker === 'MOBILE_CLAUDE_FULL'))
    await models.verify(expected)
    await validateRuntimeWire(repoRoot, resolve(runDir, 'worker'))
    await writeFile(resolve(runDir, 'approvals.json'), JSON.stringify(approvals, null, 2))
  } finally {
    for (const client of clients) {
      await writeFile(resolve(runDir, 'wire-' + client.engine + '.json'), JSON.stringify(client.trace, null, 2))
      await client.close()
    }
    await models?.close()
    await worker?.stop()
    await control.stop()
    process.stderr.write('真实 Worker 预检证据：' + runDir + '\n')
  }
})
