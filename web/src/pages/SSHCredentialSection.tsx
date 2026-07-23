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
  const [form, setForm] = useState<FormState | null>(null)
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const save = useMutation({
    mutationFn: (values: FormState) =>
      api<SSHCredential>(
        values.id ? `/ssh/credentials/${values.id}` : '/ssh/credentials',
        {
          method: values.id ? 'PUT' : 'POST',
          body: JSON.stringify({
            name: values.name,
            privateKey: values.privateKey,
            passphrase: values.passphrase,
            enabled: values.enabled,
          }),
        },
      ),
    onSuccess: async () => {
      setForm(null)
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
    if (!file) return
    const privateKey = await file.text()
    setForm((value) => (value ? { ...value, privateKey } : value))
  }
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (form) save.mutate(form)
  }

  return (
    <section className="mt-10" aria-labelledby="ssh-credentials-title">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h2 id="ssh-credentials-title" className="text-xl font-semibold">
            登录凭证
          </h2>
          <p className="muted mt-1 text-sm">
            {items.length > 0
              ? `已保存 ${items.length} 个凭证，私钥和口令均不会回显。`
              : '先添加一个私钥，随后才能配置远程主机。'}
          </p>
        </div>
        {!form && (
          <button
            className="button"
            type="button"
            onClick={() => setForm({ ...emptyForm })}
          >
            添加凭证
          </button>
        )}
      </div>

      {form && (
        <form className="panel mt-4" onSubmit={submit}>
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div>
              <h3 className="text-lg font-semibold">
                {form.id ? '编辑凭证' : '添加凭证'}
              </h3>
              <p className="muted mt-1 text-sm">
                {form.id
                  ? '只修改名称或状态时，不需要再次提供私钥。'
                  : '支持 OpenSSH 和 PEM 格式的私钥。'}
              </p>
            </div>
            <label className="flex cursor-pointer items-center gap-2 text-sm font-medium">
              <input
                className="size-4 accent-[var(--accent)]"
                type="checkbox"
                checked={form.enabled}
                onChange={(event) =>
                  setForm({ ...form, enabled: event.target.checked })
                }
              />
              启用凭证
            </label>
          </div>

          <label className="mt-5 block text-sm font-medium">
            名称
            <input
              className="field mt-1"
              value={form.name}
              placeholder="例如：生产环境"
              maxLength={128}
              onChange={(event) =>
                setForm({ ...form, name: event.target.value })
              }
              required
            />
          </label>

          <label className="mt-4 block text-sm font-medium">
            私钥
            <textarea
              className="field mt-1 min-h-40 font-mono text-xs leading-5"
              value={form.privateKey}
              placeholder={
                form.id
                  ? '留空以继续使用当前私钥'
                  : '-----BEGIN OPENSSH PRIVATE KEY-----'
              }
              required={!form.id}
              spellCheck={false}
              onChange={(event) =>
                setForm({ ...form, privateKey: event.target.value })
              }
            />
          </label>

          <div className="mt-4 grid gap-4 md:grid-cols-2">
            <label className="block text-sm font-medium">
              从文件读取
              <input
                className="field mt-1 cursor-pointer text-sm file:mr-3 file:cursor-pointer file:rounded-md file:border-0 file:bg-[var(--surface-muted)] file:px-3 file:py-1 file:font-medium file:text-[var(--ink)]"
                type="file"
                onChange={readPrivateKey}
              />
            </label>
            <label className="block text-sm font-medium">
              私钥口令（可选）
              <input
                className="field mt-1 disabled:cursor-not-allowed disabled:opacity-50"
                type="password"
                value={form.passphrase}
                placeholder="仅在私钥已加密时填写"
                disabled={Boolean(form.id && !form.privateKey.trim())}
                autoComplete="new-password"
                onChange={(event) =>
                  setForm({ ...form, passphrase: event.target.value })
                }
              />
            </label>
          </div>

          {form.id && form.privateKey.trim() && (
            <p className="mt-4 rounded-lg bg-[var(--surface-muted)] px-3 py-2 text-sm">
              保存后会轮换当前私钥，并将凭证版本加一。
            </p>
          )}

          <div className="mt-5 flex flex-wrap gap-2">
            <button className="button" disabled={save.isPending}>
              {save.isPending ? '保存中…' : '保存凭证'}
            </button>
            <button
              type="button"
              className="button-secondary"
              disabled={save.isPending}
              onClick={() => setForm(null)}
            >
              取消
            </button>
          </div>
        </form>
      )}

      <div className="mt-4 grid gap-3">
        {items.length === 0 ? (
          <div className="rounded-xl border border-dashed border-[color:var(--border-strong)] p-6 text-center">
            <p className="font-medium">还没有登录凭证</p>
            <p className="muted mt-1 text-sm">
              点击“添加凭证”，上传或粘贴一份 SSH 私钥。
            </p>
          </div>
        ) : (
          items.map((item) => (
            <article className="panel" key={item.id}>
              <div className="flex flex-wrap items-start justify-between gap-4">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <h3 className="font-semibold">{item.name}</h3>
                    <StatusBadge enabled={item.enabled} />
                  </div>
                  <code className="muted mt-2 block break-all text-xs">
                    {item.fingerprint}
                  </code>
                  <p className="muted mt-2 text-sm">
                    {item.hostCount} 台关联主机 · 版本 {item.version}
                  </p>
                </div>
                <div className="flex flex-wrap gap-2">
                  <button
                    className="button-secondary"
                    type="button"
                    onClick={() => edit(item)}
                  >
                    编辑
                  </button>
                  <button
                    className="button-secondary"
                    type="button"
                    disabled={item.hostCount > 0 || remove.isPending}
                    title={item.hostCount > 0 ? '请先删除关联主机' : undefined}
                    onClick={() => remove.mutate(item.id)}
                  >
                    删除
                  </button>
                </div>
              </div>
            </article>
          ))
        )}
      </div>
    </section>
  )
}

function StatusBadge({ enabled }: { enabled: boolean }) {
  return (
    <span className="rounded-full border border-[color:var(--border)] bg-[var(--surface-muted)] px-2.5 py-1 text-xs font-medium">
      {enabled ? '已启用' : '已停用'}
    </span>
  )
}
