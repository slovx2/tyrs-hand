import assert from 'node:assert/strict'
import { readFile, readdir, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const required = ['MOBILE_CODEX_CHAT', 'MOBILE_CLAUDE_CHAT', 'MOBILE_CLAUDE_FULL',
  'MOBILE_CLAUDE_APPROVAL', 'MOBILE_CLAUDE_DENY', 'MOBILE_CLAUDE_PLAN']
const json = async (path) => JSON.parse(await readFile(path, 'utf8'))

async function manifestsBelow(directory) {
  const paths = []
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = resolve(directory, entry.name)
    if (entry.isDirectory()) paths.push(...await manifestsBelow(path))
    else if (entry.name === 'mobile-acceptance.json') paths.push(path)
  }
  return paths
}

export async function verifyMobileEvidence(directory, commit) {
  assert.match(commit, /^[0-9a-f]{40}$/, '必须指定被验收的提交')
  const reports = []
  for (const path of await manifestsBelow(directory)) {
    const manifest = await json(path)
    assert.equal(manifest.commit, commit, '不能拼接不同提交的验收结果')
    assert.equal(manifest.dirty, false, '发布证据必须来自已提交的源码')
    assert.equal(manifest.passed, true)
    assert.equal(manifest.schemaPassed, true)
    assert.ok(['android', 'ios'].includes(manifest.platform))
    assert.ok(manifest.clientBuild, '必须记录安装版客户端版本')
    assert.match(manifest.nativeBuild?.cliSHA256 ?? '', /^[0-9a-f]{64}$/)
    const root = dirname(path)
    const model = await json(resolve(root, 'model-assertions.json'))
    assert.equal(model.passed, true)
    for (const marker of required) {
      assert.ok(model.expected.includes(marker) && model.completed.includes(marker),
        manifest.platform + ' 缺少必需模型与文件副作用断言：' + marker)
    }
    const schema = await json(resolve(root, 'worker/schema-report.json'))
    assert.equal(schema.passed, true)
    assert.deepEqual(schema.errors, [])
    for (const engine of ['codex', 'claude-code']) {
      assert.ok(schema.engines[engine].messages > 0)
      assert.deepEqual(schema.engines[engine].pendingRequests, [])
      assert.deepEqual(schema.engines[engine].pendingCallbacks, [])
      assert.equal(schema.engines[engine].semantics?.passed, true, '缺少真实审批回答和计划执行顺序断言')
      for (const marker of required.filter((value) => value.includes('CODEX') === (engine === 'codex'))) {
        assert.ok(schema.engines[engine].semantics.scenarios[marker], '缺少真实 wire 场景：' + marker)
      }
      assert.ok((await readFile(resolve(root, 'worker/wire-' + engine + '.jsonl'), 'utf8')).trim())
      const requests = await json(resolve(root, 'model-' + engine + '.json'))
      assert.ok(requests.requests.length > 0)
      assert.deepEqual(requests.unexpected, [])
    }
    const junit = await readFile(resolve(root, 'junit-suite.xml'), 'utf8')
    assert.match(junit, /<testcase[\s>]/, '必须保留实际 Maestro 用例报告')
    assert.doesNotMatch(junit, /<(?:skipped|failure|error)[\s/>]/,
      '必需 GUI 用例不能失败或跳过')
    reports.push(manifest)
  }
  assert.deepEqual(reports.map((item) => item.platform).sort(), ['android', 'ios'],
    '双平台完整验收必须同时提供 Android 与 iOS，各一份成功证据')
  assert.equal(reports[0].adapterCommit, reports[1].adapterCommit)
  assert.equal(reports[0].nativeBuild.sdkVersion, reports[1].nativeBuild.sdkVersion)
  assert.equal(reports[0].nativeBuild.cliVersion, reports[1].nativeBuild.cliVersion)
  return { commit, mobileDualEnginePassed: true, completeProtocolMatrix: false, platforms: reports }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const directory = resolve(process.argv[2] ?? '.artifacts/mobile-ci')
  const report = await verifyMobileEvidence(directory, process.argv[3] ?? process.env.GITHUB_SHA)
  await writeFile(resolve(directory, 'mobile-coverage.json'), JSON.stringify(report, null, 2))
  process.stdout.write('Android 与 iOS 必需移动场景通过；完整协议与桌面 GUI 仍须单独验收。\n')
}
