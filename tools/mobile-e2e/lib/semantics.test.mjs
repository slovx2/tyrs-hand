import assert from 'node:assert/strict'
import test from 'node:test'
import { mobileScenarios, mobileWireSemantics } from './semantics.mjs'
import { mobileMcpScenarios, mobileMcpAnswer, mobileMcpSchema } from './mcp-scenarios.mjs'

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
    if (mobileMcpScenarios[marker]) {
      const { mode } = mobileMcpScenarios[marker]
      add('response', { id: marker + '-mcp', method: 'mcpServer/elicitation/request',
        params: { threadId: marker, mode, serverName: 'mobile_fixture',
          ...(mode === 'form' ? { requestedSchema: mobileMcpSchema } : {
            url: 'http://127.0.0.1/mobile-mcp-confirmation', elicitationId: marker.toLowerCase() }) } })
      add('request', { id: marker + '-mcp', result: mobileMcpAnswer(marker) })
      add('response', { method: 'item/completed', params: { threadId: marker,
        item: { type: 'mcpToolCall', status: 'completed', error: null } } })
    }
    if (marker.endsWith('_PLAN')) {
      ask('-color', { answers: { color: { answers: ['Blue'] } } }, [{ id: 'color' }])
      add('response', { method: 'item/plan/delta', params: { threadId: marker,
        itemId: 'plan-output', delta: 'MOBILE_PLAN_' } })
      add('response', { method: 'item/plan/delta', params: { threadId: marker,
        itemId: 'plan-output', delta: 'OUTPUT: Write Blue after confirmation.' } })
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
  const output = rows.findIndex((entry) => entry.message.method === 'item/plan/delta')
  assert.throws(() => mobileWireSemantics('claude-code', rows.filter((entry) =>
    entry.message.method !== 'item/plan/delta')), /没有输出计划/)
  const early = structuredClone(rows)
  const write = early.findIndex((entry) => entry.message.params?.item?.type === 'fileChange')
  early.splice(output, 0, early.splice(write, 1)[0])
  assert.throws(() => mobileWireSemantics('claude-code', early), /确认前已修改/)
  rows.find((entry) => entry.message.method === 'turn/start' &&
    entry.message.params.threadId === 'MOBILE_CLAUDE_PLAN').message.params.collaborationMode.mode = 'default'
  assert.throws(() => mobileWireSemantics('claude-code', rows), /没有进入计划模式/)
})

test('MCP 字符串代替数字、动作失真、工具失败或缺失真实回答均被门禁拒绝', () => {
  const marker = 'MOBILE_CLAUDE_MCP_FORM_ACCEPT'
  const change = (alter) => {
    const rows = fixture()
    alter(rows, rows.find((entry) => entry.message.id === marker + '-mcp' && !entry.message.method))
    return () => mobileWireSemantics('claude-code', rows)
  }
  assert.throws(change((_, answer) => { answer.message.result.content.count = '3' }), /类型或动作/)
  assert.throws(change((_, answer) => { answer.message.result.action = 'cancel' }), /类型或动作/)
  assert.throws(change((_, answer) => { answer.connection = 'old-connection' }), /没有到达/)
  assert.throws(change((rows) => {
    const completed = rows.find((entry) => entry.message.params?.threadId === marker &&
      entry.message.params.item)
    completed.message.params.item.status = 'failed'
  }), /工具执行失败/)
})

test('标题辅助回合不能覆盖真实业务场景，真正重复提交仍被拒绝', () => {
  const rows = fixture()
  const auxiliary = structuredClone(rows[0])
  auxiliary.message.id = 'title-call'
  auxiliary.message.params.threadId = 'title-thread'
  auxiliary.message.params.outputSchema = { type: 'object',
    properties: { title: { type: 'string' } }, required: ['title'] }
  rows.push(auxiliary)
  assert.equal(mobileWireSemantics('claude-code', rows).passed, true)
  delete auxiliary.message.params.outputSchema
  assert.throws(() => mobileWireSemantics('claude-code', rows), /重复提交/)
  auxiliary.message.params.outputSchema = { type: 'object',
    properties: { title: { type: 'string' }, result: { type: 'string' } } }
  assert.throws(() => mobileWireSemantics('claude-code', rows), /重复提交/)
})
