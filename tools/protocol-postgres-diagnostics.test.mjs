import assert from 'node:assert/strict'
import test from 'node:test'
import { postgresDiagnostics } from './protocol-postgres-diagnostics.mjs'

const ready = '2026-09-27 14:00:00.000 UTC [1] LOG:  database system is ready to accept connections'

test('无死锁的真实 PostgreSQL 启动日志可通过，不能冒充完整矩阵', () => {
  const report = postgresDiagnostics(ready, 'current')
  assert.equal(report.passed, true)
  assert.equal(report.completeProtocolMatrix, false)
})

test('即使业务专项最终成功，数据库死锁仍阻断门禁并保存行号', () => {
  const log = `${ready}\n2026-09-27 14:01:00.000 UTC [9] ERROR:  deadlock detected\nDETAIL: blocked\n` +
    '2026-09-27 14:02:00.000 UTC [10] ERROR: deadlock detected'
  const report = postgresDiagnostics(log, 'current')
  assert.equal(report.passed, false)
  assert.deepEqual(report.deadlockLines, [2, 4])
})

test('空日志和缺少运行身份不得通过', () => {
  assert.equal(postgresDiagnostics('', 'current').passed, false)
  assert.equal(postgresDiagnostics(ready, '').passed, false)
})

test('不把故障注入的请求取消误判为死锁', () => {
  assert.equal(postgresDiagnostics(`${ready}\nERROR: canceling statement due to user request`, 'current').passed, true)
})
