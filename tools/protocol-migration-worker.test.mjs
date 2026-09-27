import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { lstat, mkdtemp, mkdir, readFile, rm, stat, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { test } from 'node:test'
import { MigrationWorker } from './protocol-migration-worker.mjs'
import { startProcess } from './mobile-e2e/lib/process.mjs'
import { until } from './protocol-migration-infra.mjs'

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

test('Codex录制器符号链接只在监听关闭后移除入口，保留目标并拒绝普通文件', { timeout: 10_000 }, async t => {
  const root = await mkdtemp(resolve(tmpdir(), 'tyrs-socket-alias-'))
  const worker = new MigrationWorker({ root })
  worker.ports = { codex: 1, 'claude-code': 2 }
  await mkdir(worker.state, { recursive: true })
  const alias = resolve(worker.state, 'app-server.sock.native')
  const physical = resolve(root, 'physical.sock')
  const script = resolve(root, 'socket.mjs')
  await writeFile(script, 'import {createServer} from "node:net";createServer(c=>c.end()).listen(' +
    JSON.stringify(physical) + ',()=>console.log("ready"));')
  const child = spawn(process.execPath, [script], { stdio: ['ignore', 'pipe', 'pipe'] })
  try {
    await once(child.stdout, 'data', { signal: t.signal })
    await symlink(physical, alias)
    await assert.rejects(worker.clearInstrumentationSockets('new'), /仍有监听进程/)
    assert.ok((await lstat(alias)).isSymbolicLink())
    const exited = once(child, 'exit', { signal: t.signal })
    child.kill('SIGKILL')
    await exited
    await worker.clearInstrumentationSockets('new')
    await assert.rejects(lstat(alias), { code: 'ENOENT' })
    assert.ok((await stat(physical)).isSocket(), '不得代替原生 CLI 删除物理 socket')
    assert.deepEqual(worker.instrumentationCleanups, [{ generation: 'new', engine: 'codex',
      socketKind: 'runtime-recorder', staleSocketRemoved: true }])
    await rm(physical)
    await writeFile(physical, '保留真实文件')
    await symlink(physical, alias)
    await assert.rejects(worker.clearInstrumentationSockets('new'), /不能指向普通文件/)
    assert.equal(await readFile(physical, 'utf8'), '保留真实文件')
    assert.ok((await lstat(alias)).isSymbolicLink())
    await rm(physical)
    await worker.clearInstrumentationSockets('new')
    await assert.rejects(lstat(alias), { code: 'ENOENT' })
  } finally {
    if (child.exitCode === null && child.signalCode === null) {
      const exited = once(child, 'exit')
      child.kill('SIGKILL')
      await exited
    }
    await rm(root, { recursive: true, force: true })
  }
})

test('Worker崩溃等待真实后代退出后再清理Codex socket入口', { timeout: 15_000 }, async () => {
  const root = await mkdtemp(resolve(tmpdir(), 'tyrs-crash-tree-'))
  const worker = new MigrationWorker({ root })
  await mkdir(worker.state, { recursive: true })
  const alias = resolve(worker.state, 'app-server.sock.native')
  const physical = resolve(root, 'physical.sock')
  const childFile = resolve(root, 'socket-child.mjs')
  const parentFile = resolve(root, 'socket-parent.mjs')
  await writeFile(childFile, 'import {createServer} from "node:net";import {symlinkSync} from "node:fs";' +
    'createServer(c=>c.end()).listen(' + JSON.stringify(physical) + ',()=>{' +
    'symlinkSync(' + JSON.stringify(physical) + ',' + JSON.stringify(alias) + ');});')
  await writeFile(parentFile, 'import {spawn} from "node:child_process";' +
    'spawn(process.execPath,[' + JSON.stringify(childFile) + '],{stdio:"inherit"});')
  try {
    for (let attempt = 0; attempt < 3; attempt++) {
      worker.process = await startProcess('crash-' + attempt, process.execPath, [parentFile], {
        cwd: root, env: {}, logDir: resolve(root, 'logs'),
      })
      worker.processes.push(worker.process)
      await until('真实子进程 socket 就绪', async () => (await stat(alias)).isSocket(), 5000)
      const crash = await worker.crash()
      assert.equal(crash.signal, 'SIGKILL')
      assert.ok(crash.processCount >= 2, '必须包含父进程及真实监听子进程')
      assert.equal(crash.instrumentationSocketRemoved, true)
      await assert.rejects(lstat(alias), { code: 'ENOENT' })
      assert.ok((await stat(physical)).isSocket(), '物理 socket 留给原生 CLI 回收')
      await rm(physical)
    }
  } finally {
    await worker.close()
    await rm(root, { recursive: true, force: true })
  }
})
