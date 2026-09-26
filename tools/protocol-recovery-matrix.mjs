import { spawn } from 'node:child_process'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { closeSync, existsSync, mkdirSync, mkdtempSync, openSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { isDeepStrictEqual } from 'node:util'
import { migrationWireArtifacts } from './protocol-migration-matrix.mjs'

const modes = [
  { caseId: 'FAILURE-006', caseName: 'RealWorker33CompletedToolCrashRecovery', engines: ['codex', 'claude-code'],
    script: 'tools/protocol-worker-restart.mjs', args: [], directoryVariable: 'TYRS_HAND_RESTART_EVIDENCE' },
  { caseId: 'FAILURE-007', caseName: 'RealWorker33InflightToolCrashRecovery', engines: ['claude-code'],
    script: 'tools/protocol-worker-inflight.mjs', args: [], directoryVariable: 'TYRS_HAND_INFLIGHT_EVIDENCE' },
  { caseId: 'FAILURE-008', caseName: 'RealWorker33PendingApprovalCrashRecovery', engines: ['claude-code'],
    script: 'tools/protocol-worker-inflight.mjs', args: ['--pending'], directoryVariable: 'TYRS_HAND_INFLIGHT_EVIDENCE' },
]

export function recoveryOutcome(process, report, schema, runId, caseId = 'FAILURE-006') {
  const errors = []
  if (!modes.some(mode => mode.caseId === caseId)) errors.push('未知恢复用例')
  if (process.error || process.code !== 0 || process.signal) errors.push('恢复验收进程未正常成功退出')
  if (report?.runId !== runId || report?.caseId !== caseId) errors.push('恢复报告不属于本轮用例')
  if (report?.passed !== true || report?.build?.protocolVersion !== 33) errors.push('当前版本恢复报告未通过')
  if (!/^[a-f0-9]{64}$/.test(report?.build?.sha256?.worker ?? '')) errors.push('缺少当前 Worker 制品 SHA256')
  if (!Array.isArray(report?.cleanupErrors) || report.cleanupErrors.length) errors.push('恢复资源清理未确认成功')
  for (const value of [report?.schema, schema]) {
    if (value?.passed !== true || !Array.isArray(value.errors) || value.errors.length) errors.push('恢复 schema 未通过')
    for (const engine of ['codex', 'claude-code']) {
      const wire = value?.engines?.[engine]
      if (!(wire?.messages > 0) || !(wire.checkedResponses > 0) ||
        !Array.isArray(wire.pendingRequests) || wire.pendingRequests.length ||
        !Array.isArray(wire.pendingCallbacks) ||
        (caseId !== 'FAILURE-008' || engine !== 'claude-code') && wire.pendingCallbacks.length) errors.push(`${engine} 协议收尾未确认`)
    }
  }
  if (!isDeepStrictEqual(report?.schema, schema)) errors.push('内嵌与独立schema报告不一致')
  if (caseId !== 'FAILURE-006') {
    errors.push(...inflightOutcome(report, schema, caseId))
    return errors
  }
  for (const engine of ['codex', 'claude-code']) {
    const result = report?.engines?.[engine]
    if (result?.passed !== true || result.sideEffectCount !== 1 || result.modelCalls !== 2 ||
      result.identityUnchanged !== true || result.historyReadable !== true || result.terminalDelivered !== true ||
      result.crash?.signal !== 'SIGKILL' || !(result.pendingEventCount > 0) ||
      !result.pidBefore || !result.pidAfter || result.pidBefore === result.pidAfter ||
      result.workerSHA256 !== report?.build?.sha256?.worker) errors.push(`${engine} 缺少完整真实恢复断言`)
  }
  return errors
}

function inflightOutcome(report, schema, caseId) {
  const errors = [], result = report?.engines?.['claude-code'], crash = result?.crash
  if (!isDeepStrictEqual(Object.keys(report?.engines ?? {}), ['claude-code'])) errors.push('在途恢复仅允许声明已实测Claude')
  if (result?.passed !== true || result.identityUnchanged !== true || result.oldTurnStatus !== 'interrupted' ||
    !['failed', 'canceled'].includes(result.oldRunStatus) || result.oldModelCalls !== 1 || result.nextModelCalls !== 2 ||
    result.nextSideEffectCount !== 1 || !result.threadId || !result.oldTurnId || !result.newTurnId || result.oldTurnId === result.newTurnId ||
    crash?.signal !== 'SIGKILL' || !Number.isInteger(crash.pidBefore) || crash.pidBefore <= 0 ||
    !Number.isInteger(crash.pidAfter) || crash.pidAfter <= 0 || crash.pidBefore === crash.pidAfter ||
    !Array.isArray(crash.processTree) || !crash.processTree.includes(crash.pidBefore) ||
    crash.processTree.includes(crash.pidAfter) ||
    crash.processTree.some(pid => !Number.isInteger(pid) || pid <= 0) ||
    new Set(crash.processTree).size !== crash.processTree.length || crash.processCount !== crash.processTree.length) errors.push('Claude在途恢复缺少真实进程/中断/不重放证据')
  if (caseId === 'FAILURE-007') {
    if (result?.oldSideEffectCount !== 1 || !isDeepStrictEqual(result.oldToolStates, [{ status: 'failed', exitCode: null }]) ||
      !isDeepStrictEqual(schema?.processTerminatedCallbacks, [])) errors.push('在途工具的真实副作用或未知退出状态缺失')
    return errors
  }
  if (result?.oldSideEffectCount !== 0 || result.oldAnswer?.status !== 200 || result.oldAnswer.accepted !== false ||
    result.oldAnswer.ready !== false || result.oldAnswer.interactiveStatus !== 'interrupted' || result.oldAnswer.hasAnswer !== false ||
    result.substitutedOldAnswer?.status !== 404 || result.substitutedOldAnswer.accepted !== false ||
    result.substitutedOldAnswer.hasAnswer !== false || !/^[1-9][0-9]*$/.test(result.oldGeneration ?? '') ||
    !/^[1-9][0-9]*$/.test(result.newGeneration ?? '') || result.oldGeneration === result.newGeneration) errors.push('旧审批拒绝、代次隔离或零副作用未确认')
  const callbacks = schema?.processTerminatedCallbacks, pending = schema?.engines?.['claude-code']?.pendingCallbacks
  if (!Array.isArray(callbacks) || callbacks.length !== 1 || !Array.isArray(pending) || pending.length !== 1) {
    errors.push('必须保留唯一被杀进程的未答审批回调')
  } else {
    const callback = callbacks[0], owner = /^([1-9][0-9]*):[1-9][0-9]*$/.exec(callback.connection ?? '')
    if (callback.reason !== 'SIGKILL' || callback.workerPID !== crash?.pidBefore ||
      callback.method !== 'item/commandExecution/requestApproval' || callback.id == null || !owner ||
      !crash?.processTree?.includes(Number(owner?.[1])) ||
      !isDeepStrictEqual(pending[0], { connection: callback.connection, id: callback.id, method: callback.method })) errors.push('未答回调不能精确关联被SIGKILL的进程树')
  }
  return errors
}

export function recoveryWireArtifacts(rows, metadata, report, schema) {
  const artifacts = migrationWireArtifacts(rows, metadata)
  if (metadata.caseId !== 'FAILURE-008' || metadata.engine !== 'claude-code') return artifacts
  assert.deepEqual(inflightOutcome(report, schema, metadata.caseId), [], '被杀回调必须先通过精确生命周期校验')
  const callback = schema.processTerminatedCallbacks[0], result = report.engines['claude-code']
  const artifact = artifacts.find(value => value.connection === callback.connection)
  assert.ok(artifact, 'SIGKILL回调缺少对应真实连接')
  const pending = new Map()
  for (const message of artifact.payload.messages) {
    const sender = message.direction, key = JSON.stringify([sender, message.id])
    if (message.id == null) continue
    if (message.method) { assert.ok(!pending.has(key)); pending.set(key, message) }
    else { const answerKey = JSON.stringify([sender === 'client' ? 'server' : 'client', message.id]); assert.ok(pending.has(answerKey)); pending.delete(answerKey) }
  }
  assert.equal(pending.size, 1, '不能用SIGKILL掩盖同连接其它缺失响应')
  const request = pending.get(JSON.stringify(['server', callback.id]))
  assert.equal(request?.method, callback.method)
  assert.equal(request.params.threadId, result.threadId)
  assert.equal(request.params.turnId, result.oldTurnId)
  // 从真实Worker退出结果及进程树投影传输终止；保留原始回调，绝不制造JSON-RPC回答。
  artifact.payload.messages.push({ transport: { event: 'closed', source: 'process', exitCode: null,
    signal: result.crash.signal, expectedSignal: 'SIGKILL', pid: result.crash.pidBefore,
    connectionProcessPID: Number(callback.connection.split(':')[0]) } })
  return artifacts
}

// 只聚合同版本 Worker 的真实进程证据；保持旧32升级/回滚验收独立。
export async function runRecoveryMatrix({ root, artifacts, runId, env }) {
  assert.ok(typeof runId === 'string' && runId.length)
  mkdirSync(artifacts, { recursive: true })
  const executions = [], failures = [], directories = []
  for (const mode of modes) {
    const { caseId, caseName } = mode
    const directory = mkdtempSync(resolve(artifacts, caseId.toLowerCase() + '-'))
    directories.push({ caseId, directory })
    const descriptor = openSync(resolve(directory, 'runner.log'), 'wx', 0o600)
    let result
    try {
      result = await new Promise(resolveResult => {
        const child = spawn(process.execPath, [mode.script, ...mode.args], {
          cwd: root, env: { ...env, PROTOCOL_RUN_ID: runId, PROTOCOL_ARTIFACT_DIR: '', [mode.directoryVariable]: directory },
          stdio: ['ignore', descriptor, descriptor],
        })
        child.once('error', error => resolveResult({ code: null, signal: null, error: error.message }))
        child.once('exit', (code, signal) => resolveResult({ code, signal }))
      })
    } finally { closeSync(descriptor) }
    const errors = []
    const read = name => {
      try { return JSON.parse(readFileSync(resolve(directory, name), 'utf8')) }
      catch (error) { errors.push(`读取 ${name} 失败：${error.message}`); return undefined }
    }
    const report = read('report.json'), schema = read('schema-report.json')
    errors.push(...recoveryOutcome(result, report, schema, runId, caseId))
    for (const engine of ['codex', 'claude-code']) {
      try {
        const recorderErrors = resolve(directory, `wire-${engine}.jsonl.errors`)
        if (existsSync(recorderErrors) && readFileSync(recorderErrors, 'utf8').trim()) throw new Error('原生录制器报告失败')
        const rows = readFileSync(resolve(directory, `wire-${engine}.jsonl`), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
        for (const artifact of recoveryWireArtifacts(rows, { runId, engine, caseId, caseName, protocolErrors: schema?.errors ?? [] }, report, schema)) {
          const suffix = createHash('sha256').update(artifact.connection).digest('hex').slice(0, 20)
          writeFileSync(resolve(artifacts, `wire-${caseId.toLowerCase()}-${engine}-${suffix}.json`), JSON.stringify(artifact), { mode: 0o600, flag: 'wx' })
        }
      } catch (error) { errors.push(`${engine} wire 转换失败：${error.message}`) }
    }
    const passed = errors.length === 0
    for (const engine of mode.engines) executions.push({ runId, engine, caseName, caseIds: [caseId], status: passed ? 'passed' : 'failed' })
    if (!passed) failures.push({ suite: caseId, directory, status: result.code, signal: result.signal, error: errors.join('；') })
    writeFileSync(resolve(directory, 'matrix-result.json'), JSON.stringify({ runId, caseId, passed, process: result, errors }, null, 2), { mode: 0o600 })
    writeFileSync(resolve(artifacts, 'recovery-executions.jsonl'), executions.map(value => JSON.stringify(value)).join('\n') + '\n')
    writeFileSync(resolve(artifacts, 'recovery-failures.json'), JSON.stringify({ runId, failures }, null, 2), { mode: 0o600 })
  }
  return { executions, failures, directories }
}
