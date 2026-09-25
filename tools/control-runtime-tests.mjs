import { spawnSync, execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { randomUUID } from 'node:crypto'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const manifest = JSON.parse(readFileSync(resolve(root, 'protocol/control-runtime-cases.json'), 'utf8'))
if (manifest.scope !== 'control-database' || !manifest.cases.length) throw new Error('Control 用例清单无效')
const ids = new Set()
const targets = new Set()
for (const test of manifest.cases) {
  if (!test.id || ids.has(test.id) || !/^Test[A-Za-z0-9]+$/.test(test.test) ||
    !['./internal/database', './internal/httpapi', './internal/scheduledtasks'].includes(test.package)) {
    throw new Error(`无效或重复用例: ${JSON.stringify(test)}`)
  }
  const target = `${test.package}:${test.test}`
  if (targets.has(target)) throw new Error(`重复测试目标: ${target}`)
  ids.add(test.id)
  targets.add(target)
}
const go = process.env.GO ?? 'go'
const artifactRoot = resolve(process.env.CONTROL_RUNTIME_ARTIFACT_DIR ?? resolve(root, '.artifacts/control-runtime'))
const runId = randomUUID()
const artifacts = resolve(artifactRoot, runId)
mkdirSync(artifacts, { recursive: true })
const combination = {
  scope: manifest.scope, runId,
  commit: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim(),
  dirty: !!execFileSync('git', ['status', '--porcelain'], { cwd: root, encoding: 'utf8' }).trim(),
  go: execFileSync(go, ['version'], { cwd: root, encoding: 'utf8' }).trim(),
  platform: process.platform, startedAt: new Date().toISOString(),
}
writeFileSync(resolve(artifacts, 'combination.json'), JSON.stringify(combination, null, 2))
const tests = manifest.cases.map(test => test.test)
const result = spawnSync(go, ['test', '-p=1', '-json', '-count=1', '-tags=integration',
  '-run', `^(${tests.join('|')})$`, ...new Set(manifest.cases.map(test => test.package))],
{ cwd: root, encoding: 'utf8', timeout: 300_000, maxBuffer: 16 * 1024 * 1024 })
writeFileSync(resolve(artifacts, 'tests.jsonl'), result.stdout ?? '')
writeFileSync(resolve(artifacts, 'stderr.log'), result.stderr ?? '')
const events = []
let malformed = false
for (const line of (result.stdout ?? '').split('\n').filter(Boolean)) {
  try { events.push(JSON.parse(line)) } catch { malformed = true }
}
const cases = manifest.cases.map(test => {
  const pkg = 'github.com/slovx2/tyrs-hand/' + test.package.slice(2)
  const trace = events.filter(event => event.Package === pkg && event.Test === test.test)
  const pass = trace.some(event => event.Action === 'pass')
  const failure = trace.some(event => event.Action === 'fail' || event.Action === 'skip')
  return { ...test, status: pass && !failure ? 'passed' : 'failed',
    output: trace.filter(event => event.Output).map(event => event.Output).join('') }
})
const failed = result.error || result.status !== 0 || malformed ||
  events.some(event => ['fail', 'skip'].includes(event.Action)) || cases.some(test => test.status !== 'passed')
const escape = value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;')
const testXML = cases.map(test => `<testcase name="${test.id}" classname="${test.test}">${test.status === 'passed' ? '' :
  `<failure message="必需用例未通过">${escape(test.output)}</failure>`}</testcase>`).join('')
const infrastructureFailure = failed && cases.every(test => test.status === 'passed')
writeFileSync(resolve(artifacts, 'junit.xml'), `<?xml version="1.0"?><testsuite name="control-runtime" tests="${cases.length + Number(infrastructureFailure)}" failures="${cases.filter(test => test.status === 'failed').length + Number(infrastructureFailure)}" skipped="0">${testXML}${infrastructureFailure ? '<testcase name="infrastructure"><failure message="测试基础设施失败"/></testcase>' : ''}</testsuite>`)
writeFileSync(resolve(artifacts, 'report.json'), JSON.stringify({ ...combination, passed: !failed, cases,
  note: '仅验证真实 Control 和 PostgreSQL；不能替代真实 SSH→SDK→Mock LLM 协议矩阵。' }, null, 2))
writeFileSync(resolve(artifactRoot, 'latest.json'), JSON.stringify({ runId, directory: artifacts, passed: !failed }))
console.log(`Control 数据隔离: ${cases.filter(test => test.status === 'passed').length}/${cases.length}; ${artifacts}`)
if (failed) {
  process.stderr.write(result.stdout ?? '')
  process.stderr.write(result.stderr ?? '')
  throw result.error ?? new Error('Control 必需用例失败、缺失或被跳过')
}
