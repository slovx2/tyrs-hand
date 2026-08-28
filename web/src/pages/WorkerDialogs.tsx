import { useMutation } from '@tanstack/react-query'
import { Plus, X } from 'lucide-react'
import { useState } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'

export function CreateWorkerDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: (result: { enrollmentToken: string }) => Promise<void>
}) {
  const showToast = useUI((state) => state.showToast)
  const [name, setName] = useState('')
  const [capacity, setCapacity] = useState(6)
  const create = useMutation({
    mutationFn: () =>
      api<{ enrollmentToken: string }>('/workers', {
        method: 'POST',
        body: JSON.stringify({
          name,
          roles: ['discord'],
          maxConcurrentJobs: capacity,
        }),
      }),
    onSuccess: async (result) => {
      showToast('success', 'Worker 已创建')
      await onCreated(result)
    },
  })

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={onClose}>
      <div
        className="modal-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="create-worker-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 id="create-worker-title" className="text-lg font-semibold">
              新增 Worker
            </h2>
            <p className="muted mt-1 text-sm">
              创建后会生成一次性注册 Token，有效期 15 分钟。
            </p>
          </div>
          <button
            className="icon-button"
            type="button"
            aria-label="关闭"
            onClick={onClose}
          >
            <X aria-hidden size={18} />
          </button>
        </div>
        <label className="mt-5 block">
          <span className="label">
            名称 <span className="required-mark">*</span>
          </span>
          <input
            className="field mt-1"
            aria-label="名称"
            value={name}
            onChange={(event) => setName(event.target.value)}
            required
          />
        </label>
        <label className="mt-4 block">
          <span className="label">
            并发上限 <span className="required-mark">*</span>
          </span>
          <input
            className="field mt-1"
            aria-label="并发上限"
            type="number"
            min={1}
            value={capacity}
            onChange={(event) => setCapacity(Number(event.target.value))}
          />
        </label>
        <div className="mt-6 flex justify-end gap-2">
          <button className="button-secondary" type="button" onClick={onClose}>
            取消
          </button>
          <button
            className="button icon-label-button"
            type="button"
            disabled={!name.trim() || capacity < 1 || create.isPending}
            onClick={() => create.mutate()}
          >
            <Plus aria-hidden size={16} />
            {create.isPending ? '创建中…' : '创建并生成 Token'}
          </button>
        </div>
      </div>
    </div>
  )
}

export function CredentialDialog({
  title,
  token,
  onClose,
}: {
  title: string
  token: string
  onClose: () => void
}) {
  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={onClose}>
      <div
        className="modal-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="credential-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 id="credential-title" className="text-lg font-semibold">
              {title}
            </h2>
            <p className="muted mt-1 text-sm">
              此 Token 仅显示一次，请立即保存。
            </p>
          </div>
          <button className="icon-button" aria-label="关闭" onClick={onClose}>
            <X aria-hidden size={18} />
          </button>
        </div>
        <code className="credential-token mt-5 block break-all select-all">
          {token}
        </code>
        <div className="mt-6 flex justify-end">
          <button className="button" onClick={onClose}>
            我已保存
          </button>
        </div>
      </div>
    </div>
  )
}
