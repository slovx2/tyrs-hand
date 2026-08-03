import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { readFile, readdir } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

import { freePort, waitFor } from './lib/process.mjs'

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
  const runner = await readFile(resolve(root, 'tools/mobile-e2e/mobile-runner.mjs'), 'utf8')
  assert.match(runner, /codex-cli 0\.145\.0/)
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

test('真实 Codex 交互场景显式使用 Plan 模式', async () => {
  for (const flow of ['03-interactive.yaml', '09-secret-and-failure.yaml']) {
    const content = await readFile(resolve(root, 'client/e2e/flows', flow), 'utf8')
    const planIndex = content.indexOf('id: "parameters:mode:plan"')
    const taskIndex = content.indexOf('file: _shared/create-task.yaml')
    assert.ok(planIndex >= 0 && planIndex < taskIndex, `${flow} 必须在创建任务前选择 Plan 模式`)
  }
  const secretFlow = await readFile(resolve(root,
    'client/e2e/flows/09-secret-and-failure.yaml'), 'utf8')
  assert.equal([...secretFlow.matchAll(/id: "connection:inactive"/g)].length, 2,
    'Secret 场景必须切到 protocol Control，并在失败态场景前切回 real-codex Control')
})

test('确定性 Responses 上游可产生 SSE 并记录实际参数', async () => {
  const port = await freePort()
  const child = spawn('node', ['tools/mobile-e2e/mock-responses.mjs'], {
    cwd: root, env: { ...process.env, TYRS_HAND_E2E_RESPONSES_PORT: String(port) },
    stdio: 'ignore',
  })
  try {
    await waitFor(`http://127.0.0.1:${port}/healthz`)
    const response = await fetch(`http://127.0.0.1:${port}/v1/responses`, {
      method: 'POST', headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ model: 'gpt-5.6-sol', service_tier: 'priority',
        input: 'E2E_BASIC literal function_call_output is not an answer' }),
    })
    assert.equal(response.status, 200)
    assert.match(await response.text(), /response\.output_text\.delta/)
    await fetch(`http://127.0.0.1:${port}/v1/responses`, {
      method: 'POST', headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ model: 'gpt-5.6-sol', input: [{ type: 'function_call_output',
        call_id: 'answer-1', output: '{"answers":{"choice":"继续"}}' }] }),
    })
    const stats = await fetch(`http://127.0.0.1:${port}/__e2e/stats`).then((value) => value.json())
    assert.equal(stats.requests[0].model, 'gpt-5.6-sol')
    assert.equal(stats.requests[0].serviceTier, 'priority')
    assert.equal(stats.requests[0].hasFunctionOutput, false)
    assert.equal(stats.requests[1].hasFunctionOutput, true)
    assert.deepEqual(stats.requests[1].inputTypes, ['function_call_output'])
    assert.equal('body' in stats.requests[0], false)
  } finally {
    child.kill('SIGTERM')
  }
})
