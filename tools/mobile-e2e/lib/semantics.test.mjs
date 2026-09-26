import assert from 'node:assert/strict'
import test from 'node:test'
import { mobileScenarios, mobileWireSemantics } from './semantics.mjs'

// 合成消息仅验证门禁拒绝规则，不能计入真实运行时验收。
function fixture(engine = 'claude-code') {
  const rows = []
  const add = (direction, message) => rows.push({ connection: 'fixture', direction, message })
  for (const marker of mobileScenarios.filter((value) => value.includes('CODEX') === (engine === 'codex'))) {
    add('request', { id: marker, method: 'turn/start', params: { threadId: marker,
      input: [{ type: 'text', text: marker }],
      collaborationMode: { mode: marker.endsWith('_PLAN') ? 'plan' : 'default' } } })
    const ask = (suffix, result, questions) => {
      const id = marker + suffix
      add('response', { id, method: questions ? 'item/tool/requestUserInput' : 'item/fileChange/requestApproval',
        params: { threadId: marker, questions } })
      add('request', { id, result })
    }
    if (marker.endsWith('_APPROVAL')) ask('', { decision: 'accept' })
    if (marker.endsWith('_DENY')) ask('', { decision: 'decline' })
    if (marker.endsWith('_PLAN')) {
      ask('-color', { answers: { color: { answers: ['Blue'] } } }, [{ id: 'color' }])
      add('response', { method: 'item/completed', params: { threadId: marker,
        item: { type: 'agentMessage', text: 'MOBILE_PLAN_OUTPUT: Write Blue after confirmation.' } } })
      ask('-exit', { answers: { exit: { answers: ['执行计划'] } } }, [{ id: 'exit' }])
      add('response', { method: 'item/completed', params: { threadId: marker, item: { type: 'fileChange' } } })
    }
  }
  return rows
}

test('移动语义门禁关联真实回答并检查计划确认前后的顺序', () => {
  assert.equal(mobileWireSemantics('codex', fixture('codex')).passed, true)
  const report = mobileWireSemantics('claude-code', fixture())
  assert.equal(report.scenarios.MOBILE_CLAUDE_APPROVAL.callbacks[0].decision, 'accept')
  assert.equal(report.scenarios.MOBILE_CLAUDE_DENY.callbacks[0].decision, 'decline')
  assert.deepEqual(report.scenarios.MOBILE_CLAUDE_PLAN.callbacks[1].answers, ['执行计划'])
})

test('手机点击后答案丢失、ID不匹配或拒绝被改为允许均不能通过', () => {
  const rows = fixture()
  const answer = rows.findIndex((entry) => entry.direction === 'request' &&
    entry.message.id === 'MOBILE_CLAUDE_APPROVAL' && !entry.message.method)
  assert.throws(() => mobileWireSemantics('claude-code', rows.filter((_, index) => index !== answer)), /没有到达/)
  const wrongID = structuredClone(rows)
  wrongID[answer].connection = 'another-runtime'
  assert.throws(() => mobileWireSemantics('claude-code', wrongID), /没有到达/)
  const deny = rows.find((entry) => entry.message.id === 'MOBILE_CLAUDE_DENY' && !entry.message.method)
  deny.message.result.decision = 'accept'
  assert.throws(() => mobileWireSemantics('claude-code', rows), /真实回答不符/)
})

test('缺少计划输出、确认前执行或未进入计划模式均不能通过', () => {
  const rows = fixture()
  const output = rows.findIndex((entry) => entry.message.params?.item?.type === 'agentMessage')
  assert.throws(() => mobileWireSemantics('claude-code', rows.filter((_, index) => index !== output)), /没有输出计划/)
  const early = structuredClone(rows)
  early.splice(output, 0, early.pop())
  assert.throws(() => mobileWireSemantics('claude-code', early), /确认前已修改/)
  rows.find((entry) => entry.message.method === 'turn/start' &&
    entry.message.params.threadId === 'MOBILE_CLAUDE_PLAN').message.params.collaborationMode.mode = 'default'
  assert.throws(() => mobileWireSemantics('claude-code', rows), /没有进入计划模式/)
})
