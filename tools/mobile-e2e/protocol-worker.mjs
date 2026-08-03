const baseURL = String(process.env.TYRS_HAND_E2E_CONTROL_URL ?? '').replace(/\/$/, '')
const enrollmentToken = process.env.TYRS_HAND_E2E_ENROLLMENT_TOKEN ?? ''
const workerID = process.env.TYRS_HAND_E2E_WORKER_ID ?? 'mobile-e2e-protocol-worker'
if (!baseURL || !enrollmentToken) throw new Error('协议 Worker 缺少 Control URL 或 Enrollment Token')

let credential = ''
let stopping = false
const metadataGeneration = Date.now()
let metadataSequence = 0

async function call(path, { method = 'POST', body, authenticated = true } = {}) {
  const headers = { accept: 'application/json' }
  if (authenticated) headers.authorization = `Bearer ${credential}`
  if (body !== undefined) headers['content-type'] = 'application/json'
  const response = await fetch(`${baseURL}${path}`, {
    method, headers, body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await response.text()
  if (!response.ok) throw new Error(`${method} ${path}：${response.status} ${text}`)
  return text ? JSON.parse(text) : undefined
}

function lease(task) {
  return { leaseToken: task.claimed.LeaseToken, leaseEpoch: task.claimed.LeaseEpoch }
}

async function heartbeat(task) {
  return call(`/worker/v1/runs/${task.claimed.RunID}/heartbeat`, { body: lease(task) })
}

async function event(task, sequence, type, payload) {
  return call(`/worker/v1/runs/${task.claimed.RunID}/events`, { body: {
    ...lease(task), events: [{ sequence, type, payload }],
  } })
}

async function fail(task, code, message, codexError) {
  return call(`/worker/v1/runs/${task.claimed.RunID}/fail`, { body: {
    ...lease(task), idempotencyKey: `${task.claimed.RunID}:fail`, code, message, codexError,
  } })
}

async function complete(task, finalAnswer, turnID) {
  return call(`/worker/v1/runs/${task.claimed.RunID}/complete`, { body: {
    ...lease(task), idempotencyKey: `${task.claimed.RunID}:complete`, result: {
      finalAnswer, finalOutputType: 'text', turnId: turnID,
      durationMillis: 50, terminalEvidence: 'protocol-worker-e2e',
    },
  } })
}

async function acknowledge(task, command, action, turnID) {
  return call(`/worker/v1/runs/${task.claimed.RunID}/commands/ack`, { body: {
    ...lease(task), commandId: command.id, action, turnId: turnID,
  } })
}

async function interactive(task, secret, threadID, turnID) {
  const itemID = `item-${task.claimed.RunID}`
  const requestID = JSON.stringify(`request-${task.claimed.RunID}`)
  const params = { threadId: threadID, turnId: turnID, itemId: itemID, questions: [{
    id: 'choice', header: secret ? 'Secret' : '确认',
    question: secret ? '请输入测试 Secret' : '继续执行 E2E 吗？', isSecret: secret,
    options: secret ? undefined : [
      { label: '继续', description: '完成协议闭环。' },
      { label: '停止', description: '结束任务。' },
    ],
  }] }
  const state = await call(`/worker/v1/runs/${task.claimed.RunID}/interactive`, { body: {
    ...lease(task), requestId: JSON.parse(requestID), params, appServerGeneration: 1,
  } })
  if (secret) return { secret: true, state }
  while (!stopping) {
    const current = await call(`/worker/v1/interactive/${state.id}`, { method: 'GET' })
    if (current.status === 'resolved' && current.ready) return { secret: false, state: current }
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  return { secret: false, state }
}

async function reconcileThreadLifecycles() {
  const requests = await call('/worker/v1/thread-lifecycle-requests', { method: 'GET' })
  for (const request of requests) {
    await call(`/worker/v1/thread-lifecycle-requests/${request.id}/complete`, { body: {
      environmentId: request.environmentId, response: {},
    } })
    metadataSequence += 1
    await call('/worker/v1/thread-metadata-events', { body: {
      environmentId: request.environmentId, generation: metadataGeneration,
      events: [{
        threadId: request.threadId, sequence: metadataSequence, kind: 'lifecycle',
        source: 'app_server', lifecycleState: request.desiredState,
      }],
    } })
  }
}

async function processTask(task) {
  const prompt = task.snapshot.development?.body ?? ''
  const runtime = task.snapshot.runtime
  const threadID = `protocol-thread-${task.claimed.ControlID}`
  const turnID = `protocol-turn-${task.claimed.RunID}`
  await call(`/worker/v1/runs/${task.claimed.RunID}/thread`, { body: {
    ...lease(task), threadId: threadID, codexHome: task.snapshot.development?.development?.environmentId ?? 'protocol',
  } })
  await call(`/worker/v1/runs/${task.claimed.RunID}/submission`, { body: {
    ...lease(task), submissionId: turnID,
  } })
  await call(`/worker/v1/runs/${task.claimed.RunID}/confirm`, { body: { ...lease(task), turnId: turnID } })
  await event(task, 1, 'runtime.settings_applied', {
    phase: 'turn/start', model: runtime.model, reasoningEffort: runtime.reasoningEffort,
    serviceTier: runtime.serviceTier === 'fast' ? 'priority' : 'standard',
    collaborationMode: runtime.collaborationMode, settingsRevision: runtime.settingsRevision,
  })
  await event(task, 2, 'item/started', { item: { id: 'agent', type: 'agentMessage' } })
  await event(task, 3, 'item/agentMessage/delta', { delta: '协议级实时' })

  if (prompt.includes('E2E_FAIL_429')) {
    await fail(task, 'codex_non_retryable_error', 'deterministic rate limit', {
      message: 'deterministic rate limit', willRetry: false, threadId: threadID, turnId: turnID,
    })
    return
  }
  if (prompt.includes('E2E_ASK') || prompt.includes('E2E_SECRET')) {
    const result = await interactive(task, prompt.includes('E2E_SECRET'), threadID, turnID)
    if (result.secret) {
      while (!stopping) {
        const state = await heartbeat(task)
        const interrupt = state.commands?.find((command) => command.operation === 'interrupt')
        if (interrupt) {
          await acknowledge(task, interrupt, 'interrupt', turnID)
          await fail(task, 'user_interrupt', 'Secret 交互由移动端停止')
          return
        }
        await new Promise((resolve) => setTimeout(resolve, 400))
      }
      return
    }
  }
  if (prompt.includes('E2E_BLOCK')) {
    while (!stopping) {
      const state = await heartbeat(task)
      const interrupt = state.commands?.find((command) => command.operation === 'interrupt')
      if (interrupt) {
        await acknowledge(task, interrupt, 'interrupt', turnID)
        await fail(task, 'user_interrupt', '移动端停止任务')
        return
      }
      for (const command of state.commands ?? []) {
        if (command.operation === 'turn_input') await acknowledge(task, command, 'steer', turnID)
      }
      await new Promise((resolve) => setTimeout(resolve, 400))
    }
  }
  const answer = prompt.includes('E2E_PLAN')
    ? '## 实施计划\n\n1. 验证参数\n2. 执行任务\n3. 返回结果'
    : prompt.includes('E2E_ASK') ? '已收到移动端回答，交互闭环完成。'
      : '协议级 Worker 已完成。'
  await event(task, 4, 'item/completed', { item: { id: 'agent', type: 'agentMessage', text: answer } })
  await complete(task, answer, turnID)
}

async function main() {
  const enrolled = await call('/worker/v1/enroll', { body: { token: enrollmentToken }, authenticated: false })
  credential = enrolled.credential
  await call('/worker/v1/heartbeat', { body: { workerVersion: 'mobile-e2e-protocol',
    protocolVersion: 21, metadata: { lane: 'ios-protocol' } } })
  while (!stopping) {
    try {
      await reconcileThreadLifecycles()
      const claim = await call('/worker/v1/claims', { body: { workerId: workerID, role: 'discord', wait: true } })
      if (claim.task) await processTask(claim.task)
    } catch (error) {
      if (stopping) break
      process.stderr.write(`协议 Worker 暂时无法连接 Control：${error.message}\n`)
      await new Promise((resolve) => setTimeout(resolve, 1000))
    }
  }
}

for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => { stopping = true })
main().catch((error) => { process.stderr.write(`${error.stack ?? error}\n`); process.exit(1) })
