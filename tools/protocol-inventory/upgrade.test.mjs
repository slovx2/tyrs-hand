import assert from 'node:assert/strict'
import test from 'node:test'
import { resolve } from 'node:path'
import { schemaIndex, payloadValidator } from './schema.mjs'

const root = resolve(import.meta.dirname, '../..')
const index = schemaIndex(resolve(root, 'protocol/codex-app-server/0.157.1/json-schema'),
  resolve(root, 'protocol/extensions'))
const validate = payloadValidator(index)

test('新增无参数方法仍校验官方响应，不能因没有 Params 类型漏检', () => {
  for (const method of ['account/gatewayOAuth/read', 'account/gatewayOAuth/login',
    'account/gatewayOAuth/cancel', 'rollout/compress']) {
    validate(method, 'params', null)
    assert.throws(() => validate(method, 'params', {}))
    assert.ok(index.get(method).response, `${method} 缺少响应 schema`)
    assert.throws(() => validate(method, 'response', null))
  }
})

test('旧 Desktop rollback 作为显式扩展保留，响应采用新 Thread schema', () => {
  assert.equal(index.get('thread/rollback').references.response, 'v2/ThreadReadResponse.json')
  validate('thread/rollback', 'params', { threadId: 'thread', numTurns: 1 })
  assert.throws(() => validate('thread/rollback', 'params', { threadId: 'thread', numTurns: -1 }))
  assert.throws(() => validate('thread/rollback', 'response', { thread: {} }))
  assert.ok(index.get('thread/revert'), '原生 revert 不应被旧入口替代')
})
