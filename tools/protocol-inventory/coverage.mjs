import { foreignServerResponse } from './faults.mjs'

// 专项可以固定引擎；指定双引擎时，两边都必须在本轮实际执行通过。
export function semanticCoverage(acceptance, executions, runId) {
  return acceptance.groups.map(group => ({ ...group, missing: group.cases.filter(id => {
    const engines = group.caseEngines?.[id] ?? ['claude-code']
    if (!Array.isArray(engines) || !engines.length ||
        engines.some(engine => !['codex', 'claude-code'].includes(engine)))
      throw new Error(`语义用例 ${id} 的引擎配置无效`)
    return engines.some(engine => !executions.some(entry => entry.runId === runId &&
      entry.engine === engine && entry.status === 'passed' && entry.caseIds.includes(id)))
  }) }))
}

// 把真实 wire、schema 和本次通过的用例关联；不信任方法名计数或历史成功文件。
export function protocolCoverage(manifest, usages, artifacts, executions, runId, index, validate) {
  const missing = []
  const evidence = []
  const known = new Set(manifest.methods.map(entry => entry.method))
  for (const usage of usages)
    if (!known.has(usage.method)) missing.push({ reason: '未登记调用', ...usage })
  if (!runId) missing.push({ reason: '缺少本次执行 ID' })
  const passed = executions.filter(entry => entry.runId === runId && entry.status === 'passed')
  for (const artifact of artifacts) {
    if (artifact.runId !== runId || artifact.kind !== 'wire') continue
    const execution = passed.find(entry => entry.engine === artifact.engine && entry.caseName === artifact.caseName)
    if (!execution) continue
    if (artifact.payload.protocolErrors?.length) {
      missing.push({ caseName: artifact.caseName, reason: '运行时记录了 schema 错误' })
      continue
    }
    const pending = new Map()
    const interrupted = new Map()
    const requestKey = (sender, id) => `${sender}:${JSON.stringify(id)}`
    let closed = false
    for (const message of artifact.payload.messages) {
      if (closed) {
        missing.push({ reason: '连接关闭后仍出现报文', caseName: artifact.caseName })
        continue
      }
      if (message.transport) {
        const end = message.transport
        const processClosed = end.source === 'process' &&
          ((Number.isInteger(end.exitCode) && end.signal === null) ||
            (end.exitCode === null && typeof end.signal === 'string'))
        const connectionClosed = end.source === 'connection' && end.observed === 'read-error' &&
          typeof end.error === 'string' && end.error.length > 0 &&
          ['client-disconnect', 'runtime-restart'].includes(end.expectedReason)
        if (end.event !== 'closed' || message.method || message.id != null || (!processClosed && !connectionClosed)) {
          missing.push({ reason: '无效的传输关闭证据', caseName: artifact.caseName })
          continue
        }
        closed = true
        // 只接受用例主动注入且真实观察到的崩溃，不把任意断线视为请求完成。
        if (end.expectedSignal === 'SIGKILL' && end.signal === end.expectedSignal) {
          for (const request of pending.values()) evidence.push({ engine: artifact.engine,
            method: request.method, cases: execution.caseIds, outcome: 'interrupted' })
          pending.clear()
        }
        if (connectionClosed) {
          // 客户端断线仅结束这条连接上的待答回调，不能掩盖普通 RPC 缺响应。
          for (const [key, request] of pending) if (key.startsWith('server:')) {
            evidence.push({ engine: artifact.engine, method: request.method, cases: execution.caseIds, outcome: 'interrupted' })
            pending.delete(key)
          }
        }
        continue
      }
      const sender = message.direction === 'client' ? 'client' : 'server'
      if (message.method) {
        const contract = index.get(message.method)
        // 专用未知方法负例只允许得到标准 -32601，不算任何必需方法覆盖。
        if (!contract && message.method === 'unknown/protocol') {
          pending.set(requestKey(sender, message.id), message)
          continue
        }
        if (!known.has(message.method)) missing.push({ method: message.method, reason: 'wire 出现未登记方法' })
        try {
          if (!contract) throw new Error('缺少 schema')
          const expected = contract.kind.startsWith('Client') ? 'client' : 'server'
          if (sender !== expected) throw new Error('报文方向错误')
          if (contract.kind.endsWith('Request')) {
            if (message.id == null) throw new Error('请求缺少 ID')
            const key = requestKey(sender, message.id)
            if (pending.has(key) || interrupted.has(key)) throw new Error('重复的未完成或已取消请求 ID')
            pending.set(key, message)
            try {
              validate(message.method, 'params', message.params)
            } catch (error) {
              // 负例仍保存原始报文，并且必须收到匹配的参数错误；不能用注解跳过响应校验。
              if (sender !== 'client' || message.expectedErrorCode !== -32602) throw error
            }
          } else {
            validate(message.method, 'params', message.params)
            if (message.method === 'serverRequest/resolved') {
              const key = requestKey('server', message.params.requestId)
              const request = pending.get(key)
              if (request && request.params.threadId === message.params.threadId) {
                evidence.push({ engine: artifact.engine, method: request.method, cases: execution.caseIds, outcome: 'interrupted' })
                pending.delete(key)
                interrupted.set(key, request)
              }
            }
            evidence.push({ engine: artifact.engine, method: message.method, cases: execution.caseIds, outcome: 'success' })
          }
        } catch (error) {
          missing.push({ method: message.method, reason: String(error), caseName: artifact.caseName })
        }
      } else if (message.id != null) {
        const key = requestKey(sender === 'client' ? 'server' : 'client', message.id)
        const late = interrupted.has(key)
        const request = pending.get(key) ?? interrupted.get(key)
        if (message.expectedForeignServerResponse === true) {
          try {
            if (request) throw new Error('跨引擎负例不能覆盖本连接的挂起请求')
            evidence.push(foreignServerResponse(artifact, message, artifacts, executions, runId, index, validate))
          } catch (error) {
            missing.push({ reason: String(error), caseName: artifact.caseName })
          }
          continue
        }
        if (!request) { missing.push({ reason: '响应没有匹配请求', caseName: artifact.caseName }); continue }
        pending.delete(key)
        interrupted.delete(key)
        try {
          if (request.expectedErrorCode !== undefined) {
            if (!Number.isInteger(request.expectedErrorCode) || message.error?.code !== request.expectedErrorCode ||
                typeof message.error?.message !== 'string' || 'result' in message)
              throw new Error('负例没有返回预期的标准错误')
            evidence.push({ engine: artifact.engine, method: request.method, cases: execution.caseIds, outcome: 'rejected' })
            continue
          }
          if (message.error) {
            if (!Number.isInteger(message.error.code) || typeof message.error.message !== 'string') throw new Error('JSON-RPC 错误格式无效')
            if (request.method === 'unknown/protocol' && message.error.code !== -32601) throw new Error('未知方法错误码无效')
            if (!late && message.error.code === -32004)
              evidence.push({ engine: artifact.engine, method: request.method, cases: execution.caseIds, outcome: 'not-applicable' })
          } else {
            validate(request.method, 'response', message.result)
            // 故障用例可故意回送迟到答案，但仍校验 schema，绝不计为成功。
            evidence.push({ engine: artifact.engine, method: request.method, cases: execution.caseIds, outcome: late ? 'late' : 'success' })
          }
        } catch (error) {
          missing.push({ method: request.method, reason: String(error), caseName: artifact.caseName })
        }
      }
    }
    for (const request of pending.values()) missing.push({ method: request.method, reason: '请求缺少终结响应', caseName: artifact.caseName })
  }
  for (const entry of manifest.methods) {
    if (!entry.schema?.params) missing.push({ method: entry.method, reason: '缺少 schema 登记' })
    for (const engine of ['codex', 'claude-code']) {
      const requirement = entry.engines[engine]
      if (!['required', 'not-applicable'].includes(requirement)) {
        missing.push({ method: entry.method, engine, reason: '能力分类无效' }); continue
      }
      if (requirement === 'not-applicable' && !entry.reasons?.[engine])
        missing.push({ method: entry.method, engine, reason: '不适用能力缺少原因' })
      const cases = entry.cases?.[engine] ?? []
      if (!cases.length) missing.push({ method: entry.method, engine, reason: '未登记自动化用例' })
      const outcome = requirement === 'required' ? 'success' : 'not-applicable'
      for (const id of cases) {
        if (!evidence.some(item => item.engine === engine && item.method === entry.method && item.outcome === outcome && item.cases.includes(id)))
          missing.push({ method: entry.method, engine, caseId: id, reason: '用例没有成功执行此协议及 schema 校验' })
      }
    }
  }
  const uniqueMissing = [...new Map(missing.map(item => [JSON.stringify(item), item])).values()]
  return { runId, complete: uniqueMissing.length === 0, missing: uniqueMissing, evidence, usages }
}
