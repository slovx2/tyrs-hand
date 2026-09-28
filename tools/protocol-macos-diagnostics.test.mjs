import assert from 'node:assert/strict'
import test from 'node:test'
import { summarizeMacLoopback } from './protocol-macos-diagnostics.mjs'

const ready = () => ({ status: 0, signal: null, error: '', results: ['tcp4', 'tcp6', 'tcp']
  .map(network => ({ network, success: 2000, failures: {} })) })

test('完整 6000 次连接只能证明本次探针通过', () => {
  const report = summarizeMacLoopback(ready())
  assert.equal(report.passed, true)
  assert.equal(report.success, 6000)
  assert.equal(report.completeProtocolMatrix, false)
})

test('真实 CI 同形结果：进程退出 0 不能掩盖 EPERM、重置及超时', () => {
  const source = ready()
  source.results[0] = { network: 'tcp4', success: 1999, failures: { EPERM: 1 } }
  source.results[2] = { network: 'tcp', success: 1995, failures: { ECONNRESET: 2, ETIMEDOUT: 3 } }
  const report = summarizeMacLoopback(source)
  assert.equal(report.passed, false)
  assert.equal(report.success, 5994)
  assert.equal(report.failures, 6)
})

test('缺失、重复、非法或未完成的连接统计不能通过', () => {
  for (const mutate of [
    source => { source.results = [] },
    source => { source.results = {} },
    source => { source.results[0] = null },
    source => { source.results.pop() },
    source => { source.results[0].network = 'unknown' },
    source => { source.results[0].network = 'tcp6' },
    source => { source.results[0].success = 1999 },
    source => { source.results[0].success = -1 },
    source => { source.results[0].success = NaN },
    source => { source.results[0].failures.EPERM = -1 },
  ]) {
    const source = ready()
    mutate(source)
    assert.equal(summarizeMacLoopback(source).passed, false)
  }
})

test('进程失败、信号退出及取证解析错误不能通过', () => {
  for (const override of [{ status: 1 }, { status: null }, { signal: 'SIGKILL' }, { error: 'invalid JSON' }])
    assert.equal(summarizeMacLoopback({ ...ready(), ...override }).passed, false)
})
