import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { structuredTitleResponse } from './mobile-e2e/lib/models.mjs'

export async function startMigrationModels(adapter, workspace, { journal = false } = {}) {
  const { MockLLM } = await import(pathToFileURL(resolve(adapter, 'dist/test/fixtures/mock-llm.mjs')))
  const models = {}, urls = {}, completed = new Set(), calls = {}, auxiliaryCalls = {}
  for (const engine of ['codex', 'claude-code']) {
    const model = new MockLLM()
    models[engine] = model
    const respond = request => {
      model.enqueue(respond)
      const title = structuredTitleResponse(request)
      if (title) {
        auxiliaryCalls[engine] = (auxiliaryCalls[engine] ?? 0) + 1
        return title
      }
      const messages = request.messages ?? request.input ?? []
      const user = messages.filter(message => message.role === 'user')
      const marker = (JSON.stringify(user).match(/MIGRATION_(?:OLD|PENDING|RESUME|CLAUDE)_WRITE/g) ?? []).at(-1)
      assert.ok(marker, '未登记的迁移模型请求')
      assert.equal(marker === 'MIGRATION_CLAUDE_WRITE', engine === 'claude-code', '模型请求跨引擎')
      calls[marker] = (calls[marker] ?? 0) + 1
      assert.ok(calls[marker] <= 2, '迁移步骤不应重复调用或执行工具')
      if (marker === 'MIGRATION_RESUME_WRITE') {
        assert.match(JSON.stringify(messages), /MIGRATION_OLD_WRITE_OK/, '真实恢复请求必须带有旧回答')
        if (journal) assert.match(JSON.stringify(messages), /MIGRATION_PENDING_WRITE_OK/, '崩溃前真实回答必须可恢复')
      }
      if (engine === 'claude-code') assert.doesNotMatch(JSON.stringify(messages), /MIGRATION_OLD_WRITE_OK/)
      const id = 'tool_' + marker.toLowerCase()
      const result = engine === 'codex'
        ? messages.find(message => message.type === 'function_call_output' && message.call_id === id)
        : user.flatMap(message => Array.isArray(message.content) ? message.content : [])
          .find(block => block.type === 'tool_result' && block.tool_use_id === id)
      if (result) {
        assert.equal(Boolean(result.is_error), false, '真实 CLI 工具结果失败')
        assert.match(JSON.stringify(result), new RegExp(marker + '_SIDE_EFFECT'), '工具结果必须真实回到模型')
        completed.add(marker)
        return [{ type: 'text', text: marker + '_OK' }]
      }
      const command = `printf '%s\\n' '${marker}_SIDE_EFFECT' >> '${resolve(workspace, marker + '.txt')}'; cat '${resolve(workspace, marker + '.txt')}'`
      if (engine === 'claude-code') {
        assert.ok(request.tools.some(tool => tool.name === 'Bash'))
        return [{ type: 'tool_use', name: 'Bash', id, input: { command, timeout: 10_000 } }]
      }
      const name = request.tools.find(tool => ['exec_command', 'shell_command'].includes(tool.name))?.name
      assert.ok(name, '固定 Codex 必须声明真实命令工具')
      const input = name === 'exec_command' ? { cmd: command, workdir: workspace, max_output_tokens: 2000 }
        : { command, workdir: workspace }
      return [{ type: 'tool_use', name, id, input }]
    }
    model.enqueue(respond)
    urls[engine] = await model.start()
  }
  return { urls, models, calls,
    async verify() {
      for (const model of Object.values(models)) assert.deepEqual(model.unexpected, [])
      const expected = ['MIGRATION_OLD_WRITE', 'MIGRATION_RESUME_WRITE', 'MIGRATION_CLAUDE_WRITE']
      if (journal) expected.push('MIGRATION_PENDING_WRITE')
      for (const marker of expected) {
        assert.ok(completed.has(marker), '缺少真实模型终态：' + marker)
        assert.equal(calls[marker], 2)
        assert.equal(await readFile(resolve(workspace, marker + '.txt'), 'utf8'), marker + '_SIDE_EFFECT\n',
          '真实文件副作用必须且只能发生一次')
      }
      return { calls, auxiliaryCalls, totalRequests: Object.fromEntries(
        Object.entries(models).map(([engine, model]) => [engine, model.requests.length])), completed: [...completed] }
    },
    async close() { for (const model of Object.values(models)) await model.close() },
  }
}
