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

test('Worker 仅 title 的辅助任务必须使用结构化输出且不添加额外字段', () => {
  const titleOnly = { type: 'object', additionalProperties: false,
    properties: { title: { type: 'string' } }, required: ['title'] }
  const codex = structuredTitleResponse({ text: { format: { schema: titleOnly } } })
  assert.deepEqual(JSON.parse(codex[0].text), { title: 'Mobile runtime acceptance' })
  const claude = structuredTitleResponse({
    tools: [{ name: 'StructuredOutput', input_schema: titleOnly }],
    messages: [{ role: 'user', content: 'MOBILE_CLAUDE_APPROVAL' }],
  })
  assert.equal(claude[0].name, 'StructuredOutput')
  assert.deepEqual(claude[0].input, { title: 'Mobile runtime acceptance' })
})

test('包含其他业务字段的输出 schema 不能伪装为标题任务', () => {
  const business = { ...schema, properties: { ...schema.properties, result: { type: 'string' } } }
  assert.equal(structuredTitleResponse({ text: { format: { schema: business } } }), undefined)
})
