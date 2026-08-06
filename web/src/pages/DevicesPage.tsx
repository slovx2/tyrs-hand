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

interface Device {
  id: string
  name: string
  platform: string
  createdAt: string
  approvedAt: string
  lastSeenAt?: string
}

interface Pairing {
  id: string
  status:
    | 'waiting_scan'
    | 'waiting_confirmation'
    | 'approved'
    | 'rejected'
    | 'expired'
  deviceId?: string
  deviceName?: string
  platform?: string
  expiresAt: string
  pairingUri?: string
  qrDataUrl?: string
}

export function DevicesPage() {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const [pairing, setPairing] = useState<Pairing>()
  const devices = useQuery({
    queryKey: ['client-devices'],
    queryFn: () => api<{ items: Device[] }>('/client-devices'),
  })
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
      api<Pairing>('/client-device-pairings', { method: 'POST' }),
    onSuccess: (result) => setPairing(result),
    onError: (error: Error) => showToast('error', error.message),
  })
  const approve = useMutation({
    mutationFn: () =>
      api<Device>(`/client-device-pairings/${currentPairing?.id}/approve`, {
        method: 'POST',
      }),
    onSuccess: async () => {
      showToast('success', '设备已确认')
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
      showToast('success', '设备已删除，凭证立即失效')
      await queryClient.invalidateQueries({ queryKey: ['client-devices'] })
    },
    onError: (error: Error) => showToast('error', error.message),
  })

  return (
    <section className="devices-page">
      <div className="devices-page-header">
        <div>
          <h1>客户端设备</h1>
          <p className="devices-page-subtitle">
            管理已授权的移动端设备，扫码即可安全绑定。
          </p>
        </div>
        <button
          className="button device-add-button"
          disabled={createPairing.isPending}
          onClick={() => createPairing.mutate()}
        >
          {createPairing.isPending ? (
            <RefreshCw size={15} className="animate-spin" aria-hidden="true" />
          ) : (
            <Plus size={16} aria-hidden="true" />
          )}
          <span>{createPairing.isPending ? '正在生成…' : '添加设备'}</span>
        </button>
      </div>

      {currentPairing && (
        <section className="device-panel device-pairing-panel">
          <div className="device-panel-header">
            <h2>绑定新设备</h2>
            <span className="device-panel-count">步骤 1 / 1</span>
          </div>
          {currentPairing.status === 'waiting_scan' && (
            <div className="device-pairing-content">
              {currentPairing.qrDataUrl && (
                <div className="device-qr-frame">
                  <img src={currentPairing.qrDataUrl} alt="设备绑定二维码" />
                </div>
              )}
              <div className="device-pairing-copy">
                <div className="device-pairing-eyebrow">Tyrs Hand Mobile</div>
                <h3>使用 App 扫描二维码</h3>
                <p>
                  打开 Tyrs Hand App 的「添加设备」并完成扫描。二维码将在 10
                  分钟后自动失效，且仅能绑定一台设备。
                </p>
                <div className="device-pairing-chips">
                  <span>
                    <i />
                    10 分钟内有效
                  </span>
                  <span>单次有效</span>
                  <span>
                    <LockKeyhole size={12} />
                    端到端加密
                  </span>
                </div>
                <button
                  className="button-secondary device-refresh-button"
                  onClick={() => pairingStatus.refetch()}
                >
                  <RefreshCw size={14} aria-hidden="true" />
                  刷新扫码状态
                </button>
              </div>
            </div>
          )}
          {currentPairing.status === 'waiting_confirmation' && (
            <div className="device-confirmation">
              <div className="device-confirmation-icon">
                <Smartphone size={22} aria-hidden="true" />
              </div>
              <div>
                <div className="device-pairing-eyebrow">等待确认</div>
                <h3>{currentPairing.deviceName}</h3>
                <p>
                  平台：{currentPairing.platform}
                  。请核对手中设备后再确认，确认后该设备将获得永久凭证。
                </p>
              </div>
              <div className="device-confirmation-actions">
                <button
                  className="button"
                  disabled={approve.isPending}
                  onClick={() => approve.mutate()}
                >
                  <ShieldCheck size={15} aria-hidden="true" />
                  确认设备
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
                本次绑定已
                {currentPairing.status === 'expired' ? '过期' : '拒绝'}
                ，请重新生成二维码。
              </p>
            </div>
          )}
        </section>
      )}

      <section className="device-panel device-list-panel">
        <div className="device-panel-header">
          <h2>已绑定设备</h2>
          <span className="device-panel-count">
            {devices.data?.items.length ?? 0} 台设备
          </span>
        </div>
        {devices.isLoading && (
          <div className="device-list-message">正在加载设备…</div>
        )}
        {devices.data?.items.length === 0 && (
          <div className="device-list-message">暂无已绑定设备</div>
        )}
        {devices.data?.items.map((device) => (
          <article className="device-row" key={device.id}>
            <div className="device-identity">
              <div className="device-icon">
                <Smartphone size={19} strokeWidth={1.8} aria-hidden="true" />
              </div>
              <div>
                <h3>{device.name}</h3>
                <p>{device.platform} · 可信设备</p>
              </div>
            </div>
            <div className="device-metadata">
              <strong>{new Date(device.approvedAt).toLocaleString()}</strong>
              <span>添加时间</span>
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
                  window.confirm(`删除设备“${device.name}”？其凭证会立即失效。`)
                ) {
                  remove.mutate(device)
                }
              }}
            >
              移除设备
            </button>
          </article>
        ))}
      </section>
    </section>
  )
}
