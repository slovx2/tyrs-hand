import { spawn } from 'node:child_process'
import { appendFileSync, readFileSync } from 'node:fs'
import { access, rm } from 'node:fs/promises'
import { createConnection, createServer } from 'node:net'
import { pathToFileURL } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'

// 原样转发字节，仅用固定 ws 的 Receiver 旁路解码真实 CLI WebSocket。
const [configPath, ...args] = process.argv.slice(2)
const config = JSON.parse(readFileSync(configPath, 'utf8'))
const listenIndex = args.indexOf('--listen')
const socketPath = listenIndex >= 0 && args[listenIndex + 1]?.startsWith('unix://')
  ? args[listenIndex + 1].slice(7) : null
const nativePath = socketPath ? socketPath + '.native' : null
if (socketPath) args[listenIndex + 1] = 'unix://' + nativePath
const child = spawn(config.binary, args, { env: process.env, stdio: 'inherit' })
const stopped = new Promise((resolve) => {
  child.once('error', (error) => { process.stderr.write(String(error)); resolve(1) })
  child.once('exit', (code, signal) => {
    lifecycle({ event: 'native-exited', code, signal })
    resolve(code ?? (signal === 'SIGINT' ? 130 : 143))
  })
})
const sockets = new Set()
const connections = new Set()
let server
let stopping
const lifecycle = (event) => appendFileSync(config.trace + '.lifecycle.jsonl', JSON.stringify({
  timestamp: new Date().toISOString(), engine: config.engine, ...event,
}) + '\n', { mode: 0o600 })
lifecycle({ event: 'native-started', pid: child.pid })
const evidenceFailure = (error) => {
  const failure = { timestamp: new Date().toISOString(), engine: config.engine, error: String(error) }
  appendFileSync(config.trace + '.errors', JSON.stringify(failure) + '\n', { mode: 0o600 })
  process.stderr.write('协议证据记录失败：' + String(error) + '\n')
  child.kill('SIGTERM')
  process.exitCode = 1
}
const drainTimeout = 1500
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => {
  stopping ??= (async () => {
    // Worker 已关闭客户端时，目录响应仍可能在真实 CLI 中。只收齐原生侧证据，
    // 不把未交付下游的响应当作客户端成功，也不延后执行中的业务工具/审批。
    if ([...connections].some(item => [...item.pending.values()].some(request => request.method !== 'model/list'))) {
      evidenceFailure('停止时存在未完成的业务请求，禁止作为目录收尾处理')
      child.kill(signal)
      return
    }
    const deadline = Date.now() + drainTimeout
    while ([...connections].some(item => item.pending.size) && Date.now() < deadline) await delay(5)
    for (const item of connections) {
      if (item.pending.size) evidenceFailure('停止时原生请求未完成：' + JSON.stringify([...item.pending.values()]))
    }
    child.kill(signal)
  })()
})
try {
  if (socketPath) {
    const { default: WebSocket } = await import(pathToFileURL(config.wsModule))
    const { Receiver } = WebSocket
    let sequence = 0
    const observe = (stream, direction, state) => {
      let handshake = Buffer.alloc(0), upgraded = false
      const receiver = new Receiver({ isServer: direction === 'request' })
      receiver.on('message', (bytes) => {
        try {
          const message = JSON.parse(bytes.toString())
          const key = JSON.stringify(message.id)
          if (direction === 'request' && message.method && message.id !== undefined) {
            state.pending.set(key, { id: message.id, method: message.method })
          }
          if (direction === 'response' && !message.method && message.id !== undefined) {
            const request = state.pending.get(key)
            state.pending.delete(key)
            if (state.clientClosed) lifecycle({ event: 'native-response-after-client-close',
              connection: state.connection, id: message.id, method: request?.method, delivered: false })
          }
          appendFileSync(config.trace, JSON.stringify({ timestamp: new Date().toISOString(),
            engine: config.engine, connection: state.connection, direction,
            ...(direction === 'response' && state.clientClosed ? { delivery: 'native-only-client-closed' } : {}),
            message }) + '\n', { mode: 0o600 })
          if (state.clientClosed && !state.pending.size) state.finish()
        } catch (error) {
          evidenceFailure(error)
        }
      })
      receiver.on('error', evidenceFailure)
      stream.on('data', (bytes) => {
        // Receiver 原地解除掩码，必须使用副本，不能污染转发给真实 CLI 的字节。
        if (upgraded) { receiver.write(Buffer.from(bytes)); return }
        handshake = Buffer.concat([handshake, bytes])
        const boundary = handshake.indexOf('\r\n\r\n')
        if (boundary < 0) return
        upgraded = true
        receiver.write(handshake.subarray(boundary + 4))
        handshake = Buffer.alloc(0)
      })
      stream.on('close', () => receiver.end())
    }
    const deadline = Date.now() + 15000
    while (true) {
      try { await access(nativePath); break } catch { /* 等待真实 CLI 创建 Unix Socket */ }
      if (child.exitCode !== null || child.signalCode !== null || Date.now() > deadline) {
        throw new Error('原生 CLI 未能创建 Unix Socket')
      }
      await delay(25)
    }
    server = createServer((incoming) => {
      if (stopping) { incoming.destroy(); return }
      const outgoing = createConnection(nativePath)
      const connection = process.pid + ':' + ++sequence
      const state = { connection, pending: new Map(), clientClosed: false, timer: null,
        finish() { clearTimeout(this.timer); outgoing.destroy() } }
      connections.add(state)
      sockets.add(incoming); sockets.add(outgoing)
      observe(incoming, 'request', state)
      observe(outgoing, 'response', state)
      incoming.on('error', () => incoming.destroy())
      outgoing.on('error', () => incoming.destroy())
      incoming.on('close', () => {
        sockets.delete(incoming)
        state.clientClosed = true
        // pipe 在目标关闭时会暂停源；下游已离开后仍需消费真实 CLI 的收尾响应。
        outgoing.unpipe(incoming)
        outgoing.resume()
        lifecycle({ event: 'client-closed', connection, pending: [...state.pending.values()] })
        if (!state.pending.size) { state.finish(); return }
        if ([...state.pending.values()].some(request => request.method !== 'model/list')) {
          evidenceFailure('客户端关闭时业务请求未完成：' + JSON.stringify([...state.pending.values()]))
          state.finish(); return
        }
        state.timer = setTimeout(() => {
          evidenceFailure('客户端关闭后原生目录响应超时：' + JSON.stringify([...state.pending.values()]))
          state.finish()
        }, drainTimeout)
      })
      outgoing.on('close', () => {
        clearTimeout(state.timer)
        connections.delete(state)
        sockets.delete(outgoing); incoming.destroy()
      })
      incoming.pipe(outgoing, { end: false })
      outgoing.pipe(incoming, { end: false })
    })
    await new Promise((resolve, reject) => {
      server.once('error', reject)
      server.listen(socketPath, resolve)
    })
  }
  const code = await stopped
  process.exitCode ||= code
} finally {
  for (const socket of sockets) socket.destroy()
  if (server) await new Promise((resolve) => server.close(resolve))
  if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM')
  if (socketPath) await rm(socketPath, { force: true })
}
