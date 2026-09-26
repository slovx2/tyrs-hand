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
    "  const app = path.join(derived, 'Build/Products/Release-iphonesimulator/TyrsHandDev.app');",
    "  fs.mkdirSync(app, { recursive: true }); fs.writeFileSync(path.join(app, 'TyrsHandDev'), 'fixture');",
    '}',
    "if (name === 'codesign' && args.includes('--verify')) process.exit(Number(process.env.BUILD_GATE_SIGNATURE_STATUS || 0));",
    "if (name === 'plutil') {",
    "  if (args[1] === 'CFBundleIdentifier') console.log('com.tyrshand.app.dev');",
    "  else if (args[1] === 'CFBundleExecutable') console.log('TyrsHandDev');",
    '  else {',
    "    const xml = fs.readFileSync(args.at(-1), 'utf8'); const match = xml.match(/<key>application-identifier<\\/key><string>([^<]+)<\\/string>/);",
    '    if (!match) process.exit(1); console.log(match[1]);',
    '  }',
    '}',
    "if (name === 'xcrun' && args[0] === 'lipo') { console.log(process.env.BUILD_GATE_ARCHITECTURES); process.exit(Number(process.env.BUILD_GATE_LIPO_STATUS || 0)); }",
    "if (name === 'xcrun' && args[0] === 'llvm-objdump') {",
    "  if (process.env.BUILD_GATE_SECTION === 'missing') process.exit(0);",
    "  console.log('Contents of section __TEXT,__entitlements:');",
    "  if (process.env.BUILD_GATE_SECTION === 'duplicate') console.log('Contents of section __TEXT,__entitlements:');",
    "  if (process.env.BUILD_GATE_SECTION === 'invalid') { console.log(' 10000000 not-hex  ...'); process.exit(0); }",
    "  const id = args.includes('--arch=x86_64') ? process.env.BUILD_GATE_X86_IDENTIFIER : process.env.BUILD_GATE_APP_IDENTIFIER;",
    "  const bytes = Buffer.from('<plist><dict>' + (id ? '<key>application-identifier</key><string>' + id + '</string>' : '') + '</dict></plist>');",
    '  for (let index = 0; index < bytes.length; index += 16) {',
    "    const words = bytes.subarray(index,index+16).toString('hex').match(/.{1,8}/g);",
    "    console.log(' ' + (0x10000000+index).toString(16) + ' ' + words.join(' ').padEnd(35) + '  ...');",
    '  }',
    '}',
  ].join('\n')
  for (const name of ['pnpm', 'pod', 'xcodebuild', 'codesign', 'plutil', 'xcrun']) {
    await writeFile(resolve(bin, name), stub, { mode: 0o755 })
  }
  const result = spawnSync('bash', [script, 'ios'], { encoding: 'utf8', env: { ...process.env,
    PATH: bin + ':' + process.env.PATH, BUILD_GATE_TRACE: trace,
    BUILD_GATE_APP_IDENTIFIER: options.identifier ?? 'com.tyrshand.app.dev',
    BUILD_GATE_X86_IDENTIFIER: options.x86Identifier ?? options.identifier ?? 'com.tyrshand.app.dev',
    BUILD_GATE_SECTION: options.section ?? 'valid',
    BUILD_GATE_ARCHITECTURES: options.architectures ?? 'arm64 x86_64',
    BUILD_GATE_LIPO_STATUS: String(options.lipoStatus ?? 0),
    BUILD_GATE_SIGNATURE_STATUS: String(options.signatureStatus ?? 0),
  } })
  const commands = (await readFile(trace, 'utf8')).trim().split('\n').map((line) => JSON.parse(line))
  const diagnostics = JSON.parse(await readFile(resolve(directory,
    'client/.e2e-build/ios/signing-diagnostics/status.json'), 'utf8'))
  await rm(directory, { recursive: true, force: true })
  return { result, commands, diagnostics }
}

test('iOS 模拟器构建验签并验证每个 Mach-O 架构的嵌入身份后才安装', async () => {
  const { result, commands, diagnostics } = await buildFixture()
  assert.equal(result.status, 0, result.stderr)
  const build = commands.find((command) => command.name === 'xcodebuild')
  assert.ok(build.args.includes('CODE_SIGNING_ALLOWED=YES'))
  assert.ok(build.args.includes('CODE_SIGN_IDENTITY=-'))
  const verification = commands.findIndex((command) => command.name === 'codesign' && command.args.includes('--verify'))
  const identity = commands.findIndex((command) => command.name === 'plutil')
  const install = commands.findIndex((command) => command.name === 'xcrun' && command.args.includes('install'))
  assert.ok(verification >= 0 && identity > verification && install > identity)
  assert.equal(commands.some((command) => command.name === 'codesign' && command.args.includes('--display')), false)
  assert.equal(commands.filter((command) => command.name === 'xcrun' && command.args[0] === 'llvm-objdump').length, 2)
  assert.deepEqual(diagnostics, { stage: 'verify-install', status: 'completed' })
})

test('签名失败、缺失应用身份或身份不匹配时不能安装', async () => {
  for (const options of [{ signatureStatus: 1 }, { identifier: '' }, { identifier: 'com.example.other' }]) {
    const { result, commands, diagnostics } = await buildFixture(options)
    assert.notEqual(result.status, 0)
    assert.equal(commands.some((command) => command.name === 'xcrun' && command.args.includes('install')), false)
    assert.equal(diagnostics.status, 'failed')
    assert.ok(diagnostics.exitCode > 0)
  }
})

test('任一架构身份错误不能被另一架构的正确身份掩盖', async () => {
  const { result, commands, diagnostics } = await buildFixture({ x86Identifier: 'com.example.other' })
  assert.notEqual(result.status, 0)
  assert.equal(diagnostics.stage, 'validate-identity-x86_64')
  assert.equal(commands.some((command) => command.name === 'xcrun' && command.args.includes('install')), false)
})

test('架构查询失败、空架构或未知架构必须停止安装', async () => {
  for (const options of [{ lipoStatus: 1 }, { architectures: '' }, { architectures: 'armv7' }]) {
    const { result, commands, diagnostics } = await buildFixture(options)
    assert.notEqual(result.status, 0)
    assert.equal(diagnostics.stage, 'list-architectures')
    assert.equal(commands.some((command) => command.name === 'xcrun' && command.args.includes('install')), false)
  }
})

test('缺失、重复或无效的二进制 section 必须停止安装并报告阶段', async () => {
  for (const section of ['missing', 'duplicate', 'invalid']) {
    const { result, commands, diagnostics } = await buildFixture({ section })
    assert.notEqual(result.status, 0)
    assert.equal(diagnostics.stage, 'extract-entitlements-arm64')
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
