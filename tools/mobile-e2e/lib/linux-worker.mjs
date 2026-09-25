import { readFile } from 'node:fs/promises'
import { spawn } from 'node:child_process'
import { relay } from './network.mjs'

// 此入口必须运行在 unshare 创建的网络命名空间内，只有 loopback 和固定 Unix 中继。
const config = JSON.parse(await readFile(process.argv[2], 'utf8'))
const relays = []
let child
try {
  for (const port of config.outboundPorts) {
    relays.push(await relay({ host: '127.0.0.1', port }, { path: `${config.root}/out-${port}.sock` }))
  }
  for (const port of config.sshPorts) {
    relays.push(await relay(`${config.root}/ssh-${port}.sock`, { host: '127.0.0.1', port }))
  }
  child = spawn(config.binary, [], { cwd: config.cwd, env: process.env, stdio: 'inherit' })
  const stop = () => child.kill('SIGTERM')
  process.on('SIGTERM', stop)
  process.on('SIGINT', stop)
  process.exitCode = await new Promise((resolve) => {
    child.once('error', (error) => { process.stderr.write(String(error)); resolve(1) })
    child.once('exit', (code, signal) => resolve(code ?? (signal === 'SIGTERM' ? 0 : 1)))
  })
  process.removeListener('SIGTERM', stop)
  process.removeListener('SIGINT', stop)
} finally {
  for (const item of relays.reverse()) await item.close()
}
