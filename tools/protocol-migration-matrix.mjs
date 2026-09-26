import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { closeSync, mkdirSync, mkdtempSync, openSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

const modes = [
  { caseId: 'MIGRATION-005', caseName: 'RealWorker32To33Migration', args: [] },
  { caseId: 'MIGRATION-006', caseName: 'RealWorker32To33PendingJournalMigration', args: ['--journal'] },
  { caseId: 'MIGRATION-007', caseName: 'RealWorkerRollbackThenUpgradeMigration', args: ['--rollback'] },
]

export function migrationOutcome(result, report, schema, { runId, caseId }) {
  const errors = []
  if (result.error || result.code !== 0 || result.signal) errors.push('迁移进程未正常成功退出')
  if (!report || report.runId !== runId || report.caseId !== caseId) errors.push('迁移报告不属于本轮用例')
  if (report?.passed !== true) errors.push('迁移报告未通过')
  if (!Array.isArray(report?.cleanupErrors) || report.cleanupErrors.length) errors.push('迁移资源清理未确认成功')
  if (report?.schema?.passed !== true || !Array.isArray(report?.schema?.errors) || report.schema.errors.length) {
    errors.push('迁移报告中的 schema 未通过')
  }
  if (schema?.passed !== true || !Array.isArray(schema?.errors) || schema.errors.length) errors.push('独立 schema 报告未通过')
  return errors
}

export function migrationWireArtifacts(rows, { runId, engine, caseName, caseId, protocolErrors = [] }) {
  const connections = new Map()
  for (const row of rows) {
    assert.equal(row.engine, engine, '原始 wire 引擎与文件不一致')
    assert.equal(typeof row.connection, 'string', '原始 wire 缺少连接身份')
    assert.ok(['request', 'response'].includes(row.direction), '原始 wire 方向无效')
    assert.ok(row.message && typeof row.message === 'object' && !Array.isArray(row.message))
    assert.ok(!Object.hasOwn(row.message, 'direction'), '报文占用了 inventory 方向元数据字段')
    let messages = connections.get(row.connection)
    if (!messages) { messages = []; connections.set(row.connection, messages) }
    // 只附加方向元数据，原始请求、响应和通知字段保持原值；绝不补造关闭事件。
    messages.push({ direction: row.direction === 'request' ? 'client' : 'server', ...row.message })
  }
  assert.ok(connections.size, `${engine} 缺少真实 wire`)
  return [...connections].map(([connection, messages]) => ({
    formatVersion: 1, runId, engine, caseName, caseIds: [caseId], kind: 'wire', connection,
    payload: { messages, protocolErrors },
  }))
}

async function execute(root, directory, env, args) {
  const log = openSync(resolve(directory, 'runner.log'), 'wx', 0o600)
  try {
    return await new Promise(resolveResult => {
      const child = spawn(process.execPath, ['tools/protocol-migration.mjs', ...args], {
        cwd: root, env, stdio: ['ignore', log, log],
      })
      child.once('error', error => resolveResult({ code: null, signal: null, error: error.message }))
      child.once('exit', (code, signal) => resolveResult({ code, signal }))
    })
  } finally { closeSync(log) }
}

function readJSON(path, errors) {
  try { return JSON.parse(readFileSync(path, 'utf8')) }
  catch (error) { errors.push(`读取 ${path} 失败：${error.message}`); return undefined }
}

// 仅由完整 matrix 调用；编译和依赖准备在外层网络隔离之外，Worker 自身仍强制隔离。
export async function runMigrationMatrix({ root, artifacts, runId, env }) {
  assert.equal(typeof runId, 'string')
  assert.ok(runId.length)
  mkdirSync(artifacts, { recursive: true })
  const executions = [], failures = [], directories = []
  for (const mode of modes) {
    const directory = mkdtempSync(resolve(artifacts, mode.caseId.toLowerCase() + '-'))
    directories.push({ caseId: mode.caseId, directory })
    const childEnv = { ...env, PROTOCOL_RUN_ID: runId, TYRS_HAND_MIGRATION_EVIDENCE: directory,
      // 禁止 SDK 测试夹具把任务系统提示额外写入 matrix 根目录。
      PROTOCOL_ARTIFACT_DIR: '' }
    const result = await execute(root, directory, childEnv, mode.args)
    const errors = []
    const report = readJSON(resolve(directory, 'migration-report.json'), errors)
    const schema = readJSON(resolve(directory, 'schema-report.json'), errors)
    errors.push(...migrationOutcome(result, report, schema, { runId, caseId: mode.caseId }))
    for (const engine of ['codex', 'claude-code']) {
      try {
        const raw = readFileSync(resolve(directory, `wire-${engine}.jsonl`), 'utf8')
        const rows = raw.split('\n').filter(line => line.trim()).map(line => JSON.parse(line))
        for (const artifact of migrationWireArtifacts(rows, { runId, engine, caseName: mode.caseName,
          caseId: mode.caseId, protocolErrors: schema?.errors ?? [] })) {
          const suffix = createHash('sha256').update(artifact.connection).digest('hex').slice(0, 20)
          // inventory 只扫描本轮目录根部；每个真实连接单独成文件以保留请求 ID 作用域。
          writeFileSync(resolve(artifacts, `wire-${mode.caseId.toLowerCase()}-${engine}-${suffix}.json`),
            JSON.stringify(artifact), { mode: 0o600, flag: 'wx' })
        }
      } catch (error) { errors.push(`${engine} wire 转换失败：${error.message}`) }
    }
    const passed = errors.length === 0
    for (const engine of ['codex', 'claude-code']) executions.push({ runId, engine,
      caseName: mode.caseName, caseIds: [mode.caseId], status: passed ? 'passed' : 'failed' })
    if (!passed) failures.push({ suite: mode.caseId, status: result.code, signal: result.signal,
      directory, error: errors.join('；') })
    writeFileSync(resolve(directory, 'matrix-result.json'), JSON.stringify({ runId, caseId: mode.caseId,
      passed, process: result, errors }, null, 2), { mode: 0o600 })
    writeFileSync(resolve(artifacts, 'migration-executions.jsonl'), executions.map(value => JSON.stringify(value)).join('\n') + '\n')
    writeFileSync(resolve(artifacts, 'migration-failures.json'), JSON.stringify({ runId, failures }, null, 2))
  }
  return { executions, failures, directories }
}
