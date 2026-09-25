import net from 'node:net'
import { chmodSync } from 'node:fs'

// 仅将临时数据库的指定端口暴露为 Unix socket，使隔离网络内的测试无需公网访问。
const bindings = JSON.parse(process.argv[2])
const servers = await Promise.all(bindings.map(async ({ path, port }) => {
  const server = net.createServer(socket => {
    const upstream = net.createConnection({ host: '127.0.0.1', port })
    socket.on('error', () => upstream.destroy())
    upstream.on('error', () => socket.destroy())
    socket.on('close', () => upstream.destroy())
    upstream.on('close', () => socket.destroy())
    socket.pipe(upstream).pipe(socket)
  })
  await new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(path, resolve)
  })
  chmodSync(path, 0o600)
  return server
}))
process.send?.('ready')
process.on('SIGTERM', () => {
  for (const server of servers) server.close()
  process.exit(0)
})
