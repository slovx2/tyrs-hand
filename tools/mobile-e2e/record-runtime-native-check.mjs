import assert from 'node:assert/strict'
import { execFileSync, spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { createConnection } from 'node:net'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'
import test from 'node:test'

// 只验录制器生命周期：固定真实 Codex 原生程序，无 Worker/SSH，不能登记主验收链。
const native = process.env.TYRS_HAND_TEST_CODEX_NATIVE_BIN
const wsModule = resolve(process.env.TYRS_HAND_ADAPTER_ROOT ?? '../claude-codex', 'node_modules/ws/index.js')
const { default: WebSocket } = await import(pathToFileURL(wsModule))
async function until(probe) {
  const deadline = Date.now() + 5000
  while (Date.now() < deadline) { const result = await probe(); if (result) return result; await delay(10) }
  throw new Error('真实录制器状态等待超时')
}
async function rows(path) {
  try { return (await readFile(path, 'utf8')).trim().split('\n').filter(Boolean).map(JSON.parse) }
  catch (error) { if (error.code === 'ENOENT') return []; throw error }
}
for (const mode of ['drain', 'timeout', 'business']) {
  test(`真实 Codex 录制器关闭边界：${mode}`, { timeout: 12_000 }, async () => {
    assert.ok(native, '必须显式指定固定0.147.0原生二进制 TYRS_HAND_TEST_CODEX_NATIVE_BIN')
    const root = await mkdtemp('/tmp/tyrs-recorder-native-')
    assert.equal(execFileSync(native, ['--version'], { encoding: 'utf8', env: { HOME: root, PATH: process.env.PATH } }).trim(), 'codex-cli 0.147.0')
    const socketPath = resolve(root, 'runtime.sock'), trace = resolve(root, 'wire.jsonl')
    await writeFile(resolve(root, 'config.toml'), 'model="mock-model"\nmodel_provider="mock"\n[model_providers.mock]\nname="Mock"\nbase_url="http://127.0.0.1:9/v1"\nwire_api="responses"\n')
    await writeFile(resolve(root, 'recorder.json'), JSON.stringify({ engine: 'codex', binary: native, wsModule, trace }))
    const child = spawn(process.execPath, [resolve('tools/mobile-e2e/lib/record-runtime.mjs'),
      resolve(root, 'recorder.json'), 'app-server', '--listen', 'unix://' + socketPath], {
      env: { PATH: process.env.PATH, HOME: root, CODEX_HOME: root, TMPDIR: root }, stdio: ['ignore', 'pipe', 'pipe'],
    })
    const exited = once(child, 'exit')
    let diagnostic = ''
    child.stderr.on('data', bytes => { diagnostic += bytes.toString() })
    child.stdout.on('data', bytes => { diagnostic += bytes.toString() })
    let socket, nativePID, stopped = false
    try {
      nativePID = await until(async () => (await rows(trace + '.lifecycle.jsonl')).find(row => row.event === 'native-started')?.pid)
      await until(async () => {
        try { const stream = createConnection(socketPath); await once(stream, 'connect'); stream.destroy(); return true }
        catch { return false }
      })
      socket = new WebSocket('ws://localhost/', { perMessageDeflate: false, createConnection: () => createConnection(socketPath) })
      await once(socket, 'open')
      const initialized = once(socket, 'message')
      socket.send(JSON.stringify({ id: 1, method: 'initialize', params: { clientInfo: { name: 'recorder-check', version: '1' }, capabilities: { experimentalApi: true } } }))
      assert.equal(JSON.parse((await initialized)[0]).id, 1)
      socket.send(JSON.stringify({ method: 'initialized', params: {} }))
      socket.send(JSON.stringify({ id: 9, method: 'model/list', params: { limit: 100 } }))
      await until(async () => (await rows(trace)).some(row => row.direction === 'response' && row.message.id === 9))
      process.kill(nativePID, 'SIGSTOP'); stopped = true
      const method = mode === 'business' ? 'thread/start' : 'model/list'
      socket.send(JSON.stringify({ id: 2, method, params: method === 'model/list' ? { limit: 100 } : { cwd: root } }))
      await until(async () => (await rows(trace)).some(row => row.direction === 'request' && row.message.id === 2))
      socket.terminate()
      await until(async () => (await rows(trace + '.lifecycle.jsonl')).some(row => row.event === 'client-closed' && row.pending?.some(request => request.id === 2)))
      child.kill('SIGINT')
      if (mode === 'drain') {
        process.kill(nativePID, 'SIGCONT'); stopped = false
        await until(async () => (await rows(trace + '.lifecycle.jsonl')).some(row => row.event === 'native-response-after-client-close' && row.id === 2))
      } else {
        await until(async () => (await rows(trace + '.errors')).length)
        process.kill(nativePID, 'SIGCONT'); stopped = false
      }
      const [code] = await exited
      const recorded = await rows(trace)
      if (mode === 'drain') {
        assert.equal(code, 0)
        assert.equal((await rows(trace + '.errors')).length, 0)
        const actual = recorded.find(row => row.direction === 'response' && row.message.id === 2)
        assert.ok(actual.message.result.data.length > 0, '必须收到真实 CLI 的模型目录')
        assert.equal(actual.delivery, 'native-only-client-closed')
        const observed = (await rows(trace + '.lifecycle.jsonl')).find(row => row.event === 'native-response-after-client-close')
        assert.equal(observed.delivered, false)
        assert.equal(observed.method, 'model/list')
      } else {
        assert.equal(code, 1)
        assert.ok((await rows(trace + '.errors')).length > 0)
        assert.ok(!recorded.some(row => row.direction === 'response' && row.message.id === 2), '不得补造失败或业务成功响应')
      }
    } catch (error) {
      error.message += '\n临时无凭据 CLI 诊断：' + diagnostic.slice(-2500)
      error.message += '\n录制器生命周期：' + JSON.stringify(await rows(trace + '.lifecycle.jsonl'))
      error.message += '\n录制器错误：' + JSON.stringify(await rows(trace + '.errors'))
      throw error
    } finally {
      socket?.terminate()
      if (stopped) { try { process.kill(nativePID, 'SIGCONT') } catch {} }
      if (child.exitCode === null && child.signalCode === null) { child.kill('SIGTERM'); await exited }
      await rm(root, { recursive: true, force: true })
    }
  })
}
