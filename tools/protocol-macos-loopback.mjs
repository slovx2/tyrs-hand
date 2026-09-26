import { execFileSync, spawnSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { collectMacNetworkDiagnostics } from './protocol-macos-diagnostics.mjs'

if (process.platform !== 'darwin') throw new Error('此诊断仅适用于 macOS')
const directory = resolve('.artifacts/protocol/macos-loopback')
mkdirSync(directory, { recursive: true })
const binary = resolve(directory, 'loopback-probe')
execFileSync('go', ['build', '-o', binary, './tools/protocol-loopback'], { stdio: 'inherit' })
const profiles = {
  current: '(version 1)(allow default)(deny network-outbound)(allow network-outbound (remote ip "localhost:*") (remote unix-socket))',
}
const windows = [], reports = []
for (const [name, profile] of Object.entries(profiles)) {
  const startedAt = Date.now()
  const child = spawnSync('/usr/bin/sandbox-exec', ['-p', profile, binary], {
    encoding: 'utf8', timeout: 60_000, maxBuffer: 1024 * 1024,
  })
  const report = { name, profile, status: child.status, signal: child.signal, results: [],
    error: child.error?.message ?? child.stderr.trim() }
  try { report.results = JSON.parse(child.stdout) }
  catch (error) { report.error ||= error.message }
  windows.push({ suite: name, pid: child.pid, startedAt, completedAt: Date.now(), status: child.status })
  reports.push(report)
}
writeFileSync(resolve(directory, 'report.json'), JSON.stringify({
  os: execFileSync('/usr/bin/sw_vers', ['-productVersion'], { encoding: 'utf8' }).trim(),
  go: execFileSync('go', ['version'], { encoding: 'utf8' }).trim(), reports,
  limitation: '探针仅作诊断；不会替换真实 SSH 验收、重试业务或修改矩阵的隔离策略',
}, null, 2))
collectMacNetworkDiagnostics(directory, windows)
for (const report of reports) console.log(JSON.stringify({ name: report.name, status: report.status,
  success: report.results.reduce((sum, row) => sum + row.success, 0),
  failures: report.results.reduce((sum, row) => sum + Object.values(row.failures).reduce((a, b) => a + b, 0), 0),
  error: report.error }))
