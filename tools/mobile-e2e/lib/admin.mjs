import { createHmac } from 'node:crypto'

function decodeBase32(value) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = ''
  for (const character of value.replace(/=+$/, '').toUpperCase()) {
    const index = alphabet.indexOf(character)
    if (index < 0) throw new Error('TOTP Secret 不是有效 Base32')
    bits += index.toString(2).padStart(5, '0')
  }
  const bytes = Buffer.alloc(Math.floor(bits.length / 8))
  for (let index = 0; index < bytes.length; index++) {
    bytes[index] = Number.parseInt(bits.slice(index * 8, index * 8 + 8), 2)
  }
  return bytes
}

function totp(secret) {
  const counter = Buffer.alloc(8)
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30_000)))
  const digest = createHmac('sha1', decodeBase32(secret)).update(counter).digest()
  const offset = digest[digest.length - 1] & 0x0f
  return ((digest.readUInt32BE(offset) & 0x7fffffff) % 1_000_000).toString().padStart(6, '0')
}

export class AdminClient {
  constructor(baseURL) {
    this.baseURL = baseURL
    this.cookie = ''
    this.csrf = ''
  }

  async request(path, { method = 'GET', body, csrf = false } = {}) {
    const headers = { accept: 'application/json' }
    if (this.cookie) headers.cookie = this.cookie
    if (csrf) headers['x-csrf-token'] = this.csrf
    if (body !== undefined) headers['content-type'] = 'application/json'
    const response = await fetch(`${this.baseURL}/api/v1${path}`, {
      method, headers, body: body === undefined ? undefined : JSON.stringify(body),
    })
    const text = await response.text()
    if (!response.ok) throw new Error(`${method} ${path}：${response.status} ${text}`)
    return text ? JSON.parse(text) : undefined
  }

  async initialize(setupToken) {
    const setup = await this.request('/setup/admin', { method: 'POST', body: {
      setupToken, username: 'mobile-e2e-admin', password: 'mobile-e2e-password-2026',
    } })
    const response = await fetch(`${this.baseURL}/api/v1/auth/login`, {
      method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({
        username: 'mobile-e2e-admin', password: 'mobile-e2e-password-2026',
        totp: totp(setup.totpSecret),
      }),
    })
    if (!response.ok) throw new Error(`管理员登录失败：${response.status} ${await response.text()}`)
    this.cookie = (response.headers.get('set-cookie') ?? '').split(';', 1)[0]
    const login = await response.json()
    this.csrf = login.csrfToken
  }

  createNode(name) {
    return this.request('/execution-nodes', { method: 'POST', csrf: true,
      body: { name, roles: ['discord'], maxConcurrentJobs: 2 } })
  }

  configureProvider(baseUrl) {
    return this.request('/settings/agent-provider', { method: 'PUT', csrf: true, body: {
      modelSource: 'provider', baseUrl, apiKey: 'mobile-e2e-key', model: 'gpt-5.6-sol',
      reasoningEffort: 'high', serviceTier: 'standard', proxyUrl: '',
    } })
  }

  createPairing() {
    return this.request('/client-device-pairings', { method: 'POST', csrf: true, body: {} })
  }

  async approveWhenClaimed(pairingId, timeoutMs = 90_000) {
    const deadline = Date.now() + timeoutMs
    while (Date.now() < deadline) {
      const pairing = await this.request(`/client-device-pairings/${pairingId}`)
      if (pairing.status === 'waiting_confirmation') {
        await this.request(`/client-device-pairings/${pairingId}/approve`, {
          method: 'POST', csrf: true, body: {},
        })
        return
      }
      if (pairing.status === 'rejected' || pairing.status === 'expired') {
        throw new Error(`设备绑定进入 ${pairing.status}`)
      }
      await new Promise((resolve) => setTimeout(resolve, 500))
    }
    throw new Error('等待设备 claim 超时')
  }
}
