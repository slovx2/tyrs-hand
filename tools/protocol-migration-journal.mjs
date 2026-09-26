import assert from 'node:assert/strict'
import { createServer, request } from 'node:http'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { sha256, until } from './protocol-migration-infra.mjs'

export class MigrationFaultProxy {
  constructor(target) {
    this.target = target
    this.armed = false
    this.runId = undefined
    this.upgradedSockets = new Set()
    this.rejected = { events: 0, complete: 0 }
    this.server = createServer((incoming, outgoing) => {
      const match = incoming.url.match(/^\/worker\/v1\/runs\/([a-f0-9-]+)\/(events|complete)$/)
      if (this.armed && match && (!this.runId || this.runId === match[1])) {
        this.runId ??= match[1]
        this.rejected[match[2]]++
        incoming.resume()
        outgoing.writeHead(503, { 'content-type': 'application/json' })
        outgoing.end(JSON.stringify({ error: 'migration-fixture-delivery-unavailable' }))
        return
      }
      const upstream = request(new URL(incoming.url, this.target), {
        method: incoming.method, headers: incoming.headers,
      }, response => { outgoing.writeHead(response.statusCode, response.headers); response.pipe(outgoing) })
      upstream.on('error', () => { if (!outgoing.headersSent) outgoing.writeHead(502); outgoing.end() })
      incoming.pipe(upstream)
    })
    this.server.on('upgrade', (incoming, client, head) => {
      const upstream = request(new URL(incoming.url, this.target), {
        method: incoming.method, headers: incoming.headers,
      })
      upstream.on('upgrade', (response, socket, upstreamHead) => {
        this.upgradedSockets.add(socket); this.upgradedSockets.add(client)
        socket.on('close', () => { this.upgradedSockets.delete(socket); client.destroy() })
        client.on('close', () => { this.upgradedSockets.delete(client); socket.destroy() })
        socket.on('error', () => client.destroy()); client.on('error', () => socket.destroy())
        const headers = []
        for (let index = 0; index < response.rawHeaders.length; index += 2) {
          headers.push(response.rawHeaders[index] + ': ' + response.rawHeaders[index + 1])
        }
        client.write(`HTTP/1.1 ${response.statusCode} ${response.statusMessage}\r\n${headers.join('\r\n')}\r\n\r\n`)
        if (upstreamHead.length) client.write(upstreamHead)
        if (head.length) socket.write(head)
        client.pipe(socket); socket.pipe(client)
      })
      upstream.on('response', () => client.destroy())
      upstream.on('error', () => client.destroy())
      upstream.end()
    })
  }

  async start() {
    await new Promise((resolve, reject) => {
      this.server.once('error', reject)
      this.server.listen(0, '127.0.0.1', resolve)
    })
    this.baseURL = `http://127.0.0.1:${this.server.address().port}`
    return this
  }

  async close() {
    for (const socket of this.upgradedSockets) socket.destroy()
    this.server.closeAllConnections()
    await new Promise((resolve, reject) => this.server.close(error => error ? reject(error) : resolve()))
  }
}

export async function pendingJournal(worker, proxy) {
  return until('旧 Worker 自然持久化待补报 journal', async () => {
    if (!proxy.runId || proxy.rejected.complete === 0) return undefined
    const path = resolve(worker.state, 'control-state/runs', proxy.runId + '.json')
    const bytes = await readFile(path)
    const value = JSON.parse(bytes)
    if (!value.result || !value.pendingEvents?.length || value.terminalDelivered) return undefined
    assert.equal(value.task.snapshot.runtime.engine, undefined, '必须取自真实旧版无engine journal')
    assert.equal(value.task.claimed.RunID, proxy.runId)
    return { path, bytes, value }
  })
}

export async function verifyMigratedJournal(previous) {
  const bytes = await readFile(previous.path)
  const backup = await readFile(previous.path + '.before-runtime-scope')
  assert.deepEqual(backup, previous.bytes, '迁移备份必须保留崩溃前原始 journal 字节')
  const migrated = JSON.parse(bytes)
  assert.equal(migrated.task.snapshot.runtime.engine, 'codex')
  assert.equal(migrated.task.claimed.RunID, previous.value.task.claimed.RunID)
  assert.deepEqual(migrated.result, previous.value.result)
  assert.deepEqual(migrated.pendingEvents, previous.value.pendingEvents)
  assert.equal(migrated.nextSequence, previous.value.nextSequence)
  assert.equal(Boolean(migrated.terminalDelivered), false)
  // 报告不写任务快照、系统指令、事件正文或任何凭据。
  return { runId: migrated.task.claimed.RunID, beforeSHA256: sha256(previous.bytes),
    backupSHA256: sha256(backup), afterSHA256: sha256(bytes), engine: 'codex',
    pendingEventCount: migrated.pendingEvents.length, nextSequence: migrated.nextSequence,
    hasResult: true, terminalDelivered: false }
}

export async function waitJournalDelivered(worker, previous) {
  return until('新 Worker 清除已补报 journal', async () => {
    try {
      const value = JSON.parse(await readFile(previous.path))
      return value.terminalDelivered && !value.pendingEvents?.length
    } catch (error) { if (error.code === 'ENOENT') return true; throw error }
  })
}
