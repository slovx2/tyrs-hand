import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { mkdir, mkdtemp, readFile, realpath, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { SSHProtocolClient } from './mobile-e2e/lib/ssh-protocol.mjs'
import { validateRuntimeWire } from './mobile-e2e/lib/wire.mjs'
import { output } from './mobile-e2e/lib/process.mjs'
import { MigrationControl, sha256, until } from './protocol-migration-infra.mjs'
import { MigrationWorker } from './protocol-migration-worker.mjs'
import { startMigrationModels } from './protocol-migration-models.mjs'
import { MigrationFaultProxy, waitJournalDelivered } from './protocol-migration-journal.mjs'

// 独立当前版本恢复验收：复用进程/数据库夹具，不构建或运行任何旧版源码。
const repo = resolve(fileURLToPath(new URL('..', import.meta.url)))
const adapter = resolve(process.env.TYRS_HAND_ADAPTER_ROOT ?? resolve(repo, '../claude-codex'))
const evidence = resolve(process.env.TYRS_HAND_RESTART_EVIDENCE ??
  resolve(repo, '.artifacts/protocol-worker-restart', randomUUID()))
const root = await realpath(await mkdtemp('/tmp/tyrs-restart-'))
await mkdir(evidence, { recursive: true, mode: 0o700 })
const report = { caseId: 'FAILURE-006', passed: false, runId: process.env.PROTOCOL_RUN_ID ?? randomUUID(),
  evidence, temporaryRoot: root, startedAt: new Date().toISOString(), engines: {},
  limitations: ['只覆盖真实工具已完成且事件/终态待补报时崩溃，不覆盖工具在途或审批 pending',
    '投递失败通过代理注入 HTTP 503；SIGKILL 作用于真实 Worker 及该 fixture 子进程树',
    '临时端口、临时 HOME、虚拟凭据与回环 Mock LLM；不覆盖生产安装包或部署',
    '复用迁移基础设施类的 old/new 键，但两键均指向本次同一组协议33二进制'], steps: [] }
let worker, control, models, proxy, workerClosed = false
const clients = []
const mark = step => { report.steps.push({ step, at: new Date().toISOString() }); console.log('Worker恢复验收：' + step) }

async function buildCurrent() {
  const goroot = output('go', ['env', 'GOROOT'], { cwd: repo })
  const go = resolve(goroot, 'bin/go')
  assert.equal(output(go, ['version']).split(' ')[2], 'go1.26.6')
  assert.match(await readFile(resolve(repo, 'internal/workerprotocol/types.go'), 'utf8'), /const Version = 33/)
  const binaries = {}, hashes = {}
  for (const name of ['admin', 'server', 'worker']) {
    binaries[name] = resolve(root, name)
    output(go, ['build', '-o', binaries[name], './cmd/tyrs-hand-' + name], {
      cwd: repo, env: { ...process.env, PATH: resolve(goroot, 'bin') + ':' + process.env.PATH }, timeout: 180_000,
    })
    hashes[name] = sha256(await readFile(binaries[name]))
  }
  report.build = { protocolVersion: 33, commit: output('git', ['rev-parse', 'HEAD'], { cwd: repo }),
    dirty: Boolean(output('git', ['status', '--porcelain'], { cwd: repo })), sha256: hashes }
  return { old: binaries, new: binaries }
}

function rows(turnId) {
  assert.match(turnId, /^[a-zA-Z0-9_-]+$/)
  return JSON.parse(control.sql(`SELECT COALESCE(json_agg(row_to_json(x)),'[]') FROM (
    SELECT id,status,worker_event_sequence,worker_terminal_key FROM codex_turn_runs
    WHERE confirmed_codex_turn_id='${turnId}') x`))
}

async function pending(engine, turnId) {
  return until(engine + ' 自然写入当前版本待补报 journal', async () => {
    if (!proxy.runId || proxy.rejected.complete === 0 || proxy.rejected.events === 0) return undefined
    const path = resolve(worker.state, engine === 'codex' ? '' : 'claude-code',
      'control-state/runs', proxy.runId + '.json')
    const bytes = await readFile(path)
    const journal = JSON.parse(bytes)
    if (!journal.result || !journal.pendingEvents?.length || journal.terminalDelivered) return undefined
    assert.equal(journal.journalFormatVersion, 1)
    assert.equal(journal.task.snapshot.runtime.engine, engine)
    assert.equal(journal.task.claimed.ConfirmedTurnID, turnId)
    return { path, bytes, journal }
  })
}

async function quiescentWire() {
  return until('崩溃前原生协议请求均已有真实响应', async () => {
    for (const engine of ['codex', 'claude-code']) {
      const lines = (await readFile(resolve(evidence, `wire-${engine}.jsonl`), 'utf8')).trim().split('\n')
      const pending = { request: new Set(), response: new Set() }
      for (const line of lines) {
        const { connection, direction, message } = JSON.parse(line)
        if (message.id === undefined) continue
        const key = JSON.stringify([connection, message.id])
        if (message.method) pending[direction].add(key)
        else pending[direction === 'request' ? 'response' : 'request'].delete(key)
      }
      if (pending.request.size || pending.response.size) return undefined
    }
    return true
  })
}

try {
  assert.equal(process.versions.node, '24.14.0')
  const pin = JSON.parse(await readFile(resolve(repo, 'protocol/adapter-lock.json')))
  assert.equal(output('git', ['rev-parse', 'HEAD'], { cwd: adapter }), pin.commit)
  assert.equal(output('git', ['status', '--porcelain'], { cwd: adapter }), '')
  report.adapterCommit = pin.commit
  mark('只构建当前协议33的真实Control与Worker')
  const binaries = await buildCurrent()
  models = await startMigrationModels(adapter, resolve(root, 'project'))
  control = new MigrationControl(root, binaries)
  await control.start()
  proxy = await new MigrationFaultProxy(control.baseURL).start()
  control.workerURL = proxy.baseURL
  worker = new MigrationWorker({ root, repo, adapter, binaries, control, models, evidence })
  await worker.prepare()
  worker.env.TYRS_HAND_WORKER_ENROLLMENT_TOKEN = control.registration.enrollmentToken
  await worker.start('new')
  delete worker.env.TYRS_HAND_WORKER_ENROLLMENT_TOKEN
  await control.scan()
  report.workerId = control.registration.worker.id
  report.nativeBuild = worker.nativeBuild
  const identity = await worker.snapshot()
  const claudeHostKeySHA256 = sha256(await readFile(worker.keys['claude-code']))
  for (const [engine, marker] of [['codex', 'MIGRATION_OLD_WRITE'], ['claude-code', 'MIGRATION_CLAUDE_WRITE']]) {
    mark(engine + ' 真实工具完成后阻止事件与终态投递')
    const client = await new SSHProtocolClient(worker, engine).open()
    clients.push(client)
    const { thread } = await client.request('thread/start', { cwd: worker.workspace,
      approvalPolicy: 'never', sandbox: 'danger-full-access' })
    proxy.runId = undefined
    proxy.rejected = { events: 0, complete: 0 }
    proxy.armed = true
    const { turn } = await client.request('turn/start', { threadId: thread.id,
      input: [{ type: 'text', text: marker }] })
    const completed = await client.waitFor('turn/completed', p => p.threadId === thread.id && p.turn.id === turn.id)
    assert.equal(completed.params.turn.status, 'completed')
    const previous = await pending(engine, turn.id)
    const sideEffect = resolve(worker.workspace, marker + '.txt')
    assert.equal(await readFile(sideEffect, 'utf8'), marker + '_SIDE_EFFECT\n')
    assert.equal(models.calls[marker], 2)
    assert.equal(rows(turn.id).length, 1)
    assert.notEqual(rows(turn.id)[0].status, 'completed')
    await client.close()
    await quiescentWire()
    const callsBefore = { ...models.calls }
    const workerHashBefore = sha256(await readFile(binaries.new.worker))
    const pidBefore = worker.process.child.pid
    const crash = await worker.crash()
    assert.equal(worker.process.child.signalCode, 'SIGKILL')
    // 父进程 exit 不等于子进程已释放 socket；有界等待真实监听消失，不能强删活 socket。
    await until('SIGKILL后的原生录制进程已关闭监听', async () => {
      await worker.clearInstrumentationSockets('new')
      return true
    })
    assert.deepEqual(await readFile(previous.path), previous.bytes, 'SIGKILL不能替换待补报journal')
    mark(engine + ' 真实Worker已被SIGKILL，启动同一二进制')
    await worker.start('new')
    const pidAfter = worker.process.child.pid
    assert.notEqual(pidAfter, pidBefore)
    assert.equal(sha256(await readFile(binaries.new.worker)), workerHashBefore)
    assert.deepEqual(await worker.snapshot(), identity)
    assert.equal(sha256(await readFile(worker.keys['claude-code'])), claudeHostKeySHA256)
    proxy.armed = false
    await until(engine + ' 真实Control接收补报终态', () => {
      const current = rows(turn.id)
      assert.ok(current.length <= 1)
      return current.length === 1 && current[0].status === 'completed' && current[0].worker_terminal_key
    })
    await waitJournalDelivered(worker, previous)
    assert.deepEqual(models.calls, callsBefore, '恢复只能补报，不能重新调用模型')
    assert.equal(await readFile(sideEffect, 'utf8'), marker + '_SIDE_EFFECT\n', '副作用不能重放')
    const resumed = await new SSHProtocolClient(worker, engine).open()
    clients.push(resumed)
    const history = await resumed.request('thread/read', { threadId: thread.id, includeTurns: true })
    assert.match(JSON.stringify(history.thread.turns), new RegExp(marker + '_OK'))
    await resumed.close()
    report.engines[engine] = { passed: true, threadId: thread.id, turnId: turn.id,
      runId: previous.journal.task.claimed.RunID, pidBefore, pidAfter, crash,
      workerSHA256: workerHashBefore, pendingEventCount: previous.journal.pendingEvents.length,
      originalJournalSHA256: sha256(previous.bytes), modelCalls: models.calls[marker],
      sideEffectCount: 1, identityUnchanged: true, historyReadable: true, terminalDelivered: true }
  }
  for (const model of Object.values(models.models)) assert.deepEqual(model.unexpected, [])
  await worker.close()
  workerClosed = true
  report.schema = await validateRuntimeWire(repo, evidence)
  report.passed = true
  mark('双引擎已完成工具的SIGKILL恢复、真实副作用与schema全部通过')
} catch (error) {
  report.error = String(error.stack ?? error)
  if (worker) report.workerDiagnostics = await worker.diagnostics().catch(() => [])
  process.exitCode = 1
} finally {
  report.cleanupErrors = []
  const cleanup = async (name, action) => {
    try { await action() } catch (error) { report.cleanupErrors.push({ name, error: String(error) }) }
  }
  for (const [index, client] of clients.entries()) await cleanup('ssh-client-' + index, () => client.close())
  if (worker) {
    report.instrumentationCleanups = worker.instrumentationCleanups
    if (!workerClosed) await cleanup('worker', () => worker.close())
  }
  if (proxy) await cleanup('fault-proxy', () => proxy.close())
  if (control) await cleanup('control-and-databases', () => control.close())
  if (models) await cleanup('mock-models', () => models.close())
  if (report.cleanupErrors.length) { report.passed = false; process.exitCode = 1 }
  report.finishedAt = new Date().toISOString()
  await writeFile(resolve(evidence, 'report.json'), JSON.stringify(report, null, 2))
  console.log(JSON.stringify({ caseId: report.caseId, passed: report.passed, evidence,
    error: report.error?.split('\n')[0] }))
}
