import assert from 'node:assert/strict'
import { appendFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { mobileMcpSchema, mobileMcpScenarios } from '../lib/mcp-scenarios.mjs'

// 复用已锁定适配器的 MCP SDK；不另外安装依赖，也不模拟服务端回调。
const [adapter, workspace] = process.argv.slice(2)
assert.ok(adapter && workspace, '缺少隔离 MCP 夹具路径')
const require = createRequire(resolve(adapter, 'package.json'))
const { Server } = require('@modelcontextprotocol/sdk/server/index.js')
const { StdioServerTransport } = require('@modelcontextprotocol/sdk/server/stdio.js')
const { CallToolRequestSchema, ListToolsRequestSchema } = require('@modelcontextprotocol/sdk/types.js')
const server = new Server({ name: 'mobile-fixture', version: '1.0.0' }, { capabilities: { tools: {} } })
server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools: [{
  name: 'confirm_mobile', description: 'Request typed user input or URL confirmation before writing a file',
  inputSchema: { type: 'object', properties: { scenario: { type: 'string',
    enum: Object.keys(mobileMcpScenarios) } }, required: ['scenario'], additionalProperties: false },
  _meta: { 'anthropic/alwaysLoad': true },
}] }))
server.setRequestHandler(CallToolRequestSchema, async ({ params }) => {
  assert.equal(params.name, 'confirm_mobile')
  const marker = params.arguments?.scenario
  const scenario = mobileMcpScenarios[marker]
  assert.ok(scenario, '必须使用已登记的 MCP 场景')
  const request = scenario.mode === 'form'
    ? { mode: 'form', message: 'MOBILE_MCP_TYPED_FORM', requestedSchema: mobileMcpSchema }
    : { mode: 'url', message: 'MOBILE_MCP_URL_CONFIRMATION',
      url: 'http://127.0.0.1/mobile-mcp-confirmation', elicitationId: marker.toLowerCase() }
  const response = await server.elicitInput(request)
  const result = { action: response.action, content: response.content ?? null }
  if (response.action === 'accept') {
    await appendFile(resolve(workspace, marker + '.jsonl'), JSON.stringify(result) + '\n')
  }
  return { content: [{ type: 'text', text: 'MCP_RESULT ' + JSON.stringify(result) }] }
})
await server.connect(new StdioServerTransport())
