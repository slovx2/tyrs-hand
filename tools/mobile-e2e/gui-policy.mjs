import assert from 'node:assert/strict'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const xml = value => value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;')

// skip 只记录自动化范围，不生成成功清单，也不豁免手工验收。
export async function guiAutomationEnabled(platform, directory = resolve(root, '.artifacts/mobile-runtime')) {
  const policy = JSON.parse(await readFile(new URL('./acceptance-policy.json', import.meta.url), 'utf8'))
  const entry = policy[platform]
  assert.ok(entry && ['run', 'skip'].includes(entry.automatedGui), '未知移动 GUI 验收策略')
  assert.equal(entry.manualGui, 'required', '手工 GUI 验收不能被自动化 skip 豁免')
  if (entry.automatedGui === 'run') return true
  assert.ok(entry.reason, '跳过自动化必须记录原因')
  await mkdir(directory, { recursive: true })
  const report = { platform, automatedGui: 'skipped', reason: entry.reason,
    phases: ['ssh-setup', 'suite'], manualGui: 'pending', manualGuiRequired: true,
    mobileAcceptanceComplete: false, releaseReady: false }
  await writeFile(resolve(directory, platform + '-gui-skip.json'), JSON.stringify(report, null, 2) + '\n')
  const cases = report.phases.map(phase =>
    '  <testcase classname="' + platform + '.gui" name="' + phase + '"><skipped message="' + xml(entry.reason) + '"/></testcase>').join('\n')
  await writeFile(resolve(directory, platform + '-gui-junit.xml'),
    '<?xml version="1.0" encoding="UTF-8"?>\n<testsuite name="' + platform + '-automated-gui" tests="2" skipped="2" failures="0" errors="0">\n' + cases + '\n</testsuite>\n')
  process.stderr.write('[mobile-e2e] ' + platform + ' 自动化 GUI: SKIP；手工 GUI: PENDING（必需）。' + entry.reason + '\n')
  return false
}

if (import.meta.main) {
  const enabled = await guiAutomationEnabled(process.argv[2], process.argv[3] && resolve(process.argv[3]))
  process.stdout.write(String(enabled) + '\n')
}
