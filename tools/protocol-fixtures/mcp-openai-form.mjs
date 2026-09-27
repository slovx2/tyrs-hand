import assert from 'node:assert/strict'
import { appendFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'

// 固定适配器已锁定 MCP SDK；真实 CLI 负责启动服务、调用工具和发起用户交互。
const [adapter, mode, effect] = process.argv.slice(2)
assert.ok(adapter && effect, '必须指定隔离夹具目录')
assert.ok(['form', 'openai/form', 'openaiForm'].includes(mode), '只测试固定协议声明的表单模式')
const require = createRequire(resolve(adapter, 'package.json'))
const { Server } = require('@modelcontextprotocol/sdk/server/index.js')
const { StdioServerTransport } = require('@modelcontextprotocol/sdk/server/stdio.js')
const { CallToolRequestSchema, ListToolsRequestSchema, ElicitResultSchema } = require('@modelcontextprotocol/sdk/types.js')
const server = new Server({ name: 'typed-form-fixture', version: '1.0.0' }, { capabilities: { tools: {} } })
server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools: [{
  name: 'confirm_typed', description: 'Ask for typed form input before writing a fixture file',
  inputSchema: { type: 'object', properties: {}, additionalProperties: false },
}] }))
server.setRequestHandler(CallToolRequestSchema, async ({ params }) => {
  assert.equal(params.name, 'confirm_typed')
  // 扩展 MCP 方法与 app-server 的 mode 不同；由真实 CLI 执行官方转换。
  const method = { form: 'elicitation/create', 'openai/form': 'openai/form', openaiForm: 'openai/elicitation/create' }[mode]
  const response = await server.request({ method, params: {
    ...(mode === 'openai/form' ? {} : { mode: 'form' }),
    message: 'MCP_TYPED_FORM', requestedSchema: {
      type: 'object', properties: {
        // 扩展表单必须使用标准 MCP 不能表达的 schema，防止原生解析回退为 form。
        count: mode === 'form' ? { type: 'integer', minimum: 1 } : { anyOf: [{ type: 'integer', minimum: 1 }] },
        enabled: { type: 'boolean' },
      }, required: ['count', 'enabled'], additionalProperties: false,
    },
  } }, ElicitResultSchema, { timeout: 120_000 })
  const result = { action: response.action, content: response.content ?? null }
  if (response.action === 'accept') {
    assert.equal(response.content.count, 2)
    assert.equal(response.content.enabled, false)
    await appendFile(effect, JSON.stringify(result) + '\n')
  }
  return { content: [{ type: 'text', text: 'MCP_RESULT ' + JSON.stringify(result) }] }
})
await server.connect(new StdioServerTransport())
