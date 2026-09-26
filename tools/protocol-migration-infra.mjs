import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash, randomBytes } from 'node:crypto'
import { mkdir, readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { AdminClient } from './mobile-e2e/lib/admin.mjs'
import { freePort, output, startProcess, waitFor } from './mobile-e2e/lib/process.mjs'

export const OLD_COMMIT = '81f6c951d83c0b0e10a97436d9605cba3b3f77ba'
const postgresImage = 'postgres:18.3-bookworm@sha256:80630f83606d8db77d30b3851b16a9f78be2d0d4dda6f7b82a1fdca5ebe3acba'
const redisImage = 'redis:8.4.0-bookworm@sha256:c22af04bb576503bf16b3e34a1fd2fd82de0f765afd866d2e380145e0af30d78'
export const sha256 = value => createHash('sha256').update(value).digest('hex')
export const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))

export async function until(label, probe, timeout = 60_000) {
  const deadline = Date.now() + timeout
  let last
  while (Date.now() < deadline) {
    try { const result = await probe(); if (result) return result } catch (error) { last = error }
    await sleep(250)
  }
  throw new Error(`${label} 超时`, { cause: last })
}

export async function buildMigrationBinaries(repo, root) {
  try { output('git', ['cat-file', '-e', OLD_COMMIT + '^{commit}'], { cwd: repo }) }
  catch {
    // CI 浅克隆可能没有旧对象；只取精确 SHA 到 FETCH_HEAD，不切换分支。
    output('git', ['fetch', '--no-tags', 'origin', OLD_COMMIT], { cwd: repo, timeout: 120_000 })
  }
  assert.equal(output('git', ['rev-parse', OLD_COMMIT + '^{commit}'], { cwd: repo }), OLD_COMMIT)
  const oldSource = resolve(root, 'old-source')
  await mkdir(oldSource)
  // git archive 仅提取固定提交，不操作当前分支或 worktree。
  const archive = resolve(root, 'old-source.tar')
  output('git', ['archive', '--format=tar', '--output', archive, OLD_COMMIT], { cwd: repo })
  output('tar', ['-xf', archive, '-C', oldSource])
  const goroot = output('go', ['env', 'GOROOT'], { cwd: repo })
  const go = resolve(goroot, 'bin/go')
  assert.equal(output(go, ['version']).split(' ')[2], 'go1.26.6')
  const versions = { old: OLD_COMMIT, new: output('git', ['rev-parse', 'HEAD'], { cwd: repo }) }
  const binaries = {}, manifests = {}
  for (const generation of ['old', 'new']) {
    const source = generation === 'old' ? oldSource : repo
    binaries[generation] = {}
    manifests[generation] = { commit: versions[generation], binaries: {} }
    const protocol = await readFile(resolve(source, 'internal/workerprotocol/types.go'), 'utf8')
    const protocolVersion = Number(protocol.match(/const Version = (\d+)/)?.[1])
    assert.equal(protocolVersion, generation === 'old' ? 32 : 33)
    manifests[generation].protocolVersion = protocolVersion
    for (const command of ['admin', 'server', 'worker']) {
      const binary = resolve(root, `${generation}-${command}`)
      output(go, ['build', '-o', binary, `./cmd/tyrs-hand-${command}`], {
        cwd: source, env: { ...process.env, PATH: resolve(goroot, 'bin') + ':' + process.env.PATH },
        timeout: 180_000,
      })
      binaries[generation][command] = binary
      manifests[generation].binaries[command] = sha256(await readFile(binary))
    }
  }
  return { binaries, manifests, oldSource }
}

export class MigrationControl {
  constructor(root, binaries) {
    Object.assign(this, { root, binaries })
    this.containers = []
    this.processes = []
  }

  docker(args) { return output('docker', args, { timeout: 120_000 }) }

  sql(query) {
    return this.docker(['exec', this.postgres, 'psql', '-U', 'migration', '-d', 'migration',
      '-X', '-q', '-t', '-A', '-v', 'ON_ERROR_STOP=1', '-c', query])
  }

  async start() {
    this.postgres = this.docker(['run', '--detach', '--rm', '-p', '127.0.0.1::5432',
      '-e', 'POSTGRES_DB=migration', '-e', 'POSTGRES_USER=migration',
      '-e', 'POSTGRES_PASSWORD=mock-only', postgresImage])
    this.containers.push(this.postgres)
    this.redis = this.docker(['run', '--detach', '--rm', '-p', '127.0.0.1::6379', redisImage])
    this.containers.push(this.redis)
    // 镜像初始化会短暂启动仅 Unix socket 的服务器；必须等最终 TCP 服务。
    await until('PostgreSQL 启动', () => this.docker(['exec', this.postgres, 'pg_isready', '-h', '127.0.0.1', '-U', 'migration']))
    await until('Redis 启动', () => this.docker(['exec', this.redis, 'redis-cli', 'ping']))
    const pgPort = this.docker(['port', this.postgres, '5432/tcp']).split(':').at(-1)
    const redisPort = this.docker(['port', this.redis, '6379/tcp']).split(':').at(-1)
    this.port = await freePort()
    this.baseURL = `http://127.0.0.1:${this.port}`
    await mkdir(resolve(this.root, 'control-home'), { mode: 0o700 })
    this.environment = {
      HOME: resolve(this.root, 'control-home'), PATH: process.env.PATH, LANG: 'en_US.UTF-8',
      TYRS_HAND_DATABASE_URL: `postgres://migration:mock-only@127.0.0.1:${pgPort}/migration?sslmode=disable`,
      TYRS_HAND_REDIS_URL: `redis://127.0.0.1:${redisPort}/1`,
      TYRS_HAND_HTTP_ADDR: `127.0.0.1:${this.port}`, TYRS_HAND_PUBLIC_URL: this.baseURL,
      TYRS_HAND_SETUP_TOKEN: randomBytes(24).toString('hex'),
      TYRS_HAND_MASTER_KEY: randomBytes(32).toString('base64'),
      TYRS_HAND_COOKIE_SECURE: 'false', TYRS_HAND_ENV: 'development',
    }
    await this.startGeneration('old')
    this.admin = new AdminClient(this.baseURL)
    await this.admin.initialize(this.environment.TYRS_HAND_SETUP_TOKEN)
    await this.admin.request('/settings/discord', { method: 'PUT', csrf: true,
      body: { guildId: '999000000000000001', enabled: false } })
    // 唯一业务前提夹具：模拟已同步的成员目录。Workspace、项目、会话均由真实接口产生。
    this.sql("INSERT INTO discord_members(guild_id,discord_user_id,username) VALUES ('999000000000000001','999000000000000002','migration-fixture')")
    this.registration = await this.admin.createWorker('real-migration-32-to-33')
    this.workspace = await this.admin.request('/workspaces', { method: 'POST', csrf: true,
      body: { ownerDiscordUserId: '999000000000000002', workerId: this.registration.worker.id } })
    return this
  }

  async startGeneration(generation) {
    output(this.binaries[generation].admin, ['migrate'], { cwd: this.root, env: this.environment, timeout: 90_000 })
    this.server = await startProcess(`${generation}-control-${this.processes.length + 1}`, this.binaries[generation].server, [], {
      cwd: this.root, env: this.environment, inheritEnv: false, logDir: resolve(this.root, 'logs'),
    })
    this.processes.push(this.server)
    await waitFor(this.baseURL + '/healthz', { process: this.server })
  }

  async upgrade() { await this.server.stop(); await this.startGeneration('new') }

  snapshotDatabase() {
    return execFileSync('docker', ['exec', this.postgres, 'pg_dump', '-U', 'migration',
      '-d', 'migration', '--format=custom'], { timeout: 60_000, maxBuffer: 64 * 1024 * 1024 })
  }

  async restoreOldDatabase(snapshot) {
    await this.server.stop()
    // 保留首次升级的数据库；恢复旧快照仅用于隔离 Worker 回滚故障，不冒充原库向后兼容。
    this.docker(['exec', this.postgres, 'psql', '-U', 'migration', '-d', 'postgres',
      '-v', 'ON_ERROR_STOP=1', '-c', 'ALTER DATABASE migration RENAME TO migration_after_first_upgrade'])
    this.docker(['exec', this.postgres, 'createdb', '-U', 'migration', 'migration'])
    execFileSync('docker', ['exec', '-i', this.postgres, 'pg_restore', '-U', 'migration',
      '-d', 'migration', '--exit-on-error'], { input: snapshot, timeout: 60_000, maxBuffer: 8 * 1024 * 1024 })
    await this.startGeneration('old')
  }

  async scan() {
    return until('真实 Worker 项目扫描', async () => {
      const result = await this.admin.request(`/workers/${this.registration.worker.id}/workspace/scan`,
        { method: 'POST', csrf: true, body: {} })
      return result.scan?.projects?.length ? result : undefined
    })
  }

  async close() {
    for (const process of this.processes.reverse()) await process.stop()
    for (const container of this.containers.reverse()) this.docker(['rm', '-f', container])
  }
}
