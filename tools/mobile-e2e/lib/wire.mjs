import assert from 'node:assert/strict'
import { readFile, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { schemaIndex, payloadValidator } from '../../protocol-inventory/schema.mjs'

export async function validateRuntimeWire(repoRoot, runDir) {
  const index = schemaIndex(resolve(repoRoot, 'protocol/codex-app-server/0.147.0/json-schema'),
    resolve(repoRoot, 'protocol/extensions'))
  const validate = payloadValidator(index)
  const report = { passed: false, engines: {}, errors: [] }
  for (const engine of ['codex', 'claude-code']) {
    try {
      const failures = await readFile(resolve(runDir, 'wire-' + engine + '.jsonl.errors'), 'utf8')
      report.errors.push({ engine, error: '协议录制曾失败，重启不能抹去证据缺口：' + failures })
    } catch (error) {
      if (error.code !== 'ENOENT') throw error
    }
    const messages = (await readFile(resolve(runDir, 'wire-' + engine + '.jsonl'), 'utf8'))
      .trim().split('\n').map((line) => JSON.parse(line))
    const pending = { request: new Map(), response: new Map() }
    const methods = new Map()
    let checkedResponses = 0
    for (const { connection, direction, message } of messages) {
      try {
        if (message.method) {
          const spec = index.get(message.method)
          assert.ok(spec, '未登记的方法：' + message.method)
          assert.equal(spec.kind.startsWith('Client'), direction === 'request', message.method + ' 方向错误')
          validate(message.method, 'params', message.params)
          methods.set(message.method, (methods.get(message.method) ?? 0) + 1)
          if (message.id !== undefined) {
            const key = JSON.stringify([connection, message.id])
            assert.ok(!pending[direction].has(key), '重复的未完成请求 ID：' + key)
            pending[direction].set(key, message.method)
          }
          continue
        }
        const outstanding = pending[direction === 'request' ? 'response' : 'request']
        const key = JSON.stringify([connection, message.id])
        const method = outstanding.get(key)
        assert.ok(method, '响应没有对应请求：' + key)
        outstanding.delete(key)
        if (message.error) {
          assert.equal(Number.isInteger(message.error.code), true)
          assert.equal(typeof message.error.message, 'string')
          assert.equal(message.result, undefined)
        } else validate(method, 'response', message.result)
        checkedResponses++
      } catch (error) {
        report.errors.push({ engine, direction, method: message.method, id: message.id, error: error.message })
      }
    }
    for (const method of ['initialize', 'thread/start', 'turn/start', 'turn/completed']) {
      if (!methods.has(method)) report.errors.push({ engine, method, error: '真实客户端未执行必需链路' })
    }
    report.engines[engine] = { messages: messages.length, checkedResponses,
      methods: Object.fromEntries(methods), pendingRequests: [...pending.request.values()],
      pendingCallbacks: [...pending.response.values()] }
    if (pending.response.size) report.errors.push({ engine, error: '仍有未回答的服务端回调' })
    if (pending.request.size) report.errors.push({ engine, error: '仍有未完成的客户端请求' })
  }
  report.passed = report.errors.length === 0
  await writeFile(resolve(runDir, 'schema-report.json'), JSON.stringify(report, null, 2))
  assert.deepEqual(report.errors, [], '真实移动链路请求和响应必须符合已固定schema')
  return report
}
