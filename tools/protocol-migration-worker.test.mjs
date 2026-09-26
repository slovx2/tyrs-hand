import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, mkdir, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { test } from 'node:test'
import { MigrationWorker } from './protocol-migration-worker.mjs'

// 同一 network.relay 的 Unix socket 故障可在 macOS/Linux 都直接复现。
test('迁移夹具只清理SIGKILL遗留的SSH中继，拒绝活监听和普通文件', { timeout: 10_000 }, async t => {
  const root = await mkdtemp(resolve(tmpdir(), 'tyrs-relay-test-'))
  const worker = new MigrationWorker({ root })
  worker.ports = { codex: 1, 'claude-code': 2 }
  await mkdir(worker.state, { recursive: true })
  const path = resolve(root, 'ssh-1.sock')
  const script = resolve(root, 'relay.mjs')
  const module = new URL('./mobile-e2e/lib/network.mjs', import.meta.url).href
  await writeFile(script, `import {relay} from ${JSON.stringify(module)};await relay(${JSON.stringify(path)},{host:'127.0.0.1',port:9});console.log('ready');`)
  const children = []
  const launch = () => {
    const owned = spawn(process.execPath, [script], { stdio: ['ignore', 'pipe', 'pipe'] })
    children.push(owned)
    return owned
  }
  const observed = (target, event) => once(target, event, { signal: t.signal })
  let child
  try {
    child = launch()
    await observed(child.stdout, 'data')
    await assert.rejects(worker.clearInstrumentationSockets('new'), /仍有监听进程/)
    assert.ok((await stat(path)).isSocket())
    child.kill('SIGKILL')
    await observed(child, 'exit')
    const blocked = launch()
    let error = ''
    blocked.stderr.on('data', data => { error += data })
    const [code] = await observed(blocked, 'exit')
    assert.equal(code, 1)
    assert.match(error, /EADDRINUSE/)
    await worker.clearInstrumentationSockets('new')
    await assert.rejects(stat(path), { code: 'ENOENT' })
    assert.deepEqual(worker.instrumentationCleanups, [{ generation: 'new', engine: 'codex',
      socketKind: 'linux-ssh-relay', staleSocketRemoved: true }])
    child = launch()
    await observed(child.stdout, 'data')
    child.kill('SIGKILL')
    await observed(child, 'exit')
    await worker.clearInstrumentationSockets('new')
    await writeFile(path, '不可删除的普通文件')
    await assert.rejects(worker.clearInstrumentationSockets('new'))
    assert.equal(await readFile(path, 'utf8'), '不可删除的普通文件')
  } finally {
    for (const owned of children) if (owned.exitCode === null && owned.signalCode === null) {
      owned.kill('SIGKILL')
      await once(owned, 'exit')
    }
    await rm(root, { recursive: true, force: true })
  }
})
