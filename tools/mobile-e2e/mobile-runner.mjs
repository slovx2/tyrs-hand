import { mkdir, readFile, readdir, writeFile } from 'node:fs/promises'
import { dirname, extname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawn } from 'node:child_process'

import { ControlHarness } from './lib/control.mjs'
import { output, run, startProcess } from './lib/process.mjs'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const argumentsMap = new Map()
for (let index = 2; index < process.argv.length; index += 2) {
  argumentsMap.set(process.argv[index], process.argv[index + 1])
}
const platform = argumentsMap.get('--platform')
if (platform !== 'android' && platform !== 'ios') throw new Error('--platform 必须是 android 或 ios')
const lane = argumentsMap.get('--lane') ?? 'protocol'
if (lane !== 'protocol') throw new Error('Mobile E2E 仅支持宿主架构的 protocol lane')
const appID = argumentsMap.get('--app-id') ?? 'com.tyrshand.app.dev'
const flow = resolve(repoRoot, argumentsMap.get('--flow') ?? 'client/e2e/flows/suite.yaml')
const deviceID = resolveDeviceID()
const stamp = new Date().toISOString().replaceAll(':', '').replaceAll('.', '')
const runDir = resolve(repoRoot, '.local/e2e/evidence', `${stamp}-mobile-${platform}-${lane}`)
const controls = []
const processes = []
const maestroProcesses = []
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

async function startProtocolWorker(control, enrollmentToken, label) {
  const managed = await startProcess(label, 'node', ['tools/mobile-e2e/protocol-worker.mjs'], {
    cwd: repoRoot, logDir: `${runDir}/logs`, env: {
      TYRS_HAND_E2E_CONTROL_URL: control.baseURL,
      TYRS_HAND_E2E_ENROLLMENT_TOKEN: enrollmentToken,
      TYRS_HAND_E2E_WORKER_ID: label,
    },
  })
  processes.push(managed)
  return managed
}

async function seed(control, worker) {
  const raw = output('go', ['run', './tools/mobile-e2e/fixture', 'seed',
    '--worker-id', worker.worker.id], {
    cwd: repoRoot, env: { ...process.env, TYRS_HAND_DATABASE_URL: control.databaseURL },
  })
  return JSON.parse(raw)
}

async function assertInstalled() {
  if (platform === 'android') output('adb', ['-s', deviceID, 'shell', 'pm', 'path', appID])
  else output('xcrun', ['simctl', 'get_app_container', deviceID, appID, 'app'])
}

async function installMediaFixture() {
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=', 'base64')
  const imagePath = `${runDir}/tyrs-hand-e2e.png`
  await writeFile(imagePath, png)
  if (platform === 'android') {
    run('adb', ['-s', deviceID, 'shell', 'mkdir', '-p', '/sdcard/Pictures', '/sdcard/Download'])
    run('adb', ['-s', deviceID, 'push', imagePath, '/sdcard/Pictures/tyrs-hand-e2e.png'])
    run('adb', ['-s', deviceID, 'push', resolve(repoRoot, 'client/e2e/fixtures/tyrs-hand-e2e.txt'),
      '/sdcard/Download/tyrs-hand-e2e.txt'])
    run('adb', ['-s', deviceID, 'shell', 'am', 'broadcast', '-a', 'android.intent.action.MEDIA_SCANNER_SCAN_FILE',
      '-d', 'file:///sdcard/Pictures/tyrs-hand-e2e.png'])
  } else {
    run('xcrun', ['simctl', 'addmedia', deviceID, imagePath])
  }
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
    ...Object.entries(environment).flatMap(([key, value]) => ['-e', `${key}=${value}`])]
  const log = []
  const child = spawn(maestro, args, { cwd: repoRoot,
    env: { ...process.env, ...environment }, stdio: ['ignore', 'pipe', 'pipe'] })
  const childExit = new Promise((resolve) => child.once('exit', resolve))
  maestroProcesses.push({ stop: async () => {
    if (child.exitCode !== null) return
    child.kill('SIGTERM')
    await Promise.race([childExit, new Promise((resolve) => setTimeout(resolve, 5_000))])
    if (child.exitCode === null) child.kill('SIGKILL')
  } })
  for (const stream of [child.stdout, child.stderr]) stream.on('data', (chunk) => {
    process.stderr.write(chunk)
    log.push(chunk)
  })
  const heartbeat = setInterval(() => process.stderr.write('[mobile-e2e] Maestro 仍在运行\n'), 30_000)
  const code = await childExit
  clearInterval(heartbeat)
  await writeFile(`${runDir}/logs/maestro-${label}.log`, Buffer.concat(log))
  await redactEvidenceSecrets(runDir, Object.values(environment).filter((value) => value.includes('pairing')))
  if (code !== 0) throw new Error(`Maestro 失败（${code}）`)
}

async function main() {
  checkVersions()
  await mkdir(`${runDir}/logs`, { recursive: true })
  const primary = await new ControlHarness({
    repoRoot, runDir: `${runDir}/primary`, label: 'primary',
  }).start()
  controls.push(primary)
  const primaryWorker = await primary.admin.createWorker(`mobile-e2e-${lane}`)
  await seed(primary, primaryWorker)
  await startProtocolWorker(primary, primaryWorker.enrollmentToken, 'primary-protocol-worker')

  const secondary = await new ControlHarness({ repoRoot, runDir: `${runDir}/secondary`, label: 'secondary' }).start()
  controls.push(secondary)
  const secondaryWorker = await secondary.admin.createWorker('mobile-e2e-secondary')
  await seed(secondary, secondaryWorker)
  await startProtocolWorker(secondary, secondaryWorker.enrollmentToken, 'secondary-protocol-worker')

  const firstPairing = await primary.admin.createPairing()
  const secondPairing = await secondary.admin.createPairing()
  if (platform === 'android') {
    run('adb', ['-s', deviceID, 'reverse', `tcp:${primary.port}`, `tcp:${primary.port}`])
    run('adb', ['-s', deviceID, 'reverse', `tcp:${secondary.port}`, `tcp:${secondary.port}`])
  }
  await assertInstalled()
  await installMediaFixture()
  const approvals = [primary.admin.approveWhenClaimed(firstPairing.id),
    secondary.admin.approveWhenClaimed(secondPairing.id)]
  const maestroEnvironment = { TYRS_HAND_E2E_PLATFORM: platform,
    TYRS_HAND_E2E_APP_ID: appID, TYRS_HAND_E2E_PAIRING_URI: firstPairing.pairingUri,
    TYRS_HAND_E2E_SECOND_PAIRING_URI: secondPairing.pairingUri }
  await Promise.all([runMaestro(maestroEnvironment), ...approvals])

  await primary.stopServer()
  await runMaestro(maestroEnvironment, 'offline-send',
    resolve(repoRoot, 'client/e2e/flows/offline-send.yaml'))
  await primary.startServer()
  await runMaestro(maestroEnvironment, 'offline-recover',
    resolve(repoRoot, 'client/e2e/flows/offline-recover.yaml'))
  output('go', ['run', './tools/mobile-e2e/fixture', 'assert-message-once',
    '--text', 'E2E_OFFLINE_IDEMPOTENT'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } })
  output('go', ['run', './tools/mobile-e2e/fixture', 'seed-history'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } })
  await runMaestro(maestroEnvironment, 'history-pagination',
    resolve(repoRoot, 'client/e2e/flows/history-pagination.yaml'))
  output('go', ['run', './tools/mobile-e2e/fixture', 'force-cursor-reset'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } })
  await runMaestro(maestroEnvironment, 'cursor-reset',
    resolve(repoRoot, 'client/e2e/flows/cursor-reset-recover.yaml'))
  const snapshots = controls.map((control) => JSON.parse(output('go', [
    'run', './tools/mobile-e2e/fixture', 'snapshot'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: control.databaseURL } })))
  await primary.writeManifest({ lane, appID, codexVersion: null,
    controls: snapshots })
  failed = false
}

try {
  await main()
} finally {
  for (const managed of maestroProcesses.reverse()) await managed.stop()
  for (const managed of processes.reverse()) await managed.stop()
  for (const control of controls.reverse()) await control.stop()
  process.stderr.write(`[mobile-e2e] ${failed ? '失败证据' : '证据'}：${runDir}\n`)
}
