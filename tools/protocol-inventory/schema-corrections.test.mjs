// 官方 JSON Schema 勘误的依据校验：勘误后的字段集必须与同版本官方 TS 定义逐一相等，
// 且原生形态报文通过、被勘误掉的下划线形态不再被当作唯一合法形态。
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { payloadValidator, schemaCorrections, schemaIndex } from './schema.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const schemaRoot = resolve(root, 'protocol/codex-app-server/0.157.1/json-schema')
const index = schemaIndex(schemaRoot, resolve(root, 'protocol/extensions'))

function typescriptFields(file, variant) {
  const source = readFileSync(resolve(schemaRoot, '..', file), 'utf8')
  const body = source.match(new RegExp(`\\{ "type": "${variant}", ([^}]*)\\}`))?.[1]
  assert.ok(body, `官方 TS 缺少 ${variant} 变体`)
  return ['type', ...body.split(',').map(field => field.trim().split(':')[0].replace(/\?$/, '')).filter(Boolean)].sort()
}

test('schema 勘误：每条勘误的依据是同版本官方 TS，勘误后字段集完全一致', () => {
  const corrections = schemaCorrections(schemaRoot)
  assert.ok(corrections.length > 0)
  for (const correction of corrections) {
    assert.ok(correction.reason?.length > 10, '勘误必须写明原因')
    const method = [...index.entries()].find(([, spec]) => spec.references?.response === correction.file)?.[0]
    assert.ok(method, `${correction.file} 必须是某个请求的响应`)
    const variant = index.get(method).response.definitions[correction.definition].oneOf
      .find(item => item.properties.type.enum[0] === correction.variant)
    assert.deepEqual(Object.keys(variant.properties).sort(), typescriptFields(correction.typescript, correction.variant))
  }
})

test('schema 勘误：timeline 原生驼峰边界通过，缺字段仍失败', () => {
  const validate = payloadValidator(index)
  const page = data => ({ data, nextCursor: null, activeRealtimeSessionAtPageStart: null })
  const started = { type: 'turnStarted', position: 0, turnId: 't1', startedAt: 1 }
  const completed = { type: 'turnCompleted', position: 1, turnId: 't1', status: 'completed', error: null,
    startedAt: 1, completedAt: 2, durationMs: 1000 }
  validate('thread/timeline/list', 'response', page([started, completed]))
  assert.throws(() => validate('thread/timeline/list', 'response', page([{ ...completed, status: undefined }])))
  const { turnId, ...withoutTurn } = started
  assert.throws(() => validate('thread/timeline/list', 'response', page([withoutTurn])))
})
