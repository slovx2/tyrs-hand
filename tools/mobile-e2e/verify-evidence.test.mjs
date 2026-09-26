import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import test from 'node:test'
import { verifyMobileEvidence } from './verify-evidence.mjs'
import { mobileScenarios, mobileMcpScenarios, mobileMcpAnswer } from './lib/mcp-scenarios.mjs'

const commit = 'a'.repeat(40)
const markers = mobileScenarios
const mcpResults = Object.fromEntries(Object.keys(mobileMcpScenarios).map((marker) => {
  const { action, content } = mobileMcpAnswer(marker)
  return [marker, { action, content }]
}))
const save = (path, value) => writeFile(path, JSON.stringify(value))

// 仅测试证据门禁的拒绝规则；合成内容不得用于主链路协议验收。
async function fixture(directory, platform, override = {}) {
  const root = resolve(directory, platform)
  await mkdir(resolve(root, 'worker'), { recursive: true })
  await save(resolve(root, 'mobile-acceptance.json'), {
    platform, commit, dirty: false, passed: true, schemaPassed: true,
    maestroPhases: ['ssh-setup', 'suite'],
    clientBuild: 'fixture', adapterCommit: 'b'.repeat(40),
    nativeBuild: { cliSHA256: 'c'.repeat(64), sdkVersion: '0.3.282', cliVersion: '2.1.282' },
    ...override,
  })
  await save(resolve(root, 'cleanup-report.json'), { primaryError: null, cleanupErrors: [] })
  await save(resolve(root, 'model-assertions.json'), { passed: true, expected: markers, completed: markers, mcpResults })
  await mkdir(resolve(root, 'screenshots'), { recursive: true })
  for (const marker of Object.keys(mobileMcpScenarios)) {
    for (const phase of ['pending', 'answered']) {
      await writeFile(resolve(root, 'screenshots', marker + '-' + phase + '.png'),
        Buffer.from('89504e470d0a1a0a', 'hex'))
    }
  }
  const engine = { messages: 1, pendingRequests: [], pendingCallbacks: [],
    semantics: { passed: true, scenarios: Object.fromEntries(markers.map((marker) => [marker, {}])) } }
  await save(resolve(root, 'worker/schema-report.json'), {
    passed: true, errors: [], engines: { codex: engine, 'claude-code': engine },
  })
  for (const name of ['codex', 'claude-code']) {
    await save(resolve(root, 'worker/wire-' + name + '.jsonl'), { fixture: true })
    await save(resolve(root, 'model-' + name + '.json'), { requests: [{}], unexpected: [] })
  }
  for (const phase of ['ssh-setup', 'suite']) {
    await writeFile(resolve(root, 'junit-' + phase + '.xml'), '<testsuite><testcase name="fixture"/></testsuite>')
  }
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

test('缺失 MCP 完成、类型失真或缺少点击前后截图均不能通过双平台门禁', async () => {
  const root = await mkdtemp(resolve(tmpdir(), 'mobile-mcp-evidence-gate-'))
  const marker = 'MOBILE_CLAUDE_MCP_FORM_ACCEPT'
  try {
    await fixture(root, 'android')
    const ios = await fixture(root, 'ios')
    await save(resolve(ios, 'model-assertions.json'), { passed: true, expected: markers,
      completed: markers.filter((entry) => entry !== marker), mcpResults })
    await assert.rejects(verifyMobileEvidence(root, commit), /缺少必需模型与文件副作用/)
    const wrong = structuredClone(mcpResults)
    wrong[marker].content.count = '3'
    await save(resolve(ios, 'model-assertions.json'), { passed: true, expected: markers,
      completed: markers, mcpResults: wrong })
    await assert.rejects(verifyMobileEvidence(root, commit), /准确类型的 MCP/)
    await fixture(root, 'ios')
    await rm(resolve(ios, 'screenshots', marker + '-answered.png'))
    await assert.rejects(verifyMobileEvidence(root, commit), /ENOENT/)
  } finally { await rm(root, { recursive: true, force: true }) }
})

test('SSH 准备阶段缺失、失败或跳过均不能被业务阶段成功掩盖', async () => {
  const root = await mkdtemp(resolve(tmpdir(), 'mobile-phase-gate-'))
  try {
    await fixture(root, 'android')
    const ios = await fixture(root, 'ios')
    await rm(resolve(ios, 'junit-ssh-setup.xml'))
    await assert.rejects(verifyMobileEvidence(root, commit), /ENOENT/)
    for (const result of ['failure', 'error', 'skipped']) {
      await writeFile(resolve(ios, 'junit-ssh-setup.xml'),
        `<testsuite><testcase name="ssh"><${result}/></testcase></testsuite>`)
      await assert.rejects(verifyMobileEvidence(root, commit), /不能失败或跳过：ssh-setup/)
    }
    await fixture(root, 'ios', { maestroPhases: ['suite'] })
    await assert.rejects(verifyMobileEvidence(root, commit), /两个 GUI 阶段/)
  } finally {
    await rm(root, { recursive: true, force: true })
  }
})

test('成功清单不能覆盖 GUI 首因或清理失败', async () => {
  const root = await mkdtemp(resolve(tmpdir(), 'mobile-cleanup-evidence-'))
  try {
    await fixture(root, 'android')
    const ios = await fixture(root, 'ios')
    await save(resolve(ios, 'cleanup-report.json'), { primaryError: 'Maestro失败', cleanupErrors: [] })
    await assert.rejects(verifyMobileEvidence(root, commit), /GUI 原始失败/)
    await save(resolve(ios, 'cleanup-report.json'), { primaryError: null,
      cleanupErrors: [{ name: 'worker', error: '日志保存失败' }] })
    await assert.rejects(verifyMobileEvidence(root, commit), /清理异常/)
  } finally { await rm(root, { recursive: true, force: true }) }
})
