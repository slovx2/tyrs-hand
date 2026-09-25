import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { readFileSync, readdirSync, writeFileSync, mkdirSync, existsSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { schemaIndex, payloadValidator } from './schema.mjs'
import { protocolCoverage, semanticCoverage } from './coverage.mjs'

const root = resolve(import.meta.dirname, '../..')
const require = createRequire(import.meta.url)
const ts = require('typescript')
const index = schemaIndex(join(root, 'protocol/codex-app-server/0.147.0/json-schema'), join(root, 'protocol/extensions'))
const usages = JSON.parse(execFileSync('go', ['run', './tools/protocol-inventory'], { cwd: root, encoding: 'utf8' }))
function walk(directory) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name)
    if (entry.isDirectory()) { if (entry.name !== 'generated') walk(path); continue }
    if (!/\.[cm]?tsx?$/.test(path) || /\.(test|spec)\./.test(path)) continue
    const source = ts.createSourceFile(path, readFileSync(path, 'utf8'), ts.ScriptTarget.Latest, true)
    function visit(node) {
      if (ts.isStringLiteral(node) && index.has(node.text)) usages.push({
        method: node.text, file: path.slice(root.length + 1), source: 'typescript-protocol',
        line: source.getLineAndCharacterOfPosition(node.getStart()).line + 1,
      })
      if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) &&
        ['request', 'notify', 'call'].includes(node.expression.name.text)) {
        for (const argument of node.arguments) {
          if (!ts.isStringLiteral(argument)) continue
          if (argument.text === 'initialize' || argument.text.includes('/')) usages.push({
            method: argument.text, file: path.slice(root.length + 1), source: 'typescript-call',
            line: source.getLineAndCharacterOfPosition(argument.getStart()).line + 1,
          })
        }
      }
      ts.forEachChild(node, visit)
    }
    visit(source)
  }
}
for (const directory of ['client/src', 'web/src']) walk(join(root, directory))
const manifestPath = join(root, 'protocol/runtime-matrix.json')
const defaultArtifacts = join(root, '.artifacts/protocol')
const latestPath = join(defaultArtifacts, 'latest.json')
const artifacts = resolve(process.env.PROTOCOL_ARTIFACT_DIR ??
  (existsSync(latestPath) ? JSON.parse(readFileSync(latestPath, 'utf8')).directory : defaultArtifacts))
mkdirSync(artifacts, { recursive: true })
const runPath = join(artifacts, 'run.json')
const runId = existsSync(runPath) ? JSON.parse(readFileSync(runPath, 'utf8')).runId : null
const wire = readdirSync(artifacts).filter(name => name.startsWith('wire-') && name.endsWith('.json'))
  .map(name => JSON.parse(readFileSync(join(artifacts, name), 'utf8')))
for (const artifact of wire) {
  if (artifact.runId !== runId || artifact.kind !== 'wire') continue
  for (const message of artifact.payload.messages) {
    if (message.method && message.method !== 'unknown/protocol') usages.push({
      method: message.method, file: `wire:${artifact.engine}`, source: 'wire',
    })
  }
}
if (process.argv.includes('--update')) {
  const previous = existsSync(manifestPath) ? JSON.parse(readFileSync(manifestPath, 'utf8')) : { methods: [] }
  const methods = [...new Set([...usages.map(entry => entry.method), ...previous.methods.map(entry => entry.method)])].sort().map(method => ({
    method, engines: { codex: 'required', 'claude-code': 'required' }, cases: { codex: [], 'claude-code': [] },
    ...previous.methods.find(entry => entry.method === method),
    schema: index.get(method)?.references ?? null,
    callers: [...new Set([...(previous.methods.find(entry => entry.method === method)?.callers ?? []),
      ...usages.filter(entry => entry.method === method).map(entry => entry.file)])],
  }))
  writeFileSync(manifestPath, JSON.stringify({ protocolVersion: '0.147.0', releaseReady: false, methods }, null, 2) + '\n')
  process.exit(0)
}
const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
const executionPath = join(artifacts, 'executions.jsonl')
const executions = existsSync(executionPath) ? readFileSync(executionPath, 'utf8').split('\n').filter(Boolean).map(line => JSON.parse(line)) : []
const report = protocolCoverage(manifest, usages, wire, executions, runId, index, payloadValidator(index))
const acceptance = JSON.parse(readFileSync(join(root, 'protocol/acceptance-cases.json'), 'utf8'))
report.groups = semanticCoverage(acceptance, executions, runId)
for (const group of report.groups) {
  for (const caseId of group.missing) report.missing.push({ group: group.id, caseId, reason: '必需语义用例未执行通过' })
}
report.complete = report.missing.length === 0
writeFileSync(join(artifacts, 'coverage.json'), JSON.stringify(report, null, 2))
if (!report.complete) {
  console.error(`完整协议矩阵未通过：${report.missing.length} 项缺口，详见 ${join(artifacts, 'coverage.json')}`)
  process.exitCode = 1
}
