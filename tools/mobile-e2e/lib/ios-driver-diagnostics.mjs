import { mkdir, readFile, readdir, stat, writeFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { resolve } from 'node:path'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'

const execute = promisify(execFile)
const sensitiveLine = /Input text|Inputting text|text=|device-pair|OPENSSH|PRIVATE.KEY|PRIVATE_KEY/i

function sanitize(value, secrets) {
  let text = String(value)
  for (const secret of secrets.filter(Boolean)) text = text.replaceAll(secret, '[REDACTED]')
  return text.split('\n').filter((line) => !sensitiveLine.test(line)).join('\n')
}

async function diagnosticCommand(command, args) {
  const result = await execute(command, args, { timeout: 10_000, maxBuffer: 1024 * 1024 })
  return result.stdout
}

// Maestro 2.3.0 把 XCTest 原生输出放在 debug-output 之外；只收当前 phase 的文件。
export async function collectIosDriverDiagnostics({ runDir, label, startedAt, deviceID,
  secrets = [], logDirectory = resolve(homedir(), 'Library/Logs/maestro/xctest_runner_logs'),
  command = diagnosticCommand }) {
  const destination = resolve(runDir, 'logs', `ios-driver-${label}`)
  const report = { deviceID, startedAt: new Date(startedAt).toISOString(), files: [], diagnostics: {}, errors: [] }
  await mkdir(destination, { recursive: true })
  try {
    for (const entry of await readdir(logDirectory, { withFileTypes: true })) {
      if (!entry.isFile() || !/^xctest_runner_[\d_-]+\.log$/.test(entry.name)) continue
      const source = resolve(logDirectory, entry.name)
      const metadata = await stat(source)
      if (metadata.mtimeMs < startedAt) continue
      await writeFile(resolve(destination, entry.name), sanitize(await readFile(source, 'utf8'), secrets))
      report.files.push(entry.name)
    }
  } catch (error) {
    report.errors.push({ stage: 'xctest-logs', error: sanitize(error.message, secrets) })
  }
  const tasks = [
    ['xcode-version', 'xcodebuild', ['-version']],
    ['xcode-directory', 'xcode-select', ['--print-path']],
    ['device', 'xcrun', ['simctl', 'list', 'devices', 'booted', '-j']],
    ['runtimes', 'xcrun', ['simctl', 'list', 'runtimes', 'available', '-j']],
    ['driver-processes', 'xcrun', ['simctl', 'spawn', deviceID, 'launchctl', 'list']],
  ]
  await Promise.all(tasks.map(async ([name, executable, args]) => {
    try {
      let result = await command(executable, args)
      if (name === 'device') {
        const devices = JSON.parse(result).devices
        result = JSON.stringify(Object.fromEntries(Object.entries(devices)
          .map(([runtime, values]) => [runtime, values.filter((device) => device.udid === deviceID)])
          .filter(([, values]) => values.length > 0)), null, 2)
      }
      if (name === 'driver-processes') result = result.split('\n')
        .filter((line) => /maestro|xctest|testmanagerd/i.test(line)).join('\n')
      await writeFile(resolve(destination, `${name}.log`), sanitize(result, secrets))
      report.diagnostics[name] = 'collected'
    } catch (error) {
      report.errors.push({ stage: name, error: sanitize(error.message, secrets) })
    }
  }))
  await writeFile(resolve(destination, 'report.json'), JSON.stringify(report, null, 2))
  return report
}
