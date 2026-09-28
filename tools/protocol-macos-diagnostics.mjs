import { spawnSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

// 探针退出 0 只说明取证完成；连接失败和缺失结果必须独立报告。
export function summarizeMacLoopback(report) {
  const rows = Array.isArray(report.results) ? report.results : []
  const errors = []
  let success = 0, failures = 0
  if (report.status !== 0 || report.signal || report.error) errors.push('探针进程未正常完成')
  if (rows.length !== 3 || new Set(rows.map(row => row?.network)).size !== 3)
    errors.push('缺少三种地址族的完整结果')
  for (const network of ['tcp4', 'tcp6', 'tcp']) {
    const row = rows.find(item => item?.network === network)
    if (!row) { errors.push(`缺少 ${network} 结果`); continue }
    const counts = Object.values(row.failures ?? {})
    if (![row.success, ...counts].every(value => Number.isSafeInteger(value) && value >= 0)) {
      errors.push(`${network} 连接计数无效`)
      continue
    }
    const failed = counts.reduce((sum, count) => sum + count, 0)
    success += row.success
    failures += failed
    if (row.success + failed !== 2000) errors.push(`${network} 未完成 2000 次独立连接`)
    if (failed) errors.push(`${network} 有 ${failed} 次连接失败`)
  }
  return { scope: 'macos-loopback-diagnostic', completeProtocolMatrix: false,
    passed: errors.length === 0, success, failures, errors }
}

// 仅在临时 CI runner 中读取本轮失败窗口的网络拒绝日志，不读取开发机系统日志。
// 诊断既不重试失败用例，也不改变网络隔离或验收结果。
export function collectMacNetworkDiagnostics(artifacts, windows) {
  if (process.platform !== 'darwin' || process.env.GITHUB_ACTIONS !== 'true' || !windows.length) return
  const start = Math.floor(Math.min(...windows.map(window => window.startedAt)) / 1000)
  const end = Math.ceil(Math.max(...windows.map(window => window.completedAt)) / 1000) + 1
  const result = spawnSync('/usr/bin/sudo', ['-n', '/usr/bin/log', 'show', '--style', 'json',
    '--start', '@' + start, '--end', '@' + end, '--info', '--debug',
    '--predicate', 'eventMessage CONTAINS "deny" AND eventMessage CONTAINS "network"'],
  { encoding: 'utf8', timeout: 15_000, maxBuffer: 8 * 1024 * 1024 })
  const report = { windows, commandStatus: result.status, signal: result.signal,
    available: false, events: [], limitation: '没有日志不能证明没有系统拒绝；记录也未必给出实际拒绝的规则' }
  try {
    if (result.error || result.status !== 0) throw result.error ?? new Error(result.stderr?.slice(0, 1000))
    const entries = JSON.parse(result.stdout)
    if (!Array.isArray(entries)) throw new Error('系统日志格式不是事件数组')
    report.events = entries.filter(entry => typeof entry.eventMessage === 'string' &&
      /deny.*network|network.*deny/i.test(entry.eventMessage)).map(entry => ({
      timestamp: entry.timestamp, processID: entry.processID, processImagePath: entry.processImagePath,
      senderImagePath: entry.senderImagePath, eventMessage: entry.eventMessage.slice(0, 2000),
    }))
    report.available = true
  } catch (error) { report.error = String(error.message).slice(0, 1000) }
  writeFileSync(resolve(artifacts, 'macos-network-denials.json'), JSON.stringify(report, null, 2),
    { mode: 0o600 })
}
