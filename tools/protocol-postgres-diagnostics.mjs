import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

// API 重试后成功仍可能留下真实死锁；协议报文和专项断言不能替代数据库健康检查。
export function postgresDiagnostics(log, runId) {
  const deadlockLines = log.split(/\r?\n/).flatMap((line, index) =>
    /ERROR:\s+deadlock detected/.test(line) ? [index + 1] : [])
  const errors = []
  if (!runId || !log.includes('database system is ready to accept connections'))
    errors.push('缺少本轮 PostgreSQL 启动证据')
  if (deadlockLines.length) errors.push(`PostgreSQL 记录 ${deadlockLines.length} 次死锁`)
  return { runId, scope: 'postgres-deadlocks', completeProtocolMatrix: false,
    passed: errors.length === 0, deadlockLines, errors }
}

export function writePostgresDiagnostics(directory, runId) {
  const log = readFileSync(resolve(directory, 'postgres.log'), 'utf8')
  const report = postgresDiagnostics(log, runId)
  writeFileSync(resolve(directory, 'postgres-diagnostics.json'), JSON.stringify(report, null, 2))
  return report
}
