import { mkdir, readFile, readdir, writeFile } from 'node:fs/promises'
import { dirname, extname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawn } from 'node:child_process'

import { ControlHarness } from './lib/control.mjs'
import { freePort, output, run, startProcess, waitFor } from './lib/process.mjs'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const argumentsMap = new Map()
for (let index = 2; index < process.argv.length; index += 2) {
  argumentsMap.set(process.argv[index], process.argv[index + 1])
}
const platform = argumentsMap.get('--platform')
if (platform !== 'android' && platform !== 'ios') throw new Error('--platform 必须是 android 或 ios')
const lane = argumentsMap.get('--lane') ?? (platform === 'android' ? 'real-codex' : 'protocol')
if (!['real-codex', 'protocol'].includes(lane)) throw new Error('--lane 必须是 real-codex 或 protocol')
const appID = argumentsMap.get('--app-id') ?? 'com.tyrshand.app.dev'
const flow = resolve(repoRoot, argumentsMap.get('--flow') ?? 'client/e2e/flows/suite.yaml')
const customFlow = argumentsMap.has('--flow')
const postFlow = argumentsMap.has('--post-flow')
  ? resolve(repoRoot, argumentsMap.get('--post-flow')) : null
const postFixture = argumentsMap.get('--post-fixture') ?? ''
const allowedPostFixtures = new Set([
  'seed-history', 'seed-forward-history', 'force-cursor-reset', 'notification-target',
])
if ((postFlow === null) !== (postFixture === '')) {
  throw new Error('--post-flow 与 --post-fixture 必须同时提供')
}
if (postFixture && !allowedPostFixtures.has(postFixture)) {
  throw new Error(`--post-fixture 不支持 ${postFixture}`)
}
const deviceID = resolveDeviceID()
const stamp = new Date().toISOString().replaceAll(':', '').replaceAll('.', '')
const runDir = resolve(repoRoot, '.local/e2e/evidence', `${stamp}-mobile-${platform}-${lane}`)
const controls = []
const processes = []
const maestroProcesses = []
let failed = true
let responsesStatsURL = ''
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
  if (lane === 'real-codex' && output('codex', ['--version']) !== 'codex-cli 0.145.0') {
    throw new Error('真实 Codex lane 需要 codex-cli 0.145.0')
  }
  if (lane === 'real-codex' || process.env.TYRS_HAND_E2E_NATIVE_SERVICES !== '1') output('docker', ['info'])
  if (process.env.TYRS_HAND_E2E_NATIVE_SERVICES === '1') {
    if (!output('postgres', ['--version']).includes(' 18.3')) throw new Error('需要 PostgreSQL 18.3')
    if (!output('redis-server', ['--version']).includes('v=8.4.0')) throw new Error('需要 Redis 8.4.0')
  }
  if (platform === 'android') output('adb', ['-s', deviceID, 'get-state'])
  else output('xcrun', ['simctl', 'getenv', deviceID, 'SIMULATOR_UDID'])
}

async function startProtocolWorker(control, enrollmentToken, environmentID, label) {
  const managed = await startProcess(label, 'node', ['tools/mobile-e2e/protocol-worker.mjs'], {
    cwd: repoRoot, logDir: `${runDir}/logs`, env: {
      TYRS_HAND_E2E_CONTROL_URL: control.baseURL,
      TYRS_HAND_E2E_ENROLLMENT_TOKEN: enrollmentToken,
      TYRS_HAND_E2E_ENVIRONMENT_ID: environmentID,
      TYRS_HAND_E2E_WORKER_ID: label,
    },
  })
  processes.push(managed)
  return managed
}

async function seed(control, node, image, protocol, projectName) {
  const raw = output('go', ['run', './tools/mobile-e2e/fixture', 'seed',
    '--node-id', node.node.id, '--image', image, '--project-name', projectName,
    ...(protocol ? ['--protocol'] : [])], {
    cwd: repoRoot, env: { ...process.env, TYRS_HAND_DATABASE_URL: control.databaseURL },
  })
  return JSON.parse(raw)
}

async function waitForContainer(name) {
  const deadline = Date.now() + 120_000
  while (Date.now() < deadline) {
    try {
      output('docker', ['inspect', name])
      output('docker', ['exec', name, 'test', '-d', '/var/lib/tyrs-hand'])
      return
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 1000))
    }
  }
  throw new Error(`等待开发容器 ${name} 超时`)
}

async function startRealWorker(control, node, fixture, image, workerImage) {
  const compact = fixture.environmentId.replaceAll('-', '')
  const workerContainer = `tyrs-hand-mobile-worker-${compact}`
  const runtimeVolume = `tyrs-hand-mobile-runtime-${compact}`
  const workerVolume = `tyrs-hand-mobile-worker-data-${compact}`
  output('docker', ['volume', 'create', runtimeVolume])
  output('docker', ['volume', 'create', workerVolume])
  const runtimeMountpoint = output('docker', ['volume', 'inspect', runtimeVolume,
    '--format', '{{.Mountpoint}}'])
  const socketGID = output('docker', ['run', '--rm', '--mount',
    'type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock',
    '--entrypoint', 'stat', workerImage, '-c', '%g', '/var/run/docker.sock'])
  run('docker', ['run', '--rm', '--user', '0:0', '--volume', `${runtimeVolume}:/runtime`,
    '--volume', `${workerVolume}:/data/worker`, '--entrypoint', 'chown', workerImage,
    '-R', '10001:10001', '/runtime', '/data/worker'])
  const workerEnvironment = {
    TYRS_HAND_ENV: 'development',
    TYRS_HAND_WORKER_CONTROL_URL: `http://host.docker.internal:${control.port}`,
    TYRS_HAND_WORKER_CREDENTIAL_FILE: '/data/worker/node-credential',
    TYRS_HAND_WORKER_ENROLLMENT_TOKEN: node.enrollmentToken,
    TYRS_HAND_WORKER_PROTOCOL_VERSION: '20', TYRS_HAND_WORKER_ID: 'mobile-e2e-real-worker',
    TYRS_HAND_WORKER_ROLE: 'discord', TYRS_HAND_WORKER_MAX_CONCURRENT_JOBS: '2',
    TYRS_HAND_WORKER_DATA_ROOT: '/data/worker', TYRS_HAND_REPO_CACHE_ROOT: '/data/worker/repo-cache',
    TYRS_HAND_WORKTREE_ROOT: '/data/worker/worktrees', TYRS_HAND_CODEX_HOME_ROOT: '/data/worker/codex-homes',
    TYRS_HAND_ENABLE_DEVELOPMENT_CONTAINERS: 'true', TYRS_HAND_DEVELOPMENT_IMAGE: image,
    TYRS_HAND_DEVELOPMENT_RUNTIME_DIR: '/run/tyrs-hand-development-runtime',
    TYRS_HAND_DEVELOPMENT_RUNTIME_HOST_DIR: runtimeMountpoint,
    TYRS_HAND_DOCKER_HOST: 'unix:///var/run/docker.sock',
    TYRS_HAND_DEVELOPMENT_HOST_DOCKER: 'false',
    TYRS_HAND_HEARTBEAT_INTERVAL: '2s', TYRS_HAND_CODEX_BIN: 'codex',
  }
  run('docker', ['run', '--detach', '--name', workerContainer,
    '--add-host', 'host.docker.internal:host-gateway', '--group-add', socketGID,
    '--mount', 'type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock',
    '--volume', `${runtimeVolume}:/run/tyrs-hand-development-runtime`,
    '--volume', `${workerVolume}:/data/worker`,
    ...Object.keys(workerEnvironment).flatMap((key) => ['--env', key]), workerImage], {
    env: { ...process.env, ...workerEnvironment },
  })
  const managed = {
    async stop() {
      let logs = ''
      try { logs = output('docker', ['logs', workerContainer]) } catch { /* 容器可能已退出 */ }
      await writeFile(`${runDir}/logs/real-worker.log`, logs)
      for (const resource of [workerContainer, fixture.containerName]) {
        try { run('docker', ['rm', '--force', resource]) } catch { /* 最佳努力清理 */ }
      }
      for (const resource of [runtimeVolume, workerVolume,
        `tyrs-hand-e2e-data-${compact}`, `tyrs-hand-e2e-home-${compact}`]) {
        try { run('docker', ['volume', 'rm', resource]) } catch { /* 最佳努力清理 */ }
      }
      try { run('docker', ['network', 'rm', `tyrs-hand-e2e-net-${compact}`]) } catch { /* 最佳努力清理 */ }
    },
  }
  processes.push(managed)
  await waitForContainer(fixture.containerName)
  run('docker', ['exec', '--user', '0:0', fixture.containerName, 'mkdir', '-p',
    '/var/lib/tyrs-hand/workspaces/e2e-project'])
  run('docker', ['exec', '--user', '0:0', fixture.containerName, 'chown', '-R', '1000:1000',
    '/var/lib/tyrs-hand/workspaces/e2e-project'])
  output('go', ['run', './tools/mobile-e2e/fixture', 'wait-ready',
    '--environment-id', fixture.environmentId], {
    cwd: repoRoot, env: { ...process.env, TYRS_HAND_DATABASE_URL: control.databaseURL },
  })
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

async function installMediaFixture() {
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=', 'base64')
  const imagePath = `${runDir}/tyrs-hand-e2e.png`
  await writeFile(imagePath, png)
  if (platform === 'android') {
    run('adb', ['-s', deviceID, 'shell', 'mkdir', '-p', '/sdcard/Pictures', '/sdcard/Download'])
    run('adb', ['-s', deviceID, 'push', imagePath, '/sdcard/Pictures/tyrs-hand-e2e.png'])
    run('adb', ['-s', deviceID, 'push', resolve(repoRoot, 'client/e2e/fixtures/tyrs-hand-e2e.txt'),
      '/sdcard/Download/tyrs-hand-e2e.txt'])
    run('adb', ['-s', deviceID, 'shell', 'dd', 'if=/dev/zero',
      'of=/sdcard/Download/tyrs-hand-too-large.bin', 'bs=1048576', 'count=26'])
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
  const responsesPort = await freePort()
  const responses = await startProcess('mock-responses', 'node', ['tools/mobile-e2e/mock-responses.mjs'], {
    cwd: repoRoot, logDir: `${runDir}/logs`, env: { TYRS_HAND_E2E_RESPONSES_PORT: String(responsesPort) },
  })
  processes.push(responses)
  await waitFor(`http://127.0.0.1:${responsesPort}/healthz`, { process: responses })
  responsesStatsURL = `http://127.0.0.1:${responsesPort}/__e2e/stats`

  const image = process.env.TYRS_HAND_E2E_DEVELOPMENT_IMAGE ?? 'tyrs-hand-development:e2e'
  const primary = await new ControlHarness({ repoRoot, runDir: `${runDir}/primary`, label: 'primary',
    developmentImage: image, listenHost: lane === 'real-codex' ? '0.0.0.0' : '127.0.0.1' }).start()
  controls.push(primary)
  output('go', ['run', './tools/mobile-e2e/fixture', 'configure-provider', '--base-url',
    `http://host.docker.internal:${responsesPort}/v1`], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL,
      TYRS_HAND_MASTER_KEY: primary.masterKey } })
  const primaryNode = await primary.admin.createNode(`mobile-e2e-${lane}`)
  const workerImage = process.env.TYRS_HAND_E2E_WORKER_IMAGE ?? 'tyrs-hand-worker:e2e'
  if (lane === 'real-codex') {
    run('docker', ['build', '--target', 'development', '--tag', image, '.'], { cwd: repoRoot })
    run('docker', ['build', '--target', 'worker', '--tag', workerImage, '.'], { cwd: repoRoot })
  }
  const primaryFixture = await seed(primary, primaryNode, image, lane === 'protocol', 'alpha-primary')
  if (lane === 'real-codex') {
    await startRealWorker(primary, primaryNode, primaryFixture, image, workerImage)
    run('docker', ['exec', '--user', '0:0', primaryFixture.containerName, 'mkdir', '-p',
      '/var/lib/tyrs-hand/workspaces/e2e-secondary'])
    run('docker', ['exec', '--user', '0:0', primaryFixture.containerName, 'chown', '-R', '1000:1000',
      '/var/lib/tyrs-hand/workspaces/e2e-secondary'])
  }
  else await startProtocolWorker(primary, primaryNode.enrollmentToken,
    primaryFixture.environmentId, 'primary-protocol-worker')
  output('go', ['run', './tools/mobile-e2e/fixture', 'seed-project-matrix',
    '--environment-id', primaryFixture.environmentId, '--primary-name', 'alpha-primary',
    '--secondary-name', 'zeta-secondary'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } })

  const secondary = await new ControlHarness({ repoRoot, runDir: `${runDir}/secondary`, label: 'secondary' }).start()
  controls.push(secondary)
  output('go', ['run', './tools/mobile-e2e/fixture', 'configure-provider', '--base-url',
    `http://host.docker.internal:${responsesPort}/v1`], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: secondary.databaseURL,
      TYRS_HAND_MASTER_KEY: secondary.masterKey } })
  const secondaryNode = await secondary.admin.createNode('mobile-e2e-secondary')
  const secondaryFixture = await seed(secondary, secondaryNode, 'protocol-development:e2e', true,
    'beta-control')
  await startProtocolWorker(secondary, secondaryNode.enrollmentToken,
    secondaryFixture.environmentId, 'secondary-protocol-worker')

  const firstPairing = await primary.admin.createPairing()
  const secondPairing = await secondary.admin.createPairing()
  if (platform === 'android') {
    run('adb', ['-s', deviceID, 'reverse', `tcp:${primary.port}`, `tcp:${primary.port}`])
    run('adb', ['-s', deviceID, 'reverse', `tcp:${secondary.port}`, `tcp:${secondary.port}`])
  }
  await assertInstalled()
  isolateAndroidAppLinks()
  await installMediaFixture()
  const approvals = [primary.admin.approveWhenClaimed(firstPairing.id),
    secondary.admin.approveWhenClaimed(secondPairing.id)]
  const maestroEnvironment = { TYRS_HAND_E2E_PLATFORM: platform,
    TYRS_HAND_E2E_APP_ID: appID, TYRS_HAND_E2E_PAIRING_URI: firstPairing.pairingUri,
    TYRS_HAND_E2E_SECOND_PAIRING_URI: secondPairing.pairingUri }
  await Promise.all([runMaestro(maestroEnvironment), ...approvals])

  if (customFlow) {
    if (postFlow) {
      const fixtureResult = output('go', ['run', './tools/mobile-e2e/fixture', postFixture], { cwd: repoRoot,
        env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } })
      let postEnvironment = maestroEnvironment
      if (postFixture === 'notification-target') {
        const target = JSON.parse(fixtureResult)
        postEnvironment = { ...maestroEnvironment, TYRS_HAND_E2E_NOTIFICATION_URI:
          `tyrshand://notification?serverId=${target.serverId}&sessionId=${target.sessionId}` }
      }
      await runMaestro(postEnvironment, 'custom-post', postFlow)
    }
    const snapshots = controls.map((control) => JSON.parse(output('go', [
      'run', './tools/mobile-e2e/fixture', 'snapshot'], { cwd: repoRoot,
      env: { ...process.env, TYRS_HAND_DATABASE_URL: control.databaseURL } })))
    await primary.writeManifest({ lane, appID, codexVersion: lane === 'real-codex' ? '0.145.0' : null,
      controls: snapshots, flow })
    failed = false
    return
  }

  output('go', ['run', './tools/mobile-e2e/fixture', 'assert-preference',
    '--mode', 'plan', '--tier', 'fast', '--effort', 'max'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: secondary.databaseURL } })
  output('go', ['run', './tools/mobile-e2e/fixture', 'assert-session-project',
    '--text', 'E2E_PROJECT_DRAFT retained across navigation', '--project-name', 'zeta-secondary'], {
    cwd: repoRoot, env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } })
  output('go', ['run', './tools/mobile-e2e/fixture', 'assert-intent-once',
    '--session-text', 'E2E_PLAN_IDEMPOTENT prepare one plan intent',
    '--instruction', 'Implement the plan.'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: secondary.databaseURL } })
  output('go', ['run', './tools/mobile-e2e/fixture', 'assert-intent-once',
    '--session-text', 'E2E_BLOCK_IDEMPOTENT accept one stop intent', '--operation', 'interrupt'], {
    cwd: repoRoot, env: { ...process.env, TYRS_HAND_DATABASE_URL: secondary.databaseURL } })

  await runMaestro(maestroEnvironment, 'select-primary',
    resolve(repoRoot, 'client/e2e/flows/select-primary-control.yaml'))

  await primary.stopServer()
  await runMaestro(maestroEnvironment, 'offline-send',
    resolve(repoRoot, 'client/e2e/flows/offline-send.yaml'))
  await primary.startServer()
  await runMaestro(maestroEnvironment, 'offline-recover',
    resolve(repoRoot, 'client/e2e/flows/offline-recover.yaml'))
  output('go', ['run', './tools/mobile-e2e/fixture', 'assert-message-once',
    '--text', 'E2E_OFFLINE_IDEMPOTENT'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } })
  if (platform === 'android') {
    await primary.stopServer()
    await runMaestro(maestroEnvironment, 'offline-attachment-send',
      resolve(repoRoot, 'client/e2e/flows/offline-attachment-send.yaml'))
    await primary.startServer()
    await runMaestro(maestroEnvironment, 'offline-attachment-recover',
      resolve(repoRoot, 'client/e2e/flows/offline-attachment-recover.yaml'))
    output('go', ['run', './tools/mobile-e2e/fixture', 'assert-message-once',
      '--text', 'E2E_OFFLINE_ATTACHMENT_IDEMPOTENT'], { cwd: repoRoot,
      env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } })
    output('go', ['run', './tools/mobile-e2e/fixture', 'assert-attachment-once',
      '--text', 'E2E_OFFLINE_ATTACHMENT_IDEMPOTENT'], { cwd: repoRoot,
      env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } })
  }
  output('go', ['run', './tools/mobile-e2e/fixture', 'seed-history'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } })
  await runMaestro(maestroEnvironment, 'history-pagination',
    resolve(repoRoot, 'client/e2e/flows/history-pagination.yaml'))
  output('go', ['run', './tools/mobile-e2e/fixture', 'seed-forward-history'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } })
  await runMaestro(maestroEnvironment, 'forward-pagination',
    resolve(repoRoot, 'client/e2e/flows/forward-pagination.yaml'))
  output('go', ['run', './tools/mobile-e2e/fixture', 'force-cursor-reset'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } })
  await runMaestro(maestroEnvironment, 'cursor-reset',
    resolve(repoRoot, 'client/e2e/flows/cursor-reset-recover.yaml'))
  const notificationTarget = JSON.parse(output('go', ['run', './tools/mobile-e2e/fixture',
    'notification-target'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: primary.databaseURL } }))
  await runMaestro({ ...maestroEnvironment, TYRS_HAND_E2E_NOTIFICATION_URI:
    `tyrshand://notification?serverId=${notificationTarget.serverId}&sessionId=${notificationTarget.sessionId}` },
  'notification-deep-link', resolve(repoRoot, 'client/e2e/flows/notification-deep-link.yaml'))
  if (platform === 'android') {
    await runMaestro(maestroEnvironment, 'attachment-boundaries',
      resolve(repoRoot, 'client/e2e/flows/20-attachment-boundaries.yaml'))
  }
  const snapshots = controls.map((control) => JSON.parse(output('go', [
    'run', './tools/mobile-e2e/fixture', 'snapshot'], { cwd: repoRoot,
    env: { ...process.env, TYRS_HAND_DATABASE_URL: control.databaseURL } })))
  await primary.writeManifest({ lane, appID, codexVersion: lane === 'real-codex' ? '0.145.0' : null,
    controls: snapshots })
  failed = false
}

try {
  await main()
} finally {
  for (const managed of maestroProcesses.reverse()) await managed.stop()
  if (responsesStatsURL) {
    try {
      const stats = await fetch(responsesStatsURL).then((response) => response.json())
      await writeFile(`${runDir}/mock-responses-stats.json`, JSON.stringify(stats, null, 2))
    } catch { /* 上游进程提前退出时保留其已有日志 */ }
  }
  for (const managed of processes.reverse()) await managed.stop()
  for (const control of controls.reverse()) await control.stop()
  process.stderr.write(`[mobile-e2e] ${failed ? '失败证据' : '证据'}：${runDir}\n`)
}
