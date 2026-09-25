import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import test from 'node:test'
import { verifyMobileEvidence } from './verify-evidence.mjs'

const commit = 'a'.repeat(40)
const markers = ['MOBILE_CODEX_CHAT', 'MOBILE_CLAUDE_CHAT', 'MOBILE_CLAUDE_FULL',
  'MOBILE_CLAUDE_APPROVAL', 'MOBILE_CLAUDE_DENY', 'MOBILE_CLAUDE_PLAN']
const save = (path, value) => writeFile(path, JSON.stringify(value))

// 仅测试证据门禁的拒绝规则；合成内容不得用于主链路协议验收。
async function fixture(directory, platform, override = {}) {
  const root = resolve(directory, platform)
  await mkdir(resolve(root, 'worker'), { recursive: true })
  await save(resolve(root, 'mobile-acceptance.json'), {
    platform, commit, dirty: false, passed: true, schemaPassed: true,
    clientBuild: 'fixture', adapterCommit: 'b'.repeat(40),
    nativeBuild: { cliSHA256: 'c'.repeat(64), sdkVersion: '0.3.282', cliVersion: '2.1.282' },
    ...override,
  })
  await save(resolve(root, 'model-assertions.json'), { passed: true, expected: markers, completed: markers })
  const engine = { messages: 1, pendingRequests: [], pendingCallbacks: [] }
  await save(resolve(root, 'worker/schema-report.json'), {
    passed: true, errors: [], engines: { codex: engine, 'claude-code': engine },
  })
  for (const name of ['codex', 'claude-code']) {
    await save(resolve(root, 'worker/wire-' + name + '.jsonl'), { fixture: true })
    await save(resolve(root, 'model-' + name + '.json'), { requests: [{}], unexpected: [] })
  }
  await writeFile(resolve(root, 'junit-suite.xml'), '<testsuite><testcase name="fixture"/></testsuite>')
  return root
}

test('移动门禁要求两平台、相同提交且没有必需用例跳过', async () => {
  const root = await mkdtemp(resolve(tmpdir(), 'mobile-evidence-gate-'))
  try {
    await assert.rejects(verifyMobileEvidence(root, commit), /Android 与 iOS/)
    await fixture(root, 'android')
    await assert.rejects(verifyMobileEvidence(root, commit), /Android 与 iOS/)
    const ios = await fixture(root, 'ios')
    assert.equal((await verifyMobileEvidence(root, commit)).mobileDualEnginePassed, true)
    await writeFile(resolve(ios, 'junit-suite.xml'), '<testsuite><testcase name="plan"><skipped/></testcase></testsuite>')
    await assert.rejects(verifyMobileEvidence(root, commit), /不能失败或跳过/)
    await fixture(root, 'ios', { commit: 'd'.repeat(40) })
    await assert.rejects(verifyMobileEvidence(root, commit), /不同提交/)
    await fixture(root, 'ios', { dirty: true })
    await assert.rejects(verifyMobileEvidence(root, commit), /已提交的源码/)
    await fixture(root, 'ios')
    await save(resolve(ios, 'model-assertions.json'), {
      passed: true, expected: markers, completed: markers.filter((marker) => !marker.endsWith('_PLAN')),
    })
    await assert.rejects(verifyMobileEvidence(root, commit), /MOBILE_CLAUDE_PLAN/)
  } finally {
    await rm(root, { recursive: true, force: true })
  }
})
