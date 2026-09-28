import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { join, basename, dirname, relative, resolve } from 'node:path'
import Ajv from 'ajv'

export function schemaIndex(root, extensionsRoot) {
  const files = new Map()
  const walk = directory => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name)
      if (entry.isDirectory()) walk(path)
      else if (entry.name.endsWith('.json')) files.set(basename(entry.name, '.json'), path)
    }
  }
  walk(root)
  const loadResponse = schemaCorrector(root)
  const index = new Map()
  // null 参数或共用响应的请求，必须显式关联官方生成的响应类型。
  const responseNames = {
    'account/logout': 'LogoutAccountResponse',
    'account/rateLimits/read': 'GetAccountRateLimitsResponse',
    'config/mcpServer/reload': 'McpServerRefreshResponse',
    'configRequirements/read': 'ConfigRequirementsReadResponse',
    'config/value/write': 'ConfigWriteResponse',
    'config/batchWrite': 'ConfigWriteResponse',
    'externalAgentConfig/import/readHistories': 'ExternalAgentConfigImportHistoriesReadResponse',
    'account/gatewayOAuth/read': 'GatewayOAuthReadResponse',
    'account/gatewayOAuth/login': 'GatewayOAuthLoginResponse',
    'account/gatewayOAuth/cancel': 'GatewayOAuthCancelResponse',
    'rollout/compress': 'RolloutCompressResponse',
    'windowsSandbox/readiness': 'WindowsSandboxReadinessResponse',
    'account/usage/read': 'GetAccountTokenUsageResponse',
    'account/workspaceMessages/read': 'GetWorkspaceMessagesResponse',
  }
  for (const kind of ['ClientRequest', 'ServerRequest', 'ClientNotification', 'ServerNotification']) {
    const schema = JSON.parse(readFileSync(join(root, `${kind}.json`), 'utf8'))
    for (const variant of schema.oneOf) for (const method of variant.properties.method.enum) {
      const params = variant.properties.params
      const responseName = responseNames[method] ?? params?.$ref?.split('/').at(-1)?.replace(/Params$/, 'Response')
      const responseFile = kind.endsWith('Request') ? files.get(responseName) : undefined
      index.set(method, {
        kind, params: { ...params, definitions: schema.definitions },
        response: responseFile ? loadResponse(responseFile) : undefined,
        references: { params: `${kind}.json${params?.$ref ?? ''}`, response: responseFile ? relative(root, responseFile) : null },
      })
    }
  }
  if (extensionsRoot) for (const file of readdirSync(extensionsRoot).filter(name => name.endsWith('.json'))) {
    const extension = JSON.parse(readFileSync(join(extensionsRoot, file), 'utf8'))
    if (index.has(extension.method)) throw new Error(`扩展不能覆盖原生 schema: ${extension.method}`)
    index.set(extension.method, { ...extension,
      response: extension.responseFile
        ? JSON.parse(readFileSync(join(root, extension.responseFile), 'utf8')) : extension.response,
      references: {
        params: `extensions/${file}#/params`, response: extension.responseFile ?? `extensions/${file}#/response`,
      } })
  }
  return index
}

// 官方 JSON Schema 与同版本官方 TS 定义矛盾时，按 protocol/schema-corrections/<版本>.json 做字段改名勘误。
// 只改名、不放宽：原字段必须存在、新字段必须不存在，任何一条无法命中即报错，上游修正后须删除勘误。
export function schemaCorrections(root) {
  const path = resolve(root, '../../../schema-corrections', `${basename(dirname(root))}.json`)
  return existsSync(path) ? JSON.parse(readFileSync(path, 'utf8')).corrections : []
}

function schemaCorrector(root) {
  const corrections = schemaCorrections(root)
  return file => {
    const schema = JSON.parse(readFileSync(file, 'utf8'))
    for (const correction of corrections.filter(item => join(root, item.file) === file)) {
      const variants = schema.definitions?.[correction.definition]?.oneOf ?? []
      const variant = variants.find(item => item.properties?.type?.enum?.[0] === correction.variant)
      if (!variant) throw new Error(`schema 勘误未命中：${correction.file} ${correction.definition}.${correction.variant}`)
      for (const [from, to] of Object.entries(correction.rename)) {
        if (!(from in variant.properties) || to in variant.properties) {
          throw new Error(`schema 勘误未命中：${correction.definition}.${correction.variant} ${from}→${to}`)
        }
        variant.properties[to] = variant.properties[from]
        delete variant.properties[from]
        variant.required = variant.required?.map(name => name === from ? to : name)
      }
    }
    return schema
  }
}

export function payloadValidator(index) {
  const ajv = new Ajv({ strict: false, allErrors: true, validateFormats: false })
  const validators = new Map()
  return (method, field, payload) => {
    const schema = index.get(method)?.[field]
    if (!schema) throw new Error(`缺少 ${method} ${field} schema`)
    const key = `${method}:${field}`
    if (!validators.has(key)) validators.set(key, ajv.compile(schema))
    const validate = validators.get(key)
    if (!validate(payload)) throw new Error(`${method} ${field}: ${ajv.errorsText(validate.errors)}`)
  }
}
