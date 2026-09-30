// 合并 CI 协议分片的执行记录与通信证据，供覆盖率门禁在同一 runId 下统一判定。
// 每个分片必须到齐且属于本轮；证据重名说明分片划分重叠，直接失败。
import { copyFileSync, existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'

export function mergeProtocolShards({ source, target, runId, shards }) {
  if (!source || !target || !runId || !shards.length) throw new Error('缺少分片目录、输出目录、runId 或分片列表')
  mkdirSync(target, { recursive: true })
  const executions = []
  const failures = []
  for (const name of shards) {
    const directory = resolve(source, `protocol-shard-${name}`)
    if (!existsSync(directory)) throw new Error(`缺少协议分片证据: ${name}`)
    const files = readdirSync(directory, { withFileTypes: true, recursive: true })
      .filter(entry => entry.isFile()).map(entry => join(entry.parentPath, entry.name))
    const run = files.find(file => basename(file) === 'run.json')
    if (!run || JSON.parse(readFileSync(run, 'utf8')).runId !== runId) throw new Error(`协议分片 ${name} 不属于本轮`)
    for (const file of files) {
      const base = basename(file)
      if (base === 'executions.jsonl') executions.push(...readFileSync(file, 'utf8').split('\n').filter(Boolean))
      else if (base === 'runtime-failures.json') failures.push(...JSON.parse(readFileSync(file, 'utf8')).failures)
      else if ((base.startsWith('wire-') || base.startsWith('fault-injection-')) && base.endsWith('.json')) {
        if (existsSync(join(target, base))) throw new Error(`协议分片证据重名: ${base}`)
        copyFileSync(file, join(target, base))
      }
    }
  }
  writeFileSync(join(target, 'run.json'), JSON.stringify({ runId, shards }))
  writeFileSync(join(target, 'executions.jsonl'), executions.length ? executions.join('\n') + '\n' : '')
  writeFileSync(join(target, 'runtime-failures.json'), JSON.stringify({ runId, failures }, null, 2))
  return { executions: executions.length, failures }
}

if (import.meta.main) {
  const [source, target] = process.argv.slice(2)
  const shards = (process.env.PROTOCOL_SHARDS ?? '').split(',').filter(Boolean)
  const result = mergeProtocolShards({ source, target, runId: process.env.PROTOCOL_RUN_ID, shards })
  console.log(`已合并 ${shards.length} 个协议分片：${result.executions} 条执行记录`)
  if (result.failures.length) {
    console.error(`分片真实运行时验收失败: ${result.failures.map(item => item.suite).join(', ')}`)
    process.exitCode = 1
  }
}
