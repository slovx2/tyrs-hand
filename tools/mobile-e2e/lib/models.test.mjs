import assert from 'node:assert/strict'
import test from 'node:test'
import { structuredTitleResponse } from './models.mjs'

const schema = { type: 'object', properties: { title: { type: 'string' },
  description: { type: 'string' } }, required: ['title', 'description'] }

test('Claude 原生标题输出使用 StructuredOutput，不执行提示词中的任务标识', () => {
  const response = structuredTitleResponse({
    tools: [{ name: 'StructuredOutput', input_schema: schema }],
    messages: [{ role: 'user', content: 'Generate a title: MOBILE_CLAUDE_APPROVAL' }],
  })
  assert.equal(response.length, 1)
  assert.equal(response[0].type, 'tool_use')
  assert.equal(response[0].name, 'StructuredOutput')
  assert.equal(response[0].input.title, 'Mobile runtime acceptance')
})

test('Codex 结构化标题返回 schema 规定的文本 JSON', () => {
  const response = structuredTitleResponse({ text: { format: { schema } } })
  assert.equal(response[0].type, 'text')
  assert.equal(JSON.parse(response[0].text).title, 'Mobile runtime acceptance')
})

test('普通任务提到 title 字段不能绕过真实任务及副作用验收', () => {
  assert.equal(structuredTitleResponse({ tools: [{ name: 'Write' }],
    messages: [{ role: 'user', content: '"title" "description" MOBILE_CLAUDE_APPROVAL' }] }), undefined)
})
