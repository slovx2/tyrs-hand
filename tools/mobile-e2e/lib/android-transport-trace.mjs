import { execFile } from 'node:child_process'
import { appendFile } from 'node:fs/promises'
import { createConnection } from 'node:net'
import { promisify } from 'node:util'

const exec = promisify(execFile)

export function probeTCP(port, timeoutMs = 1000) {
  return new Promise((resolve) => {
    const socket = createConnection({ host: '127.0.0.1', port })
    let finished = false
    const finish = (value) => {
      if (finished) return
      finished = true
      socket.setTimeout(0)
      socket.destroy()
      resolve(value)
    }
    socket.once('connect', () => finish({ connected: true }))
    socket.once('error', (error) => finish({ connected: false, error: error.code }))
    socket.setTimeout(timeoutMs, () => finish({ connected: false, error: 'TIMEOUT' }))
  })
}

// 只读观察：不恢复转发、不重连业务会话，也不把探针连接算 SSH 验收。
export class AndroidTransportTrace {
  constructor({ deviceID, ports, path, intervalMs = 2000,
    readReverse = async () => (await exec('adb', ['-s', deviceID, 'reverse', '--list'], { timeout: 3000 })).stdout,
    probe = probeTCP }) {
    Object.assign(this, { deviceID, ports: [...new Set(ports)], path, intervalMs, readReverse, probe })
    this.name = 'android-transport-trace'
    this.pending = Promise.resolve()
    this.stopped = false
    this.errors = []
    this.queued = 0
  }

  async start() {
    await this.capture('start')
    this.timer = setInterval(() => {
      if (this.queued > 0) return
      void this.capture('running').catch((error) => { this.errors.push(error.message) })
    }, this.intervalMs)
    return this
  }

  capture(phase) {
    if (this.stopped) return this.pending
    this.queued++
    const next = this.pending.then(async () => {
      const [reverse, connections] = await Promise.all([
        this.readReverse().then((text) => ({ rows: text.trim().split('\n').filter(Boolean)
          .map((line) => line.trim().split(/\s+/).slice(-2)) }),
        (error) => ({ error: error.message })),
        Promise.all(this.ports.map(async (port) => ({ port, ...await this.probe(port) }))),
      ])
      const expected = this.ports.map((port) => `tcp:${port}`)
      const mappings = reverse.rows?.filter(([source]) => expected.includes(source)) ?? []
      const missing = this.ports.filter((port) => !mappings.some(([source, target]) =>
        source === `tcp:${port}` && target === `tcp:${port}`))
      await appendFile(this.path, JSON.stringify({ at: new Date().toISOString(), phase,
        deviceID: this.deviceID, mappings, missing, adbError: reverse.error, hostTCP: connections }) + '\n')
    }).finally(() => { this.queued-- })
    this.pending = next.catch(() => {})
    return next
  }

  async stop() {
    clearInterval(this.timer)
    await this.capture('before-cleanup')
    this.stopped = true
    await this.pending
    if (this.errors.length) throw new AggregateError(this.errors.map((message) => new Error(message)),
      'Android 转发诊断未完整保存')
  }
}
