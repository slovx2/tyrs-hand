import assert from 'node:assert/strict'

export const mobileScenarios = ['MOBILE_CODEX_CHAT', 'MOBILE_CLAUDE_CHAT', 'MOBILE_CLAUDE_FULL',
  'MOBILE_CLAUDE_APPROVAL', 'MOBILE_CLAUDE_DENY', 'MOBILE_CLAUDE_PLAN']

// 仅从真实上游 wire 提取可公开的关联信息，不能以 GUI 点击代替审批回答。
export function mobileWireSemantics(engine, messages) {
  const scenarios = new Map(), threads = new Map(), pending = new Map()
  const planFragments = new Map()
  for (const [sequence, entry] of messages.entries()) {
    const { connection, direction, message } = entry
    const params = message.params ?? {}
    if (direction === 'request' && message.method === 'turn/start') {
      const marker = params.input?.find((item) => item.type === 'text' &&
        mobileScenarios.includes(item.text))?.text
      if (marker) {
        assert.equal(marker.includes('CODEX'), engine === 'codex', '场景串入另一引擎')
        assert.ok(!scenarios.has(marker), '场景被重复提交：' + marker)
        const scenario = { marker, threadId: params.threadId, started: sequence,
          mode: params.collaborationMode?.mode, callbacks: [], fileChanges: [], planOutput: [] }
        scenarios.set(marker, scenario)
        threads.set(params.threadId, scenario)
      }
    }
    const scenario = threads.get(params.threadId)
    if (direction === 'response' && scenario) {
      if (message.method === 'item/completed' && params.item?.type === 'fileChange') {
        scenario.fileChanges.push(sequence)
      }
      if (message.method === 'item/completed' && params.item?.type === 'plan' &&
          params.item.text?.includes('MOBILE_PLAN_OUTPUT:')) scenario.planOutput.push(sequence)
      if (message.method === 'item/plan/delta') {
        const key = JSON.stringify([connection, params.threadId, params.itemId])
        const text = (planFragments.get(key) ?? '') + params.delta
        planFragments.set(key, text)
        if (text.includes('MOBILE_PLAN_OUTPUT:')) scenario.planOutput.push(sequence)
      }
      if (message.id !== undefined && ['item/fileChange/requestApproval',
        'item/commandExecution/requestApproval', 'item/tool/requestUserInput',
        'item/permissions/requestApproval'].includes(message.method)) {
        const callback = { id: message.id, method: message.method, requested: sequence,
          questions: params.questions?.map((question) => question.id) }
        scenario.callbacks.push(callback)
        pending.set(JSON.stringify([connection, message.id]), callback)
      }
    }
    if (direction === 'request' && !message.method) {
      const callback = pending.get(JSON.stringify([connection, message.id]))
      if (callback) {
        assert.ok(!message.error, '客户端回答返回协议错误：' + callback.method)
        callback.answered = sequence
        callback.decision = message.result?.decision
        callback.answers = callback.questions?.flatMap((id) => message.result?.answers?.[id]?.answers ?? [])
        pending.delete(JSON.stringify([connection, message.id]))
      }
    }
  }
  assert.equal(pending.size, 0, '必需审批或提问没有到达真实适配器的回答')
  const required = mobileScenarios.filter((marker) => marker.includes('CODEX') === (engine === 'codex'))
  for (const marker of required) assert.ok(scenarios.has(marker), '缺少真实客户端场景：' + marker)
  if (engine === 'claude-code') {
    assert.deepEqual(scenarios.get('MOBILE_CLAUDE_FULL').callbacks, [], '完全访问不应产生审批')
    for (const [marker, decision] of [['MOBILE_CLAUDE_APPROVAL', 'accept'], ['MOBILE_CLAUDE_DENY', 'decline']]) {
      const scenario = scenarios.get(marker)
      assert.equal(scenario.callbacks.length, 1, marker + ' 必须且只能回答一次文件审批')
      const callback = scenario.callbacks[0]
      assert.equal(callback.method, 'item/fileChange/requestApproval')
      assert.equal(callback.decision, decision, marker + ' 的真实回答不符')
      assert.ok(callback.answered > callback.requested, '审批回答必须晚于请求')
    }
    const plan = scenarios.get('MOBILE_CLAUDE_PLAN')
    assert.equal(plan.mode, 'plan', '客户端没有进入计划模式')
    assert.equal(plan.callbacks.length, 2, '计划必须完成提问和退出确认')
    const [question, confirmation] = plan.callbacks
    assert.equal(question.method, 'item/tool/requestUserInput')
    assert.equal(confirmation.method, 'item/tool/requestUserInput')
    assert.deepEqual(question.answers, ['Blue'])
    assert.deepEqual(confirmation.answers, ['执行计划'])
    assert.ok(question.answered < confirmation.requested, '计划确认发生在提问回答之前')
    assert.ok(plan.planOutput.some((sequence) => sequence < confirmation.requested), '确认前没有输出计划')
    assert.ok(plan.fileChanges.length > 0, '确认后没有真实文件修改事件')
    assert.ok(plan.fileChanges.every((sequence) => sequence > confirmation.answered), '计划确认前已修改项目文件')
  }
  return { passed: true, scenarios: Object.fromEntries(scenarios) }
}
