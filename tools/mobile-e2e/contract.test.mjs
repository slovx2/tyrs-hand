import assert from 'node:assert/strict'
import { readFile, readdir } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

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
  assert.equal(dependencies.codexVersion, '0.147.0')
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

test('协议 Worker 交互场景显式使用 Plan 模式', async () => {
  for (const flow of ['03-interactive.yaml', '09-secret-and-failure.yaml']) {
    const content = await readFile(resolve(root, 'client/e2e/flows', flow), 'utf8')
    const planIndex = content.indexOf('id: "parameters:mode:plan"')
    const taskIndex = content.indexOf('file: _shared/create-task.yaml')
    assert.ok(planIndex >= 0 && planIndex < taskIndex, `${flow} 必须在创建任务前选择 Plan 模式`)
  }
  const secretFlow = await readFile(resolve(root,
    'client/e2e/flows/09-secret-and-failure.yaml'), 'utf8')
  assert.equal([...secretFlow.matchAll(/id: "connection:inactive"/g)].length, 2,
    'Secret 场景必须切到次 Control，并在失败态场景前切回主 Control')
})

test('Mobile E2E fixture 只承载协议 25 官方 JSON-RPC', async () => {
  const [worker, runner, fixture, protocol] = await Promise.all([
    readFile(resolve(root, 'tools/mobile-e2e/protocol-worker/main.go'), 'utf8'),
    readFile(resolve(root, 'tools/mobile-e2e/mobile-runner.mjs'), 'utf8'),
    readFile(resolve(root, 'tools/mobile-e2e/fixture/main.go'), 'utf8'),
    readFile(resolve(root, 'internal/workerprotocol/types.go'), 'utf8'),
  ])
  assert.match(protocol, /const Version = 25/)
  assert.match(runner, /go', \['run', '\.\/tools\/mobile-e2e\/protocol-worker'/)
  for (const method of ['initialize', 'thread/list', 'thread/read', 'thread/resume',
    'thread/start', 'turn/start', 'turn/steer', 'turn/interrupt']) {
    assert.ok(worker.includes(`"${method}"`), `fixture 缺少官方方法 ${method}`)
  }
  assert.match(worker, /ClaimAppServerTunnel/)
  assert.match(worker, /ClaimMaterialization/)
  assert.match(fixture, /official_thread_projections/)
  assert.doesNotMatch(`${worker}\n${runner}\n${fixture}`,
    /workspace_sessions|session_messages|codex_interactive_requests|commands\/ack|\/runs\//)
})

test('E2E 断言保持 profile 隔离并清理完整进程树', async () => {
  const [runner, processHelper] = await Promise.all([
    readFile(resolve(root, 'tools/mobile-e2e/mobile-runner.mjs'), 'utf8'),
    readFile(resolve(root, 'tools/mobile-e2e/lib/process.mjs'), 'utf8'),
  ])
  const attachmentAssertion = runner.slice(runner.indexOf("'assert-attachment-once'"),
    runner.indexOf("'assert-attachment-once'") + 400)
  assert.match(attachmentAssertion, /TYRS_HAND_DATABASE_URL: primary\.databaseURL/)
  assert.match(processHelper, /detached: isolatedProcessGroup/)
  assert.match(processHelper, /process\.kill\(-child\.pid, signal\)/)
})
