import type { ReactNode } from 'react'
import type { Worker } from './workerTypes'

export function StatusBadge({
  children,
  tone = 'muted',
}: {
  children: ReactNode
  tone?: 'success' | 'danger' | 'muted'
}) {
  return (
    <span
      className={`status-badge ${tone === 'success' ? 'is-success' : ''} ${tone === 'danger' ? 'is-danger' : ''}`}
    >
      {children}
    </span>
  )
}

export function CapabilityBadges({ worker }: { worker: Worker }) {
  return (
    <div className="worker-badges">
      <StatusBadge tone={worker.heartbeatAt ? 'success' : 'muted'}>
        心跳 {worker.heartbeatAt ? '正常' : '暂无'}
      </StatusBadge>
      <StatusBadge
        tone={worker.metadata?.ssh?.status === 'online' ? 'success' : 'muted'}
      >
        SSH {worker.metadata?.ssh?.status ?? '未知'}
      </StatusBadge>
      <StatusBadge
        tone={
          worker.metadata?.host?.appServer === 'online' ? 'success' : 'muted'
        }
      >
        Codex {worker.metadata?.host?.appServer ?? '未知'}
      </StatusBadge>
      <StatusBadge
        tone={
          worker.metadata?.browser?.status === 'online' ? 'success' : 'muted'
        }
      >
        Chrome {worker.metadata?.browser?.status ?? '未知'}
      </StatusBadge>
    </div>
  )
}

export function CapabilityStatus({ worker }: { worker: Worker }) {
  const ssh = worker.metadata?.ssh
  const outboundSSH = worker.metadata?.outboundSSH
  const host = worker.metadata?.host
  const browser = worker.metadata?.browser
  if (!ssh && !outboundSSH && !host && !browser) return null
  return (
    <dl className="runtime-status-grid mt-5">
      <div>
        <dt>内置 SSH</dt>
        <dd>
          {ssh?.status ?? 'unknown'} · {ssh?.listenAddress ?? '未知地址'}
        </dd>
      </div>
      <div>
        <dt>Codex</dt>
        <dd>
          {host?.appServer ?? 'unknown'} · {host?.codexHome ?? '未知 Home'}
        </dd>
      </div>
      <div>
        <dt>出站 SSH</dt>
        <dd>
          {outboundSSH?.status ?? 'unknown'} · {outboundSSH?.hostCount ?? 0}{' '}
          台主机
        </dd>
      </div>
      <div>
        <dt>Chrome</dt>
        <dd>
          {browser?.status ?? 'unknown'} · {browser?.tabCount ?? 0} 个标签页
        </dd>
      </div>
    </dl>
  )
}
