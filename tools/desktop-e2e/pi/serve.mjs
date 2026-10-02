// Pi 桌面 GUI 验收环境：真实 Control、Worker（仅启用 Pi 入口，Codex 基础入口常驻、Claude 关闭）与回环 Mock 模型常驻，
// 供安装版 ChatGPT.app 经专用 SSH Host `tyrs-e2e-pi` 接入。Worker 使用临时 HOME 与 sandbox-exec 外连限制，
// Pi 只读取临时 agentDir 中指向 Mock 的 models.json，不读取个人模型配置。退出时核对模型终态、文件副作用与真实 wire。
import assert from 'node:assert/strict'
import { mkdir, readFile, realpath, rm, writeFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { ControlHarness } from '../../mobile-e2e/lib/control.mjs'
import { freePort, output, run, startProcess } from '../../mobile-e2e/lib/process.mjs'
import { validateRuntimeWire } from '../../mobile-e2e/lib/wire.mjs'
import { cleanupManaged, completionError } from '../../mobile-e2e/lib/cleanup.mjs'
import { startPiModel } from './models.mjs'
import { piAliases, piEffects, piModels, piScenarios, selectMarkers, steerPayload } from './scenarios.mjs'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const argumentsMap = new Map()
for (let index = 2; index < process.argv.length; index += 2) argumentsMap.set(process.argv[index], process.argv[index + 1])
const sshFragment = resolve(argumentsMap.get('--ssh-config') ?? resolve(homedir(), '.ssh/config.d/tyrs-desktop-e2e-pi'))
const alias = argumentsMap.get('--alias') ?? 'tyrs-e2e-pi'
const fixedRoot = argumentsMap.get('--root') ?? '/tmp/000-tyrs-desktop-pi'
const stamp = new Date().toISOString().replaceAll(':', '').replaceAll('.', '')
const runDir = resolve(repoRoot, '.local/e2e/evidence', `${stamp}-desktop-pi`)
const managed = { worker: [], models: [], controls: [] }
const quote = (value) => "'" + value.replaceAll("'", "'\\''") + "'"
const sandboxPolicy = '(version 1)(allow default)(deny network-outbound)' +
  '(allow network-outbound (remote ip "localhost:*") (remote unix-socket))'

// 停止、steer、换模与计划模式必须来自客户端的真实协议请求。
async function verifyPiWire(path, expected) {
  const rows = (await readFile(path, 'utf8')).trim().split('\n').map((line) => JSON.parse(line).message)
  // Desktop 会另开临时线程以 turnTrigger=thread_title 生成标题（Pi 明确不支持结构化输出而拒绝），断言只看对话回合。
  const userTurn = (message, marker) => message.method === 'turn/start' && message.params?.turnTrigger !== 'thread_title' &&
    JSON.stringify(message.params?.input ?? []).includes(marker)
  const threadOf = (marker) => rows.find((message) => userTurn(message, marker))?.params.threadId
  const threadRows = (threadId) => rows.filter((message) => message.params?.threadId === threadId)
  if (expected.includes('DESKTOP_PI_STOP')) {
    const threadId = threadOf('DESKTOP_PI_STOP')
    assert.ok(threadId, '缺少停止场景的回合')
    assert.ok(threadRows(threadId).some((message) => message.method === 'turn/interrupt'), '停止必须由客户端发出真实 turn/interrupt')
    assert.ok(threadRows(threadId).some((message) => message.method === 'turn/completed' &&
      message.params.turn.status === 'interrupted'), '停止后的回合必须以 interrupted 终结')
    assert.ok(!threadRows(threadId).some((message) => JSON.stringify(message.params).includes('DESKTOP_PI_STOP_TOO_LATE')),
      '停止后迟到的模型回复不能下发')
  }
  if (expected.includes('DESKTOP_PI_STEER')) {
    const threadId = threadOf('DESKTOP_PI_STEER')
    assert.ok(threadRows(threadId).some((message) => message.method === 'turn/steer' &&
      JSON.stringify(message.params.input).includes(steerPayload)), 'steer 必须由客户端发出真实 turn/steer')
  }
  if (expected.includes('DESKTOP_PI_PLAN')) {
    const start = rows.find((message) => userTurn(message, 'DESKTOP_PI_PLAN'))
    assert.equal(start?.params.collaborationMode?.mode, 'plan', '计划场景须由客户端以计划模式发起')
  }
}

async function startWorker({ control, registration, modelURL, adapter }) {
  await rm(fixedRoot, { recursive: true, force: true })
  await mkdir(fixedRoot, { recursive: true, mode: 0o700 })
  // 固定根目录：ChatGPT.app 中登记的测试项目路径（<root>/project）跨次运行保持有效；短路径避免 Unix Socket 长度上限。
  const root = await realpath(fixedRoot)
  const home = resolve(root, 'home'), workspace = resolve(root, 'project'), state = resolve(root, 'state')
  const agent = resolve(home, '.pi/agent'), codexHome = resolve(root, 'codex')
  for (const path of [agent, codexHome, workspace, resolve(root, 'tmp')]) await mkdir(path, { recursive: true, mode: 0o700 })
  await writeFile(resolve(workspace, 'README.md'), 'Pi 桌面验收项目\n')
  run('git', ['init', '--quiet', workspace])
  await writeFile(resolve(agent, 'models.json'), JSON.stringify({ providers: { desktop: { baseUrl: modelURL,
    api: 'openai-completions', apiKey: 'mock-only', models: piModels.map(({ id, name }) => ({ id, name, reasoning: true,
      input: ['text'], contextWindow: 64000, maxTokens: 2048, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } })) } } }))
  await writeFile(resolve(agent, 'settings.json'), JSON.stringify({ defaultProvider: 'desktop', defaultModel: piModels[0].id,
    retry: { enabled: false } }))
  // Codex 基础入口常驻但不参与验收，模型同样只指向回环 Mock。
  await writeFile(resolve(codexHome, 'config.toml'), `model="mock-model"\nmodel_provider="mock"\napproval_policy="never"\n` +
    `[model_providers.mock]\nname="Mock"\nbase_url=${JSON.stringify(modelURL)}\nwire_api="responses"\nsupports_websockets=false\n`)
  const clientKey = resolve(root, 'client-key'), knownHosts = resolve(root, 'known_hosts')
  run('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', clientKey])
  await writeFile(resolve(root, 'authorized_keys'), await readFile(clientKey + '.pub'), { mode: 0o600 })
  const binary = resolve(root, 'tyrs-hand-worker')
  run('go', ['build', '-o', binary, './cmd/tyrs-hand-worker'], { cwd: repoRoot })
  // Pi 适配器经 wire 录制器启动，录制真实客户端与适配器之间的全部协议消息。
  const piBin = resolve(root, 'codex-harness-adapter-pi'), recorder = resolve(root, 'pi-recorder.json'), wrapper = resolve(root, 'pi-cli')
  await writeFile(piBin, `#!/bin/sh\nexec ${quote(process.execPath)} ${quote(resolve(adapter, 'packages/pi/dist/pi/src/adapter.mjs'))} "$@"\n`, { mode: 0o700 })
  await writeFile(recorder, JSON.stringify({ engine: 'pi', binary: piBin, wsModule: resolve(adapter, 'node_modules/ws/index.js'),
    trace: resolve(runDir, 'worker/wire-pi.jsonl') }), { mode: 0o600 })
  await writeFile(wrapper, `#!/bin/sh\nexec ${quote(process.execPath)} ${quote(resolve(repoRoot, 'tools/mobile-e2e/lib/record-runtime.mjs'))} ${quote(recorder)} "$@"\n`, { mode: 0o700 })
  const ports = { codex: await freePort(), pi: Number(argumentsMap.get('--port') ?? await freePort()) }
  const env = { PATH: process.env.PATH, HOME: home, TMPDIR: resolve(root, 'tmp'), USER: 'desktop-pi', LOGNAME: 'desktop-pi', LANG: 'en_US.UTF-8',
    PI_CODING_AGENT_DIR: agent, PI_CLI: resolve(adapter, 'packages/pi/node_modules/.bin/pi'),
    TYRS_HAND_WORKER_ID: registration.worker.id, TYRS_HAND_WORKER_ROLE: 'discord', TYRS_HAND_WORKER_MAX_CONCURRENT_JOBS: '2',
    TYRS_HAND_WORKER_HOME: home, TYRS_HAND_WORKER_CODEX_HOME: codexHome, TYRS_HAND_WORKER_DATA_ROOT: state,
    TYRS_HAND_WORKER_WORKSPACE_ROOT: workspace, TYRS_HAND_WORKER_SHELL: '/bin/sh',
    TYRS_HAND_WORKER_CREDENTIAL_FILE: resolve(root, 'credential'), TYRS_HAND_WORKER_ENROLLMENT_TOKEN: registration.enrollmentToken,
    TYRS_HAND_WORKER_AUTHORIZED_KEYS_FILE: resolve(root, 'authorized_keys'),
    TYRS_HAND_WORKER_SSH_HOST_KEY_FILE: resolve(state, 'ssh/host_key'), TYRS_HAND_WORKER_SSH_LISTEN_ADDR: `127.0.0.1:${ports.codex}`,
    TYRS_HAND_WORKER_PI_ENABLED: 'true', TYRS_HAND_WORKER_PI_BIN: wrapper, TYRS_HAND_WORKER_PI_SSH_LISTEN_ADDR: `127.0.0.1:${ports.pi}`,
    TYRS_HAND_WORKER_CLAUDE_ENABLED: 'false', TYRS_HAND_CODEX_BIN: process.env.TYRS_HAND_TEST_CODEX_BIN ?? output('which', ['codex']),
    TYRS_HAND_WORKER_CONTROL_URL: control.baseURL, TYRS_HAND_WORKER_GLOBAL_ENV_FILE: resolve(root, 'codex.env'),
    TYRS_HAND_WORKER_ENV_FILE: resolve(root, 'worker.env'), TYRS_HAND_SSH_AGENT_DIR: resolve(root, 'ssh-agent'),
    TYRS_HAND_HEARTBEAT_INTERVAL: '1s', TYRS_HAND_NODE_HEARTBEAT_INTERVAL: '1s', TYRS_HAND_WORKER_SYNC_FALLBACK_INTERVAL: '1s',
    TYRS_HAND_CONTROL_TIMEOUT: '5s' }
  const worker = await startProcess('pi-desktop-worker', '/usr/bin/sandbox-exec', ['-p', sandboxPolicy, binary],
    { cwd: workspace, env, inheritEnv: false, logDir: resolve(runDir, 'logs') })
  const runtime = await control.admin.waitForRuntime(registration.worker.id, 'pi')
  assert.equal(runtime.sshListenAddress, `127.0.0.1:${ports.pi}`)
  await writeFile(knownHosts, output('ssh-keyscan', ['-p', String(ports.pi), '127.0.0.1']) + '\n', { mode: 0o600 })
  return { root, workspace, ports, clientKey, knownHosts,
    async stop() {
      // 每个引擎最多 5 秒关闭；留足时间避免先杀 Worker 留下孤儿运行时，再按唯一根目录回收残留。
      await worker.stop({ timeoutMs: 20_000 })
      output('sh', ['-c', `pkill -KILL -f ${quote(root)} || true`])
      await rm(root, { recursive: true, force: true })
    } }
}

async function main() {
  const expected = selectMarkers({ suite: argumentsMap.get('--suite') ?? 'smoke', only: argumentsMap.get('--only') })
  await mkdir(resolve(runDir, 'worker'), { recursive: true })
  assert.equal(process.versions.node, JSON.parse(await readFile(resolve(repoRoot, 'protocol/adapter-lock.json'))).node, '必须使用固定 Node')
  const adapter = resolve(process.env.TYRS_HAND_ADAPTER_ROOT ?? resolve(repoRoot, 'adapter-source'))
  run('npm', ['--prefix', 'packages/pi', 'run', 'build'], { cwd: adapter })
  const model = await startPiModel({ scenarios: piScenarios, aliases: piAliases, evidenceDir: runDir })
  managed.models.push({ name: 'pi-model', stop: () => model.close() })
  const control = new ControlHarness({ repoRoot, runDir: resolve(runDir, 'control'), label: 'desktop-pi' })
  managed.controls.push(control)
  await control.start()
  const registration = await control.admin.createWorker('desktop-pi-e2e')
  const worker = await startWorker({ control, registration, modelURL: model.url, adapter })
  managed.worker.push({ name: 'pi-worker', stop: () => worker.stop() })
  model.setWorkspace(worker.workspace)
  await mkdir(dirname(sshFragment), { recursive: true, mode: 0o700 })
  await writeFile(sshFragment, `# 由 tools/desktop-e2e/pi/serve.mjs 生成，退出时删除；仅指向本机临时测试 Worker 的 Pi 入口。\n` +
    `Host ${alias}\n  HostName 127.0.0.1\n  Port ${worker.ports.pi}\n  User developer\n  IdentityFile ${worker.clientKey}\n` +
    `  IdentitiesOnly yes\n  UserKnownHostsFile ${worker.knownHosts}\n  StrictHostKeyChecking yes\n  ConnectTimeout 5\n`, { mode: 0o600 })
  managed.worker.push({ name: 'ssh-config', stop: () => rm(sshFragment, { force: true }) })
  const session = { runDir, controlURL: control.baseURL, workerId: registration.worker.id, workspace: worker.workspace,
    host: alias, port: worker.ports.pi, sshConfig: sshFragment, include: `Include ${sshFragment}`, expected }
  await writeFile(resolve(runDir, 'desktop-session.json'), JSON.stringify(session, null, 2))
  console.log('[desktop-pi] ready ' + JSON.stringify(session))
  await new Promise((done) => { for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, done) })
  // 通过标准：所需场景都有真实模型终态与文件副作用，停止/steer/计划来自真实协议请求，全部 wire 符合固定 schema。
  await model.verify(expected, piEffects)
  await verifyPiWire(resolve(runDir, 'worker/wire-pi.jsonl'), expected)
  const report = await validateRuntimeWire(repoRoot, resolve(runDir, 'worker'), { engines: ['pi'] })
  await writeFile(resolve(runDir, 'desktop-result.json'), JSON.stringify({ passed: true, expected,
    wire: { passed: report.passed, errors: report.errors?.length ?? 0 } }, null, 2))
  console.log('[desktop-pi] passed ' + JSON.stringify({ expected, wireErrors: report.errors?.length ?? 0 }))
}

let failure
try { await main() } catch (error) { failure = error }
const cleanupErrors = await cleanupManaged(managed)
const error = completionError(failure, cleanupErrors)
if (error) { console.error(error); process.exitCode = 1 }
