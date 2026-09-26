import assert from 'node:assert/strict'
import test from 'node:test'
import { protocolCoverage } from './coverage.mjs'
import { payloadValidator } from './schema.mjs'

const method = 'item/fileChange/requestApproval'
const index = new Map([[method, { kind: 'ServerRequest',
  params: { type: 'object', required: ['threadId'], properties: { threadId: { type: 'string' } } },
  response: { type: 'object', required: ['decision'], properties: { decision: { const: 'accept' } } },
}]])
const validate = payloadValidator(index)
function fixture() {
  const execution = { runId: 'current', caseName: 'real isolation', status: 'passed', caseIds: ['ISOLATION-004'] }
  const reply = { id: 'approval-id', result: { decision: 'accept' } }
  const source = { ...execution, engine: 'claude-code', kind: 'wire', payload: { protocolErrors: [], messages: [
    { direction: 'server', id: reply.id, method, params: { threadId: 'source' } },
    { direction: 'client', ...reply },
  ] } }
  const target = { ...execution, engine: 'codex', kind: 'wire', payload: { protocolErrors: [], messages: [
    { direction: 'client', ...reply, expectedForeignServerResponse: true },
  ] } }
  const fault = { ...execution, kind: 'fault-injection', payload: { type: 'foreign-server-response',
    sourceEngine: 'claude-code', targetEngine: 'codex', requestMethod: method, requestId: reply.id, reply,
    sideEffectsBefore: 0, sideEffectsAfterForeignReply: 0, sideEffectsAfterAuthorizedReply: 1,
  } }
  return { executions: ['claude-code', 'codex'].map(engine => ({ ...execution, engine })),
    artifacts: [source, target, fault], source, target, fault }
}
function run(value) {
  return protocolCoverage({ methods: [{ method, schema: { params: 'fixture' },
    engines: { codex: 'required', 'claude-code': 'required' },
    cases: { codex: ['ISOLATION-004'], 'claude-code': ['ISOLATION-004'] },
  }] }, [], value.artifacts, value.executions, 'current', index, validate)
}
const schemaErrors = result => result.missing.filter(item => item.reason !== '用例没有成功执行此协议及 schema 校验')
test('真实双端请求答案加副作用证据可接纳跨引擎负例，但不能算成功覆盖', () => {
  const result = run(fixture())
  assert.deepEqual(schemaErrors(result), [])
  assert.equal(result.complete, false)
  assert.equal(result.evidence.filter(item => item.engine === 'codex' && item.outcome === 'rejected').length, 1)
  assert.equal(result.evidence.filter(item => item.engine === 'codex' && item.outcome === 'success').length, 0)
})
test('无标记、伪来源、旧制品、缺少成功执行或副作用不符都不能掩盖无匹配响应', () => {
  for (const mutate of [
    value => { delete value.target.payload.messages[0].expectedForeignServerResponse },
    value => { value.artifacts.pop() },
    value => { value.artifacts.push(structuredClone(value.fault)) },
    value => { value.fault.runId = 'old' },
    value => { value.fault.payload.sourceEngine = 'codex' },
    value => { value.fault.payload.sideEffectsAfterForeignReply = 1 },
    value => { value.fault.payload.sideEffectsBefore = 1 },
    value => { value.fault.payload.sideEffectsAfterAuthorizedReply = 0 },
    value => { value.fault.payload.reply = { id: 'different', result: { decision: 'accept' } } },
    value => { value.fault.payload.requestMethod = 'unregistered' },
    value => { value.executions[0].status = 'skipped' },
    value => { value.source.payload.messages.splice(0, 1) },
    value => { value.source.payload.messages.pop() },
    value => { value.source.payload.messages[0].params.threadId = 42 },
    value => { value.target.payload.messages[0].result = { decision: 'broken' } },
    value => { value.target.payload.messages.unshift({ direction: 'server', id: 'approval-id', method, params: { threadId: 'target' } }) },
  ]) {
    const value = fixture()
    mutate(value)
    assert.ok(schemaErrors(run(value)).length > 0, String(mutate))
  }
})
