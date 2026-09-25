import assert from 'node:assert/strict'
import { once } from 'node:events'
import { mkdtemp, readFile, rm, stat } from 'node:fs/promises'
import { createConnection, createServer } from 'node:net'
import test from 'node:test'
import { relay } from './network.mjs'
import { startProcess, waitFor } from './process.mjs'

test('测试进程只接收白名单环境，不能继承宿主凭据', async () => {
  const root = await mkdtemp('/tmp/tyrs-env-test-')
  const key = 'TYRS_MOBILE_SENTINEL_SECRET'
  const previous = process.env[key]
  process.env[key] = 'host-secret-not-for-worker'
  let managed
  try {
    managed = await startProcess('isolated', process.execPath,
      ['-e', 'process.stdout.write(JSON.stringify(process.env))'], {
        cwd: root, env: { HOME: root, TEST_MARKER: 'allowed' }, inheritEnv: false, logDir: root,
      })
    assert.deepEqual(await managed.exit, { code: 0, signal: null })
    await managed.stop()
    const actual = JSON.parse(await readFile(root + '/isolated.log', 'utf8'))
    assert.equal(actual.HOME, root)
    assert.equal(actual.TEST_MARKER, 'allowed')
    assert.equal(actual[key], undefined)
    assert.equal(actual.ANTHROPIC_API_KEY, undefined)
    assert.equal(actual.OPENAI_API_KEY, undefined)
  } finally {
    if (previous === undefined) delete process.env[key]
    else process.env[key] = previous
    await managed?.stop()
    await rm(root, { recursive: true, force: true })
  }
})

test('依赖缺失时等待健康检查立即失败，清理不能悬挂', { timeout: 3000 }, async () => {
  const root = await mkdtemp('/tmp/tyrs-start-test-')
  let managed
  try {
    managed = await startProcess('missing', root + '/does-not-exist', [], {
      cwd: root, env: {}, inheritEnv: false, logDir: root,
    })
    const result = await managed.exit
    assert.equal(result.error.code, 'ENOENT')
    await assert.rejects(waitFor('http://127.0.0.1:1', { process: managed }), /启动失败/)
    await managed.stop()
    assert.match(await readFile(root + '/missing.log', 'utf8'), /ENOENT/)
  } finally {
    await managed?.stop()
    await rm(root, { recursive: true, force: true })
  }
})

test('固定 Unix 中继双向传输且关闭时释放连接与 socket', { timeout: 3000 }, async () => {
  const root = await mkdtemp('/tmp/tyrs-relay-test-')
  const received = []
  const server = createServer((socket) => socket.on('data', (bytes) => {
    received.push(bytes.toString())
    socket.write('ACK:' + bytes)
  }))
  let bridge, client
  try {
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
    bridge = await relay(root + '/fixed.sock', { host: '127.0.0.1', port: server.address().port })
    client = createConnection(root + '/fixed.sock')
    await once(client, 'connect')
    const reply = once(client, 'data')
    client.write('actual-payload')
    assert.equal((await reply)[0].toString(), 'ACK:actual-payload')
    assert.deepEqual(received, ['actual-payload'])
    const closed = once(client, 'close')
    await bridge.close()
    bridge = undefined
    await closed
    await assert.rejects(stat(root + '/fixed.sock'), { code: 'ENOENT' })
  } finally {
    client?.destroy()
    await bridge?.close()
    await new Promise((resolve) => server.close(resolve))
    await rm(root, { recursive: true, force: true })
  }
})
