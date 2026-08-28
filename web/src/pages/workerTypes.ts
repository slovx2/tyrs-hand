export interface Worker {
  id: string
  name: string
  roles: string[]
  enabled: boolean
  maxConcurrentJobs: number
  protocolVersion: number
  workerVersion?: string
  status: string
  heartbeatAt?: string
  lastError?: string
  sshHostKeyFingerprint?: string
  metadata?: {
    ssh?: {
      status?: string
      listenAddress?: string
    }
    outboundSSH?: {
      status?: string
      revision?: string
      credentialCount?: number
      hostCount?: number
      lastError?: string
    }
    host?: {
      home?: string
      codexHome?: string
      workspaceRoot?: string
      appServer?: string
    }
    browser?: {
      status?: string
      bridgeVersion?: string
      extensionVersion?: string
      chromeVersion?: string
      profile?: string
      tabCount?: number
      lastError?: string
    }
  }
}

export interface WorkerDefaults {
  discordWorkerId?: string | null
}
