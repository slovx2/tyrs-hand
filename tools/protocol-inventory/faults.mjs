import { isDeepStrictEqual } from 'node:util'

// 只接纳真实跨引擎审批负例：另一条 wire 的请求、答案和独立文件副作用必须一致。
// 这不是匹配当前连接的正常响应，永远不能成为必需协议的成功覆盖。
export function foreignServerResponse(artifact, message, artifacts, executions, runId, index, validate) {
  const engines = ['codex', 'claude-code']
  const passed = engine => executions.some(entry => entry.runId === runId &&
    entry.engine === engine && entry.caseName === artifact.caseName && entry.status === 'passed' &&
    entry.caseIds.includes('ISOLATION-004'))
  if (message.direction !== 'client' || message.error || message.method ||
      !Object.hasOwn(message, 'result') || !passed(artifact.engine))
    throw new Error('跨引擎审批负例缺少本轮通过的真实响应证据')
  const candidates = artifacts.filter(item => item.kind === 'fault-injection' &&
    item.runId === runId && item.caseName === artifact.caseName &&
    item.caseIds?.includes('ISOLATION-004') && item.payload?.type === 'foreign-server-response' &&
    item.payload.targetEngine === artifact.engine &&
    isDeepStrictEqual(item.payload.requestId, message.id))
  if (candidates.length !== 1) throw new Error('跨引擎审批负例缺少唯一的副作用制品')
  const fault = candidates[0].payload
  if (!engines.includes(fault.sourceEngine) || !engines.includes(fault.targetEngine) ||
      fault.sourceEngine === fault.targetEngine || !passed(fault.sourceEngine) ||
      fault.sideEffectsBefore !== 0 || fault.sideEffectsAfterForeignReply !== 0 ||
      fault.sideEffectsAfterAuthorizedReply !== 1 ||
      !isDeepStrictEqual(fault.reply, { id: message.id, result: message.result }))
    throw new Error('跨引擎审批负例的方向、答案或文件副作用不符')
  const contract = index.get(fault.requestMethod)
  if (contract?.kind !== 'ServerRequest' || ![
    'item/fileChange/requestApproval', 'item/commandExecution/requestApproval',
  ].includes(fault.requestMethod)) throw new Error('跨引擎负例必须关联真实工具审批请求')
  validate(fault.requestMethod, 'response', message.result)
  const source = artifacts.some(item => {
    if (item.kind !== 'wire' || item.runId !== runId || item.engine !== fault.sourceEngine ||
        item.caseName !== artifact.caseName || item.payload.protocolErrors?.length) return false
    const messages = item.payload.messages
    const requestIndex = messages.findIndex(entry => entry.direction === 'server' &&
      entry.method === fault.requestMethod && isDeepStrictEqual(entry.id, message.id))
    if (requestIndex < 0) return false
    validate(fault.requestMethod, 'params', messages[requestIndex].params)
    return messages.slice(requestIndex + 1).some(entry => entry.direction === 'client' &&
      !entry.method && !entry.error && isDeepStrictEqual(entry.id, message.id) &&
      isDeepStrictEqual(entry.result, message.result))
  })
  if (!source) throw new Error('跨引擎审批负例没有真实来源请求和后续授权答案')
  return { engine: artifact.engine, method: fault.requestMethod, cases: ['ISOLATION-004'], outcome: 'rejected' }
}
