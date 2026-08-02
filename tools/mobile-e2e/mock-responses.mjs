import { createServer } from 'node:http'

const port = Number(process.env.TYRS_HAND_E2E_RESPONSES_PORT ?? '18991')
let sequence = 0
let blocked = []
const requests = []

function walk(value, visit) {
  if (Array.isArray(value)) {
    for (const item of value) walk(item, visit)
    return
  }
  if (!value || typeof value !== 'object') return
  visit(value)
  for (const child of Object.values(value)) walk(child, visit)
}

function requestSummary(body, id) {
  const inputTypes = new Set()
  const tools = new Set()
  walk(body.input, (value) => {
    if (typeof value.type === 'string') inputTypes.add(value.type)
    if (value.type === 'function' && typeof value.name === 'string') tools.add(value.name)
  })
  const markers = [...JSON.stringify(body).matchAll(/E2E_[A-Z0-9_]+/g)]
    .map((match) => match[0])
  return {
    id, model: body.model ?? '', serviceTier: body.service_tier ?? '',
    hasFunctionOutput: inputTypes.has('function_call_output'),
    inputTypes: [...inputTypes].sort(), markers: [...new Set(markers)].sort(),
    tools: [...new Set([
      ...tools,
      ...(Array.isArray(body.tools) ? body.tools.map((tool) => tool?.name ?? tool?.type ?? '') : []),
    ].filter(Boolean))].sort(),
  }
}

function event(value) {
  return `event: ${value.type}\ndata: ${JSON.stringify(value)}\n\n`
}

function completed(id) {
  return { type: 'response.completed', response: { id, usage: {
    input_tokens: 0, input_tokens_details: null, output_tokens: 0,
    output_tokens_details: null, total_tokens: 0,
  } } }
}

function finalEvents(id, text) {
  const item = { type: 'message', role: 'assistant', id: `message-${id}`,
    content: [{ type: 'output_text', text }] }
  return [
    { type: 'response.created', response: { id } },
    { type: 'response.output_item.added', output_index: 0, item: { ...item, content: [] } },
    { type: 'response.content_part.added', item_id: item.id, output_index: 0,
      content_index: 0, part: { type: 'output_text', text: '' } },
    { type: 'response.output_text.delta', item_id: item.id, output_index: 0,
      content_index: 0, delta: text.slice(0, Math.ceil(text.length / 2)) },
    { type: 'response.output_text.delta', item_id: item.id, output_index: 0,
      content_index: 0, delta: text.slice(Math.ceil(text.length / 2)) },
    { type: 'response.output_item.done', output_index: 0, item },
    completed(id),
  ]
}

function writeSSE(response, events) {
  response.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' })
  for (const value of events) response.write(event(value))
  response.end()
}

function interactiveEvents(id) {
  const argumentsValue = { questions: [{ id: 'choice', header: '确认',
    question: '继续执行 E2E 吗？',
    options: [
      { label: '继续', description: '完成真实交互闭环。' },
      { label: '停止', description: '结束当前任务。' },
    ] }] }
  return [
    { type: 'response.created', response: { id } },
    { type: 'response.output_item.done', item: { type: 'function_call',
      call_id: `input-${id}`, name: 'request_user_input', arguments: JSON.stringify(argumentsValue) } },
    completed(id),
  ]
}

const server = createServer(async (request, response) => {
  if (request.url === '/healthz') {
    response.writeHead(204).end()
    return
  }
  if (request.url === '/__e2e/release' && request.method === 'POST') {
    for (const release of blocked.splice(0)) release()
    response.writeHead(204).end()
    return
  }
  if (request.url === '/__e2e/stats') {
    response.setHeader('content-type', 'application/json')
    response.end(JSON.stringify({ requests }))
    return
  }
  if (request.url !== '/v1/responses' || request.method !== 'POST') {
    response.writeHead(404).end()
    return
  }
  let raw = ''
  for await (const chunk of request) raw += chunk
  const body = JSON.parse(raw)
  const bodyText = JSON.stringify(body)
  const id = `e2e-response-${++sequence}`
  const summary = requestSummary(body, id)
  requests.push(summary)

  if (bodyText.includes('E2E_FAIL_429')) {
    response.writeHead(429, { 'content-type': 'application/json' })
    response.end(JSON.stringify({ error: { message: 'deterministic rate limit', type: 'rate_limit_error' } }))
    return
  }
  if (bodyText.includes('E2E_BLOCK')) {
    await new Promise((resolve) => {
      blocked.push(resolve)
      request.once('close', resolve)
    })
    if (!response.destroyed) writeSSE(response, finalEvents(id, '阻塞任务已释放。'))
    return
  }
  const answered = summary.hasFunctionOutput
  if (!answered && bodyText.includes('E2E_ASK')) {
    writeSSE(response, interactiveEvents(id))
    return
  }
  let text = '真实 Codex E2E 已完成。\n\n```text\nworker -> app-server -> responses\n```'
  if (bodyText.includes('E2E_PLAN')) text = '## 实施计划\n\n1. 验证参数\n2. 执行任务\n3. 返回结果'
  if (answered) text = '已收到移动端回答，交互闭环完成。'
  if (bodyText.includes('E2E_STEER')) text = '已接收运行中的 steer，并完成当前 Turn。'
  writeSSE(response, finalEvents(id, text))
})

server.listen(port, '0.0.0.0', () => {
  process.stdout.write(`mock responses listening on ${port}\n`)
})

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => server.close(() => process.exit(0)))
}
