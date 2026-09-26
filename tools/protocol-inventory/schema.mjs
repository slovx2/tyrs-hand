import { readFileSync, readdirSync } from 'node:fs'
import { join, basename, relative } from 'node:path'
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
  const index = new Map()
  // null 参数或共用响应的请求，必须显式关联官方生成的响应类型。
  const responseNames = {
    'config/mcpServer/reload': 'McpServerRefreshResponse',
    'configRequirements/read': 'ConfigRequirementsReadResponse',
    'config/value/write': 'ConfigWriteResponse',
    'config/batchWrite': 'ConfigWriteResponse',
    'externalAgentConfig/import/readHistories': 'ExternalAgentConfigImportHistoriesReadResponse',
  }
  for (const kind of ['ClientRequest', 'ServerRequest', 'ClientNotification', 'ServerNotification']) {
    const schema = JSON.parse(readFileSync(join(root, `${kind}.json`), 'utf8'))
    for (const variant of schema.oneOf) for (const method of variant.properties.method.enum) {
      const params = variant.properties.params
      const responseName = responseNames[method] ?? params?.$ref?.split('/').at(-1)?.replace(/Params$/, 'Response')
      const responseFile = kind.endsWith('Request') ? files.get(responseName) : undefined
      index.set(method, {
        kind, params: { ...params, definitions: schema.definitions },
        response: responseFile ? JSON.parse(readFileSync(responseFile, 'utf8')) : undefined,
        references: { params: `${kind}.json${params?.$ref ?? ''}`, response: responseFile ? relative(root, responseFile) : null },
      })
    }
  }
  if (extensionsRoot) for (const file of readdirSync(extensionsRoot).filter(name => name.endsWith('.json'))) {
    const extension = JSON.parse(readFileSync(join(extensionsRoot, file), 'utf8'))
    if (index.has(extension.method)) throw new Error(`扩展不能覆盖原生 schema: ${extension.method}`)
    index.set(extension.method, { ...extension, references: {
      params: `extensions/${file}#/params`, response: `extensions/${file}#/response`,
    } })
  }
  return index
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
