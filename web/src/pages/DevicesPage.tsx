import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  LockKeyhole,
  Plus,
  RefreshCw,
  ShieldCheck,
  Smartphone,
} from 'lucide-react'
import { useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'
import type { Worker } from './WorkersPage'

interface Machine {
  workerId: string
  name: string
  sshHostKeyFingerprint: string
  status: string
  approvedAt: string
}

interface Device {
  id: string
  name: string
  platform: string
  approvedAt: string
  lastSeenAt?: string
  machines: Machine[]
}

interface Pairing {
  id: string
  status:
    | 'waiting_scan'
    | 'waiting_confirmation'
    | 'approved'
    | 'rejected'
    | 'expired'
  deviceName?: string
  platform?: string
  workerId: string
  workerName: string
  sshHostKeyFingerprint: string
  expiresAt: string
  pairingUri?: string
  qrDataUrl?: string
}

export function DevicesPage() {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [workerId, setWorkerId] = useState('')
  const [pairing, setPairing] = useState<Pairing>()
  const devices = useQuery({
    queryKey: ['client-devices'],
    queryFn: () => api<{ items: Device[] }>('/client-devices'),
  })
  const workers = useQuery({
    queryKey: ['workers'],
    queryFn: () => api<{ items: Worker[] }>('/workers'),
  })
  const eligibleWorkers = (workers.data?.items ?? []).filter(
    (worker) => worker.enabled && Boolean(worker.sshHostKeyFingerprint),
  )
  const selectedWorkerId = workerId || eligibleWorkers[0]?.id || ''
  const pairingStatus = useQuery({
    queryKey: ['client-device-pairing', pairing?.id],
    queryFn: () => api<Pairing>(`/client-device-pairings/${pairing?.id}`),
    enabled: Boolean(pairing),
    refetchInterval:
      pairing?.status === 'approved' || pairing?.status === 'rejected'
        ? false
        : 2000,
  })
  const currentPairing = pairingStatus.data
    ? { ...pairing, ...pairingStatus.data }
    : pairing

  const createPairing = useMutation({
    mutationFn: () =>
      api<Pairing>('/client-device-pairings', {
        method: 'POST',
        body: JSON.stringify({ workerId: selectedWorkerId }),
      }),
    onSuccess: setPairing,
    onError: (error: Error) => showToast('error', error.message),
  })
  const approve = useMutation({
    mutationFn: () =>
      api<Device>(`/client-device-pairings/${currentPairing?.id}/approve`, {
        method: 'POST',
      }),
    onSuccess: async () => {
      showToast('success', '设备已获得这台 Worker 的定时任务只读权限')
      setPairing(undefined)
      await queryClient.invalidateQueries({ queryKey: ['client-devices'] })
    },
    onError: (error: Error) => showToast('error', error.message),
  })
  const reject = useMutation({
    mutationFn: () =>
      api<void>(`/client-device-pairings/${currentPairing?.id}/reject`, {
        method: 'POST',
      }),
    onSuccess: () => setPairing(undefined),
    onError: (error: Error) => showToast('error', error.message),
  })
  const remove = useMutation({
    mutationFn: (device: Device) =>
      api<void>(`/client-devices/${device.id}`, { method: 'DELETE' }),
    onSuccess: async () => {
      showToast('success', '设备凭证与所有机器授权已撤销')
      await queryClient.invalidateQueries({ queryKey: ['client-devices'] })
    },
  })

  return (
    <section className="devices-page">
      <div className="devices-page-header">
        <div>
          <h1>移动端定时任务授权</h1>
          <p className="devices-page-subtitle">
            扫码只允许移动端查看所选 Worker
            的定时任务和运行记录；项目、会话和聊天仍只通过 SSH。
          </p>
        </div>
        <div className="flex flex-wrap items-end gap-2">
          <label>
            <span className="label">目标 Worker</span>
            <select
              value={selectedWorkerId}
              onChange={(event) => setWorkerId(event.target.value)}
            >
              {eligibleWorkers.length === 0 && (
                <option value="">没有已上线的 SSH Worker</option>
              )}
              {eligibleWorkers.map((worker) => (
                <option value={worker.id} key={worker.id}>
                  {worker.name}
                </option>
              ))}
            </select>
          </label>
          <button
            className="button device-add-button"
            disabled={!selectedWorkerId || createPairing.isPending}
            onClick={() => createPairing.mutate()}
          >
            {createPairing.isPending ? (
              <RefreshCw size={15} className="animate-spin" />
            ) : (
              <Plus size={16} />
            )}
            <span>{createPairing.isPending ? '正在生成…' : '生成二维码'}</span>
          </button>
        </div>
      </div>

      {currentPairing && (
        <section className="device-panel device-pairing-panel">
          <div className="device-panel-header">
            <h2>授权 {currentPairing.workerName}</h2>
            <span className="device-panel-count">仅定时任务只读</span>
          </div>
          {currentPairing.status === 'waiting_scan' && (
            <div className="device-pairing-content">
              {currentPairing.qrDataUrl && (
                <div className="device-qr-frame">
                  <img
                    src={currentPairing.qrDataUrl}
                    alt="定时任务授权二维码"
                  />
                </div>
              )}
              <div className="device-pairing-copy">
                <div className="device-pairing-eyebrow">Tyrs Hand Mobile</div>
                <h3>用移动端连接页扫码</h3>
                <p>
                  二维码 10 分钟内单次有效。扫码后仍需在这里核对并确认设备。
                </p>
                <div className="device-pairing-chips">
                  <span>
                    <i />
                    10 分钟内有效
                  </span>
                  <span>单次有效</span>
                  <span>
                    <LockKeyhole size={12} />
                    不开放 App Server
                  </span>
                </div>
              </div>
            </div>
          )}
          {currentPairing.status === 'waiting_confirmation' && (
            <div className="device-confirmation">
              <div className="device-confirmation-icon">
                <Smartphone size={22} />
              </div>
              <div>
                <div className="device-pairing-eyebrow">等待管理员确认</div>
                <h3>{currentPairing.deviceName}</h3>
                <p>
                  {currentPairing.platform} · {currentPairing.workerName} ·
                  仅查看定时任务
                </p>
              </div>
              <div className="device-confirmation-actions">
                <button
                  className="button"
                  disabled={approve.isPending}
                  onClick={() => approve.mutate()}
                >
                  <ShieldCheck size={15} />
                  确认授权
                </button>
                <button
                  className="button-secondary"
                  onClick={() => reject.mutate()}
                >
                  拒绝
                </button>
              </div>
            </div>
          )}
          {(currentPairing.status === 'expired' ||
            currentPairing.status === 'rejected') && (
            <div className="device-pairing-error">
              <p>
                本次授权已
                {currentPairing.status === 'expired' ? '过期' : '拒绝'}
                ，请重新生成二维码。
              </p>
            </div>
          )}
        </section>
      )}

      <section className="device-panel device-list-panel">
        <div className="device-panel-header">
          <h2>已授权设备</h2>
          <span className="device-panel-count">
            {devices.data?.items.length ?? 0} 台设备
          </span>
        </div>
        {devices.isLoading && (
          <div className="device-list-message">正在加载设备…</div>
        )}
        {devices.data?.items.length === 0 && (
          <div className="device-list-message">暂无已授权设备</div>
        )}
        {devices.data?.items.map((device) => (
          <article className="device-row" key={device.id}>
            <div className="device-identity">
              <div className="device-icon">
                <Smartphone size={19} />
              </div>
              <div>
                <h3>{device.name}</h3>
                <p>
                  {device.platform} · {device.machines.length} 台机器
                </p>
              </div>
            </div>
            <div className="device-metadata">
              <strong>
                {device.machines.map((item) => item.name).join('、')}
              </strong>
              <span>定时任务授权</span>
            </div>
            <div className="device-metadata">
              <strong>
                {device.lastSeenAt
                  ? new Date(device.lastSeenAt).toLocaleString()
                  : '尚未使用'}
              </strong>
              <span>最近使用</span>
            </div>
            <button
              className="button-danger"
              disabled={remove.isPending}
              onClick={() => {
                if (
                  window.confirm(`撤销设备“${device.name}”的全部定时任务权限？`)
                )
                  remove.mutate(device)
              }}
            >
              撤销设备
            </button>
          </article>
        ))}
      </section>
    </section>
  )
}
