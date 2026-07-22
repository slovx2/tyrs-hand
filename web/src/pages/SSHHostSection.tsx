import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'
import type { SSHCredential, SSHExecutionNode, SSHHost } from './sshTypes'

interface HostForm {
  id: string
  alias: string
  hostname: string
  port: number
  username: string
  credentialId: string
  proxyJumpHostId: string
  executionNodeIds: string[]
  enabled: boolean
}

const emptyHost: HostForm = {
  id: '',
  alias: '',
  hostname: '',
  port: 22,
  username: 'root',
  credentialId: '',
  proxyJumpHostId: '',
  executionNodeIds: [],
  enabled: true,
}

export function SSHHostSection({
  items,
  credentials,
  nodes,
}: {
  items: SSHHost[]
  credentials: SSHCredential[]
  nodes: SSHExecutionNode[]
}) {
  const [form, setForm] = useState<HostForm>(emptyHost)
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ['ssh-hosts'] }),
      queryClient.invalidateQueries({ queryKey: ['ssh-credentials'] }),
    ])
  const save = useMutation({
    mutationFn: () =>
      api<SSHHost>(form.id ? `/ssh/hosts/${form.id}` : '/ssh/hosts', {
        method: form.id ? 'PUT' : 'POST',
        body: JSON.stringify({
          alias: form.alias,
          hostname: form.hostname,
          port: form.port,
          username: form.username,
          credentialId: form.credentialId,
          proxyJumpHostId: form.proxyJumpHostId || null,
          executionNodeIds: form.executionNodeIds,
          enabled: form.enabled,
        }),
      }),
    onSuccess: async () => {
      setForm(emptyHost)
      await refresh()
      showToast('success', 'SSH 主机已保存')
    },
  })
  const remove = useMutation({
    mutationFn: (id: string) =>
      api<void>(`/ssh/hosts/${id}`, { method: 'DELETE' }),
    onSuccess: async () => {
      await refresh()
      showToast('success', 'SSH 主机已删除')
    },
  })
  const edit = (item: SSHHost) =>
    setForm({
      id: item.id,
      alias: item.alias,
      hostname: item.hostname,
      port: item.port,
      username: item.username,
      credentialId: item.credentialId,
      proxyJumpHostId: item.proxyJumpHostId ?? '',
      executionNodeIds: item.executionNodeIds,
      enabled: item.enabled,
    })
  const toggleNode = (id: string, checked: boolean) =>
    setForm((value) => ({
      ...value,
      executionNodeIds: checked
        ? [...value.executionNodeIds, id]
        : value.executionNodeIds.filter((item) => item !== id),
    }))
  const submit = (event: FormEvent) => {
    event.preventDefault()
    save.mutate()
  }

  return (
    <section className="mt-10">
      <h2 className="text-xl font-semibold">主机</h2>
      <form className="panel mt-4" onSubmit={submit}>
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
          <label>
            <span className="label">别名</span>
            <input
              value={form.alias}
              required
              pattern="[A-Za-z0-9._-]+"
              onChange={(event) =>
                setForm({ ...form, alias: event.target.value })
              }
            />
          </label>
          <label>
            <span className="label">HostName</span>
            <input
              value={form.hostname}
              required
              onChange={(event) =>
                setForm({ ...form, hostname: event.target.value })
              }
            />
          </label>
          <label>
            <span className="label">端口</span>
            <input
              type="number"
              min={1}
              max={65535}
              value={form.port}
              onChange={(event) =>
                setForm({ ...form, port: Number(event.target.value) })
              }
            />
          </label>
          <label>
            <span className="label">用户名</span>
            <input
              value={form.username}
              required
              onChange={(event) =>
                setForm({ ...form, username: event.target.value })
              }
            />
          </label>
          <label>
            <span className="label">凭证</span>
            <select
              value={form.credentialId}
              required
              onChange={(event) =>
                setForm({ ...form, credentialId: event.target.value })
              }
            >
              <option value="">请选择</option>
              {credentials.map((item) => (
                <option value={item.id} key={item.id}>
                  {item.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            <span className="label">ProxyJump</span>
            <select
              value={form.proxyJumpHostId}
              onChange={(event) =>
                setForm({ ...form, proxyJumpHostId: event.target.value })
              }
            >
              <option value="">无</option>
              {items
                .filter((item) => item.id !== form.id)
                .map((item) => (
                  <option value={item.id} key={item.id}>
                    {item.alias}
                  </option>
                ))}
            </select>
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
        </div>
        <fieldset className="mt-5">
          <legend className="label">Execution Node</legend>
          <div className="mt-2 flex flex-wrap gap-4">
            {nodes.map((node) => (
              <label className="flex items-center gap-2" key={node.id}>
                <input
                  type="checkbox"
                  checked={form.executionNodeIds.includes(node.id)}
                  onChange={(event) =>
                    toggleNode(node.id, event.target.checked)
                  }
                />
                {node.name}
              </label>
            ))}
          </div>
        </fieldset>
        <div className="mt-5 flex gap-2">
          <button className="button-primary" disabled={save.isPending}>
            {form.id ? '保存主机' : '添加主机'}
          </button>
          {form.id && (
            <button
              type="button"
              className="button-secondary"
              onClick={() => setForm(emptyHost)}
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
              <div>
                <h3 className="font-semibold">{item.alias}</h3>
                <p className="muted mt-1 text-sm">
                  {item.username}@{item.hostname}:{item.port} ·{' '}
                  {item.credentialName}
                </p>
                <p className="muted mt-1 text-xs">
                  {item.proxyJumpAlias
                    ? `ProxyJump ${item.proxyJumpAlias} · `
                    : ''}
                  {item.executionNodeIds.length} 个节点 ·
                  {item.enabled ? ' 已启用' : ' 已停用'}
                </p>
              </div>
              <div className="flex gap-2">
                <button className="button-secondary" onClick={() => edit(item)}>
                  编辑
                </button>
                <button
                  className="button-secondary"
                  disabled={remove.isPending}
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
