import assert from 'node:assert/strict'
import { createPublicKey } from 'node:crypto'
import { execFile } from 'node:child_process'
import { lstat, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { promisify } from 'node:util'
import { createConnection } from 'node:net'
import { freePort, output, startProcess } from './mobile-e2e/lib/process.mjs'
import { relay } from './mobile-e2e/lib/network.mjs'
import { sha256, until } from './protocol-migration-infra.mjs'

const exec = promisify(execFile)
const quote = value => "'" + value.replaceAll("'", "'\\''") + "'"
const sandbox = '(version 1)(allow default)(deny network-outbound)' +
  '(allow network-outbound (remote ip "localhost:*") (remote unix-socket))'

function hostPublicKey(privateKey) {
  // 真实 Worker 生成 PKCS8 Ed25519；导出公钥，不转换或重写持久 Host Key。
  const jwk = createPublicKey(privateKey).export({ format: 'jwk' })
  assert.equal(jwk.crv, 'Ed25519')
  const fields = [Buffer.from('ssh-ed25519'), Buffer.from(jwk.x, 'base64url')]
  const encoded = Buffer.concat(fields.flatMap(field => {
    const size = Buffer.alloc(4); size.writeUInt32BE(field.length)
    return [size, field]
  }))
  return 'ssh-ed25519 ' + encoded.toString('base64')
}

export class MigrationWorker {
  constructor({ root, repo, adapter, binaries, control, models, evidence }) {
    Object.assign(this, { root, repo, adapter, binaries, control, models, evidence })
    this.processes = []; this.relays = []
    this.instrumentationCleanups = []
    this.home = resolve(root, 'home')
    this.state = resolve(root, 'state')
    this.workspace = resolve(root, 'project')
    this.codexHome = resolve(root, 'codex')
    this.credential = resolve(root, 'credential')
    this.keys = { codex: resolve(this.state, 'ssh/host_key'),
      'claude-code': resolve(this.state, 'claude-code/ssh/host_key') }
    this.knownHosts = resolve(root, 'known_hosts')
    this.clientKey = resolve(root, 'client-key')
  }

  async prepare() {
    this.ports = { codex: await freePort(), 'claude-code': await freePort() }
    for (const path of [this.home, this.workspace, this.codexHome, resolve(this.root, 'tmp')]) {
      await mkdir(path, { recursive: true, mode: 0o700 })
    }
    const nativeCLI = resolve(this.adapter, 'node_modules',
      `@anthropic-ai/claude-agent-sdk-${process.platform}-${process.arch}`, 'claude')
    const nativeVersion = (await exec(nativeCLI, ['--version'], { env: { HOME: this.home,
      PATH: process.env.PATH, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1' }, timeout: 10_000 })).stdout.trim()
    assert.equal(nativeVersion, '2.1.282 (Claude Code)')
    this.nativeBuild = { version: nativeVersion, sha256: sha256(await readFile(nativeCLI)) }
    output('git', ['init', '--quiet', this.workspace])
    await writeFile(resolve(this.workspace, 'README.md'), '真实 Worker 32 → 33 迁移验收\n')
    output('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', this.clientKey])
    await writeFile(resolve(this.root, 'authorized_keys'), await readFile(this.clientKey + '.pub'), { mode: 0o600 })
    await writeFile(resolve(this.codexHome, 'config.toml'), `model="mock-model"
model_provider="mock"
approval_policy="never"
sandbox_mode="danger-full-access"
[model_providers.mock]
name="Mock"
base_url=${JSON.stringify(this.models.urls.codex + '/v1')}
wire_api="responses"
supports_websockets=false
request_max_retries=0
stream_max_retries=0
`, { mode: 0o600 })
    const real = { codex: process.env.TYRS_HAND_TEST_CODEX_BIN ?? 'codex',
      'claude-code': resolve(this.adapter, 'scripts/worker-runtime') }
    assert.equal(output(real.codex, ['--version']), 'codex-cli 0.147.0')
    this.runtimeBins = {}
    for (const engine of ['codex', 'claude-code']) {
      const config = resolve(this.root, `${engine}-recorder.json`)
      await writeFile(config, JSON.stringify({ engine, binary: real[engine],
        wsModule: resolve(this.adapter, 'node_modules/ws/index.js'),
        trace: resolve(this.evidence, `wire-${engine}.jsonl`) }), { mode: 0o600 })
      const wrapper = resolve(this.root, engine + '-cli')
      await writeFile(wrapper, '#!/bin/sh\nexec ' + quote(process.execPath) + ' ' +
        quote(resolve(this.repo, 'tools/mobile-e2e/lib/record-runtime.mjs')) + ' ' + quote(config) + ' "$@"\n',
      { mode: 0o700 })
      this.runtimeBins[engine] = wrapper
    }
    this.env = {
      PATH: process.env.PATH, HOME: this.home, TMPDIR: resolve(this.root, 'tmp'), LANG: 'en_US.UTF-8',
      USER: 'migration-fixture', LOGNAME: 'migration-fixture',
      TYRS_HAND_WORKER_ID: this.control.registration.worker.id, TYRS_HAND_WORKER_ROLE: 'discord',
      TYRS_HAND_WORKER_MAX_CONCURRENT_JOBS: '2', TYRS_HAND_WORKER_HOME: this.home,
      TYRS_HAND_WORKER_CODEX_HOME: this.codexHome, TYRS_HAND_WORKER_DATA_ROOT: this.state,
      TYRS_HAND_WORKER_WORKSPACE_ROOT: this.workspace, TYRS_HAND_WORKER_SHELL: '/bin/sh',
      TYRS_HAND_WORKER_CREDENTIAL_FILE: this.credential,
      TYRS_HAND_WORKER_AUTHORIZED_KEYS_FILE: resolve(this.root, 'authorized_keys'),
      TYRS_HAND_WORKER_SSH_HOST_KEY_FILE: this.keys.codex,
      TYRS_HAND_WORKER_SSH_LISTEN_ADDR: `127.0.0.1:${this.ports.codex}`,
      TYRS_HAND_WORKER_CONTROL_URL: this.control.workerURL ?? this.control.baseURL, TYRS_HAND_CODEX_BIN: this.runtimeBins.codex,
      TYRS_HAND_WORKER_GLOBAL_ENV_FILE: resolve(this.root, 'codex.env'),
      TYRS_HAND_WORKER_ENV_FILE: resolve(this.root, 'worker.env'),
      TYRS_HAND_SSH_AGENT_DIR: resolve(this.root, 'ssh-agent'),
      TYRS_HAND_HEARTBEAT_INTERVAL: '1s', TYRS_HAND_NODE_HEARTBEAT_INTERVAL: '1s',
      TYRS_HAND_WORKER_CLAIM_FALLBACK_INTERVAL: '200ms', TYRS_HAND_WORKER_SYNC_FALLBACK_INTERVAL: '1s',
      TYRS_HAND_CONTROL_TIMEOUT: '5s', TYRS_HAND_TURN_IDLE_TIMEOUT: '1m', TYRS_HAND_TURN_MAX_DURATION: '2m',
    }
    if (process.platform === 'linux') {
      this.outboundPorts = [this.control.workerURL ?? this.control.baseURL, ...Object.values(this.models.urls)].map(url => Number(new URL(url).port))
      for (const port of this.outboundPorts) this.relays.push(await relay(resolve(this.root, `out-${port}.sock`), { host: '127.0.0.1', port }))
      for (const port of Object.values(this.ports)) this.relays.push(await relay({ host: '127.0.0.1', port }, { path: resolve(this.root, `ssh-${port}.sock`) }))
    }
  }

  async start(generation) {
    if (this.process) {
      assert.ok(this.process.child.exitCode !== null || this.process.child.signalCode !== null, '上代 Worker 必须先停止')
      await this.clearInstrumentationSockets(generation)
    }
    const env = { ...this.env, TYRS_HAND_WORKER_PROTOCOL_VERSION: generation === 'old' ? '32' : '33' }
    if (generation === 'old') env.TYRS_HAND_WORKER_ENROLLMENT_TOKEN = this.control.registration.enrollmentToken
    else {
      const config = resolve(this.state, 'claude-code/config/claude')
      await mkdir(config, { recursive: true, mode: 0o700 })
      await writeFile(resolve(config, 'settings.json'), JSON.stringify({ model: 'mock-claude', env: {
        ANTHROPIC_API_KEY: 'mock-only', ANTHROPIC_BASE_URL: this.models.urls['claude-code'],
        CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1',
      } }), { mode: 0o600 })
      Object.assign(env, { TYRS_HAND_WORKER_CLAUDE_ENABLED: 'true',
        TYRS_HAND_WORKER_CLAUDE_BIN: this.runtimeBins['claude-code'],
        TYRS_HAND_WORKER_CLAUDE_SSH_LISTEN_ADDR: `127.0.0.1:${this.ports['claude-code']}` })
    }
    let command, args
    if (process.platform === 'darwin') {
      command = '/usr/bin/sandbox-exec'; args = ['-p', sandbox, this.binaries[generation].worker]
    } else if (process.platform === 'linux') {
      const config = resolve(this.root, 'network.json')
      await writeFile(config, JSON.stringify({ root: this.root, binary: this.binaries[generation].worker,
        cwd: this.workspace, outboundPorts: this.outboundPorts, sshPorts: Object.values(this.ports) }))
      command = 'unshare'; args = ['--user', '--map-root-user', '--net', '/bin/sh', '-ec',
        'ip link set lo up; exec "$@"', 'migration-worker', process.execPath,
        resolve(this.repo, 'tools/mobile-e2e/lib/linux-worker.mjs'), config]
    } else throw new Error('迁移验收仅支持 macOS/Linux')
    this.process = await startProcess(`${generation}-worker-${this.processes.length + 1}`, command, args, {
      cwd: this.workspace, env, inheritEnv: false, logDir: resolve(this.root, 'logs'),
    })
    this.processes.push(this.process)
    const engines = generation === 'old' ? ['codex'] : ['codex', 'claude-code']
    let known = ''
    for (const engine of engines) {
      await until(`${engine} 自然生成 Host Key`, async () => (await readFile(this.keys[engine])).length)
      const publicKey = hostPublicKey(await readFile(this.keys[engine]))
      known += `[127.0.0.1]:${this.ports[engine]} ${publicKey}\n`
    }
    await writeFile(this.knownHosts, known, { mode: 0o600 })
    await until(`${generation} 真实 SSH`, async () => (await this.ssh('codex', 'printf migration-ready')) === 'migration-ready')
    if (generation === 'new') {
      for (const engine of engines) {
        await this.control.admin.waitForRuntime(this.control.registration.worker.id, engine)
        // Control 可短暂保留重启前的 heartbeat；必须等本次真实 SSH 入口可用。
        const identity = await until(`${engine} 本次 SSH 运行时就绪`, async () => {
          const value = JSON.parse(await this.ssh(engine, 'tyrs-hand-worker runtime info'))
          return value.status === 'running' ? value : undefined
        })
        assert.equal(identity.workerId, this.control.registration.worker.id)
        assert.equal(identity.engine, engine)
      }
    }
  }

  sshArguments(engine, command) {
    return ['-F', '/dev/null', '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'StrictHostKeyChecking=yes',
      '-o', `UserKnownHostsFile=${this.knownHosts}`, '-o', 'ConnectTimeout=5', '-i', this.clientKey,
      '-p', String(this.ports[engine]), 'developer@127.0.0.1', command]
  }

  async ssh(engine, command) {
    const operation = exec('ssh', this.sshArguments(engine, command), { timeout: 15_000 })
    // 单次命令没有输入；旧 Worker 会等待 stdin EOF，不能让夹具留下开放输入管道。
    operation.child.stdin.end()
    return (await operation).stdout.trim()
  }

  async snapshot() {
    return { credentialSHA256: sha256(await readFile(this.credential)),
      authorizedKeysSHA256: sha256(await readFile(resolve(this.root, 'authorized_keys'))),
      codexHostKeySHA256: sha256(await readFile(this.keys.codex)) }
  }

  async clearInstrumentationSockets(generation) {
    for (const [engine, directory] of [['codex', this.state], ['claude-code', resolve(this.state, 'claude-code')]]) {
      const path = resolve(directory, 'app-server.sock.native')
      try { assert.ok((await lstat(path)).isSocket()) }
      catch (error) { if (error.code === 'ENOENT') continue; throw error }
      await new Promise((resolveProbe, reject) => {
        const socket = createConnection(path)
        socket.setTimeout(500)
        socket.once('connect', () => { socket.destroy(); reject(new Error('上代录制器原生 socket 仍有监听进程')) })
        socket.once('timeout', () => { socket.destroy(); reject(new Error('无法确认录制器原生 socket 已关闭')) })
        socket.once('error', error => {
          socket.destroy()
          if (['ECONNREFUSED', 'ENOENT'].includes(error.code)) resolveProbe()
          else reject(error)
        })
      })
      await rm(path, { force: true })
      this.instrumentationCleanups.push({ generation, engine, staleSocketRemoved: true })
    }
  }

  async crash() {
    const parent = this.process.child.pid
    assert.ok(parent && this.process.child.exitCode === null)
    const entries = output('ps', ['-axo', 'pid=,ppid=']).split('\n')
      .map(line => line.trim().split(/\s+/).map(Number))
    const owned = [parent]
    for (let index = 0; index < owned.length; index++) {
      for (const [pid, ppid] of entries) if (ppid === owned[index]) owned.push(pid)
    }
    // 仅对刚从此 fixture 父 PID 收集的树操作，不按进程名匹配。
    process.kill(parent, 'SIGKILL')
    for (const pid of owned.slice(1).reverse()) {
      try { process.kill(pid, 'SIGKILL') } catch (error) { if (error.code !== 'ESRCH') throw error }
    }
    await this.process.exit
    // Worker 会清理正式 app-server.sock；录制器额外引入的 .native 不在其管理范围。
    // 仅清理此 fixture 的旁路 socket，正式 socket 和全部会话状态留给真实 Worker 恢复。
    const instrumentationSocket = resolve(this.state, 'app-server.sock.native')
    assert.ok((await lstat(instrumentationSocket)).isSocket())
    await rm(instrumentationSocket)
    return { signal: 'SIGKILL', processCount: owned.length, instrumentationSocketRemoved: true }
  }

  async close() {
    for (const process of this.processes.reverse()) await process.stop()
    for (const item of this.relays.reverse()) await item.close()
  }
}
