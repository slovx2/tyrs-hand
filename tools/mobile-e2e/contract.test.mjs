import assert from 'node:assert/strict'
import { readFile, readdir } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { mobileScenarios, mobileMcpScenarios } from './lib/mcp-scenarios.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')

async function filesBelow(directory) {
  const result = []
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (entry.isDirectory() && ['node_modules', 'android', 'ios', 'dist', '.expo', '.e2e-build'].includes(entry.name)) {
      continue
    }
    const path = resolve(directory, entry.name)
    if (entry.isDirectory()) result.push(...await filesBelow(path))
    else result.push(path)
  }
  return result
}

test('Maestro 与运行时依赖全部固定', async () => {
  const installer = await readFile(resolve(root, 'tools/mobile-e2e/install-maestro.sh'), 'utf8')
  assert.match(installer, /version="2\.3\.0"/)
  assert.match(installer, /aaf524c6bcd456013855b1337464f964d9a65e2fb88861affea9b4c014644e50/)
  const control = await readFile(resolve(root, 'tools/mobile-e2e/lib/control.mjs'), 'utf8')
  assert.match(control, /postgres:18\.3-bookworm@sha256:[0-9a-f]{64}/)
  assert.match(control, /redis:8\.4\.0-bookworm@sha256:[0-9a-f]{64}/)
  const dependencies = JSON.parse(await readFile(
    resolve(root, 'deploy/worker/dependencies.json'), 'utf8'))
  assert.equal(dependencies.codexMinimumVersion, '0.147.0')
  const native = await readFile(resolve(root, 'tools/mobile-e2e/install-native-services.sh'), 'utf8')
  assert.match(native, /d95663fbbf3a80f81a9d98d895266bdcb74ba274bcc04ef6d76630a72dee016f/)
  assert.match(native, /ca909aa15252f2ecb3a048cd086469827d636bf8334f50bb94d03fba4bfc56e8/)
  assert.match(native, /967311f84955316969bdb1d8d4b983718ef42338639c621ec4c34fddef355e99/)
  assert.match(native, /contrib\/pgcrypto install/)
})

test('所有 Flow 只用稳定 ID 操作生产 UI', async () => {
  const flowFiles = (await filesBelow(resolve(root, 'client/e2e/flows')))
    .filter((path) => path.endsWith('.yaml'))
  assert.ok(flowFiles.length >= 8)
  const flows = (await Promise.all(flowFiles.map((path) => readFile(path, 'utf8')))).join('\n')
  const sourceFiles = (await filesBelow(resolve(root, 'client')))
    .filter((path) => /\.(tsx?|mjs)$/.test(path) && !path.includes('/e2e/'))
  const source = (await Promise.all(sourceFiles.map((path) => readFile(path, 'utf8')))).join('\n')
  const externalIDs = new Set([
    'com.google.android.providers.media.module:id/icon_thumbnail',
    'com.google.android.providers.media.module:id/button_add',
  ])
  const ids = [...flows.matchAll(/id:\s*"([^"]+)"/g)].map((match) => match[1])
  assert.ok(ids.length > 30)
  for (const id of ids) {
    const normalized = id.replace(':.*', '').replace(/:\d+$/, '')
    const parts = normalized.split(':')
    const candidates = [normalized]
    while (parts.length > 1) {
      parts.pop()
      candidates.push(`${parts.join(':')}:`)
    }
    assert.ok(externalIDs.has(id) || candidates.some((candidate) => source.includes(candidate)),
      `Flow ID 没有生产端契约：${id}`)
  }
  assert.doesNotMatch(flows, /tapOn:\s*\n\s*text:/)
})

test('默认 suite 使用真实双引擎并验证计划、权限和审批', async () => {
  const runner = await readFile(resolve(root, 'tools/mobile-e2e/mobile-runner.mjs'), 'utf8')
  const worker = await readFile(resolve(root, 'tools/mobile-e2e/lib/worker.mjs'), 'utf8')
  const suite = await readFile(resolve(root, 'client/e2e/flows/suite.yaml'), 'utf8')
  const flow = await readFile(resolve(root, 'client/e2e/flows/dual-engine-suite.yaml'), 'utf8')
  const setup = await readFile(resolve(root, 'client/e2e/flows/_shared/dual-engine-ssh-setup.yaml'), 'utf8')
  const mcpFlow = await readFile(resolve(root, 'client/e2e/flows/_shared/mcp-suite.yaml'), 'utf8')
  assert.match(suite, /dual-engine-suite\.yaml/)
  assert.match(runner, /new WorkerHarness/)
  assert.match(runner, /models\.verify/)
  assert.doesNotMatch(runner, /protocol-worker|seed-automations|MockRuntime/)
  assert.match(worker, /cmd\/tyrs-hand-worker/)
  assert.match(worker, /inheritEnv: false/)
  assert.match(worker, /sandbox-exec/)
  assert.match(worker, /--net/)
  assert.equal(mobileScenarios.length, 12)
  for (const marker of mobileScenarios) {
    assert.ok((setup + flow + mcpFlow).includes(marker), marker + ' 必须经过 GUI')
  }
  assert.match(runner, /import \{ mobileScenarios \} from '\.\/lib\/mcp-scenarios\.mjs'/)
  assert.match(runner, /await models\.verify\(mobileScenarios\)/, '所有 GUI 场景必须核对模型和副作用')
  assert.match(flow, /runFlow: _shared\/mcp-suite\.yaml/)
  assert.deepEqual(Object.values(mobileMcpScenarios).map((entry) => entry.mode + ':' + entry.action),
    ['form:accept', 'form:decline', 'form:cancel', 'url:accept', 'url:decline', 'url:cancel'])
  assert.match(flow, /interactive:.*:accept/)
  assert.match(flow, /interactive:.*:decline/)
  assert.match(flow, /MODE: "plan"/)
  assert.match(flow, /PERMISSIONS: "danger-full-access"/)
  assert.match(flow, /stopApp/)
  assert.match(setup, /clearState: true/)
  assert.doesNotMatch(flow, /clearState: true/, '第二阶段必须保留已完成的真实 SSH 客户端状态')
  assert.match(flow, /TYRS_HAND_E2E_SSH_SETUP_DONE/)
  const prepared = runner.indexOf("await runMaestro(setupEnvironment, 'ssh-setup'")
  assert.ok(prepared >= 0 && runner.indexOf('createPairing(') > prepared,
    '真实配对链接必须在前置 SSH GUI 成功后生成，不能消耗其十分钟有效期')
  assert.match(runner, /approveWhenClaimed\(firstPairing.id, 600_000/, '配对等待仍须有界')
})
