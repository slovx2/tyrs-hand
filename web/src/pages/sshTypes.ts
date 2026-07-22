export interface SSHCredential {
  id: string
  name: string
  publicKey: string
  fingerprint: string
  enabled: boolean
  version: number
  hostCount: number
  updatedAt: string
}

export interface SSHHost {
  id: string
  alias: string
  hostname: string
  port: number
  username: string
  credentialId: string
  credentialName: string
  proxyJumpHostId?: string
  proxyJumpAlias?: string
  executionNodeIds: string[]
  enabled: boolean
  updatedAt: string
}

export interface SSHExecutionNode {
  id: string
  name: string
  enabled: boolean
}
