import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type ChangeEvent, type FormEvent } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'
import type { SSHCredential } from './sshTypes'

interface FormState {
  id: string
  name: string
  privateKey: string
  passphrase: string
  enabled: boolean
}

const emptyForm: FormState = {
  id: '',
  name: '',
  privateKey: '',
  passphrase: '',
  enabled: true,
}

export function SSHCredentialSection({ items }: { items: SSHCredential[] }) {
  const [form, setForm] = useState<FormState>(emptyForm)
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const save = useMutation({
    mutationFn: () =>
      api<SSHCredential>(
        form.id ? `/ssh/credentials/${form.id}` : '/ssh/credentials',
        {
          method: form.id ? 'PUT' : 'POST',
          body: JSON.stringify({
            name: form.name,
            privateKey: form.privateKey,
            passphrase: form.passphrase,
            enabled: form.enabled,
          }),
        },
      ),
    onSuccess: async () => {
      setForm(emptyForm)
      await queryClient.invalidateQueries({ queryKey: ['ssh-credentials'] })
      showToast('success', 'SSH 凭证已保存')
    },
  })
  const remove = useMutation({
    mutationFn: (id: string) =>
      api<void>(`/ssh/credentials/${id}`, { method: 'DELETE' }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['ssh-credentials'] })
      showToast('success', 'SSH 凭证已删除')
    },
  })

  const edit = (item: SSHCredential) => {
    setForm({
      id: item.id,
      name: item.name,
      privateKey: '',
      passphrase: '',
      enabled: item.enabled,
    })
  }
  const readPrivateKey = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    if (file) {
      const privateKey = await file.text()
      setForm((value) => ({ ...value, privateKey }))
    }
  }
  const submit = (event: FormEvent) => {
    event.preventDefault()
    save.mutate()
  }

  return (
    <section className="mt-8">
      <h2 className="text-xl font-semibold">凭证</h2>
      <form className="panel mt-4" onSubmit={submit}>
        <div className="grid gap-4 lg:grid-cols-2">
          <label>
            <span className="label">名称</span>
            <input
              value={form.name}
              onChange={(event) =>
                setForm({ ...form, name: event.target.value })
              }
              required
            />
          </label>
          <label className="flex items-center gap-2 pt-7">
            <input
              type="checkbox"
              checked={form.enabled}
              onChange={(event) =>
                setForm({ ...form, enabled: event.target.checked })
              }
            />
            启用
          </label>
          <label className="lg:col-span-2">
            <span className="label">
              私钥{form.id ? '（留空表示不轮换）' : ''}
            </span>
            <textarea
              rows={7}
              value={form.privateKey}
              required={!form.id}
              spellCheck={false}
              onChange={(event) =>
                setForm({ ...form, privateKey: event.target.value })
              }
            />
          </label>
          <label>
            <span className="label">从文件读取</span>
            <input type="file" onChange={readPrivateKey} />
          </label>
          <label>
            <span className="label">私钥口令</span>
            <input
              type="password"
              value={form.passphrase}
              onChange={(event) =>
                setForm({ ...form, passphrase: event.target.value })
              }
            />
          </label>
        </div>
        <div className="mt-5 flex gap-2">
          <button className="button-primary" disabled={save.isPending}>
            {form.id ? '保存并按需轮换' : '添加凭证'}
          </button>
          {form.id && (
            <button
              type="button"
              className="button-secondary"
              onClick={() => setForm(emptyForm)}
            >
              取消
            </button>
          )}
        </div>
      </form>

      <div className="mt-4 grid gap-3">
        {items.map((item) => (
          <article className="panel" key={item.id}>
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div className="min-w-0">
                <h3 className="font-semibold">{item.name}</h3>
                <code className="muted mt-1 block break-all text-xs">
                  {item.fingerprint}
                </code>
                <p className="muted mt-2 text-sm">
                  {item.enabled ? '已启用' : '已停用'} · {item.hostCount} 台主机
                  · 版本 {item.version}
                </p>
              </div>
              <div className="flex gap-2">
                <button className="button-secondary" onClick={() => edit(item)}>
                  编辑或轮换
                </button>
                <button
                  className="button-secondary"
                  disabled={item.hostCount > 0 || remove.isPending}
                  title={item.hostCount > 0 ? '仍有关联主机' : undefined}
                  onClick={() => remove.mutate(item.id)}
                >
                  删除
                </button>
              </div>
            </div>
          </article>
        ))}
      </div>
    </section>
  )
}
