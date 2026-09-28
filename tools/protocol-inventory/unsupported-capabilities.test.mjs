// 占位用例 UNSUPPORTED-NO-GATE：用户明确决定不支持的能力，不做门禁。
//
// 用户 2026-09-29 决定以下 Codex 能力明确不支持、去掉门禁，不写专项，也不模拟外部服务：
// - 需要 ChatGPT 账户登录态的方法：账户用量、限额、工作区消息、重置额度、充值提醒、插件分享与远端插件技能，
//   以及随 ChatGPT 令牌恢复产生的 provider 认证恢复通知；
// - realtime 语音会话条目；
// - feedback/upload（上传至外部 Sentry）；
// - Touch ID 用户验证的登记、删除与校验（Linux Worker 上不可用）。
// 运行时行为不因此改变：Hub 仍将这些方法原样交给固定版本的 Codex，由原生决定成功或报错；
// 这里只表示本系统不为其提供验收保证。
//
// 本文件是空实现的占位：不驱动任何通信，只校验登记自洽，防止占位被用于未经决定的能力。
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { protocolCoverage, unsupportedCapabilityCase } from './coverage.mjs'
import { schemaIndex } from './schema.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const manifest = JSON.parse(readFileSync(resolve(root, 'protocol/runtime-matrix.json'), 'utf8'))
const index = schemaIndex(resolve(root, 'protocol/codex-app-server/0.157.1/json-schema'), resolve(root, 'protocol/extensions'))

// 与用户决定一一对应；新增不支持项必须先取得决定并同步此清单。
const decided = new Set([
  'account/rateLimits/read', 'account/usage/read', 'account/workspaceMessages/read',
  'account/rateLimitResetCredit/consume', 'account/sendAddCreditsNudgeEmail',
  'plugin/share/checkout', 'plugin/share/delete', 'plugin/share/list', 'plugin/share/save',
  'plugin/share/updateTargets', 'plugin/skill/read',
  'modelProvider/authRecoveryStarted', 'modelProvider/authRecoveryCompleted',
  'thread/realtime/item/started', 'thread/realtime/item/completed', 'thread/realtime/item/transcript/delta',
  'feedback/upload', 'userVerification/enroll', 'userVerification/delete', 'userVerification/verify',
].map(method => `${method}@codex`))

test('UNSUPPORTED-NO-GATE：仅用户决定的能力登记为明确不支持，且写明原因并挂占位', () => {
  const registered = new Set()
  for (const entry of manifest.methods) {
    for (const engine of ['codex', 'claude-code']) {
      const placeholder = (entry.cases?.[engine] ?? []).includes(unsupportedCapabilityCase)
      if (entry.engines[engine] !== 'unsupported') {
        assert.ok(!placeholder, `${entry.method}@${engine} 未登记为不支持，不能挂占位`)
        continue
      }
      registered.add(`${entry.method}@${engine}`)
      assert.ok(placeholder, `${entry.method}@${engine} 必须挂占位用例`)
      assert.ok(entry.reasons?.[engine]?.length > 10, `${entry.method}@${engine} 必须写明不支持原因`)
    }
  }
  assert.deepEqual([...registered].sort(), [...decided].sort(), '明确不支持清单须与用户决定一致')
})

test('UNSUPPORTED-NO-GATE：覆盖统计直接通过占位，缺原因或缺占位仍记缺口', () => {
  const coverage = entries => protocolCoverage({ methods: entries }, [], [], [], 'run', index, () => {}).missing
  const entry = { method: 'feedback/upload', engines: { codex: 'unsupported', 'claude-code': 'required' },
    cases: { codex: [unsupportedCapabilityCase], 'claude-code': ['X'] }, schema: { params: 'x' },
    reasons: { codex: '测试原因：用户决定不支持此能力' } }
  assert.deepEqual(coverage([entry]).filter(item => item.engine === 'codex'), [])
  assert.ok(coverage([{ ...entry, cases: { codex: [], 'claude-code': ['X'] } }])
    .some(item => item.engine === 'codex' && /占位/.test(item.reason)))
  assert.ok(coverage([{ ...entry, reasons: {} }]).some(item => item.engine === 'codex' && /缺少原因/.test(item.reason)))
})
