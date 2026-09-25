import { createConnection, createServer } from 'node:net'
import { rm } from 'node:fs/promises'

// 每个中继只连接预先指定的测试服务，不提供动态代理或公网出口。
export async function relay(listen, target) {
  const sockets = new Set()
  const server = createServer((incoming) => {
    const outgoing = createConnection(target)
    sockets.add(incoming); sockets.add(outgoing)
    incoming.on('error', () => outgoing.destroy())
    outgoing.on('error', () => incoming.destroy())
    incoming.on('close', () => { sockets.delete(incoming); outgoing.destroy() })
    outgoing.on('close', () => { sockets.delete(outgoing); incoming.destroy() })
    incoming.pipe(outgoing).pipe(incoming)
  })
  await new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(listen, resolve)
  })
  return { address: server.address(), async close() {
    for (const socket of sockets) socket.destroy()
    await new Promise((resolve) => server.close(resolve))
    if (typeof listen === 'string') await rm(listen, { force: true })
  } }
}
