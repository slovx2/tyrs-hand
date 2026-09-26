import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, resolve } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { mobileMcpAnswer, mobileMcpScenarios, mobileMcpSchema } from './lib/mcp-scenarios.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const adapter = process.env.TYRS_HAND_ADAPTER_ROOT ?? resolve(root, '../claude-codex')
const require = createRequire(resolve(adapter, 'package.json'))
const { Client } = require('@modelcontextprotocol/sdk/client/index.js')
const { StdioClientTransport } = require('@modelcontextprotocol/sdk/client/stdio.js')
const { ElicitRequestSchema } = require('@modelcontextprotocol/sdk/types.js')

// 只验真实 stdio 夹具；手机主验收仍须 runtime.test 和 GUI 的完整 SSH/SDK 链路。
test('真实 MCP SDK stdio 六场景保留类型、动作和实际文件副作用', { timeout: 30_000 }, async () => {
  const workspace = await mkdtemp(resolve(tmpdir(), 'mobile-mcp-fixture-'))
  const client = new Client({ name: 'mobile-fixture-test', version: '1.0.0' },
    { capabilities: { elicitation: { form: {}, url: {} } } })
  let current, callbacks = 0
  client.setRequestHandler(ElicitRequestSchema, async ({ params }) => {
    callbacks++
    assert.equal(params.mode, mobileMcpScenarios[current].mode)
    if (params.mode === 'form') assert.deepEqual(params.requestedSchema, mobileMcpSchema)
    else assert.equal(params.elicitationId, current.toLowerCase())
    const { action, content } = mobileMcpAnswer(current)
    return content === null ? { action } : { action, content }
  })
  try {
    await client.connect(new StdioClientTransport({ command: process.execPath,
      args: [resolve(root, 'tools/mobile-e2e/fixtures/mcp-server.mjs'), adapter, workspace],
      env: { HOME: workspace, PATH: process.env.PATH }, stderr: 'pipe' }))
    assert.equal((await client.listTools()).tools[0].name, 'confirm_mobile')
    for (const marker of Object.keys(mobileMcpScenarios)) {
      current = marker
      const result = await client.callTool({ name: 'confirm_mobile', arguments: { scenario: marker } })
      assert.ok(!result.isError)
      const { action, content } = mobileMcpAnswer(marker)
      assert.deepEqual(result.content, [{ type: 'text',
        text: 'MCP_RESULT ' + JSON.stringify({ action, content }) }])
      const path = resolve(workspace, marker + '.jsonl')
      if (action === 'accept') assert.equal(await readFile(path, 'utf8'), JSON.stringify({ action, content }) + '\n')
      else await assert.rejects(readFile(path), { code: 'ENOENT' })
    }
    assert.equal(callbacks, 6)
  } finally { await client.close(); await rm(workspace, { recursive: true, force: true }) }
})
