import assert from 'node:assert/strict'
import test from 'node:test'
import { protocolCoverage } from './coverage.mjs'
import { payloadValidator } from './schema.mjs'

const method = 'fixture/read'
const index = new Map([[method, { kind: 'ClientRequest',
  params: { type: 'object', required: ['threadId'], properties: { threadId: { type: 'string' } } },
  response: { type: 'object', required: ['status'], properties: { status: { const: 'ready' } } },
}]])
const validate = payloadValidator(index)
function fixtures() {
  const manifest = { methods: [{ method, schema: { params: 'FixtureParams.json' },
    engines: { codex: 'required', 'claude-code': 'required' },
    cases: { codex: ['TEST-001'], 'claude-code': ['TEST-001'] } }] }
  const executions = ['codex', 'claude-code'].map(engine => ({ engine, runId: 'current',
    status: 'passed', caseIds: ['TEST-001'], caseName: 'actual test' }))
  const artifacts = executions.map(entry => ({ ...entry, kind: 'wire', payload: {
    messages: [{ direction: 'client', id: 1, method, params: { threadId: 'a' } },
      { id: 1, result: { status: 'ready' } }], protocolErrors: [],
  } }))
  return { manifest, executions, artifacts }
}
function run(fixture, usages = []) {
  return protocolCoverage(fixture.manifest, usages, fixture.artifacts, fixture.executions, 'current', index, validate)
}

test('只有两个引擎本轮通过的用例、真实请求响应和 schema 全部匹配才通过', () => {
  assert.equal(run(fixtures()).complete, true)
})
test('旧记录、失败或 skip 结果不能提供覆盖', () => {
  for (const mutation of [entry => { entry.runId = 'old' }, entry => { entry.status = 'failed' }, entry => { entry.status = 'skipped' }]) {
    const fixture = fixtures()
    mutation(fixture.executions[0])
    assert.equal(run(fixture).complete, false)
  }
})
test('报文格式错误、没有响应以及仅有用例登记都必须失败', () => {
  for (const mutation of [messages => { messages[1].result.status = 'wrong' }, messages => { messages.pop() }, messages => { messages.length = 0 }]) {
    const fixture = fixtures()
    mutation(fixture.artifacts[0].payload.messages)
    assert.equal(run(fixture).complete, false)
  }
})
test('新增调用未登记，或者登记用例没有执行该协议，均失败', () => {
  assert.equal(run(fixtures(), [{ method: 'new/method' }]).complete, false)
  const fixture = fixtures()
  fixture.manifest.methods[0].cases.codex = ['OTHER-001']
  assert.equal(run(fixture).complete, false)
})
test('不适用能力必须有原因与真实拒绝响应，空成功不能通过', () => {
  const fixture = fixtures()
  const entry = fixture.manifest.methods[0]
  entry.engines['claude-code'] = 'not-applicable'
  assert.equal(run(fixture).complete, false)
  entry.reasons = { 'claude-code': 'OpenAI 专属能力' }
  assert.equal(run(fixture).complete, false)
  fixture.artifacts[1].payload.messages[1] = { id: 1, error: { code: -32004, message: '不适用' } }
  assert.equal(run(fixture).complete, true)
})
