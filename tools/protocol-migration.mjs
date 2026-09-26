import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { mkdir, mkdtemp, readFile, realpath, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { SSHProtocolClient } from './mobile-e2e/lib/ssh-protocol.mjs'
import { validateRuntimeWire } from './mobile-e2e/lib/wire.mjs'
import { output } from './mobile-e2e/lib/process.mjs'
import { buildMigrationBinaries, MigrationControl, OLD_COMMIT, sha256, until } from './protocol-migration-infra.mjs'
import { rollbackJournal } from './protocol-migration-rollback.mjs'
import { MigrationWorker } from './protocol-migration-worker.mjs'
import { startMigrationModels } from './protocol-migration-models.mjs'
import { MigrationFaultProxy, pendingJournal, verifyMigratedJournal, waitJournalDelivered } from './protocol-migration-journal.mjs'

// --rollback 使用真实旧数据库快照隔离 Worker 回滚再升级；不宣称同库降级兼容。
const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const rollbackMode = process.argv.includes('--rollback')
const journalMode = process.argv.includes('--journal') || rollbackMode
assert.ok(process.argv.slice(2).every(argument => ['--journal', '--rollback'].includes(argument)), '未知迁移验收参数')
const adapter = resolve(process.env.TYRS_HAND_ADAPTER_ROOT ?? resolve(repo, '../claude-codex'))
const evidence = resolve(process.env.TYRS_HAND_MIGRATION_EVIDENCE ?? resolve(repo, '.artifacts/protocol-migration', randomUUID()))
const root = await realpath(await mkdtemp('/tmp/tyrs-mig-'))
await mkdir(evidence, { recursive: true, mode: 0o700 })
const report = { caseId: rollbackMode ? 'MIGRATION-007' : journalMode ? 'MIGRATION-006' : 'MIGRATION-005', passed: false, startedAt: new Date().toISOString(),
  runId: process.env.PROTOCOL_RUN_ID ?? randomUUID(),
  oldCommit: OLD_COMMIT, evidence, temporaryRoot: root,
  limitations: ['虚拟 Discord 成员是身份前提夹具；Workspace、项目与会话均由真实接口产生',
    '使用独立临时 SSH 端口；生产入口仍为 2222/3333',
    ...(journalMode ? [] : ['本用例不覆盖待补报 journal']),
    ...(rollbackMode ? ['回滚恢复升级前真实数据库快照，不覆盖升级后数据库直接降级'] : ['本用例不覆盖回滚']),
    '本用例不覆盖签名 Linux 安装包或生产部署'], steps: [] }
let control, worker, models, proxy, previousJournal, beforeReplayCalls
const clients = []
const mark = step => { report.steps.push({ step, at: new Date().toISOString() }); console.log('迁移验收：' + step) }

function controlRows(threadId) {
  assert.match(threadId, /^[a-zA-Z0-9_-]+$/)
  return JSON.parse(control.sql(`SELECT COALESCE(json_agg(row_to_json(x)),'[]') FROM (
    SELECT id,external_thread_id,session_id,workspace_id,workspace_project_id,status
    FROM codex_thread_controls WHERE external_thread_id='${threadId}') x`))
}

async function open(engine) {
  const client = await new SSHProtocolClient(worker, engine).open()
  clients.push(client)
  return client
}

async function runTurn(client, threadId, marker) {
  const { turn } = await client.request('turn/start', { threadId,
    input: [{ type: 'text', text: marker }] })
  const complete = await client.waitFor('turn/completed', params => params.threadId === threadId && params.turn.id === turn.id)
  assert.equal(complete.params.turn.status, 'completed', complete.params.turn.error?.message)
  const history = await client.request('thread/read', { threadId, includeTurns: true })
  assert.match(JSON.stringify(history.thread.turns), new RegExp(marker + '_OK'))
  return { turnId: turn.id, turns: history.thread.turns.length }
}

async function waitControlTurn(turnId) {
  assert.match(turnId, /^[a-zA-Z0-9_-]+$/)
  return until('Control 接收真实 Turn 终态', () => {
    const rows = JSON.parse(control.sql(`SELECT COALESCE(json_agg(row_to_json(x)),'[]') FROM (
      SELECT id,control_id,primary_intent_id,status,worker_event_sequence,worker_terminal_key
      FROM codex_turn_runs WHERE confirmed_codex_turn_id='${turnId}') x`))
    assert.ok(rows.length <= 1, '同一原生 Turn 不得产生重复 Control run')
    return rows.length === 1 && rows[0].status === 'completed' && rows[0].worker_terminal_key ? rows[0] : undefined
  })
}

try {
  assert.equal(process.versions.node, '24.14.0')
  const pin = JSON.parse(await readFile(resolve(repo, 'protocol/adapter-lock.json')))
  assert.equal(output('git', ['rev-parse', 'HEAD'], { cwd: adapter }), pin.commit)
  assert.equal(output('git', ['status', '--porcelain'], { cwd: adapter }), '', '适配器必须固定且干净')
  assert.equal(JSON.parse(await readFile(resolve(adapter, 'node_modules/@anthropic-ai/claude-agent-sdk/package.json'))).version, pin.claudeAgentSdk)
  report.adapter = pin
  report.workerDirty = Boolean(output('git', ['status', '--porcelain'], { cwd: repo }))
  mark('构建固定旧源码和当前源码的真实二进制')
  const build = await buildMigrationBinaries(repo, root)
  report.build = build.manifests
  models = await startMigrationModels(adapter, resolve(root, 'project'), { journal: journalMode })
  control = new MigrationControl(root, build.binaries)
  await control.start()
  if (journalMode) {
    proxy = await new MigrationFaultProxy(control.baseURL).start()
    control.workerURL = proxy.baseURL
  }
  worker = new MigrationWorker({ root, repo, adapter, binaries: build.binaries, control, models, evidence })
  await worker.prepare()
  await worker.start('old')
  const scan = await control.scan()
  assert.ok(scan.scan.projects.some(project => project.hostPath === worker.workspace), '真实扫描应发现项目根目录')
  report.workerId = control.registration.worker.id
  report.workspaceId = control.workspace.id
  report.ports = worker.ports
  report.nativeBuild = worker.nativeBuild
  mark('旧协议 32 Control 注册和真实 Worker 扫描已完成')
  let old = await open('codex')
  const { thread } = await old.request('thread/start', { cwd: worker.workspace, model: 'mock-model',
    approvalPolicy: 'never', sandbox: 'danger-full-access' })
  report.codexThreadId = thread.id
  // 旧版线程登记是异步的；先确认真实 Control 映射再发第一条输入。
  report.oldControl = await until('旧 Control 会话映射', () => {
    const rows = controlRows(thread.id)
    return rows.length === 1 && rows[0].session_id ? rows[0] : undefined
  })
  assert.equal(report.oldControl.workspace_id, control.workspace.id)
  report.oldTurn = await runTurn(old, thread.id, 'MIGRATION_OLD_WRITE')
  report.oldRun = await waitControlTurn(report.oldTurn.turnId)
  const before = await worker.snapshot()
  report.before = before
  if (journalMode) {
    proxy.armed = true
    report.pendingTurn = await runTurn(old, thread.id, 'MIGRATION_PENDING_WRITE')
    previousJournal = await pendingJournal(worker, proxy)
    await old.close()
    report.crash = await worker.crash()
    // 进程停止后才固定证据，避免重试元数据与快照竞争。
    previousJournal.bytes = await readFile(previousJournal.path)
    previousJournal.value = JSON.parse(previousJournal.bytes)
    await writeFile(resolve(root, 'journal-before-upgrade.json'), previousJournal.bytes, { mode: 0o600 })
    beforeReplayCalls = structuredClone(models.calls)
    mark('真实旧 journal 已积压事件与终态，fixture PID 树已 SIGKILL')
  } else {
    await old.close()
    await worker.process.stop()
  }
  mark('旧真实 SSH → Codex CLI → Mock LLM 文件副作用和历史已产生')
  if (rollbackMode) {
    const rollback = await rollbackJournal({ root, control, worker, models,
      previous: previousJournal, proxy, open, threadId: thread.id })
    previousJournal = rollback.journal
    report.rollback = rollback.report
    assert.deepEqual(await worker.snapshot(), before)
    mark('真实旧32重写新版pending journal并移除来源字段；原迁移标记与备份保留')
  }
  await control.upgrade()
  await worker.start('new')
  if (journalMode) {
    report.journal = await verifyMigratedJournal(previousJournal)
    assert.deepEqual(models.calls, beforeReplayCalls, '迁移待补报 journal 不得重放任务模型或工具')
    assert.equal(await readFile(resolve(worker.state, 'control-state/codex-runtime-scope-v1'), 'utf8'), '1\n')
    report.journal.rejected = { ...proxy.rejected }
    assert.ok(proxy.rejected.events > 0)
    assert.ok(proxy.rejected.complete > 0)
    proxy.armed = false
    report.recoveredRun = await waitControlTurn(report.pendingTurn.turnId)
    assert.equal(report.recoveredRun.id, proxy.runId)
    assert.equal(report.recoveredRun.worker_event_sequence, previousJournal.value.nextSequence - 1,
      '待补报事件序号必须完整送达且不能重置')
    await waitJournalDelivered(worker, previousJournal)
    assert.deepEqual(models.calls, beforeReplayCalls, '补报完成不能重新调用任务模型')
    mark('新 Worker 已迁移并补报同一真实 journal，未重放工具')
  }
  assert.deepEqual(await worker.snapshot(), before, '升级不能改变 credential 或 Codex Host Key')
  assert.equal(control.sql('SELECT count(*) FROM workers'), '1', '不能重新注册第二个 Worker')
  const identity = JSON.parse(await readFile(resolve(worker.state, 'control-state/identity.json'), 'utf8'))
  report.persistedIdentity = identity.workerId
  assert.equal(report.persistedIdentity, control.registration.worker.id)
  assert.notEqual(sha256(await readFile(worker.keys['claude-code'])), before.codexHostKeySHA256)
  mark('原数据库升级至 33，原凭据和 Host Key 保持')
  const resumed = await open('codex')
  const result = await resumed.request('thread/resume', { threadId: thread.id })
  assert.equal(result.thread.id, thread.id)
  assert.match(JSON.stringify(result.thread.turns), /MIGRATION_OLD_WRITE_OK/)
  report.resumedTurn = await runTurn(resumed, thread.id, 'MIGRATION_RESUME_WRITE')
  report.resumedRun = await waitControlTurn(report.resumedTurn.turnId)
  const newControl = controlRows(thread.id)
  assert.equal(newControl.length, 1)
  assert.equal(newControl[0].id, report.oldControl.id)
  assert.equal(newControl[0].session_id, report.oldControl.session_id)
  assert.equal(control.sql(`SELECT engine FROM codex_thread_controls WHERE id='${report.oldControl.id}'`), 'codex')
  const claude = await open('claude-code')
  const started = await claude.request('thread/start', { cwd: worker.workspace, model: 'mock-claude',
    approvalPolicy: 'never', sandbox: 'danger-full-access' })
  report.claudeThreadId = started.thread.id
  assert.notEqual(started.thread.id, thread.id)
  const claudeRows = await until('Claude Control 会话映射', () => {
    const rows = controlRows(started.thread.id)
    return rows.length === 1 ? rows : undefined
  })
  report.claudeTurn = await runTurn(claude, started.thread.id, 'MIGRATION_CLAUDE_WRITE')
  report.claudeRun = await waitControlTurn(report.claudeTurn.turnId)
  assert.notEqual(claudeRows[0].session_id, report.oldControl.session_id)
  assert.equal(claudeRows[0].workspace_id, report.oldControl.workspace_id)
  assert.equal(control.sql(`SELECT engine FROM codex_thread_controls WHERE id='${claudeRows[0].id}'`), 'claude-code')
  const codexList = await resumed.request('thread/list', { limit: 100 })
  const claudeList = await claude.request('thread/list', { limit: 100 })
  assert.ok(codexList.data.some(item => item.id === thread.id))
  assert.ok(claudeList.data.some(item => item.id === started.thread.id))
  assert.ok(!codexList.data.some(item => item.id === started.thread.id))
  assert.ok(!claudeList.data.some(item => item.id === thread.id))
  report.models = await models.verify()
  report.files = {}
  for (const marker of report.models.completed) report.files[marker] = sha256(await readFile(resolve(worker.workspace, marker + '.txt')))
  for (const client of clients) await client.close()
  await worker.process.stop()
  if (journalMode) {
    const calls = structuredClone(models.calls)
    const claudeKey = sha256(await readFile(worker.keys['claude-code']))
    await worker.start('new')
    assert.deepEqual(await worker.snapshot(), before)
    assert.equal(sha256(await readFile(worker.keys['claude-code'])), claudeKey)
    const restart = await open('codex')
    const resumedAgain = await restart.request('thread/resume', { threadId: thread.id })
    assert.match(JSON.stringify(resumedAgain.thread.turns), /MIGRATION_PENDING_WRITE_OK/)
    await restart.close()
    assert.deepEqual(models.calls, calls, '再次重启不能重放已补报执行')
    assert.deepEqual(await readFile(previousJournal.backupPath ?? previousJournal.path + '.before-runtime-scope'), previousJournal.bytes)
    assert.deepEqual(await waitControlTurn(report.pendingTurn.turnId), report.recoveredRun,
      '再次重启不能新增完成记录或改变已经确认的事件序号')
    await worker.process.stop()
    report.journal.restartIdempotent = true
    report.models = await models.verify()
  }
  report.schema = await validateRuntimeWire(repo, evidence)
  report.passed = true
  mark('旧会话恢复、新 Claude 隔离、真实文件与 schema 验证通过')
} catch (error) {
  report.error = { message: error.message, cause: error.cause?.message, stack: error.stack }
  process.exitCode = 1
} finally {
  report.cleanupErrors = []
  for (const cleanup of [...clients.map(client => () => client.close()),
    () => worker?.close(), () => proxy?.close(), () => control?.close(), () => models?.close()]) {
    try { await cleanup() } catch (error) { report.cleanupErrors.push(error.message) }
  }
  if (report.cleanupErrors.length) { report.passed = false; process.exitCode = 1 }
  report.finishedAt = new Date().toISOString()
  report.instrumentationCleanups = worker?.instrumentationCleanups ?? []
  try { report.workerDiagnostics = await worker?.diagnostics() }
  catch { report.workerDiagnostics = [{ error: '无法读取已关闭的测试 Worker 诊断摘要' }] }
  await writeFile(resolve(evidence, 'migration-report.json'), JSON.stringify(report, null, 2))
  console.log(JSON.stringify({ caseId: report.caseId, passed: report.passed, evidence, error: report.error?.message }))
}
