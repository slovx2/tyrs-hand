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
  const [form, setForm] = useState<HostForm | null>(null)
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ['ssh-hosts'] }),
      queryClient.invalidateQueries({ queryKey: ['ssh-credentials'] }),
    ])
  const save = useMutation({
    mutationFn: (values: HostForm) =>
      api<SSHHost>(values.id ? `/ssh/hosts/${values.id}` : '/ssh/hosts', {
        method: values.id ? 'PUT' : 'POST',
        body: JSON.stringify({
          alias: values.alias,
          hostname: values.hostname,
          port: values.port,
          username: values.username,
          credentialId: values.credentialId,
          proxyJumpHostId: values.proxyJumpHostId || null,
          executionNodeIds: values.executionNodeIds,
          enabled: values.enabled,
        }),
      }),
    onSuccess: async () => {
      setForm(null)
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
  const edit = (item: SSHHost) => {
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
  }
  const changeProxyJump = (proxyJumpHostId: string) => {
    const proxy = items.find((item) => item.id === proxyJumpHostId)
    setForm((value) =>
      value
        ? {
            ...value,
            proxyJumpHostId,
            executionNodeIds: proxy
              ? value.executionNodeIds.filter((id) =>
                  proxy.executionNodeIds.includes(id),
                )
              : value.executionNodeIds,
          }
        : value,
    )
  }
  const toggleNode = (id: string, checked: boolean) => {
    setForm((value) =>
      value
        ? {
            ...value,
            executionNodeIds: checked
              ? [...new Set([...value.executionNodeIds, id])]
              : value.executionNodeIds.filter((item) => item !== id),
          }
        : value,
    )
  }
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (form) save.mutate(form)
  }
  const selectedProxy = items.find((item) => item.id === form?.proxyJumpHostId)

  return (
    <section className="mt-12" aria-labelledby="ssh-hosts-title">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h2 id="ssh-hosts-title" className="text-xl font-semibold">
            远程主机
          </h2>
          <p className="muted mt-1 text-sm">
            {items.length > 0
              ? `已配置 ${items.length} 台主机。`
              : '添加主机后，Codex 可以直接使用这里的别名发起 SSH 连接。'}
          </p>
        </div>
        {!form && (
          <button
            className="button"
            type="button"
            disabled={credentials.length === 0}
            title={credentials.length === 0 ? '请先添加登录凭证' : undefined}
            onClick={() => setForm({ ...emptyHost })}
          >
            添加主机
          </button>
        )}
      </div>

      {form && (
        <form className="panel mt-4" onSubmit={submit}>
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div>
              <h3 className="text-lg font-semibold">
                {form.id ? `编辑主机：${form.alias}` : '添加主机'}
              </h3>
              <p className="muted mt-1 text-sm">
                主机保存后，配置会自动同步到选中的执行节点。
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
              启用主机
            </label>
          </div>

          <fieldset className="mt-6">
            <legend className="font-semibold">连接信息</legend>
            <div className="mt-3 grid gap-4 md:grid-cols-2 xl:grid-cols-4">
              <label className="text-sm font-medium">
                SSH 别名
                <input
                  className="field mt-1"
                  value={form.alias}
                  placeholder="例如：production"
                  required
                  maxLength={128}
                  pattern="[A-Za-z0-9._-]+"
                  title="只能使用字母、数字、点、下划线和连字符"
                  onChange={(event) =>
                    setForm({ ...form, alias: event.target.value })
                  }
                />
              </label>
              <label className="text-sm font-medium">
                主机地址
                <input
                  className="field mt-1"
                  value={form.hostname}
                  placeholder="IP 地址或域名"
                  required
                  maxLength={255}
                  onChange={(event) =>
                    setForm({ ...form, hostname: event.target.value })
                  }
                />
              </label>
              <label className="text-sm font-medium">
                端口
                <input
                  className="field mt-1"
                  type="number"
                  min={1}
                  max={65535}
                  value={form.port}
                  onChange={(event) =>
                    setForm({ ...form, port: Number(event.target.value) })
                  }
                />
              </label>
              <label className="text-sm font-medium">
                用户名
                <input
                  className="field mt-1"
                  value={form.username}
                  placeholder="例如：root"
                  required
                  maxLength={128}
                  onChange={(event) =>
                    setForm({ ...form, username: event.target.value })
                  }
                />
              </label>
            </div>
          </fieldset>

          <fieldset className="mt-6 border-t border-[color:var(--border)] pt-6">
            <legend className="font-semibold">认证与跳板机</legend>
            <div className="mt-3 grid gap-4 md:grid-cols-2">
              <label className="text-sm font-medium">
                使用凭证
                <select
                  className="field mt-1"
                  value={form.credentialId}
                  required
                  onChange={(event) =>
                    setForm({ ...form, credentialId: event.target.value })
                  }
                >
                  <option value="">请选择凭证</option>
                  {credentials.map((item) => (
                    <option value={item.id} key={item.id}>
                      {item.name}
                      {item.enabled ? '' : '（已停用）'}
                    </option>
                  ))}
                </select>
              </label>
              <label className="text-sm font-medium">
                ProxyJump（可选）
                <select
                  className="field mt-1"
                  value={form.proxyJumpHostId}
                  onChange={(event) => changeProxyJump(event.target.value)}
                >
                  <option value="">不使用跳板机</option>
                  {items
                    .filter((item) => item.id !== form.id)
                    .map((item) => (
                      <option value={item.id} key={item.id}>
                        {item.alias}
                        {item.enabled ? '' : '（已停用）'}
                      </option>
                    ))}
                </select>
              </label>
            </div>
          </fieldset>

          <fieldset className="mt-6 border-t border-[color:var(--border)] pt-6">
            <legend className="font-semibold">下发到执行节点</legend>
            <p className="muted mt-1 text-sm">
              {selectedProxy
                ? `使用 ${selectedProxy.alias} 作为跳板机时，只能选择它已经覆盖的节点。`
                : '只有选中的 Worker 会收到这台主机和对应凭证。未选择时不会下发。'}
            </p>
            {nodes.length === 0 ? (
              <div className="mt-3 rounded-lg border border-dashed border-[color:var(--border-strong)] p-4 text-sm">
                目前没有执行节点。你可以先保存主机，注册 Worker 后再回来分配。
              </div>
            ) : (
              <div className="mt-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                {nodes.map((node) => {
                  const checked = form.executionNodeIds.includes(node.id)
                  const allowedByProxy =
                    !selectedProxy ||
                    selectedProxy.executionNodeIds.includes(node.id)
                  const canSelect = node.enabled && allowedByProxy
                  return (
                    <label
                      className={`flex items-start gap-3 rounded-xl border p-3 text-sm transition-colors ${
                        checked
                          ? 'border-[color:var(--accent)] bg-[var(--surface-muted)]'
                          : 'border-[color:var(--border)]'
                      } ${canSelect || checked ? 'cursor-pointer' : 'cursor-not-allowed opacity-50'}`}
                      key={node.id}
                    >
                      <input
                        className="mt-0.5 size-4 accent-[var(--accent)]"
                        type="checkbox"
                        aria-label={node.name}
                        checked={checked}
                        disabled={!canSelect && !checked}
                        onChange={(event) =>
                          toggleNode(node.id, event.target.checked)
                        }
                      />
                      <span>
                        <span className="block font-medium">{node.name}</span>
                        <span className="muted mt-0.5 block text-xs">
                          {node.enabled
                            ? allowedByProxy
                              ? '将接收此配置'
                              : '跳板机未分配到此节点'
                            : '节点已停用'}
                        </span>
                      </span>
                    </label>
                  )
                })}
              </div>
            )}
          </fieldset>

          <div className="mt-6 flex flex-wrap gap-2">
            <button className="button" disabled={save.isPending}>
              {save.isPending ? '保存中…' : '保存主机'}
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
            <p className="font-medium">还没有远程主机</p>
            <p className="muted mt-1 text-sm">
              {credentials.length === 0
                ? '请先在上方添加登录凭证。'
                : '点击“添加主机”，配置连接地址和执行节点。'}
            </p>
          </div>
        ) : (
          items.map((item) => {
            const assignedNodes = item.executionNodeIds.map(
              (id) => nodes.find((node) => node.id === id)?.name ?? '未知节点',
            )
            return (
              <article className="panel" key={item.id}>
                <div className="flex flex-wrap items-start justify-between gap-4">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <h3 className="font-semibold">{item.alias}</h3>
                      <span className="rounded-full border border-[color:var(--border)] bg-[var(--surface-muted)] px-2.5 py-1 text-xs font-medium">
                        {item.enabled ? '已启用' : '已停用'}
                      </span>
                    </div>
                    <code className="mt-2 block break-all text-sm">
                      {item.username}@{item.hostname}:{item.port}
                    </code>
                    <p className="muted mt-2 text-sm">
                      凭证：{item.credentialName}
                      {item.proxyJumpAlias
                        ? ` · 跳板机：${item.proxyJumpAlias}`
                        : ''}
                    </p>
                    <p className="muted mt-1 text-xs">
                      执行节点：
                      {assignedNodes.length > 0
                        ? assignedNodes.join('、')
                        : '未分配（不会下发）'}
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
                      disabled={remove.isPending}
                      onClick={() => remove.mutate(item.id)}
                    >
                      删除
                    </button>
                  </div>
                </div>
              </article>
            )
          })
        )}
      </div>
    </section>
  )
}
