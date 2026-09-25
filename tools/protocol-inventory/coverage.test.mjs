import assert from 'node:assert/strict'
import test from 'node:test'
import { protocolCoverage } from './coverage.mjs'
import { payloadValidator, schemaIndex } from './schema.mjs'
import { resolve } from 'node:path'

const method = 'fixture/read'
const index = new Map([[method, { kind: 'ClientRequest',
  params: { type: 'object', required: ['threadId'], properties: { threadId: { type: 'string' } } },
  response: { type: 'object', required: ['status'], properties: { status: { const: 'ready' } } },
}]])
const validate = payloadValidator(index)

test('MCP reload 的 null 参数与官方响应均进行真实 schema 校验', () => {
  const root = resolve(import.meta.dirname, '../..')
  const validate = payloadValidator(schemaIndex(resolve(root, 'protocol/codex-app-server/0.147.0/json-schema')))
  validate('config/mcpServer/reload', 'params', null)
  validate('config/mcpServer/reload', 'response', {})
  assert.throws(() => validate('config/mcpServer/reload', 'params', {}))
  assert.throws(() => validate('config/mcpServer/reload', 'response', null))
})

test('运行时扩展必须校验真实身份字段与版本，不能仅凭方法名计覆盖', () => {
  const root = resolve(import.meta.dirname, '../..')
  const validate = payloadValidator(schemaIndex(
    resolve(root, 'protocol/codex-app-server/0.147.0/json-schema'), resolve(root, 'protocol/extensions')))
  validate('runtime/info', 'params', {})
  assert.throws(() => validate('runtime/info', 'params', { engine: 'claude-code' }))
  const codex = { engine: 'codex', protocolVersion: '0.147.0', cliBuild: 'codex-cli 0.147.0',
    capabilities: [], releaseReady: true, workerId: 'worker', status: 'running' }
  validate('runtime/info', 'response', codex)
  assert.throws(() => validate('runtime/info', 'response', { ...codex, workerId: undefined }))
  const claude = { engine: 'claude-code', protocolVersion: '0.147.0', cliBuild: '2.1.282 (Claude Code)',
    capabilities: [], releaseReady: false, nodeVersion: '24.14.0', sdkVersion: '0.3.282', cliSha256: 'a'.repeat(64) }
  validate('runtime/info', 'response', claude)
  assert.throws(() => validate('runtime/info', 'response', { ...claude, cliSha256: 'unverified' }))
  assert.throws(() => validate('runtime/info', 'response', { ...claude, sdkVersion: '0.3.283' }))
})
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

test('真实进程崩溃可终结挂起请求，但不能替代成功协议覆盖', () => {
  const fixture = fixtures()
  const messages = fixture.artifacts[0].payload.messages
  messages.push({ direction: 'client', id: 2, method, params: { threadId: 'b' } },
    { transport: { event: 'closed', source: 'process', exitCode: null, signal: 'SIGKILL', expectedSignal: 'SIGKILL' } })
  assert.equal(run(fixture).complete, true)
  assert.equal(run(fixture).evidence.filter(item => item.outcome === 'interrupted').length, 1)
  messages.splice(0, 2)
  assert.equal(run(fixture).complete, false, '崩溃证据不能作为正常功能成功覆盖')
})

test('未声明崩溃、关闭事件不符或关闭后报文不能掩盖缺失响应', () => {
  for (const mutate of [
    end => { delete end.expectedSignal },
    end => { end.signal = 'SIGTERM' },
    end => { end.exitCode = 0 },
    end => { end.source = 'guess' },
  ]) {
    const fixture = fixtures()
    const end = { event: 'closed', source: 'process', exitCode: null, signal: 'SIGKILL', expectedSignal: 'SIGKILL' }
    mutate(end)
    fixture.artifacts[0].payload.messages.push({ direction: 'client', id: 2, method, params: { threadId: 'b' } }, { transport: end })
    assert.equal(run(fixture).complete, false)
  }
  const fixture = fixtures()
  fixture.artifacts[0].payload.messages.push({ transport: { event: 'closed', source: 'process', exitCode: 0, signal: null } },
    { direction: 'client', id: 3, method, params: { threadId: 'b' } }, { id: 3, result: { status: 'ready' } })
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

test('非法参数负例必须显式登记并返回 -32602；不能替代成功覆盖', () => {
  const fixture = fixtures()
  const messages = fixture.artifacts[0].payload.messages
  messages.push({ direction: 'client', id: 2, method, params: { threadId: 42 }, expectedErrorCode: -32602 },
    { id: 2, error: { code: -32602, message: '参数无效' } })
  assert.equal(run(fixture).complete, true)
  assert.equal(run(fixture).evidence.filter(item => item.outcome === 'rejected').length, 1)
  messages.splice(0, 2)
  assert.equal(run(fixture).complete, false, '只有拒绝不能证明必需功能可用')
})

test('负例成功、错误码不符、没有响应或未声明的非法报文都不能通过', () => {
  for (const mutate of [
    messages => { messages[3] = { id: 2, result: { status: 'ready' } } },
    messages => { messages[3].error.code = -32004 },
    messages => { messages[3].result = { status: 'ready' } },
    messages => { messages[3].error.message = 3 },
    messages => { messages.pop() },
    messages => { delete messages[2].expectedErrorCode },
    messages => { messages[2].expectedErrorCode = -32004; messages[3].error.code = -32004 },
  ]) {
    const fixture = fixtures()
    const messages = fixture.artifacts[0].payload.messages
    messages.push({ direction: 'client', id: 2, method, params: {}, expectedErrorCode: -32602 },
      { id: 2, error: { code: -32602, message: '参数无效' } })
    mutate(messages)
    assert.equal(run(fixture).complete, false)
  }
})
