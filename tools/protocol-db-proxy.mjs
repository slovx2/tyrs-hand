import net from 'node:net'
import { chmodSync } from 'node:fs'

// 仅将临时数据库的指定端口暴露为 Unix socket，使隔离网络内的测试无需公网访问。
const bindings = JSON.parse(process.argv[2])
// Docker Desktop 把发布端口放在宿主机上；容器内运行矩阵时经 PROTOCOL_DOCKER_HOST 访问，默认本机回环。
const host = process.env.PROTOCOL_DOCKER_HOST || '127.0.0.1'
const servers = await Promise.all(bindings.map(async ({ path, port }) => {
  const server = net.createServer(socket => {
    const upstream = net.createConnection({ host, port })
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
