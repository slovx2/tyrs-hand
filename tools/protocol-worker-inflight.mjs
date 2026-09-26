import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { mkdir, mkdtemp, readFile, realpath, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { SSHProtocolClient } from './mobile-e2e/lib/ssh-protocol.mjs'
import { output } from './mobile-e2e/lib/process.mjs'
import { structuredTitleResponse } from './mobile-e2e/lib/models.mjs'
import { MigrationControl, sha256, until } from './protocol-migration-infra.mjs'
import { MigrationWorker } from './protocol-migration-worker.mjs'
import { schemaIndex, payloadValidator } from './protocol-inventory/schema.mjs'

const pendingMode = process.argv.includes('--pending')
assert.ok(process.argv.slice(2).every(arg => arg === '--pending'))
const repo = resolve(fileURLToPath(new URL('..', import.meta.url)))
const adapter = resolve(process.env.TYRS_HAND_ADAPTER_ROOT ?? resolve(repo, '../claude-codex'))
const evidence = resolve(process.env.TYRS_HAND_INFLIGHT_EVIDENCE ?? resolve(repo,
  '.artifacts/protocol-worker-inflight', randomUUID()))
const root = await realpath(await mkdtemp('/tmp/tyrs-inflight-'))
await mkdir(evidence, { recursive: true, mode: 0o700 })
const report = { caseId: pendingMode ? 'FAILURE-008' : 'FAILURE-007', passed: false,
  runId: process.env.PROTOCOL_RUN_ID ?? randomUUID(), evidence, temporaryRoot: root,
  startedAt: new Date().toISOString(), engines: {}, cleanupErrors: [], steps: [],
  limitations: ['仅声明Claude；Codex运行时只作为同Worker中的背景引擎，未完成对应崩溃业务场景',
    'SIGKILL真实Worker及fixture子进程树；同版本33、同Worker ID重启',
    '全部临时HOME、虚拟凭据、回环Mock LLM，不读取个人登录态或部署生产'] }
let worker, control, models, workerClosed = false, oldApproval, crashEvidence
const clients = [], callbackQueue = []
const mark = step => { report.steps.push({ step, at: new Date().toISOString() }); console.log('在途恢复验收：' + step) }
const marker = pendingMode ? 'CRASH_PENDING_WRITE' : 'CRASH_INFLIGHT_WRITE'
const nextMarker = 'CRASH_NEXT_WRITE'
const quote = text => "'" + text.replaceAll("'", "'\\''") + "'"

async function buildCurrent() {
  const goroot = output('go', ['env', 'GOROOT'], { cwd: repo })
  const go = resolve(goroot, 'bin/go')
  assert.equal(output(go, ['version']).split(' ')[2], 'go1.26.6')
  assert.match(await readFile(resolve(repo, 'internal/workerprotocol/types.go'), 'utf8'), /const Version = 33/)
  const binaries = {}, hashes = {}
  for (const name of ['admin', 'server', 'worker']) {
    binaries[name] = resolve(root, name)
    output(go, ['build', '-o', binaries[name], './cmd/tyrs-hand-' + name], {
      cwd: repo, env: { ...process.env, PATH: resolve(goroot, 'bin') + ':' + process.env.PATH }, timeout: 180_000 })
    hashes[name] = sha256(await readFile(binaries[name]))
  }
  report.build = { protocolVersion: 33, commit: output('git', ['rev-parse', 'HEAD'], { cwd: repo }),
    dirty: Boolean(output('git', ['status', '--porcelain'], { cwd: repo })), sha256: hashes }
  return { old: binaries, new: binaries }
}

async function startModels() {
  const { MockLLM } = await import(pathToFileURL(resolve(adapter, 'dist/test/fixtures/mock-llm.mjs')))
  const result = { models: {}, urls: {}, calls: {}, results: {} }
  for (const engine of ['codex', 'claude-code']) {
    const model = new MockLLM()
    result.models[engine] = model
    const respond = request => {
      model.enqueue(respond)
      const title = structuredTitleResponse(request)
      if (title) return title
      assert.equal(engine, 'claude-code', '业务模型不得跨引擎')
      const users = (request.messages ?? []).filter(message => message.role === 'user')
      const tag = (JSON.stringify(users).match(/CRASH_(?:INFLIGHT|PENDING|NEXT)_WRITE/g) ?? []).at(-1)
      assert.ok(tag, '请求必须属于显式测试回合')
      result.calls[tag] = (result.calls[tag] ?? 0) + 1
      assert.ok(result.calls[tag] <= 2, '崩溃回合不得自动重放')
      const toolID = 'tool_' + tag.toLowerCase()
      const toolResult = users.flatMap(message => Array.isArray(message.content) ? message.content : [])
        .find(item => item.type === 'tool_result' && item.tool_use_id === toolID)
      if (toolResult) {
        assert.equal(tag, nextMarker, '被SIGKILL中断的工具不能自动产生续跑结果')
        assert.equal(Boolean(toolResult.is_error), false)
        assert.ok(JSON.stringify(toolResult).includes(tag + '_SIDE_EFFECT'), '真实工具输出必须回到模型')
        result.results[tag] = true
        return [{ type: 'text', text: tag + '_OK' }]
      }
      const file = resolve(root, 'project', tag + '.txt')
      let command = `printf '%s\\n' '${tag}_SIDE_EFFECT' >> ${quote(file)}; cat ${quote(file)}`
      if (tag === 'CRASH_INFLIGHT_WRITE') command =
        `printf '%s\\n' '${tag}_SIDE_EFFECT' >> ${quote(file)}; sleep 300; printf done >> ${quote(file + '.done')}`
      return [{ type: 'tool_use', name: 'Bash', id: toolID, input: { command, timeout: 120_000 } }]
    }
    model.enqueue(respond)
    result.urls[engine] = await model.start()
  }
  return result
}

function rows(turnId) {
  assert.match(turnId, /^[a-zA-Z0-9_-]+$/)
  return JSON.parse(control.sql(`SELECT COALESCE(json_agg(row_to_json(x)),'[]') FROM (
    SELECT id,status,error_code,worker_terminal_key FROM codex_turn_runs
    WHERE confirmed_codex_turn_id='${turnId}') x`))
}

async function noFile(path) {
  try { await readFile(path); assert.fail('禁止出现副作用文件') }
  catch (error) { if (error.code !== 'ENOENT') throw error }
}

function callback(request) {
  return new Promise(resolveAnswer => callbackQueue.push({ request, resolveAnswer }))
}

async function connect() {
  const client = await new SSHProtocolClient(worker, 'claude-code', callback).open()
  clients.push(client)
  return client
}

async function startTurn(client, threadId, text) {
  return client.request('turn/start', { threadId, approvalPolicy: pendingMode ? 'on-request' : 'never',
    sandboxPolicy: { type: 'dangerFullAccess' }, input: [{ type: 'text', text }] })
}

async function lateControlAnswer(request, generation) {
  assert.match(generation, /^[0-9]+$/)
  const credential = (await readFile(worker.credential, 'utf8')).trim()
  const body = JSON.stringify({ requestId: request.id, appServerGeneration: null,
    workspaceId: control.workspace.id, threadId: request.params.threadId, turnId: request.params.turnId,
    itemId: request.params.itemId, surface: 'desktop', answer: { decision: 'accept' } })
    .replace('"appServerGeneration":null', '"appServerGeneration":' + generation)
  const response = await fetch(control.baseURL + '/worker/v1/interactive/answer', { method: 'POST',
    headers: { authorization: 'Bearer ' + credential, 'content-type': 'application/json',
      'X-Tyrs-Worker-Protocol': '33', 'X-Tyrs-Runtime-Engine': 'claude-code' },
    body })
  const state = await response.json()
  return { status: response.status, accepted: state.accepted ?? false, ready: state.ready ?? false,
    interactiveStatus: typeof state.status === 'string' ? state.status : null, hasAnswer: Boolean(state.answer) }
}

// 校验真实消息schema；只把确定被本次SIGKILL截断的精确旧回调记为终止，绝不补造响应。
async function validateWire() {
  const index = schemaIndex(resolve(repo, 'protocol/codex-app-server/0.147.0/json-schema'), resolve(repo, 'protocol/extensions'))
  const validate = payloadValidator(index)
  const result = { passed: false, engines: {}, errors: [], processTerminatedCallbacks: [] }
  for (const engine of ['codex', 'claude-code']) {
    try {
      const failures = await readFile(resolve(evidence, `wire-${engine}.jsonl.errors`), 'utf8')
      if (failures.trim()) result.errors.push({ engine, error: '原生录制器已记录失败，不能通过重启掩盖' })
    } catch (error) { if (error.code !== 'ENOENT') throw error }
    const messages = (await readFile(resolve(evidence, `wire-${engine}.jsonl`), 'utf8')).trim().split('\n').map(JSON.parse)
    const pending = { request: new Map(), response: new Map() }
    let responses = 0
    for (const { connection, direction, message } of messages) {
      try {
        const key = JSON.stringify([connection, message.id])
        if (message.method) {
          const spec = index.get(message.method)
          assert.ok(spec)
          assert.equal(spec.kind.startsWith('Client'), direction === 'request')
          validate(message.method, 'params', message.params)
          if (message.id !== undefined) {
            assert.ok(!pending[direction].has(key))
            pending[direction].set(key, { connection, id: message.id, method: message.method })
          }
        } else {
          const map = pending[direction === 'request' ? 'response' : 'request']
          const request = map.get(key)
          assert.ok(request, '响应缺少原始请求')
          map.delete(key)
          if (message.error) { assert.equal(Number.isInteger(message.error.code), true); assert.equal(typeof message.error.message, 'string') }
          else validate(request.method, 'response', message.result)
          responses++
        }
      } catch (error) { result.errors.push({ engine, method: message.method, id: message.id, error: error.message }) }
    }
    for (const callback of pending.response.values()) {
      if (pendingMode && engine === 'claude-code' && oldApproval && callback.id === oldApproval.id &&
        callback.method === oldApproval.method && crashEvidence.processTree.includes(Number(callback.connection.split(':')[0]))) {
        result.processTerminatedCallbacks.push({ ...callback, reason: 'SIGKILL', workerPID: crashEvidence.pidBefore })
      } else result.errors.push({ engine, error: '未解释的未回答原生回调', ...callback })
    }
    assert.equal(pending.request.size, 0, '不得遗漏客户端请求')
    result.engines[engine] = { messages: messages.length, checkedResponses: responses,
      pendingRequests: [...pending.request.values()], pendingCallbacks: [...pending.response.values()] }
  }
  if (pendingMode) assert.equal(result.processTerminatedCallbacks.length, 1)
  result.passed = result.errors.length === 0
  await writeFile(resolve(evidence, 'schema-report.json'), JSON.stringify(result, null, 2))
  assert.deepEqual(result.errors, [])
  return result
}

function interactive(turnId) {
  assert.match(turnId, /^[a-zA-Z0-9_-]+$/)
  return JSON.parse(control.sql(`SELECT COALESCE(json_agg(row_to_json(x)),'[]') FROM (
    SELECT id,status,app_server_generation::text AS generation FROM codex_interactive_requests
    WHERE turn_id='${turnId}') x`))
}

try {
  assert.equal(process.versions.node, '24.14.0')
  const pin = JSON.parse(await readFile(resolve(repo, 'protocol/adapter-lock.json')))
  assert.equal(output('git', ['rev-parse', 'HEAD'], { cwd: adapter }), pin.commit)
  assert.equal(output('git', ['status', '--porcelain'], { cwd: adapter }), '')
  report.adapterCommit = pin.commit
  mark('构建当前33二进制并启动真实双入口Worker')
  const binaries = await buildCurrent()
  models = await startModels()
  control = new MigrationControl(root, binaries)
  await control.start()
  worker = new MigrationWorker({ root, repo, adapter, binaries, control, models, evidence })
  await worker.prepare()
  worker.env.TYRS_HAND_WORKER_ENROLLMENT_TOKEN = control.registration.enrollmentToken
  await worker.start('new')
  delete worker.env.TYRS_HAND_WORKER_ENROLLMENT_TOKEN
  await control.scan()
  report.workerId = control.registration.worker.id
  const identity = await worker.snapshot()
  const claudeHostKey = sha256(await readFile(worker.keys['claude-code']))
  const client = await connect()
  const { thread } = await client.request('thread/start', { cwd: worker.workspace,
    approvalPolicy: pendingMode ? 'on-request' : 'never', sandbox: 'danger-full-access' })
  const { turn } = await startTurn(client, thread.id, marker)
  const file = resolve(worker.workspace, marker + '.txt')
  let oldInteractive
  if (pendingMode) {
    oldApproval = (await until('原生审批通过真实SSH到达', () => callbackQueue[0])).request
    assert.equal(oldApproval.method, 'item/commandExecution/requestApproval')
    oldInteractive = await until('真实Control登记待审批', () => {
      const value = interactive(turn.id)
      return value.length === 1 && value[0].status === 'pending' ? value[0] : undefined
    })
    await noFile(file)
  } else {
    await until('真实Bash已写入marker且仍在执行', async () =>
      await readFile(file, 'utf8') === marker + '_SIDE_EFFECT\n')
    await noFile(file + '.done')
  }
  const state = await client.request('thread/read', { threadId: thread.id, includeTurns: true })
  assert.equal(state.thread.turns.find(item => item.id === turn.id)?.status, 'inProgress')
  const run = await until('真实Control已登记活动回合', () => rows(turn.id)[0])
  const journalPath = resolve(worker.state, 'claude-code/control-state/runs', run.id + '.json')
  const originalJournal = JSON.parse(await readFile(journalPath))
  assert.ok(!originalJournal.result && !originalJournal.failure)
  const callsBefore = { ...models.calls }
  const pidBefore = worker.process.child.pid
  const processRows = output('ps', ['-axo', 'pid=,ppid=']).split('\n').map(line => line.trim().split(/\s+/).map(Number))
  const processTree = [pidBefore]
  for (let index = 0; index < processTree.length; index++) {
    for (const [pid, parent] of processRows) if (parent === processTree[index]) processTree.push(pid)
  }
  mark(pendingMode ? '审批未回答且无副作用，SIGKILL真实Worker' : '真实工具有副作用但无终态，SIGKILL真实Worker')
  const crash = await worker.crash()
  assert.equal(worker.process.child.signalCode, 'SIGKILL')
  crashEvidence = { ...crash, pidBefore, processTree }
  await until('fixture原生监听确实已关闭', async () => { await worker.clearInstrumentationSockets('new'); return true })
  await worker.start('new')
  crashEvidence.pidAfter = worker.process.child.pid
  assert.notEqual(crashEvidence.pidAfter, pidBefore)
  assert.equal(sha256(await readFile(binaries.new.worker)), report.build.sha256.worker)
  assert.deepEqual(await worker.snapshot(), identity)
  assert.equal(sha256(await readFile(worker.keys['claude-code'])), claudeHostKey)
  const resumed = await connect()
  await resumed.request('thread/resume', { threadId: thread.id })
  const history = await resumed.request('thread/read', { threadId: thread.id, includeTurns: true })
  const oldTurn = history.thread.turns.find(item => item.id === turn.id)
  assert.equal(oldTurn?.status, 'interrupted', '崩溃回合必须明确中断，不能假成功')
  assert.ok(oldTurn.error?.message)
  assert.ok(oldTurn.items.every(item => item.status !== 'inProgress'), '历史工具不能永远停在执行中')
  const oldCommands = oldTurn.items.filter(item => item.type === 'commandExecution')
  if (!pendingMode) {
    assert.equal(oldCommands.length, 1)
    assert.equal(oldCommands[0].status, 'failed', '无原生终态的工具不能标为执行成功')
    assert.equal(oldCommands[0].exitCode, null, 'SIGKILL前未观察到的退出码必须保持未知')
  }
  const recoveredRun = await until('Control也明确结束崩溃回合', () => {
    const value = rows(turn.id)[0]
    return value && ['failed', 'canceled'].includes(value.status) ? value : undefined
  })
  assert.deepEqual(models.calls, callsBefore, '仅重启和读历史不能重新请求模型')
  let oldAnswer
  if (pendingMode) {
    oldAnswer = await lateControlAnswer(oldApproval, oldInteractive.generation)
    assert.equal(oldAnswer.status, 200)
    assert.equal(oldAnswer.accepted, false)
    assert.equal(oldAnswer.interactiveStatus, 'interrupted')
    assert.equal(oldAnswer.hasAnswer, false)
    await noFile(file)
  } else {
    assert.equal(await readFile(file, 'utf8'), marker + '_SIDE_EFFECT\n')
    await noFile(file + '.done')
  }
  mark('旧回合明确中断且没有重放；显式开始新的回合')
  const nextFile = resolve(worker.workspace, nextMarker + '.txt')
  const { turn: nextTurn } = await startTurn(resumed, thread.id, nextMarker)
  let newInteractive, substitutedOldAnswer
  if (pendingMode) {
    const next = await until('新回合产生新的真实审批', () => callbackQueue[1])
    assert.notEqual(next.request.id, oldApproval.id)
    assert.equal(next.request.params.turnId, nextTurn.id)
    newInteractive = await until('Control登记新代审批', () => {
      const value = interactive(nextTurn.id)
      return value.length === 1 && value[0].status === 'pending' ? value[0] : undefined
    })
    assert.notEqual(newInteractive.generation, oldInteractive.generation)
    substitutedOldAnswer = await lateControlAnswer(oldApproval, newInteractive.generation)
    assert.equal(substitutedOldAnswer.status, 404)
    assert.equal(substitutedOldAnswer.accepted, false)
    await noFile(file)
    await noFile(nextFile)
    next.resolveAnswer({ decision: 'accept' })
  }
  const completion = await resumed.waitFor('turn/completed', params => params.turn.id === nextTurn.id)
  assert.equal(completion.params.turn.status, 'completed')
  assert.equal(await readFile(nextFile, 'utf8'), nextMarker + '_SIDE_EFFECT\n')
  assert.equal(models.results[nextMarker], true)
  assert.equal(models.calls[nextMarker], 2)
  assert.equal(models.calls[marker], 1, '旧模型请求不能被重放')
  if (pendingMode) await noFile(file)
  else {
    assert.equal(await readFile(file, 'utf8'), marker + '_SIDE_EFFECT\n', '显式新回合也不能重放旧工具')
    await noFile(file + '.done')
  }
  for (const model of Object.values(models.models)) assert.equal(model.unexpected.length, 0)
  await until('Control确认显式新回合完成', () => rows(nextTurn.id)[0]?.status === 'completed')
  await resumed.close()
  await worker.close()
  workerClosed = true
  report.schema = await validateWire()
  report.engines['claude-code'] = { passed: true, threadId: thread.id, oldTurnId: turn.id, newTurnId: nextTurn.id,
    crash: crashEvidence, oldTurnStatus: oldTurn.status, oldRunStatus: recoveredRun.status,
    oldToolStates: oldCommands.map(item => ({ status: item.status, exitCode: item.exitCode })),
    oldModelCalls: models.calls[marker], nextModelCalls: models.calls[nextMarker],
    oldSideEffectCount: pendingMode ? 0 : 1, nextSideEffectCount: 1, identityUnchanged: true,
    oldAnswer, substitutedOldAnswer, oldGeneration: oldInteractive?.generation, newGeneration: newInteractive?.generation }
  report.passed = true
  mark('真实崩溃、旧回合不重放、显式新回合执行和协议校验通过')
} catch (error) {
  report.error = String(error.stack ?? error)
  if (worker) report.workerDiagnostics = await worker.diagnostics().catch(error => [{ error: String(error) }])
  process.exitCode = 1
} finally {
  const cleanup = async (name, action) => { try { await action() } catch (error) { report.cleanupErrors.push({ name, error: String(error) }) } }
  for (const [index, client] of clients.entries()) await cleanup('ssh-client-' + index, () => client.close())
  for (const [index, client] of clients.entries()) await cleanup('save-client-wire-' + index, () =>
    writeFile(resolve(evidence, 'client-wire-' + index + '.json'), JSON.stringify(client.trace), { mode: 0o600 }))
  if (worker && !workerClosed) await cleanup('worker', () => worker.close())
  if (control) await cleanup('control-and-databases', () => control.close())
  if (models) for (const [engine, model] of Object.entries(models.models)) await cleanup('mock-' + engine, () => model.close())
  if (report.cleanupErrors.length) { report.passed = false; process.exitCode = 1 }
  report.finishedAt = new Date().toISOString()
  await writeFile(resolve(evidence, 'report.json'), JSON.stringify(report, null, 2))
  console.log(JSON.stringify({ caseId: report.caseId, passed: report.passed, evidence, error: report.error?.split('\n')[0] }))
}
