import assert from 'node:assert/strict'
import test from 'node:test'
import { runtimeWireReport } from './runtime-wire.mjs'
import { payloadValidator } from './schema.mjs'

const method = 'fixture/read'
const index = new Map([[method, { kind: 'ClientRequest',
  params: { type: 'object', required: ['threadId'], properties: { threadId: { type: 'string' } } },
  response: { type: 'object', required: ['ready'], properties: { ready: { const: true } } },
}]])
const validate = payloadValidator(index)

function fixture() {
  return {
    manifest: { methods: [{ method, engines: { codex: 'required', 'claude-code': 'required' },
      cases: { codex: ['WIRE-001'], 'claude-code': [] }, schema: { params: 'fixture' } }] },
    executions: [{ runId: 'current', status: 'passed', engine: 'codex', caseName: 'actual', caseIds: ['WIRE-001'] }],
    artifacts: [{ runId: 'current', kind: 'wire', engine: 'codex', caseName: 'actual', payload: {
      messages: [{ direction: 'client', method, id: 1, params: { threadId: 'thread' } },
        { direction: 'server', id: 1, result: { ready: true } }], protocolErrors: [],
    } }],
  }
}
const report = f => runtimeWireReport(f.manifest, f.artifacts, f.executions, 'current', index, validate)

test('单引擎实际通信可通过，明确不代表完整协议覆盖', () => {
  const result = report(fixture())
  assert.equal(result.passed, true)
  assert.equal(result.completeProtocolMatrix, false)
  assert.equal(result.scope, 'runtime-wire')
  assert.equal(result.messages, 2)
  assert.equal(result.evidence, 1)
})

test('子集仍拒绝 wire 中漏登记的方法', () => {
  const f = fixture()
  f.manifest.methods = []
  assert.ok(report(f).errors.some(entry => entry.reason === 'wire 出现未登记方法'))
  assert.equal(report(f).passed, false)
})

test('子集仍拒绝非法响应、报文方向、未终结请求和录制器 schema 错误', () => {
  for (const mutate of [
    f => { f.artifacts[0].payload.messages[1].result.ready = false },
    f => { f.artifacts[0].payload.messages[0].direction = 'server' },
    f => { f.artifacts[0].payload.messages.pop() },
    f => { f.artifacts[0].payload.protocolErrors.push('实际记录的错误') },
  ]) {
    const f = fixture()
    mutate(f)
    assert.equal(report(f).passed, false)
  }
})

test('没有执行、没有通信或只有旧证据不得通过', () => {
  for (const mutate of [
    f => { f.executions = [] },
    f => { f.artifacts = [] },
    f => { f.artifacts[0].runId = 'old' },
    f => { f.executions[0].runId = 'old' },
    f => { f.artifacts[0].payload.messages = [] },
    f => { f.artifacts[0].payload.messages = null },
    f => { f.artifacts[0].engine = 'claude-code' },
    f => { f.artifacts[0].caseName = 'other' },
    f => { f.executions[0].status = 'skipped' },
    f => { f.executions[0].status = 'failed' },
    f => { f.executions[0].engine = f.artifacts[0].engine = 'unknown' },
    f => { f.executions[0].caseIds = [] },
    f => { f.executions[0].caseIds = [null] },
  ]) {
    const f = fixture()
    mutate(f)
    assert.equal(report(f).passed, false)
  }
})
