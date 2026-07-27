import { useMutation, useQueryClient } from '@tanstack/react-query'
import { KeyRound, Power, Save } from 'lucide-react'
import { useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'
import type {
  DevelopmentEnvironment,
  DiscordMember,
} from './developmentEnvironmentTypes'

export function DevelopmentEnvironmentRuntime({
  environment,
  members,
}: {
  environment: DevelopmentEnvironment
  members: DiscordMember[]
}) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [publicKey, setPublicKey] = useState(environment.sshPublicKey ?? '')
  const [port, setPort] = useState(
    environment.sshPort ? String(environment.sshPort) : '',
  )
  const [discordUserId, setDiscordUserId] = useState(
    environment.sshDiscordUserId ?? '',
  )
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: ['development-environments'] })
  const save = useMutation({
    mutationFn: () =>
      api<void>(`/development-environments/${environment.id}/ssh`, {
        method: 'PUT',
        body: JSON.stringify({
          publicKey: publicKey.trim(),
          port: Number(port),
          discordUserId,
        }),
      }),
    onSuccess: async () => {
      showToast('info', 'SSH 配置已排队生效')
      await refresh()
    },
  })
  const disable = useMutation({
    mutationFn: () =>
      api<void>(`/development-environments/${environment.id}/ssh`, {
        method: 'DELETE',
      }),
    onSuccess: async () => {
      showToast('info', 'SSH 停用已排队')
      await refresh()
    },
  })
  const numericPort = Number(port)
  const valid =
    publicKey.trim() !== '' &&
    discordUserId !== '' &&
    Number.isInteger(numericPort) &&
    numericPort >= 1 &&
    numericPort <= 65_535

  return (
    <div role="tabpanel" className="runtime-panel">
      <dl className="runtime-status-grid">
        <RuntimeStatus label="Daemon" value={environment.daemonStatus} />
        <RuntimeStatus label="App Server" value={environment.appServerStatus} />
        <RuntimeStatus label="SSH" value={environment.sshStatus} />
        <RuntimeStatus label="Relay" value={environment.relayStatus} />
        <RuntimeStatus
          label="运行用户"
          value={environment.runtimeUser || '待上报'}
        />
        <RuntimeStatus
          label="执行节点"
          value={environment.executionNodeId || '待分配'}
          mono
        />
      </dl>

      <div className="ssh-section">
        <div className="flex items-center gap-2">
          <KeyRound aria-hidden size={18} />
          <h3 className="font-semibold">Desktop SSH</h3>
        </div>
        <div className="ssh-form-grid mt-4">
          <label className="text-sm">
            SSH 公钥
            <textarea
              className="field mt-1 min-h-24 resize-y font-mono text-xs"
              aria-label={`${environment.ownerName} SSH 公钥`}
              value={publicKey}
              onChange={(event) => setPublicKey(event.target.value)}
            />
          </label>
          <div className="grid content-start gap-4">
            <label className="text-sm">
              SSH 端口
              <input
                className="field mt-1"
                type="number"
                min={1}
                max={65_535}
                aria-label={`${environment.ownerName} SSH 端口`}
                value={port}
                onChange={(event) => setPort(event.target.value)}
              />
            </label>
            <label className="text-sm">
              Desktop 发言身份
              <select
                className="field mt-1"
                aria-label={`${environment.ownerName} Desktop 发言身份`}
                value={discordUserId}
                onChange={(event) => setDiscordUserId(event.target.value)}
              >
                <option value="">选择 Discord 成员</option>
                {members.map((member) => (
                  <option
                    key={member.discordUserId}
                    value={member.discordUserId}
                  >
                    {member.displayName || member.username}
                  </option>
                ))}
              </select>
            </label>
          </div>
        </div>
        <div className="muted mt-3 flex flex-wrap gap-x-5 gap-y-1 text-xs">
          <span>指纹 {environment.sshFingerprint || '尚未配置'}</span>
          <span>
            配置版本 {environment.sshAppliedRevision}/
            {environment.sshConfigRevision}
          </span>
          {environment.sshDisplayName && (
            <span>身份 {environment.sshDisplayName}</span>
          )}
        </div>
        {environment.daemonError && (
          <p className="error-text mt-3 text-sm">{environment.daemonError}</p>
        )}
        <div className="mt-4 flex flex-wrap gap-2">
          <button
            type="button"
            className="button icon-label-button"
            disabled={!valid || save.isPending}
            onClick={() => save.mutate()}
          >
            <Save aria-hidden size={16} />
            {save.isPending ? '保存中…' : '保存 SSH'}
          </button>
          <button
            type="button"
            className="button-secondary icon-label-button"
            disabled={!environment.sshPublicKey || disable.isPending}
            onClick={() => disable.mutate()}
          >
            <Power aria-hidden size={16} />
            {disable.isPending ? '停用中…' : '停用 SSH'}
          </button>
        </div>
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
