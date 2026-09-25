import assert from 'node:assert/strict'
import test from 'node:test'
import { protocolCoverage } from './coverage.mjs'
import { schemaIndex, payloadValidator } from './schema.mjs'
import { resolve } from 'node:path'

const index = schemaIndex(resolve(import.meta.dirname, '../../protocol/codex-app-server/0.147.0/json-schema'))
const approval = 'item/fileChange/requestApproval'
const resolved = 'serverRequest/resolved'
const methods = [approval, resolved].map(method => ({ method, schema: index.get(method).references,
  engines: { codex: 'required', 'claude-code': 'required' }, cases: { codex: ['TEST'], 'claude-code': ['TEST'] } }))
const request = id => ({ direction: 'server', id, method: approval, params: { threadId: 't', turnId: 'u', itemId: 'i', startedAtMs: 1, reason: null, grantRoot: null } })
const notification = (id, threadId = 't') => ({ direction: 'server', method: resolved, params: { threadId, requestId: id } })
function run(extra, keepSuccessful = true) {
  const executions = ['codex', 'claude-code'].map(engine => ({ engine, runId: 'current', caseName: 'fixture', caseIds: ['TEST'], status: 'passed' }))
  const messages = [...(keepSuccessful ? [request('successful'), { direction: 'client', id: 'successful', result: { decision: 'accept' } }] : []), notification('successful'), ...extra]
  const wire = executions.map(entry => ({ ...entry, kind: 'wire', payload: { messages, protocolErrors: [] } }))
  return protocolCoverage({ methods }, [], wire, executions, 'current', index, payloadValidator(index))
}

test('真实 resolved 可结束未答审批，但不算成功回答', () => {
  const extra = [request('pending'), notification('pending')]
  assert.equal(run(extra).complete, true)
  assert.equal(run(extra).evidence.filter(x => x.outcome === 'interrupted').length, 2)
  assert.equal(run(extra, false).complete, false)
  assert.equal(run([request('pending'), notification('pending', 'other')]).complete, false)
})

test('取消后的迟到答案必须匹配原始 ID 与 schema，不能冒充成功回答', () => {
  const extra = [request('pending'), notification('pending'), { direction: 'client', id: 'pending', result: { decision: 'accept' } }]
  assert.equal(run(extra).complete, true)
  assert.equal(run(extra).evidence.filter(x => x.outcome === 'late').length, 2)
  assert.equal(run(extra, false).complete, false)
  assert.equal(run([...extra, extra[2]]).complete, false)
  assert.equal(run([...extra.slice(0, 2), { ...extra[2], result: { decision: 'invalid' } }]).complete, false)
  assert.equal(run([request(1), notification('1')]).complete, false)
  assert.equal(run([request(1), notification(1), { direction: 'client', id: '1', result: { decision: 'accept' } }]).complete, false)
})

test('连接断开须有真实读取失败及预期原因，不能结束未答的普通 RPC', () => {
  const transport = { event: 'closed', source: 'connection', observed: 'read-error', error: 'EOF', expectedReason: 'client-disconnect' }
  assert.equal(run([request('pending'), { transport }]).complete, true)
  assert.equal(run([request('pending'), { transport }], false).complete, false)
  for (const change of [{ observed: undefined }, { error: '' }, { expectedReason: undefined }, { event: 'guessed' }])
    assert.equal(run([request('pending'), { transport: { ...transport, ...change } }]).complete, false)
  const report = run([{ direction: 'client', id: 42, method: 'thread/read', params: { threadId: 't' } }, { transport }])
  assert.ok(report.missing.some(x => x.method === 'thread/read' && x.reason === '请求缺少终结响应'))
})
