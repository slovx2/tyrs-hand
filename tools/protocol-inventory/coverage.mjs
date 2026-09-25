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
    let closed = false
    for (const message of artifact.payload.messages) {
      if (closed) {
        missing.push({ reason: '连接关闭后仍出现报文', caseName: artifact.caseName })
        continue
      }
      if (message.transport) {
        const end = message.transport
        if (end.event !== 'closed' || end.source !== 'process' || message.method || message.id != null ||
            !((Number.isInteger(end.exitCode) && end.signal === null) ||
              (end.exitCode === null && typeof end.signal === 'string'))) {
          missing.push({ reason: '无效的进程关闭证据', caseName: artifact.caseName })
          continue
        }
        closed = true
        // 只接受用例主动注入且真实观察到的崩溃，不把任意断线视为请求完成。
        if (end.expectedSignal === 'SIGKILL' && end.signal === end.expectedSignal) {
          for (const request of pending.values()) evidence.push({ engine: artifact.engine,
            method: request.method, cases: execution.caseIds, outcome: 'interrupted' })
          pending.clear()
        }
        continue
      }
      const sender = message.direction === 'client' ? 'client' : 'server'
      if (message.method) {
        const contract = index.get(message.method)
        // 专用未知方法负例只允许得到标准 -32601，不算任何必需方法覆盖。
        if (!contract && message.method === 'unknown/protocol') {
          pending.set(`${sender}:${message.id}`, message)
          continue
        }
        if (!known.has(message.method)) missing.push({ method: message.method, reason: 'wire 出现未登记方法' })
        try {
          if (!contract) throw new Error('缺少 schema')
          const expected = contract.kind.startsWith('Client') ? 'client' : 'server'
          if (sender !== expected) throw new Error('报文方向错误')
          if (contract.kind.endsWith('Request')) {
            if (message.id == null) throw new Error('请求缺少 ID')
            if (pending.has(`${sender}:${message.id}`)) throw new Error('重复的未完成请求 ID')
            pending.set(`${sender}:${message.id}`, message)
            try {
              validate(message.method, 'params', message.params)
            } catch (error) {
              // 负例仍保存原始报文，并且必须收到匹配的参数错误；不能用注解跳过响应校验。
              if (sender !== 'client' || message.expectedErrorCode !== -32602) throw error
            }
          } else {
            validate(message.method, 'params', message.params)
            evidence.push({ engine: artifact.engine, method: message.method, cases: execution.caseIds, outcome: 'success' })
          }
        } catch (error) {
          missing.push({ method: message.method, reason: String(error), caseName: artifact.caseName })
        }
      } else if (message.id != null) {
        const key = `${sender === 'client' ? 'server' : 'client'}:${message.id}`
        const request = pending.get(key)
        if (!request) { missing.push({ reason: '响应没有匹配请求', caseName: artifact.caseName }); continue }
        pending.delete(key)
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
            if (message.error.code === -32004)
              evidence.push({ engine: artifact.engine, method: request.method, cases: execution.caseIds, outcome: 'not-applicable' })
          } else {
            validate(request.method, 'response', message.result)
            evidence.push({ engine: artifact.engine, method: request.method, cases: execution.caseIds, outcome: 'success' })
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
