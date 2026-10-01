import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

test('安装器保留宿主 Claude CLI 配置，显式设置优先，缺省使用 PATH', async () => {
  const source = await readFile(new URL('../deploy/worker/install.sh', import.meta.url), 'utf8')
  const fragment = source.split('\n').filter(line =>
    /^\s*(existing_claude_|worker_claude_)/.test(line) ||
    /^\s*printf "TYRS_HAND_WORKER_CLAUDE_/.test(line)).join('\n')
  const root = await mkdtemp(join(tmpdir(), 'tyrs-claude-install-'))
  const path = join(root, 'worker.env')
  try {
    for (const fixture of [
      { old: '', override: {}, expected: 'claude' },
      { old: "TYRS_HAND_WORKER_CLAUDE_CLI='/opt/claude-2.1.282'\n", override: {}, expected: '/opt/claude-2.1.282' },
      { old: "TYRS_HAND_WORKER_CLAUDE_CLI='/old/claude'\n", override: { TYRS_HAND_WORKER_CLAUDE_CLI: '/new/claude' }, expected: '/new/claude' },
    ]) {
      await writeFile(path, fixture.old)
      const result = spawnSync('/bin/sh', ['-c', `set -eu\nworker_env_file=$1\n${fragment}`, 'claude-install-test', path],
        { env: { PATH: '/usr/bin:/bin', ...fixture.override }, encoding: 'utf8' })
      assert.equal(result.status, 0, result.stderr)
      assert.ok(result.stdout.includes(`TYRS_HAND_WORKER_CLAUDE_CLI='${fixture.expected}'`))
    }
  } finally { await rm(root, { recursive: true, force: true }) }
})
