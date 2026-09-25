import { cp, mkdir, readFile, readdir, realpath, writeFile } from 'node:fs/promises'
import { dirname, extname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawn } from 'node:child_process'

import { ControlHarness } from './lib/control.mjs'
import { output, run } from './lib/process.mjs'
import { startModels } from './lib/models.mjs'
import { WorkerHarness } from './lib/worker.mjs'
import { validateRuntimeWire } from './lib/wire.mjs'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const argumentsMap = new Map()
for (let index = 2; index < process.argv.length; index += 2) {
  argumentsMap.set(process.argv[index], process.argv[index + 1])
}
const platform = argumentsMap.get('--platform')
if (platform !== 'android' && platform !== 'ios') throw new Error('--platform 必须是 android 或 ios')
const lane = argumentsMap.get('--lane') ?? 'dual-engine'
if (lane !== 'dual-engine') throw new Error('完整移动端 E2E 必须使用真实 dual-engine lane')
const appID = argumentsMap.get('--app-id') ?? 'com.tyrshand.app.dev'
const flow = resolve(repoRoot, argumentsMap.get('--flow') ?? 'client/e2e/flows/suite.yaml')
const deviceID = resolveDeviceID()
const stamp = new Date().toISOString().replaceAll(':', '').replaceAll('.', '')
const runDir = resolve(repoRoot, '.local/e2e/evidence', `${stamp}-mobile-${platform}-${lane}`)
const controls = []
const processes = []
const maestroProcesses = []
const pairingAbort = new AbortController()
let failed = true
const redactableExtensions = new Set(['.html', '.json', '.log', '.txt', '.xml', '.yaml', '.yml'])

function resolveDeviceID() {
  if (platform === 'android') {
    const configured = process.env.ANDROID_SERIAL
    if (configured && !configured.startsWith('emulator-')) {
      throw new Error('ANDROID_SERIAL 必须指向 emulator-*，不会在真实 Android 设备上运行 E2E')
    }
    const emulators = configured ? [configured] : output('adb', ['devices']).split('\n')
      .map((line) => line.trim().split(/\s+/))
      .filter(([serial, state]) => serial?.startsWith('emulator-') && state === 'device')
      .map(([serial]) => serial)
    if (emulators.length !== 1) throw new Error('需要且只能有一个 Android 模拟器在线')
    process.env.ANDROID_SERIAL = emulators[0]
    return emulators[0]
  }
  const devices = JSON.parse(output('xcrun', ['simctl', 'list', 'devices', 'booted', '-j'])).devices
  const booted = Object.values(devices).flat().filter((device) => device.state === 'Booted')
  if (booted.length !== 1) throw new Error('需要且只能有一个 iOS 模拟器在线')
  return booted[0].udid
}

function checkVersions() {
  const maestro = process.env.TYRS_HAND_E2E_MAESTRO_BIN ?? 'maestro'
  if (!output(maestro, ['--version']).includes('2.3.0')) throw new Error('需要 Maestro 2.3.0')
  if (process.env.TYRS_HAND_E2E_NATIVE_SERVICES !== '1') output('docker', ['info'])
  if (process.env.TYRS_HAND_E2E_NATIVE_SERVICES === '1') {
    if (!output('postgres', ['--version']).includes(' 18.3')) throw new Error('需要 PostgreSQL 18.3')
    if (!output('redis-server', ['--version']).includes('v=8.4.0')) throw new Error('需要 Redis 8.4.0')
  }
  if (platform === 'android') output('adb', ['-s', deviceID, 'get-state'])
  else output('xcrun', ['simctl', 'getenv', deviceID, 'SIMULATOR_UDID'])
}

async function seed(control, worker, projectName, hostPath) {
  const raw = output('go', ['run', './tools/mobile-e2e/fixture', 'seed',
    '--worker-id', worker.worker.id, '--project-name', projectName, '--host-path', hostPath], {
    cwd: repoRoot, env: { ...process.env, TYRS_HAND_DATABASE_URL: control.databaseURL },
  })
  return JSON.parse(raw)
}

async function assertInstalled() {
  if (platform === 'android') output('adb', ['-s', deviceID, 'shell', 'pm', 'path', appID])
  else output('xcrun', ['simctl', 'get_app_container', deviceID, appID, 'app'])
}

function isolateAndroidAppLinks() {
  if (platform !== 'android') return
  const installed = new Set(output('adb', ['-s', deviceID, 'shell', 'pm', 'list', 'packages'])
    .split('\n').map((line) => line.trim().replace(/^package:/, '')).filter(Boolean))
  const disabled = ['com.tyrshand.app', 'com.tyrshand.app.dev', 'com.tyrshand.app.preview']
    .filter((packageName) => packageName !== appID && installed.has(packageName))
  for (const packageName of disabled) {
    run('adb', ['-s', deviceID, 'shell', 'pm', 'disable-user', '--user', '0', packageName])
  }
  if (disabled.length > 0) processes.push({ stop: async () => {
    for (const packageName of disabled) {
      try { run('adb', ['-s', deviceID, 'shell', 'pm', 'enable', packageName]) } catch { /* 恢复测试前状态 */ }
    }
  } })
}

async function redactEvidenceSecrets(directory, secrets) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = resolve(directory, entry.name)
    if (entry.isDirectory()) {
      await redactEvidenceSecrets(path, secrets)
      continue
    }
    if (!redactableExtensions.has(extname(entry.name))) continue
    let content = await readFile(path, 'utf8')
    for (const secret of secrets) content = content.replaceAll(secret, '[REDACTED]')
    await writeFile(path, content)
  }
}

async function runMaestro(environment, label = 'suite', flowPath = flow) {
  const maestro = process.env.TYRS_HAND_E2E_MAESTRO_BIN ?? 'maestro'
  const args = ['--device', deviceID, 'test', flowPath, '--debug-output', `${runDir}/maestro-debug-${label}`,
    '--format', 'JUNIT', '--output', `${runDir}/junit-${label}.xml`,
    ...Object.entries(environment).filter(([key]) => !key.includes('PRIVATE_KEY'))
      .flatMap(([key, value]) => ['-e', `${key}=${value}`])]
  const log = []
  const child = spawn(maestro, args, { cwd: repoRoot,
    env: { ...process.env, ...environment }, stdio: ['ignore', 'pipe', 'pipe'] })
  const childExit = new Promise((resolve) => {
    child.once('exit', (code, signal) => resolve({ code, signal }))
    child.once('error', (error) => resolve({ error }))
  })
  maestroProcesses.push({ stop: async () => {
    if (child.exitCode !== null || child.signalCode !== null) return
    child.kill('SIGTERM')
    let timer
    await Promise.race([childExit, new Promise((resolve) => { timer = setTimeout(resolve, 5_000) })])
    clearTimeout(timer)
    if (child.exitCode === null && child.signalCode === null) {
      child.kill('SIGKILL')
      await childExit
    }
  } })
  for (const stream of [child.stdout, child.stderr]) stream.on('data', (chunk) => {
    process.stderr.write(chunk)
    log.push(chunk)
  })
  const heartbeat = setInterval(() => process.stderr.write('[mobile-e2e] Maestro 仍在运行\n'), 30_000)
  const result = await childExit
  clearInterval(heartbeat)
  await writeFile(`${runDir}/logs/maestro-${label}.log`, Buffer.concat(log))
  if (platform === 'android') {
    // 保留真实 JS/原生崩溃栈，不能只留下启动器画面和“找不到按钮”。
    await writeFile(`${runDir}/logs/android-crash-${label}.log`,
      output('adb', ['-s', deviceID, 'logcat', '-b', 'crash', '-d']))
  }
  await redactEvidenceSecrets(runDir, Object.entries(environment)
    .filter(([key]) => key.includes('PAIRING') || key.includes('PRIVATE_KEY'))
    .flatMap(([, value]) => [value, ...value.split('\n').filter((line) => line.length > 40)]))
  if (result.error) throw new Error('Maestro 无法启动', { cause: result.error })
  if (result.code !== 0) throw new Error(`Maestro 失败（${result.code ?? result.signal}）`)
}

async function main() {
  checkVersions()
  await mkdir(`${runDir}/logs`, { recursive: true })
  if (platform === 'android') run('adb', ['-s', deviceID, 'logcat', '-b', 'crash', '-c'])
  const primary = new ControlHarness({
    repoRoot, runDir: `${runDir}/primary`, label: 'primary',
  })
  controls.push(primary)
  await primary.start()
  const adapter = resolve(process.env.TYRS_HAND_ADAPTER_ROOT ?? resolve(repoRoot, '../claude-codex'))
  run('npm', ['run', 'build'], { cwd: adapter })
  const models = await startModels(adapter, runDir)
  processes.push({ stop: () => models.close() })
  const registration = await primary.admin.createWorker(`mobile-e2e-${lane}`)
  const worker = new WorkerHarness({ repoRoot, runDir: resolve(runDir, 'worker'), control: primary,
    registration, modelURLs: models.urls })
  processes.push(worker)
  await worker.start()
  models.setWorkspace(worker.workspace)
  await seed(primary, registration, 'mobile-shared-project', worker.workspace)
  const firstPairing = await primary.admin.createPairing(registration.worker.id, 'codex')
  const secondPairing = await primary.admin.createPairing(registration.worker.id, 'claude-code')
  if (platform === 'android') {
    for (const port of [primary.port, ...Object.values(worker.ports)]) {
      run('adb', ['-s', deviceID, 'reverse', `tcp:${port}`, `tcp:${port}`])
      processes.push({ stop: async () => run('adb', ['-s', deviceID, 'reverse', '--remove', `tcp:${port}`]) })
    }
  }
  await assertInstalled()
  isolateAndroidAppLinks()
  const approvals = [primary.admin.approveWhenClaimed(firstPairing.id, 600_000, pairingAbort.signal),
    primary.admin.approveWhenClaimed(secondPairing.id, 600_000, pairingAbort.signal)]
  const projectPath = await realpath(worker.workspace)
  const segments = projectPath.split('/').filter(Boolean)
  const sourceFlows = resolve(repoRoot, 'client/e2e/flows')
  const relativeFlow = relative(sourceFlows, flow)
  if (relativeFlow.startsWith('..')) throw new Error('测试 flow 必须位于 client/e2e/flows')
  const stagedFlows = resolve(runDir, 'flows')
  await cp(sourceFlows, stagedFlows, { recursive: true })
  const directoryFlow = resolve(stagedFlows, '_shared/project-directory.yaml')
  // Android inputText 会丢弃换行，显式按 Enter 验证真实多行输入框。
  await writeFile(resolve(stagedFlows, '_shared/private-key.yaml'),
    'appId: ${TYRS_HAND_E2E_APP_ID}\n---\n' + worker.privateKey.trim().split('\n')
      .map((line) => `- inputText: ${JSON.stringify(line)}\n- pressKey: Enter\n`).join(''))
  let path = ''
  await writeFile(directoryFlow, 'appId: ${TYRS_HAND_E2E_APP_ID}\n---\n' + segments.map((segment) => {
    path += '/' + segment
    return `- scrollUntilVisible:\n    element:\n      id: "connection:ssh:directory:${encodeURIComponent(path)}"\n    direction: DOWN\n    timeout: 15000\n- tapOn:\n    id: "connection:ssh:directory:${encodeURIComponent(path)}"\n`
  }).join(''))
  const maestroEnvironment = { TYRS_HAND_E2E_PLATFORM: platform,
    TYRS_HAND_E2E_APP_ID: appID, TYRS_HAND_E2E_PAIRING_URI: firstPairing.pairingUri,
    TYRS_HAND_E2E_SECOND_PAIRING_URI: secondPairing.pairingUri,
    TYRS_HAND_E2E_PRIMARY_SERVER_ID: firstPairing.serverId,
    TYRS_HAND_E2E_WORKER_ID: registration.worker.id,
    TYRS_HAND_E2E_CODEX_PORT: String(worker.ports.codex),
    TYRS_HAND_E2E_CLAUDE_PORT: String(worker.ports['claude-code']),
    TYRS_HAND_E2E_PRIVATE_KEY: worker.privateKey }
  await Promise.all([runMaestro(maestroEnvironment, 'suite', resolve(stagedFlows, relativeFlow)), ...approvals])
  await models.verify(['MOBILE_CODEX_CHAT', 'MOBILE_CLAUDE_CHAT', 'MOBILE_CLAUDE_FULL',
    'MOBILE_CLAUDE_APPROVAL', 'MOBILE_CLAUDE_DENY', 'MOBILE_CLAUDE_PLAN'])
  const schemaReport = await validateRuntimeWire(repoRoot, resolve(runDir, 'worker'))
  const snapshots = controls.map((control) => JSON.parse(output('go', [
    'run', './tools/mobile-e2e/fixture', 'snapshot'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: control.databaseURL } })))
  const clientBuild = platform === 'android'
    ? output('adb', ['-s', deviceID, 'shell', 'dumpsys', 'package', appID])
      .split('\n').filter((line) => /versionCode=|versionName=/.test(line)).map((line) => line.trim())
    : JSON.parse(output('plutil', ['-convert', 'json', '-o', '-', resolve(
      output('xcrun', ['simctl', 'get_app_container', deviceID, appID, 'app']), 'Info.plist')]))
  await writeFile(resolve(runDir, 'mobile-acceptance.json'), JSON.stringify({
    platform, passed: true, appID, clientBuild, maestroVersion: '2.3.0',
    commit: output('git', ['rev-parse', 'HEAD'], { cwd: repoRoot }),
    dirty: !!output('git', ['status', '--porcelain'], { cwd: repoRoot }),
    adapterCommit: worker.pin.commit, nativeBuild: worker.nativeBuild,
    schemaPassed: schemaReport.passed, completeProtocolMatrix: false,
  }, null, 2))
  await primary.writeManifest({ platform, lane, appID, codexVersion: worker.pin.codexProtocol,
    controls: snapshots, flow, runtimes: worker.runtimes,
    acceptance: 'real-mobile-dual-engine', completeProtocolMatrix: false })
  failed = false
}

try {
  await main()
} finally {
  pairingAbort.abort()
  for (const managed of maestroProcesses.reverse()) await managed.stop()
  for (const managed of processes.reverse()) await managed.stop()
  for (const control of controls.reverse()) await control.stop()
  process.stderr.write(`[mobile-e2e] ${failed ? '失败证据' : '证据'}：${runDir}\n`)
}
