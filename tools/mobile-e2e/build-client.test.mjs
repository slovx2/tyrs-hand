import assert from 'node:assert/strict'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')

// 只验证构建脚本的安装门禁；替身命令不能作为真实 iOS GUI 或 Keychain 验收。
async function buildFixture(options = {}) {
  const directory = await mkdtemp(resolve(tmpdir(), 'mobile-signing-gate-'))
  const bin = resolve(directory, 'bin')
  const script = resolve(directory, 'tools/mobile-e2e/build-client.sh')
  const trace = resolve(directory, 'commands.jsonl')
  await mkdir(bin, { recursive: true })
  await mkdir(dirname(script), { recursive: true })
  await mkdir(resolve(directory, 'client/ios/TyrsHandDev.xcworkspace'), { recursive: true })
  await writeFile(script, await readFile(resolve(root, 'tools/mobile-e2e/build-client.sh')))
  const stub = [
    '#!/usr/bin/env node',
    "const fs = require('node:fs'); const path = require('node:path');",
    'const name = path.basename(process.argv[1]); const args = process.argv.slice(2);',
    "fs.appendFileSync(process.env.BUILD_GATE_TRACE, JSON.stringify({ name, args }) + '\\n');",
    "if (name === 'pnpm' && args[0] === '--version') console.log('11.14.0');",
    "if (name === 'pod' && args.includes('--version')) console.log('1.16.2');",
    "if (name === 'xcodebuild') {",
    "  const derived = args[args.indexOf('-derivedDataPath') + 1];",
    "  fs.mkdirSync(path.join(derived, 'Build/Products/Release-iphonesimulator/TyrsHandDev.app'), { recursive: true });",
    '}',
    "if (name === 'codesign' && args.includes('--verify')) process.exit(Number(process.env.BUILD_GATE_SIGNATURE_STATUS || 0));",
    "if (name === 'codesign' && args.includes('--display')) console.log('<plist><dict/></plist>');",
    "if (name === 'plutil') {",
    '  if (!process.env.BUILD_GATE_APP_IDENTIFIER) process.exit(1);',
    '  console.log(process.env.BUILD_GATE_APP_IDENTIFIER);',
    '}',
  ].join('\n')
  for (const name of ['pnpm', 'pod', 'xcodebuild', 'codesign', 'plutil', 'xcrun']) {
    await writeFile(resolve(bin, name), stub, { mode: 0o755 })
  }
  const result = spawnSync('bash', [script, 'ios'], { encoding: 'utf8', env: { ...process.env,
    PATH: bin + ':' + process.env.PATH, BUILD_GATE_TRACE: trace,
    BUILD_GATE_APP_IDENTIFIER: options.identifier ?? 'com.tyrshand.app.dev',
    BUILD_GATE_SIGNATURE_STATUS: String(options.signatureStatus ?? 0),
  } })
  const commands = (await readFile(trace, 'utf8')).trim().split('\n').map((line) => JSON.parse(line))
  await rm(directory, { recursive: true, force: true })
  return { result, commands }
}

test('iOS 模拟器构建启用签名并在验证应用身份后才安装', async () => {
  const { result, commands } = await buildFixture()
  assert.equal(result.status, 0, result.stderr)
  const build = commands.find((command) => command.name === 'xcodebuild')
  assert.ok(build.args.includes('CODE_SIGNING_ALLOWED=YES'))
  assert.ok(build.args.includes('CODE_SIGN_IDENTITY=-'))
  const verification = commands.findIndex((command) => command.name === 'codesign' && command.args.includes('--verify'))
  const identity = commands.findIndex((command) => command.name === 'plutil')
  const install = commands.findIndex((command) => command.name === 'xcrun' && command.args.includes('install'))
  assert.ok(verification >= 0 && identity > verification && install > identity)
})

test('签名失败、缺失应用身份或身份不匹配时不能安装', async () => {
  for (const options of [{ signatureStatus: 1 }, { identifier: '' }, { identifier: 'com.example.other' }]) {
    const { result, commands } = await buildFixture(options)
    assert.notEqual(result.status, 0)
    assert.equal(commands.some((command) => command.name === 'xcrun' && command.args.includes('install')), false)
  }
})

test('接受包含前缀的同一应用签名身份', async () => {
  const { result } = await buildFixture({ identifier: 'EXAMPLE123.com.tyrshand.app.dev' })
  assert.equal(result.status, 0, result.stderr)
})

test('移动预检报告写入已忽略制品目录以保持源码门禁有效', async () => {
  const workflow = await readFile(resolve(root, '.github/workflows/mobile-e2e.yml'), 'utf8')
  const paths = [...workflow.matchAll(/make test-mobile-runtime-e2e > ([^\s]+)/g)].map((match) => match[1])
  assert.equal(paths.length, 2)
  for (const path of paths) {
    assert.equal(path, '.artifacts/mobile-runtime/runtime-junit.xml')
    assert.equal(execFileSync('git', ['check-ignore', path], { cwd: root, encoding: 'utf8' }).trim(), path)
  }
  assert.doesNotMatch(workflow, /^\s+mobile-runtime-junit\.xml$/m)
})
