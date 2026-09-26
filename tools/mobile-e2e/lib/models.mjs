import assert from 'node:assert/strict'
import { readFile, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { isTitleOutputSchema } from './title-schema.mjs'
import { mobileMcpAnswer, mobileMcpScenarios, mobileMcpTool } from './mcp-scenarios.mjs'

const text = (value) => [{ type: 'text', text: value }]
const tool = (name, id, input) => ({ type: 'tool_use', name, id, input })
const title = { title: 'Mobile runtime acceptance', description: '真实 SSH 双引擎移动验收' }
const titleForSchema = (schema) => Object.fromEntries(
  Object.keys(schema.properties).map((key) => [key, title[key]]))

export function structuredTitleResponse(request) {
  const schema = request.text?.format?.schema ?? request.output_config?.format?.schema
  if (isTitleOutputSchema(schema)) return text(JSON.stringify(titleForSchema(schema)))
  // 固定 Claude CLI 通过真实 StructuredOutput 工具完成 outputSchema，
  // 标题提示词也包含用户的场景标识，不能把它当作待执行任务。
  const output = request.tools?.find((entry) => entry.name === 'StructuredOutput' &&
    isTitleOutputSchema(entry.input_schema))
  if (output) return [tool(output.name, 'toolu_mobile_title', titleForSchema(output.input_schema))]
  return undefined
}

export async function startModels(adapter, evidenceDir) {
  const { MockLLM } = await import(pathToFileURL(resolve(adapter, 'dist/test/fixtures/mock-llm.mjs')))
  const models = {}, urls = {}, completed = new Set(), mcpResults = {}
  let workspace
  for (const engine of ['codex', 'claude-code']) {
    const model = new MockLLM()
    models[engine] = model
    const respond = (request) => {
      model.enqueue(respond)
      const auxiliary = structuredTitleResponse(request)
      if (auxiliary) return auxiliary
      const messages = request.messages ?? request.input ?? []
      const user = Array.isArray(messages) ? messages.filter((message) => message.role === 'user') : []
      const matches = JSON.stringify(user).match(/MOBILE_(?:CODEX|CLAUDE)_[A-Z_]+/g) ?? []
      const marker = matches.at(-1)
      assert.ok(marker, '未登记的模型请求，不能以默认成功回答掩盖')
      assert.equal(marker.includes('CODEX'), engine === 'codex', '模型请求串入另一引擎')
      const result = (id) => user.flatMap((message) => Array.isArray(message.content) ? message.content : [])
        .findLast((block) => block.type === 'tool_result' && block.tool_use_id === id)
      const finish = (answer) => { completed.add(marker); return text(answer) }
      if (marker.endsWith('_CHAT')) return finish(marker + '_OK')
      assert.equal(engine, 'claude-code')
      assert.ok(workspace, '真实项目路径尚未就绪')
      if (mobileMcpScenarios[marker]) {
        const id = 'toolu_' + marker.toLowerCase()
        const found = result(id)
        if (!found) {
          assert.ok(request.tools?.some((entry) => entry.name === mobileMcpTool), '真实 SDK 未声明手机 MCP 工具')
          return [tool(mobileMcpTool, id, { scenario: marker })]
        }
        assert.ok(!found.is_error, 'MCP 结果必须成功回到真实模型上下文')
        const blocks = Array.isArray(found.content) ? found.content : [{ text: found.content }]
        const raw = blocks.map((entry) => entry.text ?? '').find((value) => value.startsWith('MCP_RESULT '))
        assert.ok(raw, '缺少真实 MCP 工具结果')
        const actual = JSON.parse(raw.slice('MCP_RESULT '.length))
        const { action, content } = mobileMcpAnswer(marker)
        assert.deepEqual(actual, { action, content }, 'MCP 类型或决策在回模型时失真')
        mcpResults[marker] = actual
        return finish(marker + '_OK')
      }
      if (marker === 'MOBILE_CLAUDE_FULL' || marker === 'MOBILE_CLAUDE_APPROVAL' || marker === 'MOBILE_CLAUDE_DENY') {
        const id = 'toolu_' + marker.toLowerCase()
        const found = result(id)
        if (!found) return [tool('Write', id, { file_path: resolve(workspace, marker + '.txt'), content: marker })]
        assert.equal(Boolean(found.is_error), marker.endsWith('_DENY'), '审批结果未回到模型上下文')
        return finish(marker + '_OK')
      }
      if (marker === 'MOBILE_CLAUDE_PLAN') {
        if (!result('toolu_mobile_color')) return [tool('AskUserQuestion', 'toolu_mobile_color', {
          questions: [{ question: 'Choose a color', header: 'Color', multiSelect: false,
            options: [{ label: 'Blue', description: 'Write Blue' }, { label: 'Red', description: 'Write Red' }] }],
        })]
        assert.match(JSON.stringify(result('toolu_mobile_color')), /Blue/)
        if (!result('toolu_mobile_exit')) return [
          { type: 'text', text: 'MOBILE_PLAN_OUTPUT: Write Blue after confirmation.' },
          tool('ExitPlanMode', 'toolu_mobile_exit', {}),
        ]
        assert.ok(!result('toolu_mobile_exit').is_error, '计划尚未获准执行')
        if (!result('toolu_mobile_plan_write')) return [tool('Write', 'toolu_mobile_plan_write', {
          file_path: resolve(workspace, 'MOBILE_CLAUDE_PLAN.txt'), content: 'Blue',
        })]
        assert.ok(!result('toolu_mobile_plan_write').is_error, '退出计划后实际权限没有更新')
        return finish('MOBILE_CLAUDE_PLAN_OK')
      }
      throw new Error('未实现的移动端模型场景：' + marker)
    }
    model.enqueue(respond)
    urls[engine] = await model.start()
  }
  return { urls, models, setWorkspace(value) { workspace = value },
    async verify(expected) {
      for (const model of Object.values(models)) assert.deepEqual(model.unexpected, [])
      for (const marker of expected) {
        assert.ok(completed.has(marker), '缺少真实模型终态：' + marker)
        if (mobileMcpScenarios[marker]) {
          const { action, content } = mobileMcpAnswer(marker)
          assert.deepEqual(mcpResults[marker], { action, content }, '缺少真实 MCP 入模结果')
          const effect = resolve(workspace, marker + '.jsonl')
          if (action === 'accept') {
            const lines = (await readFile(effect, 'utf8')).split('\n')
            assert.equal(lines.length, 2, 'MCP 接受必须且只能产生一次真实文件副作用')
            assert.equal(lines[1], '', 'MCP 副作用记录必须完整写入')
            assert.deepEqual(JSON.parse(lines[0]), { action, content },
              'MCP 副作用必须保留完整回答及原始类型')
          } else await assert.rejects(readFile(effect), { code: 'ENOENT' })
        }
        if (['_FULL', '_APPROVAL', '_PLAN'].some((suffix) => marker.endsWith(suffix))) {
          assert.equal(await readFile(resolve(workspace, marker + '.txt'), 'utf8'),
            marker.endsWith('_PLAN') ? 'Blue' : marker)
        }
        if (marker.endsWith('_DENY')) {
          await assert.rejects(readFile(resolve(workspace, marker + '.txt')), { code: 'ENOENT' })
        }
      }
      await writeFile(resolve(evidenceDir, 'model-assertions.json'), JSON.stringify({
        expected, completed: [...completed], mcpResults, passed: true,
      }, null, 2))
    },
    async close() {
      for (const [engine, model] of Object.entries(models)) {
        await writeFile(resolve(evidenceDir, 'model-' + engine + '.json'), JSON.stringify({
          requests: model.requests, unexpected: model.unexpected,
        }, null, 2))
        await model.close()
      }
    },
  }
}
