import { readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { protocolCoverage } from './coverage.mjs'
import { payloadValidator, schemaIndex } from './schema.mjs'

// 子集只豁免没有执行的能力覆盖；方法登记、报文、schema 和响应闭合仍执行正式校验。
const fullCoverageReasons = new Set(['未登记自动化用例', '用例没有成功执行此协议及 schema 校验'])

export function runtimeWireReport(manifest, artifacts, executions, runId, index, validate) {
  const errors = []
  if (!runId || executions.length === 0) errors.push({ reason: '缺少本轮运行时执行记录' })
  for (const entry of executions) {
    if (!['codex', 'claude-code'].includes(entry.engine) || typeof entry.caseName !== 'string' ||
        !entry.caseName || !Array.isArray(entry.caseIds) || entry.caseIds.length === 0 ||
        entry.caseIds.some(id => typeof id !== 'string' || !id))
      errors.push({ reason: '运行时执行的引擎或用例身份无效', caseName: entry.caseName, engine: entry.engine })
    if (entry.runId !== runId || entry.status !== 'passed')
      errors.push({ reason: '运行时用例未通过或不属于本轮', caseName: entry.caseName, engine: entry.engine })
    if (!artifacts.some(artifact => artifact.runId === runId && artifact.kind === 'wire' &&
      artifact.engine === entry.engine && artifact.caseName === entry.caseName && artifact.payload?.messages?.length))
      errors.push({ reason: '运行时用例缺少本轮通信记录', caseName: entry.caseName, engine: entry.engine })
  }
  const valid = []
  for (const artifact of artifacts) {
    if (artifact.runId !== runId || artifact.kind !== 'wire' || !Array.isArray(artifact.payload?.messages) ||
        artifact.payload.messages.length === 0) {
      errors.push({ reason: '通信记录为空、格式无效或不属于本轮', caseName: artifact.caseName })
      continue
    }
    if (!executions.some(entry => entry.runId === runId && entry.status === 'passed' &&
      entry.engine === artifact.engine && entry.caseName === artifact.caseName))
      errors.push({ reason: '通信记录缺少对应的成功执行', caseName: artifact.caseName, engine: artifact.engine })
    valid.push(artifact)
  }
  const coverage = protocolCoverage(manifest, [], valid, executions, runId, index, validate)
  errors.push(...coverage.missing.filter(entry => !fullCoverageReasons.has(entry.reason)))
  const uniqueErrors = [...new Map(errors.map(entry => [JSON.stringify(entry), entry])).values()]
  return { runId, scope: 'runtime-wire', completeProtocolMatrix: false, passed: uniqueErrors.length === 0,
    executions: executions.length, wireArtifacts: artifacts.length,
    messages: valid.reduce((total, artifact) => total + artifact.payload.messages.length, 0),
    evidence: coverage.evidence.length, errors: uniqueErrors }
}

export function writeRuntimeWireReport({ root, directory, runId, executions }) {
  const manifest = JSON.parse(readFileSync(resolve(root, 'protocol/runtime-matrix.json'), 'utf8'))
  const index = schemaIndex(resolve(root, 'protocol/codex-app-server/0.157.1/json-schema'),
    resolve(root, 'protocol/extensions'))
  const artifacts = readdirSync(directory).filter(name =>
    (name.startsWith('wire-') || name.startsWith('fault-injection-')) && name.endsWith('.json'))
    .map(name => JSON.parse(readFileSync(resolve(directory, name), 'utf8')))
  const report = runtimeWireReport(manifest, artifacts, executions, runId, index, payloadValidator(index))
  writeFileSync(resolve(directory, 'runtime-wire.json'), JSON.stringify(report, null, 2))
  return report
}
