import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { cp, mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { guiAutomationEnabled } from './gui-policy.mjs'
import { verifyMobileEvidence } from './verify-evidence.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')

test('Android 自动化入口写入 skipped 用例，不能安装设备或代替手工成功证据', async t => {
  const directory = await mkdtemp(resolve(tmpdir(), 'mobile-gui-skip-'))
  t.after(() => rm(directory, { recursive: true, force: true }))
  const scripts = resolve(directory, 'tools/mobile-e2e')
  await mkdir(scripts, { recursive: true })
  for (const name of ['run.sh', 'gui-policy.mjs', 'acceptance-policy.json']) {
    await cp(resolve(root, 'tools/mobile-e2e', name), resolve(scripts, name))
  }
  for (const name of ['install-maestro.sh', 'build-client.sh']) {
    await writeFile(resolve(scripts, name), ['#!/bin/sh', 'echo 不得执行安装 >&2', 'exit 99', ''].join(String.fromCharCode(10)), { mode: 0o755 })
  }
  const result = spawnSync('bash', [resolve(scripts, 'run.sh'), 'android', '--install-only'], { encoding: 'utf8' })
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stderr, /自动化 GUI: SKIP；手工 GUI: PENDING/)
  const artifacts = resolve(directory, '.artifacts/mobile-runtime')
  const report = JSON.parse(await readFile(resolve(artifacts, 'android-gui-skip.json'), 'utf8'))
  assert.equal(report.automatedGui, 'skipped')
  assert.equal(report.manualGuiRequired, true)
  assert.equal(report.manualGui, 'pending')
  assert.equal(report.mobileAcceptanceComplete, false)
  assert.equal(report.releaseReady, false)
  const junit = await readFile(resolve(artifacts, 'android-gui-junit.xml'), 'utf8')
  assert.equal([...junit.matchAll(/<testcase /g)].length, 2)
  assert.equal([...junit.matchAll(/<skipped /g)].length, 2)
  await assert.rejects(verifyMobileEvidence(artifacts, 'a'.repeat(40)), /成功证据/)
  await writeFile(resolve(scripts, 'acceptance-policy.json'), '{invalid')
  const invalid = spawnSync('bash', [resolve(scripts, 'run.sh'), 'android'], { encoding: 'utf8' })
  assert.notEqual(invalid.status, 0, '策略损坏必须拒绝执行')
  assert.notEqual(invalid.status, 99, '策略读取失败不能继续安装')
})

test('Android 自动化 skip 不改变 iOS 策略或伪造 iOS 结果', async t => {
  const directory = await mkdtemp(resolve(tmpdir(), 'mobile-gui-ios-'))
  t.after(() => rm(directory, { recursive: true, force: true }))
  assert.equal(await guiAutomationEnabled('ios', directory), true)
  assert.deepEqual(await readdir(directory), [])
  await assert.rejects(guiAutomationEnabled('unknown', directory), /未知移动 GUI/)
})
