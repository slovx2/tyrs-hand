import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import test from 'node:test'
import { AndroidTransportTrace } from './android-transport-trace.mjs'

test('只读诊断区分 Android reverse 丢失与宿主新 TCP 连接被拒绝', async () => {
  const root = await mkdtemp(resolve(tmpdir(), 'mobile-transport-trace-'))
  const server = createServer((socket) => socket.destroy())
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  const port = server.address().port
  let reads = 0, reverse = `host-1 tcp:${port} tcp:${port}\nhost-1 tcp:7777 tcp:7777`
  const trace = new AndroidTransportTrace({ deviceID: 'emulator-5554', ports: [port],
    path: resolve(root, 'transport.jsonl'), intervalMs: 60000,
    readReverse: async () => { reads++; return reverse } })
  try {
    await trace.start()
    reverse = ''
    await trace.capture('mapping-lost')
    await new Promise((resolve) => server.close(resolve))
    await trace.capture('host-listener-closed')
    await trace.stop()
    const rows = (await readFile(resolve(root, 'transport.jsonl'), 'utf8')).trim().split('\n').map(JSON.parse)
    assert.equal(reads, 4)
    assert.deepEqual(rows[0].mappings, [[`tcp:${port}`, `tcp:${port}`]], '不记录无关应用转发')
    assert.deepEqual(rows[0].missing, [])
    assert.deepEqual(rows[0].hostTCP, [{ port, connected: true }])
    assert.deepEqual(rows[1].missing, [port])
    assert.deepEqual(rows[1].hostTCP, [{ port, connected: true }], '映射丢失时宿主仍可接受新连接')
    assert.equal(rows[2].hostTCP[0].connected, false)
    assert.equal(rows[2].hostTCP[0].error, 'ECONNREFUSED')
    assert.equal(rows[3].phase, 'before-cleanup')
    assert.equal(reverse, '', '诊断不能自动修复映射掩盖故障')
  } finally {
    await trace.stop()
    if (server.listening) await new Promise((resolve) => server.close(resolve))
    await rm(root, { recursive: true, force: true })
  }
})

test('ADB 查询失败与宿主 TCP 状态同时保存，不冒充映射成功', async () => {
  const root = await mkdtemp(resolve(tmpdir(), 'mobile-transport-error-'))
  const trace = new AndroidTransportTrace({ deviceID: 'emulator-5554', ports: [1234],
    path: resolve(root, 'transport.jsonl'), readReverse: async () => { throw new Error('device offline') },
    probe: async () => ({ connected: true }) })
  try {
    await trace.capture('offline')
    const row = JSON.parse((await readFile(resolve(root, 'transport.jsonl'), 'utf8')).trim())
    assert.equal(row.adbError, 'device offline')
    assert.deepEqual(row.missing, [1234])
    assert.deepEqual(row.hostTCP, [{ port: 1234, connected: true }])
  } finally { await trace.stop(); await rm(root, { recursive: true, force: true }) }
})

test('慢采样期间跳过周期触发，清理只等待进行中的采样与最后显式采样', async () => {
  const root = await mkdtemp(resolve(tmpdir(), 'mobile-transport-slow-'))
  let reads = 0, release, sampleStarted
  const blocked = new Promise((resolve) => { release = resolve })
  const started = new Promise((resolve) => { sampleStarted = resolve })
  const trace = new AndroidTransportTrace({ deviceID: 'emulator-5554', ports: [1234],
    path: resolve(root, 'transport.jsonl'), intervalMs: 5, probe: async () => ({ connected: true }),
    readReverse: async () => {
      reads++
      if (reads === 2) { sampleStarted(); await blocked }
      return 'host-1 tcp:1234 tcp:1234'
    } })
  try {
    await trace.start()
    await started
    await new Promise((resolve) => setTimeout(resolve, 50))
    assert.equal(reads, 2)
    assert.equal(trace.queued, 1, '不能随定时器累积待运行采样')
    const stopping = trace.stop()
    release()
    await stopping
    assert.equal(reads, 3, '清理只追加一次明确的最终采样')
    assert.equal(trace.queued, 0)
  } finally { release(); await trace.stop(); await rm(root, { recursive: true, force: true }) }
})
