import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

test('安装器保留 Pi 开关、独立入口与 CLI，显式环境覆盖优先', async () => {
  const source = await readFile(new URL('../deploy/worker/install.sh', import.meta.url), 'utf8')
  // 执行安装器实际的读旧值、取覆盖值和写回语句，隔离 root/systemd 等副作用。
  const fragment = source.split('\n').filter(line =>
    /^\s*(existing_pi_|worker_pi_)/.test(line) ||
    /^\s*printf "(?:TYRS_HAND_WORKER_PI_|PI_CLI=)/.test(line)).join('\n')
  const root = await mkdtemp(join(tmpdir(), 'tyrs-pi-install-'))
  const path = join(root, 'worker.env')
  try {
    for (const fixture of [
      { old: '', override: {}, expected: ['ENABLED=false', 'BIN=/usr/local/libexec/tyrs-hand-pi', 'SSH_LISTEN_ADDR=:3334', 'PI_CLI=pi'] },
      { old: "TYRS_HAND_WORKER_PI_ENABLED='true'\nTYRS_HAND_WORKER_PI_BIN='/opt/pi-adapter'\nTYRS_HAND_WORKER_PI_SSH_LISTEN_ADDR='127.0.0.1:4334'\nPI_CLI='/opt/pi-cli'\n",
        override: {}, expected: ['ENABLED=true', 'BIN=/opt/pi-adapter', 'SSH_LISTEN_ADDR=127.0.0.1:4334', 'PI_CLI=/opt/pi-cli'] },
      { old: "TYRS_HAND_WORKER_PI_ENABLED='true'\nPI_CLI='/old/pi'\n", override: { TYRS_HAND_WORKER_PI_ENABLED: 'false', PI_CLI: '/new/pi' },
        expected: ['ENABLED=false', 'PI_CLI=/new/pi'] },
    ]) {
      await writeFile(path, fixture.old)
      const result = spawnSync('/bin/sh', ['-c', `set -eu\nworker_env_file=$1\n${fragment}`, 'pi-install-test', path],
        { env: { PATH: '/usr/bin:/bin', ...fixture.override }, encoding: 'utf8' })
      assert.equal(result.status, 0, result.stderr)
      for (const expected of fixture.expected) assert.ok(result.stdout.replaceAll("'", '').includes(expected), expected)
    }
  } finally { await rm(root, { recursive: true, force: true }) }
})
