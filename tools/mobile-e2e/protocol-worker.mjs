const baseURL = String(process.env.TYRS_HAND_E2E_CONTROL_URL ?? '').replace(/\/$/, '')
const enrollmentToken = process.env.TYRS_HAND_E2E_ENROLLMENT_TOKEN ?? ''
const workerID = process.env.TYRS_HAND_E2E_WORKER_ID ?? 'mobile-e2e-protocol-worker'
const sshHostKeyFingerprint = process.env.TYRS_HAND_E2E_SSH_HOST_KEY_FINGERPRINT
  ?? 'SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
if (!baseURL || !enrollmentToken) throw new Error('协议 Worker 缺少 Control URL 或 Enrollment Token')

const modelCatalog = { data: [{
  id: 'gpt-5.6-sol', model: 'gpt-5.6-sol', displayName: 'GPT-5.6 Sol',
  description: '移动端 E2E 协议模型',
  supportedReasoningEfforts: ['low', 'medium', 'high', 'xhigh', 'max', 'ultra']
    .map((reasoningEffort) => ({ reasoningEffort, description: reasoningEffort })),
  defaultReasoningEffort: 'high', inputModalities: ['text', 'image'],
  additionalSpeedTiers: ['fast'], serviceTiers: [
    { id: 'standard', name: '标准', description: '标准速度' },
    { id: 'fast', name: '快速', description: '快速处理' },
  ], defaultServiceTier: 'standard', isDefault: true, hidden: false,
}], nextCursor: null }

let credential = ''
let workspaceID = ''
let stopping = false
const metadataGeneration = Date.now()
let metadataSequence = 0
let lastWorkerHeartbeatAt = 0

async function directCall(path, { method = 'POST', body, authenticated = true } = {}) {
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

async function call(path, { method = 'POST', body, authenticated = true } = {}) {
  return directCall(path, { method, body, authenticated })
}

async function publishWorkerHeartbeat(force = false) {
  if (!force && Date.now() - lastWorkerHeartbeatAt < 20_000) return
  await call('/worker/v1/heartbeat', { body: { workerVersion: 'mobile-e2e-protocol',
    protocolVersion: 28, sshHostKeyFingerprint, metadata: { lane: 'mobile-protocol',
      modelCatalogs: { [workspaceID]: modelCatalog } } } })
  lastWorkerHeartbeatAt = Date.now()
}

function lease(task) {
  return { leaseToken: task.claimed.LeaseToken, leaseEpoch: task.claimed.LeaseEpoch }
}

async function heartbeat(task) {
  await publishWorkerHeartbeat()
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

async function interactive(task, secret, matrix, threadID, turnID) {
  const itemID = `item-${task.claimed.RunID}`
  const requestID = JSON.stringify(`request-${task.claimed.RunID}`)
  const questions = [{
    id: 'choice', header: secret ? 'Secret' : '确认',
    question: secret ? '请输入测试 Secret' : '继续执行 E2E 吗？', isSecret: secret,
    options: secret ? undefined : [
      { label: '继续', description: '完成协议闭环。' },
      { label: '停止', description: '结束任务。' },
    ],
  }]
  if (matrix && !secret) questions.push({ id: 'note', header: '补充说明',
    question: '请输入移动端补充说明', isSecret: false })
  const params = { threadId: threadID, turnId: turnID, itemId: itemID, questions }
  const state = await call(`/worker/v1/runs/${task.claimed.RunID}/interactive`, { body: {
    ...lease(task), requestId: JSON.parse(requestID), params, appServerGeneration: 1,
  } })
  if (secret) return { secret: true, state }
  let previousState = ''
  while (!stopping) {
    await publishWorkerHeartbeat()
    const current = await call(`/worker/v1/interactive/${state.id}`, { method: 'GET' })
    const nextState = `${current.status}:${Boolean(current.ready)}`
    if (nextState !== previousState) {
      process.stderr.write(`交互状态变化：${state.id} ${nextState}\n`)
      previousState = nextState
    }
    if (current.status === 'resolved' && current.ready) return { secret: false, state: current }
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  return { secret: false, state }
}

async function reconcileThreadLifecycles() {
  const requests = await call('/worker/v1/thread-lifecycle-requests', { method: 'GET' })
  for (const request of requests) {
    await call(`/worker/v1/thread-lifecycle-requests/${request.id}/complete`, { body: {
      workspaceId: request.workspaceId, response: {},
    } })
    metadataSequence += 1
    await call('/worker/v1/thread-metadata-events', { body: {
      workspaceId: request.workspaceId, generation: metadataGeneration,
      events: [{
        threadId: request.threadId, sequence: metadataSequence, kind: 'lifecycle',
        source: 'app_server', lifecycleState: request.desiredState,
      }],
    } })
  }
}

async function processTask(task) {
  const prompt = task.snapshot.session?.body ?? ''
  const runtime = task.snapshot.runtime
  const threadID = `protocol-thread-${task.claimed.ControlID}`
  const turnID = `protocol-turn-${task.claimed.RunID}`
  await call(`/worker/v1/runs/${task.claimed.RunID}/thread`, { body: {
    ...lease(task), threadId: threadID,
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
  const commentaryID = `commentary-${task.claimed.RunID}`
  await event(task, 2, 'item/started', { item: { id: commentaryID, type: 'agentMessage',
    phase: 'commentary', text: '正在检查移动端运行状态。' } })
  await event(task, 3, 'item/agentMessage/delta', {
    itemId: commentaryID, phase: 'commentary', delta: '\n\n协议级实时增量已到达。',
  })

  if (prompt.includes('E2E_FAIL_429')) {
    await fail(task, 'codex_non_retryable_error', 'deterministic rate limit', {
      message: 'deterministic rate limit', willRetry: false, threadId: threadID, turnId: turnID,
    })
    return
  }
  if (prompt.includes('E2E_ASK') || prompt.includes('E2E_SECRET')) {
    const result = await interactive(task, prompt.includes('E2E_SECRET'),
      prompt.includes('E2E_ASK_MATRIX'), threadID, turnID)
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
    process.stderr.write(`交互已恢复：${task.claimed.RunID}\n`)
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
  if (prompt.includes('E2E_RESTART')) {
    const deadline = Date.now() + 8_000
    while (!stopping && Date.now() < deadline) {
      await heartbeat(task)
      await new Promise((resolve) => setTimeout(resolve, 500))
    }
  }
  let finalSequence = 4
  if (prompt.includes('E2E_EVENTS')) {
    await event(task, finalSequence++, 'item/completed', { item: { id: commentaryID,
      type: 'agentMessage', phase: 'commentary',
      text: '已核对 **同步状态** 与 `settingsVersion`，开始执行操作矩阵。' } })
    await event(task, finalSequence++, 'item/started', { item: { id: 'command-e2e',
      type: 'commandExecution', command: 'pnpm typecheck', status: 'running' } })
    await event(task, finalSequence++, 'item/completed', { item: { id: 'command-e2e',
      type: 'commandExecution', command: 'pnpm typecheck', status: 'completed' } })
    await event(task, finalSequence++, 'item/completed', { item: { id: 'file-e2e',
      type: 'fileChange', changes: [{ path: 'client/src/features/chat/RunCards.tsx' }],
      status: 'completed' } })
    await event(task, finalSequence++, 'item/completed', { item: { id: 'mcp-e2e',
      type: 'mcpToolCall', server: 'filesystem', tool: 'read_file', status: 'completed' } })
    await event(task, finalSequence++, 'item/completed', { item: { id: 'search-e2e',
      type: 'webSearch', query: 'Tyrs Hand mobile E2E', status: 'completed' } })
    await event(task, finalSequence++, 'item/completed', { item: { id: 'agent-e2e',
      type: 'collabAgentToolCall', status: 'completed' } })
  }
  const naturalAnswer = prompt.includes('E2E_PLAN')
    ? '## 实施计划\n\n1. 验证参数\n2. 执行任务\n3. 返回结果'
    : prompt.includes('E2E_EVENTS')
      ? '## 事件矩阵完成\n\n最终回答保持无卡片，所有中间操作均可展开。'
      : prompt.includes('E2E_ASK') ? '已收到移动端回答，交互闭环完成。'
      : '协议级 Worker 已完成。'
  const answer = prompt.includes('E2E_GIT_METADATA')
    ? `${naturalAnswer}\n\n::git-commit{cwd="/workspace/project"}\n` +
      '::git-push{cwd="/workspace/project" branch="main"}'
    : naturalAnswer
  process.stderr.write(`准备写入最终事件：${task.claimed.RunID}\n`)
  await event(task, finalSequence, 'item/completed', { item: { id: 'final-agent',
    type: 'agentMessage', phase: 'final', text: answer } })
  process.stderr.write(`准备完成 Run：${task.claimed.RunID}\n`)
  await complete(task, answer, turnID)
  process.stderr.write(`Run 已完成：${task.claimed.RunID}\n`)
}

async function main() {
  const enrolled = await call('/worker/v1/enroll', { body: { token: enrollmentToken }, authenticated: false })
  credential = enrolled.credential
  const manifest = await call('/worker/v1/workspace', { method: 'GET' })
  workspaceID = manifest.workspace?.workspaceId ?? ''
  if (!workspaceID) throw new Error('协议 Worker 没有绑定 Workspace')
  await publishWorkerHeartbeat(true)
  while (!stopping) {
    try {
      await publishWorkerHeartbeat()
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
