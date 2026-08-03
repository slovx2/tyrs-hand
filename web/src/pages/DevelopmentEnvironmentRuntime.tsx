import { KeyRound } from 'lucide-react'
import type { DevelopmentEnvironment } from './developmentEnvironmentTypes'

export function DevelopmentEnvironmentRuntime({
  environment,
}: {
  environment: DevelopmentEnvironment
}) {
  return (
    <div role="tabpanel" className="runtime-panel">
      <dl className="runtime-status-grid">
        <RuntimeStatus label="Worker" value={environment.daemonStatus} />
        <RuntimeStatus label="App Server" value={environment.appServerStatus} />
        <RuntimeStatus label="SSH Server" value={environment.sshStatus} />
        <RuntimeStatus
          label="Worker ID"
          value={environment.executionNodeId || '待分配'}
          mono
        />
      </dl>

      <div className="ssh-section">
        <div className="flex items-center gap-2">
          <KeyRound aria-hidden size={18} />
          <h3 className="font-semibold">机器 Home 配置</h3>
        </div>
        <p className="muted mt-3 text-sm">
          Codex Provider、登录态和配置只读取 Worker 宿主用户的 CODEX_HOME。
          Desktop SSH 客户端公钥由宿主 Worker 的 authorized_keys
          文件维护，Control 不下发或改写这些配置。
        </p>
        {environment.daemonError && (
          <p className="error-text mt-3 text-sm">{environment.daemonError}</p>
        )}
      </div>
    </div>
  )
}

function RuntimeStatus({
  label,
  value,
  mono = false,
}: {
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <div>
      <dt>{label}</dt>
      <dd className={mono ? 'font-mono' : ''}>{value}</dd>
    </div>
  )
}
