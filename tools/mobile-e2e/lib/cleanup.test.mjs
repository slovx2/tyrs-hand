import assert from 'node:assert/strict'
import test from 'node:test'
import { cleanupManaged, completionError } from './cleanup.mjs'

test('adb listener 清理失败不能截断真实 Worker、模型与 Control 清理', async () => {
  const stopped = []
  const resource = (name, fail = false) => ({ name, async stop() {
    stopped.push(name)
    if (fail) throw new Error("adb: listener 'tcp:37179' not found")
  } })
  const errors = await cleanupManaged({
    maestro: [resource('maestro')],
    resources: [resource('models'), resource('worker'), resource('reverse-40927'), resource('reverse-37179', true)],
    controls: [resource('control')],
  })
  assert.deepEqual(stopped, ['maestro', 'reverse-37179', 'reverse-40927', 'worker', 'models', 'control'])
  assert.deepEqual(errors, [{ group: 'resources', name: 'reverse-37179',
    error: "adb: listener 'tcp:37179' not found" }])
  const original = new Error('Maestro 失败（1）')
  assert.equal(completionError(original, errors), original, '必须保留真实 GUI 的首个失败')
  assert.ok(completionError(undefined, errors) instanceof AggregateError, '无业务错误时清理失败仍不得假成功')
})

test('多个清理失败完整报告且不修改资源顺序集合', async () => {
  const entries = ['worker', 'model'].map((name) => ({ name, async stop() { throw new Error(name) } }))
  const errors = await cleanupManaged({ resources: entries })
  assert.deepEqual(errors.map((entry) => entry.name), ['model', 'worker'])
  assert.deepEqual(entries.map((entry) => entry.name), ['worker', 'model'])
  assert.equal(completionError(undefined, []), undefined)
})
