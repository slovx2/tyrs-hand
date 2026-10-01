// Pi 桌面验收的回环 Mock 模型：openai-completions 流式接口，按用户消息中的标记词路由到 scenarios.mjs。
// 只替代模型回复；Pi SDK、插件、适配器、Worker 与 GUI 行为全部真实执行。未登记的请求直接失败，不以默认成功掩盖。
import assert from 'node:assert/strict'
import { readFile, writeFile } from 'node:fs/promises'
import { createServer } from 'node:http'
import { resolve } from 'node:path'

const sleep = (ms) => new Promise((done) => setTimeout(done, ms))
const markerPattern = /DESKTOP_PI_[A-Z_]+/g

const textOf = (content) => typeof content === 'string' ? content : JSON.stringify(content ?? '')

export async function startPiModel({ scenarios, aliases = {}, evidenceDir }) {
  const requests = [], unexpected = [], completed = new Set(), state = {}
  let workspace
  const server = createServer(async (req, res) => {
    const chunks = []
    for await (const chunk of req) chunks.push(chunk)
    let body
    try { body = JSON.parse(Buffer.concat(chunks).toString()) } catch { body = {} }
    requests.push(body)
    const closed = new Promise((done) => res.once('close', done))
    try {
      const parts = await respond(body, closed)
      await stream(res, body, parts, closed)
    } catch (error) {
      unexpected.push(String(error?.stack ?? error))
      if (!res.headersSent) res.writeHead(500, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: String(error?.message ?? error) } }))
    }
  })

  async function respond(request, closed) {
    const messages = request.messages ?? []
    const user = messages.filter((message) => message.role === 'user')
    const found = textOf(user.map((message) => message.content)).match(markerPattern) ?? []
    const raw = found.at(-1)
    const marker = aliases[raw] ?? raw
    assert.ok(marker && scenarios[marker], `未登记的模型请求：${raw ?? '无标记'}`)
    // 工具结果在 openai-completions 中是 role=tool 的消息，按调用 ID 查找。
    const result = (id) => {
      const message = messages.findLast((item) => item.role === 'tool' && item.tool_call_id === id)
      return message && { content: message.content, text: textOf(message.content) }
    }
    const finish = (answer) => { completed.add(marker); return [{ type: 'text', text: answer }] }
    const tool = (name, id, args) => ({ type: 'tool', name, id, args })
    state[marker] ??= {}
    return scenarios[marker]({ request, messages, user, result, finish, tool, workspace,
      state: state[marker], closed, complete: () => completed.add(marker) })
  }

  async function stream(res, request, parts, closed) {
    let aborted = false
    closed.then(() => { aborted = true })
    res.writeHead(200, { 'content-type': 'text/event-stream' })
    const base = { id: `desktop-pi-${requests.length}`, object: 'chat.completion.chunk', created: 1, model: request.model }
    const send = (delta, finishReason = null) => {
      if (!aborted) res.write(`data: ${JSON.stringify({ ...base, choices: [{ index: 0, delta, finish_reason: finishReason }] })}\n\n`)
    }
    const calls = []
    for (const part of parts) {
      if (aborted) return
      if (part.type === 'reasoning') send({ role: 'assistant', reasoning_content: part.text })
      else if (part.type === 'text') send({ role: 'assistant', content: part.text })
      else if (part.type === 'tool') calls.push({ index: calls.length, id: part.id, type: 'function',
        function: { name: part.name, arguments: JSON.stringify(part.args) } })
      if (part.pauseAfterMs) await Promise.race([sleep(part.pauseAfterMs), closed])
    }
    if (calls.length) send({ role: 'assistant', tool_calls: calls })
    if (aborted) return
    res.write(`data: ${JSON.stringify({ ...base, choices: [{ index: 0, delta: {}, finish_reason: calls.length ? 'tool_calls' : 'stop' }],
      usage: { prompt_tokens: 12, completion_tokens: 8, total_tokens: 20 } })}\n\n`)
    res.end('data: [DONE]\n\n')
  }

  await new Promise((done) => server.listen(0, '127.0.0.1', done))
  return {
    url: `http://127.0.0.1:${server.address().port}/v1`,
    requests,
    setWorkspace(value) { workspace = value },
    // 通过标准：每个所需场景都有真实模型终态，且没有未登记的模型请求；文件副作用按场景声明核对。
    async verify(expected, effects = {}) {
      assert.deepEqual(unexpected, [], '存在未登记或失败的模型请求')
      for (const marker of expected) {
        assert.ok(completed.has(marker), '缺少真实模型终态：' + marker)
        const effect = effects[marker]
        if (effect) assert.equal(await readFile(resolve(workspace, effect.file), 'utf8'), effect.content, `${marker} 文件副作用不符`)
      }
      await writeFile(resolve(evidenceDir, 'model-assertions.json'), JSON.stringify({ expected, completed: [...completed], passed: true }, null, 2))
    },
    async close() {
      await writeFile(resolve(evidenceDir, 'model-pi.json'), JSON.stringify({ requests, unexpected }, null, 2))
      server.closeAllConnections()
      await new Promise((done) => server.close(done))
    },
  }
}
