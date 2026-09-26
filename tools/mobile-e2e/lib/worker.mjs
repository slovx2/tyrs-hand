import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { createReadStream } from 'node:fs'
import { execFile } from 'node:child_process'
import { mkdir, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { promisify } from 'node:util'
import { freePort, output, run, startProcess } from './process.mjs'
import { relay } from './network.mjs'

const exec = promisify(execFile)
const engines = ['codex', 'claude-code']
const sandboxPolicy = '(version 1)(allow default)(deny network-outbound)' +
  '(allow network-outbound (remote ip "localhost:*") (remote unix-socket))'

export class WorkerHarness {
  constructor({ repoRoot, runDir, control, registration, modelURLs }) {
    Object.assign(this, { repoRoot, runDir, control, registration, modelURLs })
    this.relays = []
    this.runtimes = {}
    this.ports = {}
  }

  async start() {
    this.pin = JSON.parse(await readFile(resolve(this.repoRoot, 'protocol/adapter-lock.json')))
    assert.equal(process.versions.node, this.pin.node, '必须使用固定 Node')
    this.adapter = resolve(process.env.TYRS_HAND_ADAPTER_ROOT ?? resolve(this.repoRoot, '../claude-codex'))
    assert.equal(output('git', ['rev-parse', 'HEAD'], { cwd: this.adapter }), this.pin.commit)
    assert.equal(output('git', ['status', '--porcelain'], { cwd: this.adapter }), '', '适配器必须使用已提交版本')
    this.codex = process.env.TYRS_HAND_TEST_CODEX_BIN ?? 'codex'
    assert.equal(output(this.codex, ['--version']), `codex-cli ${this.pin.codexProtocol}`)
    run('npm', ['run', 'build'], { cwd: this.adapter })
    await mkdir(this.runDir, { recursive: true })
    this.binary = resolve(this.runDir, 'tyrs-hand-worker')
    run('go', ['build', '-o', this.binary, './cmd/tyrs-hand-worker'], { cwd: this.repoRoot })
    // 短路径避免 macOS Unix Socket 的路径长度上限。
    // 手机目录选择返回真实路径，Control 的项目登记必须使用同一身份。
    this.root = await realpath(await mkdtemp('/tmp/000-tyrs-mobile-'))
    this.workspace = resolve(this.root, 'project')
    this.home = resolve(this.root, 'home')
    this.state = resolve(this.root, 'state')
    const codexHome = resolve(this.root, 'codex')
    const claudeConfig = resolve(this.state, 'claude-code/config/claude')
    for (const path of [this.workspace, this.home, codexHome, claudeConfig,
      resolve(this.root, 'tmp'), resolve(this.state, 'claude-code/ssh')]) {
      await mkdir(path, { recursive: true, mode: 0o700 })
    }
    const sdkRoot = resolve(this.adapter, 'node_modules/@anthropic-ai/claude-agent-sdk')
    const sdkVersion = JSON.parse(await readFile(resolve(sdkRoot, 'package.json'))).version
    assert.equal(sdkVersion, this.pin.claudeAgentSdk)
    const nativeCLI = resolve(this.adapter, 'node_modules',
      `@anthropic-ai/claude-agent-sdk-${process.platform}-${process.arch}`, 'claude')
    const nativeVersion = (await exec(nativeCLI, ['--version'], { env: {
      HOME: this.home, PATH: process.env.PATH, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1',
    }, timeout: 10_000 })).stdout.trim()
    assert.equal(nativeVersion, this.pin.claudeCli + ' (Claude Code)')
    const hash = createHash('sha256')
    for await (const chunk of createReadStream(nativeCLI)) hash.update(chunk)
    this.nativeBuild = { sdkVersion, cliVersion: nativeVersion, cliSHA256: hash.digest('hex') }
    run('git', ['init', '--quiet', this.workspace])
    await writeFile(resolve(this.workspace, 'README.md'), '真实移动端双引擎验收项目\n')
    this.clientKey = resolve(this.root, 'client-key')
    run('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', this.clientKey])
    this.privateKey = await readFile(this.clientKey, 'utf8')
    await writeFile(resolve(this.root, 'authorized_keys'), await readFile(this.clientKey + '.pub'), { mode: 0o600 })
    const hostKeys = { codex: resolve(this.root, 'host-key'),
      'claude-code': resolve(this.state, 'claude-code/ssh/host_key') }
    let knownHosts = ''
    for (const engine of engines) {
      this.ports[engine] = await freePort()
      for (let attempt = 0; ; attempt++) {
        assert.ok(attempt < 64, '未生成包含加号的测试指纹')
        run('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', hostKeys[engine]])
        const fingerprint = output('ssh-keygen', ['-lf', hostKeys[engine] + '.pub']).split(' ')[1]
        // 真实 GUI 每次都覆盖 %2B，避免随机指纹掩盖路由重复解码。
        if (engine === 'codex' || fingerprint.includes('+')) break
        await rm(hostKeys[engine]); await rm(hostKeys[engine] + '.pub')
      }
      knownHosts += `[127.0.0.1]:${this.ports[engine]} ${await readFile(hostKeys[engine] + '.pub', 'utf8')}`
    }
    this.knownHosts = resolve(this.root, 'known_hosts')
    await writeFile(this.knownHosts, knownHosts, { mode: 0o600 })
    await writeFile(resolve(codexHome, 'config.toml'), `model="mock-model"
model_provider="mock"
approval_policy="never"
[model_providers.mock]
name="Mock"
base_url=${JSON.stringify(this.modelURLs.codex + '/v1')}
wire_api="responses"
supports_websockets=false
request_max_retries=0
stream_max_retries=0
`, { mode: 0o600 })
    await writeFile(resolve(claudeConfig, 'settings.json'), JSON.stringify({ model: 'mock-claude', env: {
      ANTHROPIC_API_KEY: 'mock-only', ANTHROPIC_BASE_URL: this.modelURLs['claude-code'],
      CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1',
    } }), { mode: 0o600 })
    // 使用真实适配器持久配置；Claude 的环境白名单仍保持不变。
    await writeFile(resolve(this.state, 'claude-code/config.json'), JSON.stringify({ overrides: {
      mcp_servers: { mobile_fixture: { command: process.execPath,
        args: [resolve(this.repoRoot, 'tools/mobile-e2e/fixtures/mcp-server.mjs'), this.adapter, this.workspace],
        startup_timeout_sec: 30, tool_timeout_sec: 120 } },
    } }), { mode: 0o600 })
    const binaries = { codex: this.codex, 'claude-code': resolve(this.adapter, 'scripts/worker-runtime') }
    const quote = (value) => "'" + value.replaceAll("'", "'\\''") + "'"
    for (const engine of engines) {
      const configFile = resolve(this.root, engine + '-recorder.json')
      await writeFile(configFile, JSON.stringify({ engine, binary: binaries[engine],
        wsModule: resolve(this.adapter, 'node_modules/ws/index.js'),
        trace: resolve(this.runDir, 'wire-' + engine + '.jsonl') }), { mode: 0o600 })
      const wrapper = resolve(this.root, engine + '-cli')
      await writeFile(wrapper, '#!/bin/sh\nexec ' + quote(process.execPath) + ' ' +
        quote(resolve(this.repoRoot, 'tools/mobile-e2e/lib/record-runtime.mjs')) + ' ' +
        quote(configFile) + ' "$@"\n', { mode: 0o700 })
      binaries[engine] = wrapper
    }
    const env = { PATH: process.env.PATH, HOME: this.home, TMPDIR: resolve(this.root, 'tmp'),
      USER: 'mobile-e2e', LOGNAME: 'mobile-e2e', LANG: 'en_US.UTF-8',
      TYRS_HAND_WORKER_ID: this.registration.worker.id, TYRS_HAND_WORKER_ROLE: 'discord',
      TYRS_HAND_WORKER_MAX_CONCURRENT_JOBS: '2', TYRS_HAND_WORKER_HOME: this.home,
      TYRS_HAND_WORKER_CODEX_HOME: codexHome, TYRS_HAND_WORKER_DATA_ROOT: this.state,
      TYRS_HAND_WORKER_WORKSPACE_ROOT: this.workspace, TYRS_HAND_WORKER_SHELL: '/bin/sh',
      TYRS_HAND_WORKER_CREDENTIAL_FILE: resolve(this.root, 'credential'),
      TYRS_HAND_WORKER_ENROLLMENT_TOKEN: this.registration.enrollmentToken,
      TYRS_HAND_WORKER_AUTHORIZED_KEYS_FILE: resolve(this.root, 'authorized_keys'),
      TYRS_HAND_WORKER_SSH_HOST_KEY_FILE: hostKeys.codex,
      TYRS_HAND_WORKER_SSH_LISTEN_ADDR: `127.0.0.1:${this.ports.codex}`,
      TYRS_HAND_WORKER_CLAUDE_SSH_LISTEN_ADDR: `127.0.0.1:${this.ports['claude-code']}`,
      TYRS_HAND_WORKER_CLAUDE_ENABLED: 'true',
      TYRS_HAND_WORKER_CLAUDE_BIN: binaries['claude-code'],
      TYRS_HAND_CODEX_BIN: binaries.codex, TYRS_HAND_WORKER_CONTROL_URL: this.control.baseURL,
      TYRS_HAND_WORKER_GLOBAL_ENV_FILE: resolve(this.root, 'codex.env'),
      TYRS_HAND_WORKER_ENV_FILE: resolve(this.root, 'worker.env'),
      TYRS_HAND_SSH_AGENT_DIR: resolve(this.root, 'ssh-agent'),
      TYRS_HAND_HEARTBEAT_INTERVAL: '1s', TYRS_HAND_NODE_HEARTBEAT_INTERVAL: '1s',
      TYRS_HAND_WORKER_CLAIM_FALLBACK_INTERVAL: '200ms', TYRS_HAND_WORKER_SYNC_FALLBACK_INTERVAL: '1s',
      TYRS_HAND_CONTROL_TIMEOUT: '5s', TYRS_HAND_TURN_IDLE_TIMEOUT: '1m', TYRS_HAND_TURN_MAX_DURATION: '2m',
    }
    let command, args
    if (process.platform === 'darwin') {
      command = '/usr/bin/sandbox-exec'; args = ['-p', sandboxPolicy, this.binary]
    } else if (process.platform === 'linux') {
      const urls = [this.control.baseURL, ...Object.values(this.modelURLs)]
      const outboundPorts = urls.map((url) => Number(new URL(url).port))
      for (const port of outboundPorts) {
        this.relays.push(await relay(resolve(this.root, `out-${port}.sock`), { host: '127.0.0.1', port }))
      }
      for (const port of Object.values(this.ports)) {
        this.relays.push(await relay({ host: '127.0.0.1', port }, { path: resolve(this.root, `ssh-${port}.sock`) }))
      }
      const configFile = resolve(this.root, 'network.json')
      await writeFile(configFile, JSON.stringify({ root: this.root, binary: this.binary,
        cwd: this.workspace, outboundPorts, sshPorts: Object.values(this.ports) }), { mode: 0o600 })
      command = 'unshare'
      args = ['--user', '--map-root-user', '--net', '/bin/sh', '-ec',
        'ip link set lo up; exec "$@"', 'mobile-worker', process.execPath,
        resolve(this.repoRoot, 'tools/mobile-e2e/lib/linux-worker.mjs'), configFile]
    } else throw new Error('真实双引擎移动测试仅支持 macOS 或 Linux')
    this.process = await startProcess('real-worker', command, args, {
      cwd: this.workspace, env, inheritEnv: false, logDir: resolve(this.runDir, 'logs'),
    })
    for (const engine of engines) {
      this.runtimes[engine] = await this.control.admin.waitForRuntime(this.registration.worker.id, engine)
      const identity = JSON.parse(await this.ssh(engine, 'tyrs-hand-worker runtime info'))
      assert.equal(identity.engine, engine)
      assert.equal(identity.workerId, this.registration.worker.id)
      assert.equal(this.runtimes[engine].sshListenAddress, `127.0.0.1:${this.ports[engine]}`)
    }
    assert.notEqual(this.runtimes.codex.sshHostKeyFingerprint, this.runtimes['claude-code'].sshHostKeyFingerprint)
    await writeFile(resolve(this.runDir, 'runtime-combination.json'), JSON.stringify({
      ...this.pin, workerCommit: output('git', ['rev-parse', 'HEAD'], { cwd: this.repoRoot }),
      nativeBuild: this.nativeBuild,
      workerDirty: !!output('git', ['status', '--porcelain'], { cwd: this.repoRoot }),
      runtimes: this.runtimes, networkIsolation: process.platform === 'darwin' ? 'sandbox-exec' : 'network-namespace',
    }, null, 2))
    return this
  }

  sshArguments(engine, command) {
    return ['-F', '/dev/null', '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes',
      '-o', 'StrictHostKeyChecking=yes', '-o', `UserKnownHostsFile=${this.knownHosts}`,
      '-o', 'ConnectTimeout=5', '-i', this.clientKey, '-p', String(this.ports[engine]),
      'developer@127.0.0.1', command]
  }

  async ssh(engine, command) {
    return (await exec('ssh', this.sshArguments(engine, command), { timeout: 15_000 })).stdout.trim()
  }

  async stop() {
    await this.process?.stop()
    for (const item of this.relays.reverse()) await item.close()
    if (this.root) await rm(this.root, { recursive: true, force: true })
  }
}
