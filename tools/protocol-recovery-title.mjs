import assert from 'node:assert/strict'

// 只接受真实录制中该线程所有标题请求闭合且最新标题一致；重启后的成功不能掩盖旧代未完成请求。
export function completedTitleProjection(rows, threadId, title) {
  const requests = rows.filter(row => row.direction === 'request' &&
    row.message.method === 'thread/name/set' && row.message.params?.threadId === threadId)
  const request = requests.at(-1)
  if (!request || request.message.params.name !== title) return undefined
  let acknowledgedAt
  for (const pending of requests) {
    const responses = rows.slice(rows.indexOf(pending) + 1).filter(row =>
      row.connection === pending.connection && row.direction === 'response' &&
      !row.message.method && row.message.id === pending.message.id)
    if (!responses.length) return undefined
    assert.equal(responses.length, 1, '标题请求必须恰好一个真实响应')
    assert.equal(responses[0].message.error, undefined, '标题回写不能以失败响应冒充完成')
    assert.ok(Object.hasOwn(responses[0].message, 'result'), '标题响应必须有实际结果')
    acknowledgedAt = responses[0].timestamp
  }
  return { threadId, title, connection: request.connection, requestId: request.message.id,
    requestedAt: request.timestamp, acknowledgedAt }
}
