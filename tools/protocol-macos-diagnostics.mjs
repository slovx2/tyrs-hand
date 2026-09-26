import { spawnSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

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
