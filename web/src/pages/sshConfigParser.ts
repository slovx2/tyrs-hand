export interface ParsedSSHHost {
  alias: string
  hostname: string
  port: number
  username: string
  proxyJumpAlias?: string
}

export interface SSHConfigParseResult {
  hosts: ParsedSSHHost[]
  identityFiles: string[]
  ignoredDirectives: string[]
}

interface HostOptions {
  hostname?: string
  port?: string
  username?: string
  proxyJumpAlias?: string
}

interface HostBlock {
  aliases: string[]
  options: HostOptions
  line: number
}

const aliasPattern = /^[A-Za-z0-9._-]+$/
const maxImportHosts = 100

export function parseSSHConfig(source: string): SSHConfigParseResult {
  const blocks: HostBlock[] = []
  const globalOptions: HostOptions = {}
  const wildcardOptions: HostOptions = {}
  const identityFiles = new Set<string>()
  const ignoredDirectives = new Set<string>()
  let current: HostBlock | null = null

  source.split(/\r?\n/).forEach((rawLine, index) => {
    const lineNumber = index + 1
    const line = stripComment(rawLine).trim()
    if (!line) return
    const directive = parseDirective(line, lineNumber)
    const key = directive.key.toLowerCase()
    if (key === 'match') {
      throw new Error(`第 ${lineNumber} 行：暂不支持 Match 条件块`)
    }
    if (key === 'host') {
      const aliases = splitArguments(directive.value, lineNumber)
      if (aliases.length === 0) {
        throw new Error(`第 ${lineNumber} 行：Host 后需要填写别名`)
      }
      current = { aliases, options: {}, line: lineNumber }
      blocks.push(current)
      return
    }

    const target = current?.options ?? globalOptions
    switch (key) {
      case 'hostname':
        target.hostname = singleValue(directive.value, lineNumber, 'HostName')
        break
      case 'user':
        target.username = singleValue(directive.value, lineNumber, 'User')
        break
      case 'port':
        target.port = singleValue(directive.value, lineNumber, 'Port')
        break
      case 'proxyjump':
        target.proxyJumpAlias = singleValue(
          directive.value,
          lineNumber,
          'ProxyJump',
        )
        break
      case 'identityfile':
        identityFiles.add(
          singleValue(directive.value, lineNumber, 'IdentityFile'),
        )
        break
      default:
        ignoredDirectives.add(directive.key)
    }
  })

  const concreteBlocks: HostBlock[] = []
  for (const block of blocks) {
    if (block.aliases.length === 1 && block.aliases[0] === '*') {
      Object.assign(wildcardOptions, block.options)
      continue
    }
    if (block.aliases.some(hasHostPattern)) {
      ignoredDirectives.add(`Host ${block.aliases.join(' ')}`)
      continue
    }
    concreteBlocks.push(block)
  }

  const defaults = { ...globalOptions, ...wildcardOptions }
  const hosts: ParsedSSHHost[] = []
  const seenAliases = new Set<string>()
  for (const block of concreteBlocks) {
    const options = { ...defaults, ...block.options }
    for (const alias of block.aliases) {
      validateAlias(alias, block.line)
      if (seenAliases.has(alias)) {
        throw new Error(`第 ${block.line} 行：主机别名 ${alias} 重复`)
      }
      seenAliases.add(alias)
      const hostname = (options.hostname || alias).replaceAll('%h', alias)
      const username = options.username || 'root'
      const port = parsePort(options.port, block.line)
      const proxyJumpAlias = parseProxyJump(options.proxyJumpAlias, block.line)
      if (/\s/.test(hostname) || hostname.length > 255) {
        throw new Error(`第 ${block.line} 行：${alias} 的 HostName 格式不正确`)
      }
      if (/\s/.test(username) || username.length > 128) {
        throw new Error(`第 ${block.line} 行：${alias} 的 User 格式不正确`)
      }
      hosts.push({
        alias,
        hostname,
        port,
        username,
        ...(proxyJumpAlias ? { proxyJumpAlias } : {}),
      })
    }
  }
  if (hosts.length === 0) {
    throw new Error('没有找到可导入的具体 Host 配置')
  }
  if (hosts.length > maxImportHosts) {
    throw new Error(`每次最多导入 ${maxImportHosts} 台主机`)
  }
  return {
    hosts,
    identityFiles: [...identityFiles],
    ignoredDirectives: [...ignoredDirectives],
  }
}

function parseDirective(line: string, lineNumber: number) {
  const match = line.match(/^([^\s=]+)(?:\s*=\s*|\s+)(.*)$/)
  if (!match || !match[2].trim()) {
    throw new Error(`第 ${lineNumber} 行：配置格式不正确`)
  }
  return { key: match[1], value: match[2].trim() }
}

function stripComment(line: string) {
  let quote = ''
  let escaped = false
  for (let index = 0; index < line.length; index += 1) {
    const character = line[index]
    if (escaped) {
      escaped = false
      continue
    }
    if (character === '\\') {
      escaped = true
      continue
    }
    if ((character === '"' || character === "'") && !quote) {
      quote = character
      continue
    }
    if (character === quote) {
      quote = ''
      continue
    }
    if (character === '#' && !quote) return line.slice(0, index)
  }
  return line
}

function splitArguments(value: string, lineNumber: number) {
  const result: string[] = []
  let current = ''
  let quote = ''
  let escaped = false
  for (const character of value) {
    if (escaped) {
      current += character
      escaped = false
    } else if (character === '\\') {
      escaped = true
    } else if ((character === '"' || character === "'") && !quote) {
      quote = character
    } else if (character === quote) {
      quote = ''
    } else if (/\s/.test(character) && !quote) {
      if (current) {
        result.push(current)
        current = ''
      }
    } else {
      current += character
    }
  }
  if (escaped) current += '\\'
  if (quote) throw new Error(`第 ${lineNumber} 行：引号没有闭合`)
  if (current) result.push(current)
  return result
}

function singleValue(value: string, lineNumber: number, key: string) {
  const values = splitArguments(value, lineNumber)
  if (values.length !== 1) {
    throw new Error(`第 ${lineNumber} 行：${key} 只能填写一个值`)
  }
  return values[0]
}

function hasHostPattern(alias: string) {
  return ['*', '!', '?', '[', ']'].some((character) =>
    alias.includes(character),
  )
}

function validateAlias(alias: string, lineNumber: number) {
  if (!aliasPattern.test(alias) || alias.length > 128) {
    throw new Error(`第 ${lineNumber} 行：Host 别名 ${alias} 格式不正确`)
  }
}

function parsePort(value: string | undefined, lineNumber: number) {
  if (!value) return 22
  if (!/^\d+$/.test(value)) {
    throw new Error(`第 ${lineNumber} 行：Port 必须是数字`)
  }
  const port = Number(value)
  if (port < 1 || port > 65535) {
    throw new Error(`第 ${lineNumber} 行：Port 必须在 1 到 65535 之间`)
  }
  return port
}

function parseProxyJump(value: string | undefined, lineNumber: number) {
  if (!value || value.toLowerCase() === 'none') return undefined
  if (value.includes(',') || value.includes('@') || value.includes(':')) {
    throw new Error(
      `第 ${lineNumber} 行：ProxyJump 只支持一个已配置的 Host 别名`,
    )
  }
  validateAlias(value, lineNumber)
  return value
}
