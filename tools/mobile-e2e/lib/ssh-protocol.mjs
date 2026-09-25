import { spawn } from 'node:child_process'
import { createRequire } from 'node:module'
import { Duplex } from 'node:stream'
import { resolve } from 'node:path'

export class SSHProtocolClient {
  constructor(worker, engine, onRequest) {
    Object.assign(this, { worker, engine, onRequest })
    this.pending = new Map()
    this.events = []
    this.trace = []
    this.waiters = new Set()
    this.nextID = 0
  }

  async open() {
    const { WebSocket } = createRequire(resolve(this.worker.adapter, 'package.json'))('ws')
    this.process = spawn('ssh', this.worker.sshArguments(this.engine, 'codex app-server proxy'),
      { stdio: ['pipe', 'pipe', 'pipe'] })
    this.errors = ''
    this.process.stderr.on('data', (chunk) => { this.errors += chunk.toString() })
    const stream = Duplex.from({ readable: this.process.stdout, writable: this.process.stdin })
    this.socket = new WebSocket('ws://worker/', { createConnection: () => stream, handshakeTimeout: 10_000 })
    this.socket.on('message', (data) => this.handle(JSON.parse(data.toString())))
    this.socket.on('error', (error) => this.fail(error))
    this.socket.on('close', () => this.fail(new Error('SSH 协议连接关闭：' + this.errors)))
    await new Promise((resolve, reject) => {
      this.socket.once('open', resolve)
      this.socket.once('error', reject)
      this.process.once('error', reject)
    })
    await this.request('initialize', { clientInfo: { name: 'mobile-runtime-preflight', version: '1.0.0' },
      capabilities: { experimentalApi: true } })
    this.send({ method: 'initialized', params: {} })
    return this
  }

  send(message) {
    this.trace.push({ direction: 'client', ...message })
    this.socket.send(JSON.stringify(message))
  }

  handle(message) {
    this.trace.push({ direction: 'server', ...message })
    if ('id' in message && !message.method) {
      const entry = this.pending.get(message.id)
      if (!entry) return
      this.pending.delete(message.id)
      clearTimeout(entry.timer)
      if (message.error) entry.reject(new Error(JSON.stringify(message.error)))
      else entry.resolve(message.result)
      return
    }
    this.events.push(message)
    for (const waiter of this.waiters) waiter()
    if ('id' in message) {
      Promise.resolve().then(() => {
        if (!this.onRequest) throw new Error('未预期的服务端回调：' + message.method)
        return this.onRequest(message)
      }).then((result) => this.send({ id: message.id, result }), (error) => this.fail(error))
    }
  }

  fail(error) {
    this.failure ??= error
    for (const entry of this.pending.values()) { clearTimeout(entry.timer); entry.reject(error) }
    this.pending.clear()
    for (const waiter of this.waiters) waiter()
  }

  request(method, params = {}) {
    if (this.failure) return Promise.reject(this.failure)
    const id = ++this.nextID
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error('RPC 超时：' + method)) }, 30_000)
      this.pending.set(id, { resolve, reject, timer })
      this.send({ id, method, params })
    })
  }

  waitFor(method, predicate = () => true) {
    return new Promise((resolve, reject) => {
      const finish = (error, message) => {
        clearTimeout(timer); this.waiters.delete(check)
        if (error) reject(error); else resolve(message)
      }
      const check = () => {
        const message = this.events.find((item) => item.method === method && predicate(item.params))
        if (message) finish(null, message)
        else if (this.failure) finish(this.failure)
      }
      const timer = setTimeout(() => finish(new Error('事件超时：' + method)), 60_000)
      this.waiters.add(check)
      check()
    })
  }

  async close() {
    this.fail(new Error('测试连接结束'))
    this.socket?.terminate()
    if (this.process?.exitCode === null) this.process.kill('SIGTERM')
  }
}
