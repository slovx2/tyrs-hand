import assert from 'node:assert/strict'
import { mkdtemp, mkdir, readFile, readdir, rm, symlink, utimes, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import test from 'node:test'
import { collectIosDriverDiagnostics } from './lib/ios-driver-diagnostics.mjs'

test('iOS 驱动诊断只保留本 phase 日志、指定模拟器并移除敏感输入', async (t) => {
  const root = await mkdtemp(resolve(tmpdir(), 'tyrs-ios-diagnostic-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const logDirectory = resolve(root, 'native-logs')
  await mkdir(logDirectory)
  const startedAt = Date.now() - 1000
  await writeFile(resolve(logDirectory, 'xctest_runner_2026-09-26_100000.log'), [
    'XCTest runner failed to launch', 'token temporary-secret',
    'Input text hidden', 'Inputting text hidden', 'text=hidden',
    'device-pair hidden', '-----BEGIN OPENSSH PRIVATE KEY-----', 'PRIVATE_KEY=hidden',
  ].join('\n'))
  const old = resolve(logDirectory, 'xctest_runner_2026-09-25_100000.log')
  await writeFile(old, '旧 run 不得读取')
  await utimes(old, new Date(startedAt - 60_000), new Date(startedAt - 60_000))
  await symlink(old, resolve(logDirectory, 'xctest_runner_2026-09-26_110000.log'))
  const command = async (executable, args) => {
    if (args.includes('devices')) return JSON.stringify({ devices: { ios: [
      { udid: 'owned-device', state: 'Booted' }, { udid: 'other-device', state: 'Booted' },
    ] } })
    if (args.includes('launchctl')) return '10 0 maestro-driver\n20 0 unrelated-app\n'
    return `${executable} configured\nInput text hidden\n`
  }
  const report = await collectIosDriverDiagnostics({ runDir: root, label: 'setup', startedAt,
    deviceID: 'owned-device', secrets: ['temporary-secret'], logDirectory, command })
  assert.deepEqual(report.errors, [])
  assert.deepEqual(report.files, ['xctest_runner_2026-09-26_100000.log'])
  const destination = resolve(root, 'logs/ios-driver-setup')
  const native = await readFile(resolve(destination, report.files[0]), 'utf8')
  assert.match(native, /XCTest runner failed to launch/)
  assert.match(native, /\[REDACTED\]/)
  assert.doesNotMatch(native, /temporary-secret|hidden|OPENSSH|PRIVATE_KEY/)
  assert.doesNotMatch(await readFile(resolve(destination, 'device.log'), 'utf8'), /other-device/)
  assert.doesNotMatch(await readFile(resolve(destination, 'driver-processes.log'), 'utf8'), /unrelated/)
  assert.equal((await readdir(destination)).length, 7)
})

test('缺失原生日志和诊断命令失败单列记录并脱敏', async (t) => {
  const root = await mkdtemp(resolve(tmpdir(), 'tyrs-ios-diagnostic-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const report = await collectIosDriverDiagnostics({ runDir: root, label: 'setup',
    startedAt: Date.now(), deviceID: 'owned-device', secrets: ['temporary-secret'],
    logDirectory: resolve(root, 'missing'), command: async () => {
      throw new Error('诊断失败 temporary-secret\nInputting text hidden')
    } })
  assert.equal(report.errors.length, 6)
  const saved = await readFile(resolve(root, 'logs/ios-driver-setup/report.json'), 'utf8')
  assert.doesNotMatch(saved, /temporary-secret|Inputting text|hidden/)
  assert.match(saved, /\[REDACTED\]/)
})
