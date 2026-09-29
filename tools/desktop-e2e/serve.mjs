// 桌面端 GUI 验收环境：真实 Control、Mock LLM 与双入口 Worker 常驻，供安装版 ChatGPT.app（Codex）经专用 SSH Host 接入。
// Worker 使用临时 HOME、虚拟密钥与 sandbox-exec 外连限制，模型只走回环 Mock，不读取个人模型登录态。
// 测试主机写入独立的 ssh 配置片段，由用户 ~/.ssh/config 的一行 Include 引入；退出时删除片段并校验真实 wire。
import assert from 'node:assert/strict'
import { mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { ControlHarness } from '../mobile-e2e/lib/control.mjs'
import { output, run } from '../mobile-e2e/lib/process.mjs'
import { startModels } from '../mobile-e2e/lib/models.mjs'
import { WorkerHarness } from '../mobile-e2e/lib/worker.mjs'
import { validateRuntimeWire } from '../mobile-e2e/lib/wire.mjs'
import { cleanupManaged, completionError } from '../mobile-e2e/lib/cleanup.mjs'
import { desktopMarkers, desktopScenarios, steerPayload } from './scenarios.mjs'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const argumentsMap = new Map()
for (let index = 2; index < process.argv.length; index += 2) argumentsMap.set(process.argv[index], process.argv[index + 1])
const sshFragment = resolve(argumentsMap.get('--ssh-config') ?? resolve(homedir(), '.ssh/config.d/tyrs-desktop-e2e'))
const aliasPrefix = argumentsMap.get('--alias-prefix') ?? 'tyrs-e2e'
const stamp = new Date().toISOString().replaceAll(':', '').replaceAll('.', '')
const runDir = resolve(repoRoot, '.local/e2e/evidence', `${stamp}-desktop`)
const managed = { worker: [], models: [], controls: [] }

// 与移动端共用 Mock LLM 场景：在 GUI 输入框中发送标记词即触发对应的真实工具调用。
const markers = {
  'claude-code': ['MOBILE_CLAUDE_CHAT', 'MOBILE_CLAUDE_FULL', 'MOBILE_CLAUDE_APPROVAL', 'MOBILE_CLAUDE_DENY', 'MOBILE_CLAUDE_PLAN',
    ...desktopMarkers],
  codex: ['MOBILE_CODEX_CHAT'],
}

function sshConfig(worker) {
  const hosts = { codex: `${aliasPrefix}-codex`, 'claude-code': `${aliasPrefix}-claude` }
  const blocks = Object.entries(hosts).map(([engine, alias]) => `Host ${alias}
  HostName 127.0.0.1
  Port ${worker.ports[engine]}
  User developer
  IdentityFile ${worker.clientKey}
  IdentitiesOnly yes
  UserKnownHostsFile ${worker.knownHosts}
  StrictHostKeyChecking yes
  ConnectTimeout 5
`)
  return { hosts, text: `# 由 tools/desktop-e2e/serve.mjs 生成，退出时删除；仅指向本机临时测试 Worker。\n${blocks.join('\n')}` }
}

// 停止与 steer 必须来自客户端的真实协议请求，且停止后迟到的模型回复不能进入界面或历史。
async function verifyDesktopWire(path, expected) {
  const rows = (await readFile(path, 'utf8')).trim().split('\n').map((line) => JSON.parse(line).message)
  const threadOf = (marker) => rows.find((message) => message.method === 'turn/start' &&
    JSON.stringify(message.params?.input ?? []).includes(marker))?.params.threadId
  if (expected.includes('DESKTOP_CLAUDE_STOP')) {
    const threadId = threadOf('DESKTOP_CLAUDE_STOP')
    assert.ok(threadId, '缺少停止场景的回合')
    assert.ok(rows.some((message) => message.method === 'turn/interrupt' && message.params?.threadId === threadId),
      '停止必须由客户端发出真实 turn/interrupt')
    assert.ok(rows.some((message) => message.method === 'turn/completed' && message.params?.threadId === threadId &&
      message.params.turn.status === 'interrupted'), '停止后的回合必须以 interrupted 终结')
    assert.ok(!rows.some((message) => message.params?.threadId === threadId &&
      JSON.stringify(message.params).includes('DESKTOP_STOP_TOO_LATE')), '停止后迟到的模型回复不能下发')
  }
  if (expected.includes('DESKTOP_CLAUDE_STEER')) {
    const threadId = threadOf('DESKTOP_CLAUDE_STEER')
    assert.ok(rows.some((message) => message.method === 'turn/steer' && message.params?.threadId === threadId &&
      JSON.stringify(message.params.input).includes(steerPayload)), 'steer 必须由客户端发出真实 turn/steer')
  }
}

async function main() {
  await mkdir(runDir, { recursive: true })
  const control = new ControlHarness({ repoRoot, runDir: resolve(runDir, 'control'), label: 'desktop' })
  managed.controls.push(control)
  await control.start()
  const adapter = resolve(process.env.TYRS_HAND_ADAPTER_ROOT ?? resolve(repoRoot, '../claude-codex'))
  run('npm', ['run', 'build'], { cwd: adapter })
  const models = await startModels(adapter, runDir, desktopScenarios)
  managed.models.push({ name: 'models', stop: () => models.close() })
  const registration = await control.admin.createWorker('desktop-e2e')
  // 固定根目录：ChatGPT.app 中登记的测试项目路径（<root>/project）跨次运行保持有效。
  const worker = new WorkerHarness({ repoRoot, runDir: resolve(runDir, 'worker'), control, registration,
    modelURLs: models.urls, root: argumentsMap.get('--root') ?? '/tmp/000-tyrs-desktop-e2e' })
  managed.worker.push(worker)
  await worker.start()
  models.setWorkspace(worker.workspace)
  output('go', ['run', './tools/mobile-e2e/fixture', 'seed', '--worker-id', registration.worker.id,
    '--project-name', 'desktop-shared-project', '--host-path', worker.workspace],
  { cwd: repoRoot, env: { ...process.env, TYRS_HAND_DATABASE_URL: control.databaseURL } })
  const { hosts, text } = sshConfig(worker)
  await mkdir(dirname(sshFragment), { recursive: true, mode: 0o700 })
  await writeFile(sshFragment, text, { mode: 0o600 })
  managed.worker.push({ name: 'ssh-config', stop: () => rm(sshFragment, { force: true }) })
  const session = { runDir, controlURL: control.baseURL, workerId: registration.worker.id, workspace: worker.workspace,
    hosts, ports: worker.ports, sshConfig: sshFragment, include: `Include ${sshFragment}`, markers }
  await writeFile(resolve(runDir, 'desktop-session.json'), JSON.stringify(session, null, 2))
  console.log('[desktop-e2e] ready ' + JSON.stringify(session))
  await new Promise((done) => {
    for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, done)
  })
  // 通过标准：所需场景都有真实模型终态、文件副作用与工具结果回模（Mock LLM 断言），且全部真实通信符合固定 schema。
  const expected = argumentsMap.get('--expect')?.split(',') ?? Object.values(markers).flat()
  await models.verify(expected)
  await verifyDesktopWire(resolve(runDir, 'worker/wire-claude-code.jsonl'), expected)
  const report = await validateRuntimeWire(repoRoot, resolve(runDir, 'worker'))
  await writeFile(resolve(runDir, 'desktop-result.json'), JSON.stringify({ passed: true, expected,
    wire: { passed: report.passed, errors: report.errors?.length ?? 0 } }, null, 2))
  console.log('[desktop-e2e] passed ' + JSON.stringify({ expected, wireErrors: report.errors?.length ?? 0 }))
}

let failure
try { await main() } catch (error) { failure = error }
const cleanupErrors = await cleanupManaged(managed)
const error = completionError(failure, cleanupErrors)
if (error) { console.error(error); process.exitCode = 1 }
