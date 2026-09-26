import assert from 'node:assert/strict'
import test from 'node:test'
import { recoveryOutcome, recoveryWireArtifacts } from './protocol-recovery-matrix.mjs'
import { protocolCoverage } from './protocol-inventory/coverage.mjs'

const wire = { messages: 5, checkedResponses: 2, pendingRequests: [], pendingCallbacks: [] }
const schema = { passed: true, errors: [], engines: { codex: wire, 'claude-code': wire } }
const process = { code: 0, signal: null }
const engine = { passed: true, modelCalls: 2, sideEffectCount: 1, identityUnchanged: true,
  historyReadable: true, terminalDelivered: true, pidBefore: 1, pidAfter: 2,
  crash: { signal: 'SIGKILL' }, pendingEventCount: 10, workerSHA256: 'a'.repeat(64) }
const report = { runId: 'current', caseId: 'FAILURE-006', passed: true, cleanupErrors: [], schema,
  build: { protocolVersion: 33, sha256: { worker: engine.workerSHA256 } },
  engines: { codex: engine, 'claude-code': engine } }

test('恢复报告只有两引擎真实进程、补报、副作用和独立schema完整时通过', () => {
  assert.deepEqual(recoveryOutcome(process, report, schema, 'current'), [])
  for (const mutate of [
    value => { value.runId = 'previous' },
    value => { delete value.cleanupErrors },
    value => { value.cleanupErrors = ['worker close failed'] },
    value => { value.build.protocolVersion = 32 },
    value => { delete value.build.sha256.worker; delete value.engines.codex.workerSHA256 },
    value => { value.engines.codex.sideEffectCount = 2 },
    value => { value.engines['claude-code'].modelCalls = 4 },
    value => { value.engines.codex.pidAfter = value.engines.codex.pidBefore },
    value => { value.engines.codex.crash.signal = 'SIGTERM' },
    value => { value.engines.codex.pendingEventCount = 0 },
  ]) {
    const value = structuredClone(report)
    mutate(value)
    assert.ok(recoveryOutcome(process, value, schema, 'current').length)
  }
  assert.ok(recoveryOutcome(process, report, { passed: false, errors: ['pending request'] }, 'current').length)
})

function inflightFixture(caseId) {
  const result = { passed: true, threadId: 'thread', oldTurnId: 'old-turn', newTurnId: 'new-turn',
    identityUnchanged: true, oldTurnStatus: 'interrupted', oldRunStatus: 'failed',
    oldModelCalls: 1, nextModelCalls: 2, nextSideEffectCount: 1,
    oldSideEffectCount: caseId === 'FAILURE-007' ? 1 : 0,
    oldToolStates: [{ status: 'failed', exitCode: null }],
    crash: { signal: 'SIGKILL', pidBefore: 10, pidAfter: 20, processTree: [10, 11], processCount: 2 } }
  const value = { ...structuredClone(report), caseId, engines: { 'claude-code': result } }
  value.schema.processTerminatedCallbacks = []
  if (caseId === 'FAILURE-008') {
    Object.assign(result, { oldGeneration: '1790414236579168000', newGeneration: '1790414239597904000',
      oldAnswer: { status: 200, accepted: false, ready: false, interactiveStatus: 'interrupted', hasAnswer: false },
      substitutedOldAnswer: { status: 404, accepted: false, hasAnswer: false } })
    const pending = { connection: '11:2', id: 'approval', method: 'item/commandExecution/requestApproval' }
    // 拆开原基础fixture共享的引用，防止修改Claude时同时修改Codex。
    value.schema.engines['claude-code'] = { ...structuredClone(wire), pendingCallbacks: [pending] }
    value.schema.processTerminatedCallbacks = [{ ...pending, reason: 'SIGKILL', workerPID: 10 }]
  }
  return value
}

test('007只承认Claude在途工具明确中断、未知退出码和不重放', () => {
  const value = inflightFixture('FAILURE-007')
  assert.deepEqual(recoveryOutcome(process, value, value.schema, 'current', value.caseId), [])
  for (const mutate of [
    v => { v.engines.codex = v.engines['claude-code'] },
    v => { v.engines['claude-code'].oldSideEffectCount = 2 },
    v => { v.engines['claude-code'].oldToolStates[0].exitCode = 0 },
    v => { v.engines['claude-code'].oldTurnStatus = 'completed' },
    v => { v.engines['claude-code'].oldModelCalls = 2 },
    v => { v.engines['claude-code'].nextSideEffectCount = 0 },
    v => { v.engines['claude-code'].crash.processTree = [11] },
    v => { v.engines['claude-code'].crash.pidAfter = 11 },
    v => { v.schema.engines['claude-code'].pendingRequests.push('turn/start') },
  ]) {
    const changed = structuredClone(value); mutate(changed)
    assert.ok(recoveryOutcome(process, changed, changed.schema, 'current', changed.caseId).length)
  }
})

test('008只能解释精确原生请求对应的唯一被杀回调，不能清空pending', () => {
  const value = inflightFixture('FAILURE-008')
  assert.deepEqual(recoveryOutcome(process, value, value.schema, 'current', value.caseId), [])
  for (const mutate of [
    v => { v.schema.engines['claude-code'].pendingCallbacks = [] },
    v => { v.schema.processTerminatedCallbacks = [] },
    v => { v.schema.processTerminatedCallbacks[0].reason = 'SIGTERM' },
    v => { v.schema.processTerminatedCallbacks[0].workerPID = 999 },
    v => { v.schema.processTerminatedCallbacks[0].connection = '999:2' },
    v => { v.schema.processTerminatedCallbacks[0].id = 'other' },
    v => { v.schema.engines['claude-code'].pendingCallbacks.push({ id: 'other' }) },
    v => { v.engines['claude-code'].oldAnswer.accepted = true },
    v => { v.engines['claude-code'].substitutedOldAnswer.status = 200 },
    v => { v.engines['claude-code'].oldSideEffectCount = 1 },
    v => { v.engines['claude-code'].newGeneration = v.engines['claude-code'].oldGeneration },
  ]) {
    const changed = structuredClone(value); mutate(changed)
    assert.ok(recoveryOutcome(process, changed, changed.schema, 'current', changed.caseId).length)
  }
  assert.ok(recoveryOutcome(process, value, schema, 'current', value.caseId).length)
})

test('008聚合保留原回调，仅给其真实进程连接附加关闭证据且coverage只记interrupted', () => {
  const value = inflightFixture('FAILURE-008')
  const method = 'item/commandExecution/requestApproval'
  const metadata = { runId: 'current', caseId: value.caseId, caseName: 'pending-crash', engine: 'claude-code' }
  const rows = [{ engine: 'claude-code', connection: '11:2', direction: 'response',
    message: { id: 'approval', method, params: { threadId: 'thread', turnId: 'old-turn' } } }]
  const artifacts = recoveryWireArtifacts(rows, metadata, value, value.schema)
  assert.equal(artifacts[0].payload.messages.length, 2)
  assert.deepEqual(artifacts[0].payload.messages[0], { direction: 'server', ...rows[0].message })
  assert.equal(artifacts[0].payload.messages[1].transport.signal, 'SIGKILL')
  assert.equal(value.schema.engines['claude-code'].pendingCallbacks.length, 1)
  for (const mutate of [
    data => { data[0].message.params.turnId = 'new-turn' },
    data => { data[0].message.params.threadId = 'other-thread' },
    data => { data[0].connection = '20:2' },
    data => { data.push({ ...data[0], direction: 'request', message: { id: 2, method: 'thread/read' } }) },
    data => { data.push({ ...data[0], direction: 'request', message: { id: 'approval', result: { decision: 'accept' } } }) },
  ]) {
    const changed = structuredClone(rows); mutate(changed)
    assert.throws(() => recoveryWireArtifacts(changed, metadata, value, value.schema))
  }
  const coverage = protocolCoverage({ methods: [{ method, schema: { params: 'fixed' },
    engines: { codex: 'not-applicable', 'claude-code': 'required' }, reasons: { codex: '测试夹具只检查Claude中断' },
    cases: { codex: [], 'claude-code': [value.caseId] } }] }, [], artifacts,
  [{ ...metadata, caseIds: [value.caseId], status: 'passed' }], 'current',
  new Map([[method, { kind: 'ServerRequest' }]]), () => {})
  assert.ok(coverage.evidence.some(item => item.method === method && item.outcome === 'interrupted'))
  assert.ok(!coverage.evidence.some(item => item.method === method && item.outcome === 'success'))
  assert.ok(!coverage.missing.some(item => item.reason === '请求缺少终结响应'))
})
