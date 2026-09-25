import { execFileSync, fork } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const postgres = 'postgres:18.3-bookworm@sha256:80630f83606d8db77d30b3851b16a9f78be2d0d4dda6f7b82a1fdca5ebe3acba'
const redis = 'redis:8.4.0-bookworm@sha256:c22af04bb576503bf16b3e34a1fd2fd82de0f765afd866d2e380145e0af30d78'

export async function startControlInfrastructure() {
  const root = mkdtempSync('/tmp/tyrs-protocol-db-')
  const containers = []
  let proxy
  const docker = args => execFileSync('docker', args, { encoding: 'utf8', timeout: 120_000 }).trim()
  const close = () => {
    proxy?.kill('SIGTERM')
    for (const id of containers.reverse()) {
      try { docker(['rm', '-f', id]) } catch (error) { console.error('清理临时数据库失败:', error.message) }
    }
    rmSync(root, { recursive: true, force: true })
  }
  try {
    const pgID = docker(['run', '--detach', '--rm', '-p', '127.0.0.1::5432',
      '-e', 'POSTGRES_DB=protocol', '-e', 'POSTGRES_USER=protocol', '-e', 'POSTGRES_PASSWORD=mock-only', postgres])
    containers.push(pgID)
    const redisID = docker(['run', '--detach', '--rm', '-p', '127.0.0.1::6379', redis])
    containers.push(redisID)
    for (const [id, command] of [[pgID, ['pg_isready', '-U', 'protocol']], [redisID, ['redis-cli', 'ping']]]) {
      let ready = false
      for (let attempt = 0; attempt < 60 && !ready; attempt++) {
        try { docker(['exec', id, ...command]); ready = true } catch {
          await new Promise(resolve => setTimeout(resolve, 500))
        }
      }
      if (!ready) throw new Error('临时 Control 数据库启动超时')
    }
    const port = (id, exposed) => Number(docker(['port', id, exposed]).split(':').at(-1))
    const pgSocket = resolve(root, '.s.PGSQL.5432')
    const redisSocket = resolve(root, 'redis.sock')
    proxy = fork(resolve(dirname(fileURLToPath(import.meta.url)), 'protocol-db-proxy.mjs'),
      [JSON.stringify([{ path: pgSocket, port: port(pgID, '5432/tcp') },
        { path: redisSocket, port: port(redisID, '6379/tcp') }])], { stdio: ['ignore', 'inherit', 'inherit', 'ipc'] })
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('数据库 Unix socket 未就绪')), 10_000)
      proxy.once('message', message => { clearTimeout(timer); message === 'ready' ? resolve() : reject(new Error('代理状态无效')) })
      proxy.once('error', error => { clearTimeout(timer); reject(error) })
      proxy.once('exit', code => { clearTimeout(timer); reject(new Error(`代理意外退出: ${code}`)) })
    })
    return { close, env: {
      TYRS_HAND_TEST_DATABASE_URL: `postgres://protocol:mock-only@/protocol?host=${encodeURIComponent(root)}&sslmode=disable`,
      TYRS_HAND_TEST_REDIS_SOCKET: redisSocket,
    } }
  } catch (error) {
    close()
    throw error
  }
}
