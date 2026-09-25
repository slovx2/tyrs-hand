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
  child.once('exit', (code, signal) => resolve(code ?? (signal === 'SIGINT' ? 130 : 143)))
})
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => child.kill(signal))
const sockets = new Set()
let server
const evidenceFailure = (error) => {
  const failure = { timestamp: new Date().toISOString(), engine: config.engine, error: String(error) }
  appendFileSync(config.trace + '.errors', JSON.stringify(failure) + '\n', { mode: 0o600 })
  process.stderr.write('协议证据记录失败：' + String(error) + '\n')
  child.kill('SIGTERM')
  process.exitCode = 1
}
try {
  if (socketPath) {
    const { default: WebSocket } = await import(pathToFileURL(config.wsModule))
    const { Receiver } = WebSocket
    let sequence = 0
    const observe = (stream, direction, connection) => {
      let handshake = Buffer.alloc(0), upgraded = false
      const receiver = new Receiver({ isServer: direction === 'request' })
      receiver.on('message', (bytes) => {
        try {
          const message = JSON.parse(bytes.toString())
          appendFileSync(config.trace, JSON.stringify({ timestamp: new Date().toISOString(),
            engine: config.engine, connection, direction, message }) + '\n', { mode: 0o600 })
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
      stream.on('close', () => receiver.destroy())
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
      const outgoing = createConnection(nativePath)
      const connection = process.pid + ':' + ++sequence
      sockets.add(incoming); sockets.add(outgoing)
      observe(incoming, 'request', connection)
      observe(outgoing, 'response', connection)
      incoming.on('error', () => outgoing.destroy())
      outgoing.on('error', () => incoming.destroy())
      incoming.on('close', () => { sockets.delete(incoming); outgoing.destroy() })
      outgoing.on('close', () => { sockets.delete(outgoing); incoming.destroy() })
      incoming.pipe(outgoing).pipe(incoming)
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
