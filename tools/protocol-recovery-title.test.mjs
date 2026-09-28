import assert from 'node:assert/strict'
import test from 'node:test'
import { completedTitleProjection } from './protocol-recovery-title.mjs'

// 仅校验前置条件算法，不作为真实双引擎验收证据。
const request = { connection: 'first:2', direction: 'request', timestamp: '2026-09-28T05:12:42.949Z',
  message: { id: 19, method: 'thread/name/set', params: { threadId: 'thread', name: 'title' } } }
const response = { connection: 'first:2', direction: 'response', timestamp: '2026-09-28T05:12:42.950Z',
  message: { id: 19, result: {} } }

test('标题发出但未应答时不得进入已完成工具的崩溃窗口', () => {
  assert.equal(completedTitleProjection([], 'thread', 'title'), undefined)
  assert.equal(completedTitleProjection([request], 'thread', 'title'), undefined)
  const nextConnection = { ...response, connection: 'next:2' }
  assert.equal(completedTitleProjection([request, nextConnection], 'thread', 'title'), undefined)
  assert.equal(completedTitleProjection([response, request], 'thread', 'title'), undefined)
  assert.equal(completedTitleProjection([request, { ...request, connection: 'next:2' }, nextConnection],
    'thread', 'title'), undefined, '新一代重试成功不能消除旧代未应答请求')
})

test('只接受同线程、同标题、同连接请求的真实成功响应', () => {
  const rows = [request, response]
  assert.deepEqual(completedTitleProjection(rows, 'thread', 'title'), {
    threadId: 'thread', title: 'title', connection: 'first:2', requestId: 19,
    requestedAt: request.timestamp, acknowledgedAt: response.timestamp })
  assert.equal(completedTitleProjection(rows, 'other', 'title'), undefined)
  assert.equal(completedTitleProjection(rows, 'thread', 'other'), undefined)
  assert.equal(completedTitleProjection([...rows, { ...request, message: { ...request.message, id: 20 } }],
    'thread', 'title'), undefined, '旧的成功响应不能掩盖新的在途请求')
  assert.throws(() => completedTitleProjection([request, { ...response,
    message: { id: 19, error: { code: -1, message: '失败' } } }], 'thread', 'title'))
  assert.throws(() => completedTitleProjection([request, response, response], 'thread', 'title'))
})
