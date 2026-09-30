import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { mergeProtocolShards } from './protocol-merge-shards.mjs'

function shard(source, name, runId, files) {
  const directory = join(source, `protocol-shard-${name}`, runId)
  mkdirSync(directory, { recursive: true })
  writeFileSync(join(directory, 'run.json'), JSON.stringify({ runId }))
  for (const [file, content] of Object.entries(files)) writeFileSync(join(directory, file), content)
}

test('合并同一 runId 的分片执行记录、通信证据与失败，缺片、异轮和重名均失败', () => {
  const root = mkdtempSync(join(tmpdir(), 'tyrs-merge-shards-'))
  try {
    const source = join(root, 'shards')
    shard(source, 'ssh-1', 'run-1', { 'executions.jsonl': '{"a":1}\n', 'wire-a.json': '{}',
      'runtime-failures.json': JSON.stringify({ failures: [] }) })
    shard(source, 'rest', 'run-1', { 'executions.jsonl': '{"b":2}\n', 'fault-injection-b.json': '{}',
      'runtime-failures.json': JSON.stringify({ failures: [{ suite: 'MIGRATION-005' }] }), 'app.test': 'binary' })
    const target = join(root, 'merged')
    const result = mergeProtocolShards({ source, target, runId: 'run-1', shards: ['ssh-1', 'rest'] })
    assert.equal(result.executions, 2)
    assert.deepEqual(result.failures, [{ suite: 'MIGRATION-005' }])
    assert.equal(readFileSync(join(target, 'executions.jsonl'), 'utf8'), '{"a":1}\n{"b":2}\n')
    assert.equal(JSON.parse(readFileSync(join(target, 'run.json'), 'utf8')).runId, 'run-1')
    assert.equal(readFileSync(join(target, 'wire-a.json'), 'utf8'), '{}')
    assert.throws(() => mergeProtocolShards({ source, target: join(root, 'm2'), runId: 'run-1', shards: ['ssh-2'] }), /缺少协议分片/)
    assert.throws(() => mergeProtocolShards({ source, target: join(root, 'm3'), runId: 'other', shards: ['ssh-1'] }), /不属于本轮/)
    shard(source, 'dup', 'run-1', { 'wire-a.json': '{}' })
    assert.throws(() => mergeProtocolShards({ source, target: join(root, 'm4'), runId: 'run-1', shards: ['ssh-1', 'dup'] }), /重名/)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
