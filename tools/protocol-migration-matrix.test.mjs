import assert from 'node:assert/strict'
import { test } from 'node:test'
import { migrationOutcome, migrationWireArtifacts } from './protocol-migration-matrix.mjs'

test('matrix 迁移门禁拒绝进程失败、旧报告、schema失败和不完整清理', () => {
  const expected = { runId: 'this-run', caseId: 'TEST-ONLY' }
  const result = { code: 0, signal: null }
  const schema = { passed: true, errors: [] }
  const report = { ...expected, passed: true, cleanupErrors: [], schema }
  assert.deepEqual(migrationOutcome(result, report, schema, expected), [])
  for (const changed of [{ code: 1 }, { code: 0, signal: 'SIGTERM' }, { code: 0, error: 'spawn failed' }]) {
    assert.ok(migrationOutcome(changed, report, schema, expected).length)
  }
  for (const changed of [undefined, { ...report, runId: 'old-run' }, { ...report, caseId: 'OTHER' },
    { ...report, passed: false }, { ...report, cleanupErrors: undefined },
    { ...report, cleanupErrors: ['failed'] }, { ...report, schema: undefined }]) {
    assert.ok(migrationOutcome(result, changed, schema, expected).length)
  }
  for (const changed of [undefined, { passed: false, errors: [] }, { passed: true, errors: ['bad'] }]) {
    assert.ok(migrationOutcome(result, report, changed, expected).length)
  }
})

test('真实wire转换按连接保留报文、ID类型和方向，不填造关闭事件', () => {
  const context = { runId: 'this-run', caseId: 'TEST-ONLY', caseName: 'unit', engine: 'codex' }
  const request = { id: '7', method: 'thread/read', params: { threadId: '原样🙂', includeTurns: true } }
  const response = { id: '7', result: { thread: { id: '原样🙂' }, extra: null } }
  const other = { id: 7, method: 'turn/start', params: { input: [] } }
  const rows = [{ engine: 'codex', connection: 'first', direction: 'request', message: request },
    { engine: 'codex', connection: 'second', direction: 'request', message: other },
    { engine: 'codex', connection: 'first', direction: 'response', message: response }]
  const original = structuredClone(rows)
  const artifacts = migrationWireArtifacts(rows, context)
  assert.deepEqual(rows, original)
  assert.equal(artifacts.length, 2)
  assert.deepEqual(artifacts[0].payload.messages, [{ direction: 'client', ...request }, { direction: 'server', ...response }])
  assert.deepEqual(artifacts[1].payload.messages, [{ direction: 'client', ...other }])
  assert.equal(artifacts[0].runId, context.runId)
  assert.deepEqual(artifacts[0].caseIds, [context.caseId])
  assert.ok(artifacts.every(artifact => artifact.payload.messages.every(message => !Object.hasOwn(message, 'transport'))))
})

test('wire转换拒绝混入其他引擎、未知方向或缺连接，不静默丢弃', () => {
  const context = { runId: 'this-run', caseId: 'TEST-ONLY', caseName: 'unit', engine: 'codex' }
  const row = { engine: 'codex', connection: 'one', direction: 'request', message: { id: 1, method: 'initialize' } }
  for (const changed of [{ ...row, engine: 'claude-code' }, { ...row, direction: 'unknown' },
    { ...row, connection: undefined }, { ...row, message: { ...row.message, direction: 'server' } }]) {
    assert.throws(() => migrationWireArtifacts([changed], context))
  }
  assert.throws(() => migrationWireArtifacts([], context))
})
