import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
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
    <section>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-3xl font-bold">客户端设备</h1>
          <p className="muted mt-2">
            扫码绑定 App；删除设备后，其永久凭证会立即失效。
          </p>
        </div>
        <button
          className="button-primary"
          disabled={createPairing.isPending}
          onClick={() => createPairing.mutate()}
        >
          {createPairing.isPending ? '正在生成…' : '添加设备'}
        </button>
      </div>

      {currentPairing && (
        <div className="panel mt-6">
          <h2 className="text-xl font-semibold">绑定新设备</h2>
          {currentPairing.status === 'waiting_scan' && (
            <div className="mt-4 grid gap-4 sm:grid-cols-[320px_1fr] sm:items-center">
              {currentPairing.qrDataUrl && (
                <img
                  src={currentPairing.qrDataUrl}
                  alt="设备绑定二维码"
                  className="w-full max-w-80 rounded-xl bg-white p-3"
                />
              )}
              <div>
                <p>请使用 Tyrs Hand App 扫描二维码。</p>
                <p className="muted mt-2 text-sm">
                  二维码十分钟内有效，且只能被一个设备使用。
                </p>
                <button
                  className="button-secondary mt-4"
                  onClick={() => pairingStatus.refetch()}
                >
                  刷新扫码状态
                </button>
              </div>
            </div>
          )}
          {currentPairing.status === 'waiting_confirmation' && (
            <div className="mt-4">
              <p className="font-semibold">{currentPairing.deviceName}</p>
              <p className="muted mt-1 text-sm">
                平台：{currentPairing.platform}
              </p>
              <p className="mt-4">
                请核对手中设备后再确认。确认后该设备凭证永久有效。
              </p>
              <div className="mt-4 flex gap-3">
                <button
                  className="button-primary"
                  disabled={approve.isPending}
                  onClick={() => approve.mutate()}
                >
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
            <p className="error-text mt-4">
              本次绑定已{currentPairing.status === 'expired' ? '过期' : '拒绝'}
              ，请重新生成二维码。
            </p>
          )}
        </div>
      )}

      <div className="mt-8 grid gap-4">
        {devices.isLoading && <p className="muted">正在加载设备…</p>}
        {devices.data?.items.length === 0 && (
          <div className="panel muted">暂无已绑定设备</div>
        )}
        {devices.data?.items.map((device) => (
          <article
            className="panel flex flex-wrap items-center justify-between gap-4"
            key={device.id}
          >
            <div>
              <h2 className="text-lg font-semibold">{device.name}</h2>
              <p className="muted mt-1 text-sm">
                {device.platform} · 添加于{' '}
                {new Date(device.approvedAt).toLocaleString()}
              </p>
              <p className="muted mt-1 text-sm">
                最近使用：
                {device.lastSeenAt
                  ? new Date(device.lastSeenAt).toLocaleString()
                  : '尚未使用'}
              </p>
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
              删除设备
            </button>
          </article>
        ))}
      </div>
    </section>
  )
}
