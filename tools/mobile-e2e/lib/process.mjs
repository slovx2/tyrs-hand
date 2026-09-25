import { createServer } from 'node:net'
import { mkdir, writeFile } from 'node:fs/promises'
import { spawn, spawnSync } from 'node:child_process'

export async function freePort() {
  return new Promise((resolve, reject) => {
    const server = createServer()
    server.unref()
    server.on('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      server.close(() => resolve(address.port))
    })
  })
}

export function run(command, args, options = {}) {
  const result = spawnSync(command, args, { stdio: 'inherit', ...options })
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(' ')} 失败（${result.status ?? result.signal}）`)
  }
}

export function output(command, args, options = {}) {
  const result = spawnSync(command, args, { encoding: 'utf8', ...options })
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(' ')} 失败：${result.stderr || result.stdout}`)
  }
  return result.stdout.trim()
}

export async function startProcess(name, command, args, { cwd, env, logDir, inheritEnv = true }) {
  await mkdir(logDir, { recursive: true })
  const chunks = []
  const child = spawn(command, args, { cwd, env: { ...(inheritEnv ? process.env : {}), ...env },
    stdio: ['ignore', 'pipe', 'pipe'] })
  const append = (data) => {
    chunks.push(data)
    if (chunks.length > 2000) chunks.shift()
  }
  child.stdout.on('data', append)
  child.stderr.on('data', append)
  let startupError
  const exit = new Promise((resolve) => {
    child.once('error', (error) => {
      startupError = error
      append(Buffer.from(String(error)))
      resolve({ error })
    })
    child.once('exit', (code, signal) => resolve({ code, signal }))
  })
  return {
    name,
    child,
    exit,
    get startupError() { return startupError },
    async stop() {
      let timer
      if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM')
      await Promise.race([exit, new Promise((resolve) => { timer = setTimeout(resolve, 5000) })])
      clearTimeout(timer)
      if (child.exitCode === null && child.signalCode === null) {
        child.kill('SIGKILL')
        await exit
      }
      await writeFile(`${logDir}/${name}.log`, Buffer.concat(chunks))
    },
  }
}

export async function waitFor(url, { timeoutMs = 60_000, process: managed } = {}) {
  const deadline = Date.now() + timeoutMs
  let last
  while (Date.now() < deadline) {
    if (managed?.startupError) throw new Error(`${managed.name} 启动失败`, { cause: managed.startupError })
    if (managed && managed.child.exitCode !== null) throw new Error(`${managed.name} 提前退出`)
    try {
      const response = await fetch(url)
      if (response.ok) return
      last = `${response.status} ${response.statusText}`
    } catch (error) {
      last = String(error)
    }
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  throw new Error(`等待 ${url} 超时：${last ?? '无响应'}`)
}
