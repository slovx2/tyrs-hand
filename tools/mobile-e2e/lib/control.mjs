import { randomBytes } from 'node:crypto'
import { mkdir, writeFile } from 'node:fs/promises'

import { AdminClient } from './admin.mjs'
import { freePort, output, run, startProcess, waitFor } from './process.mjs'

const POSTGRES_IMAGE = 'postgres:18.3-bookworm@sha256:80630f83606d8db77d30b3851b16a9f78be2d0d4dda6f7b82a1fdca5ebe3acba'
const REDIS_IMAGE = 'redis:8.4.0-bookworm@sha256:c22af04bb576503bf16b3e34a1fd2fd82de0f765afd866d2e380145e0af30d78'

export class ControlHarness {
  constructor({ repoRoot, runDir, label, developmentImage, listenHost = '127.0.0.1' }) {
    this.repoRoot = repoRoot
    this.runDir = runDir
    this.label = label
    this.processes = []
    const suffix = `${process.pid}-${Date.now()}-${label}`.replace(/[^a-zA-Z0-9_.-]/g, '-')
    this.postgresName = `tyrs-hand-mobile-pg-${suffix}`
    this.redisName = `tyrs-hand-mobile-redis-${suffix}`
    this.setupToken = `mobile-e2e-setup-${randomBytes(16).toString('hex')}`
    this.masterKey = randomBytes(32).toString('base64')
    this.developmentImage = developmentImage
    this.listenHost = listenHost
  }

  async start() {
    await mkdir(`${this.runDir}/logs`, { recursive: true })
    this.nativeServices = process.env.TYRS_HAND_E2E_NATIVE_SERVICES === '1'
    let postgresPort
    let redisPort
    if (this.nativeServices) {
      postgresPort = await freePort()
      redisPort = await freePort()
      const postgresRoot = `${this.runDir}/postgres`
      await mkdir(postgresRoot, { recursive: true })
      run('initdb', ['--pgdata', postgresRoot, '--username', 'tyrs_hand', '--auth', 'trust'])
      const postgres = await startProcess(`${this.label}-postgres`, 'postgres',
        ['-D', postgresRoot, '-h', '127.0.0.1', '-p', String(postgresPort)], {
          cwd: this.repoRoot, env: {}, logDir: `${this.runDir}/logs`,
        })
      this.processes.push(postgres)
      for (let attempt = 0; attempt < 60; attempt++) {
        try { output('pg_isready', ['-h', '127.0.0.1', '-p', String(postgresPort)]); break }
        catch { await new Promise((resolve) => setTimeout(resolve, 500)) }
      }
      run('createdb', ['-h', '127.0.0.1', '-p', String(postgresPort),
        '-U', 'tyrs_hand', 'tyrs_hand_e2e'])
      const redis = await startProcess(`${this.label}-redis`, 'redis-server',
        ['--bind', '127.0.0.1', '--port', String(redisPort), '--save', '', '--appendonly', 'no'], {
          cwd: this.repoRoot, env: {}, logDir: `${this.runDir}/logs`,
        })
      this.processes.push(redis)
    } else {
      run('docker', ['run', '--detach', '--name', this.postgresName,
        '--env', 'POSTGRES_DB=tyrs_hand_e2e', '--env', 'POSTGRES_USER=tyrs_hand',
        '--env', 'POSTGRES_PASSWORD=e2e-password', '--publish', '127.0.0.1::5432', POSTGRES_IMAGE])
      run('docker', ['run', '--detach', '--name', this.redisName,
        '--publish', '127.0.0.1::6379', REDIS_IMAGE])
      for (let attempt = 0; attempt < 60; attempt++) {
        try {
          output('docker', ['exec', this.postgresName, 'pg_isready', '-U', 'tyrs_hand', '-d', 'tyrs_hand_e2e'])
          break
        } catch {
          await new Promise((resolve) => setTimeout(resolve, 500))
        }
      }
      postgresPort = output('docker', ['port', this.postgresName, '5432/tcp']).split(':').at(-1)
      redisPort = output('docker', ['port', this.redisName, '6379/tcp']).split(':').at(-1)
    }
    const password = this.nativeServices ? '' : ':e2e-password'
    this.databaseURL = `postgres://tyrs_hand${password}@127.0.0.1:${postgresPort}/tyrs_hand_e2e?sslmode=disable`
    this.redisURL = `redis://127.0.0.1:${redisPort}/1`
    this.port = await freePort()
    this.baseURL = `http://127.0.0.1:${this.port}`
    this.environment = {
      TYRS_HAND_DATABASE_URL: this.databaseURL,
      TYRS_HAND_REDIS_URL: this.redisURL,
      TYRS_HAND_HTTP_ADDR: `${this.listenHost}:${this.port}`,
      TYRS_HAND_PUBLIC_URL: this.baseURL,
      TYRS_HAND_SETUP_TOKEN: this.setupToken,
      TYRS_HAND_MASTER_KEY: this.masterKey,
      TYRS_HAND_COOKIE_SECURE: 'false',
      TYRS_HAND_ENV: 'development',
      ...(this.developmentImage ? { TYRS_HAND_DEVELOPMENT_IMAGE: this.developmentImage } : {}),
    }
    const binDir = `${this.runDir}/bin`
    await mkdir(binDir, { recursive: true })
    run('go', ['build', '-o', `${binDir}/admin`, './cmd/tyrs-hand-admin'], { cwd: this.repoRoot })
    run('go', ['build', '-o', `${binDir}/server`, './cmd/tyrs-hand-server'], { cwd: this.repoRoot })
    run(`${binDir}/admin`, ['migrate'], { cwd: this.repoRoot,
      env: { ...process.env, ...this.environment } })
    this.serverBin = `${binDir}/server`
    this.serverGeneration = 0
    await this.startServer()
    await waitFor(`${this.baseURL}/healthz`, { process: this.server })
    this.admin = new AdminClient(this.baseURL)
    await this.admin.initialize(this.setupToken)
    return this
  }

  async startServer() {
    this.serverGeneration += 1
    const suffix = this.serverGeneration === 1 ? '' : `-${this.serverGeneration}`
    const server = await startProcess(`${this.label}-server${suffix}`, this.serverBin, [], {
      cwd: this.repoRoot, env: this.environment, logDir: `${this.runDir}/logs`,
    })
    this.processes.push(server)
    this.server = server
    await waitFor(`${this.baseURL}/healthz`, { process: server })
  }

  async stopServer() {
    await this.server.stop()
  }

  async writeManifest(extra = {}) {
    await writeFile(`${this.runDir}/manifest.json`, JSON.stringify({
      platform: process.env.TYRS_HAND_E2E_PLATFORM, baseURL: this.baseURL,
      database: this.postgresName, redis: this.redisName, ...extra,
    }, null, 2))
  }

  async stop() {
    for (const managed of this.processes.reverse()) await managed.stop()
    if (!this.nativeServices) {
      for (const name of [this.postgresName, this.redisName]) {
        try { run('docker', ['rm', '--force', name]) } catch { /* 最佳努力清理 */ }
      }
    }
  }
}
