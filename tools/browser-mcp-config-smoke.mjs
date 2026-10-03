// 使用真实 Codex 和隔离 MCP 验证用户凭据与任务请求头的配置合并，不调用模型。
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import readline from 'node:readline'

const root = await mkdtemp(join(tmpdir(), 'browser-mcp-config-'))
const home = join(root, '.codex')
await mkdir(home)
const requests = []
const server = http.createServer(async (request, response) => {
  if (request.method !== 'POST') {
    response.writeHead(405).end()
    return
  }
  const chunks = []
  for await (const chunk of request) chunks.push(chunk)
  const message = JSON.parse(Buffer.concat(chunks).toString())
  requests.push({ auth: request.headers.authorization, task: request.headers['x-tyrs-browser-task-id'] })
  if (message.id == null) {
    response.writeHead(202).end()
    return
  }
  let result = {}
  if (message.method === 'initialize') result = {
    protocolVersion: message.params.protocolVersion, capabilities: { tools: {} },
    serverInfo: { name: 'isolated-browser', version: '1.0.0' },
  }
  if (message.method === 'tools/list') result = { tools: [{
    name: 'browser_tabs', description: '隔离浏览器',
    inputSchema: { type: 'object', properties: {} },
  }] }
  response.writeHead(200, { 'content-type': 'application/json' })
  response.end(JSON.stringify({ jsonrpc: '2.0', id: message.id, result }))
})
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
await writeFile(join(home, 'config.toml'), `
[mcp_servers.chrome]
url = "http://127.0.0.1:${server.address().port}/mcp"
tool_timeout_sec = 120
[mcp_servers.chrome.http_headers]
Authorization = "Bearer isolated-user-token"
`, { mode: 0o600 })
const child = spawn(process.env.CODEX_BIN ?? 'codex', ['app-server'], {
  cwd: root,
  env: { HOME: root, CODEX_HOME: home, PATH: process.env.PATH },
  stdio: ['pipe', 'pipe', 'pipe'],
})
child.stderr.resume()
const pending = new Map()
let nextID = 0
const lines = readline.createInterface({ input: child.stdout })
lines.on('line', (line) => {
  let value
  try { value = JSON.parse(line) } catch { return }
  const waiter = pending.get(value.id)
  if (!waiter) return
  pending.delete(value.id)
  if (value.error) waiter.reject(new Error(value.error.message))
  else waiter.resolve(value.result)
})
function call(method, params) {
  const id = ++nextID
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`${method} 超时`)), 20000)
    pending.set(id, {
      resolve(value) { clearTimeout(timer); resolve(value) },
      reject(error) { clearTimeout(timer); reject(error) },
    })
    child.stdin.write(`${JSON.stringify({ id, method, params })}\n`)
  })
}
try {
  await call('initialize', { clientInfo: { name: 'browser-config-test', version: '1.0.0' }, capabilities: { experimentalApi: true } })
  child.stdin.write(`${JSON.stringify({ method: 'initialized', params: {} })}\n`)
  await call('thread/start', {
    cwd: root, ephemeral: true, approvalPolicy: 'never', sandbox: 'read-only',
    config: { mcp_servers: { chrome: { http_headers: { 'X-Tyrs-Browser-Task-Id': '11111111-1111-4111-8111-111111111111' } } } },
  })
  const deadline = Date.now() + 15000
  while (!requests.some((request) => request.task) && Date.now() < deadline)
    await new Promise((resolve) => setTimeout(resolve, 100))
  assert.ok(requests.some((request) => request.task === '11111111-1111-4111-8111-111111111111' && request.auth === 'Bearer isolated-user-token'),
    '任务请求头必须与用户级 Authorization 合并')
  assert.ok(requests.every((request) => request.auth === 'Bearer isolated-user-token'))
  console.log('PASS: 真实 Codex 用户级连接与任务请求头合并，无模型请求')
} finally {
  child.kill('SIGTERM')
  lines.close()
  server.closeAllConnections()
  await new Promise((resolve) => server.close(resolve))
  await rm(root, { recursive: true, force: true })
}
