// 占位用例 NA-SERVER-MESSAGE：不适用的服务端消息不做门禁。
//
// 通知（ServerNotification）与服务端请求（ServerRequest）由运行时单向下发。某引擎根本不会产生的消息
// （例如 Claude 没有 OpenAI 登录、realtime 语音、Guardian、Windows 沙箱等），客户端无法发起请求并取得
// -32004 拒绝证据，也不能用“没出现”证明任何事。按用户 2026-09-29 的决定，这类消息登记为
// not-applicable、写明原因并挂上本占位用例后直接通过，不要求真实执行证据。
//
// 本文件是空实现的占位：不驱动任何通信，只校验登记自洽，防止占位被挪用来绕过门禁——
// 占位只能用于不适用的服务端消息；客户端请求的不适用仍须真实 -32004 证据，必需能力仍须成功证据。
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { notApplicableServerMessageCase, protocolCoverage } from './coverage.mjs'
import { schemaIndex } from './schema.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const manifest = JSON.parse(readFileSync(resolve(root, 'protocol/runtime-matrix.json'), 'utf8'))
const index = schemaIndex(resolve(root, 'protocol/codex-app-server/0.157.1/json-schema'), resolve(root, 'protocol/extensions'))
const serverOriginated = method => ['ServerNotification', 'ServerRequest'].includes(index.get(method)?.kind)

test('NA-SERVER-MESSAGE：占位仅用于写明原因的不适用服务端消息', () => {
  let placeholders = 0
  for (const entry of manifest.methods) {
    for (const engine of ['codex', 'claude-code']) {
      if (!(entry.cases?.[engine] ?? []).includes(notApplicableServerMessageCase)) continue
      placeholders++
      assert.equal(entry.engines[engine], 'not-applicable', `${entry.method}@${engine} 必需能力不能挂占位`)
      assert.ok(serverOriginated(entry.method), `${entry.method} 是客户端请求，不适用须取得真实 -32004 证据`)
      assert.ok(entry.reasons?.[engine]?.length > 10, `${entry.method}@${engine} 必须写明不适用原因`)
    }
  }
  assert.ok(placeholders > 0, '当前应至少登记一条不适用服务端消息')
})

test('NA-SERVER-MESSAGE：覆盖统计直接通过占位，但客户端请求和缺占位的消息仍记缺口', () => {
  const coverage = entries => protocolCoverage({ methods: entries }, [], [], [], 'run', index, () => {}).missing
  const reason = { 'claude-code': '测试原因：该引擎从不下发此消息' }
  const notification = { method: 'account/login/completed', engines: { codex: 'required', 'claude-code': 'not-applicable' },
    cases: { codex: ['X'], 'claude-code': [notApplicableServerMessageCase] }, schema: { params: 'x' }, reasons: reason }
  assert.deepEqual(coverage([notification]).filter(item => item.engine === 'claude-code'), [])
  const missingPlaceholder = { ...notification, cases: { codex: ['X'], 'claude-code': [] } }
  assert.ok(coverage([missingPlaceholder]).some(item => item.engine === 'claude-code' && /占位/.test(item.reason)))
  const clientRequest = { method: 'account/logout', engines: { codex: 'required', 'claude-code': 'not-applicable' },
    cases: { codex: ['X'], 'claude-code': [notApplicableServerMessageCase] }, schema: { params: 'x' }, reasons: reason }
  assert.ok(coverage([clientRequest]).some(item => item.engine === 'claude-code' &&
    item.reason === '用例没有成功执行此协议及 schema 校验'), '客户端请求不能靠占位通过')
})
