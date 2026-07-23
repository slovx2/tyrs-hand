import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { api } from '../api/client'
import { useUI } from '../state'
import { parseSSHConfig } from './sshConfigParser'
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

interface HostImportForm {
  config: string
  credentialId: string
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

const emptyImport: HostImportForm = {
  config: '',
  credentialId: '',
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
  const [importForm, setImportForm] = useState<HostImportForm | null>(null)
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
  const importHosts = useMutation({
    mutationFn: (values: HostImportForm) => {
      const parsed = parseSSHConfig(values.config)
      return api<{ items: SSHHost[] }>('/ssh/hosts/import', {
        method: 'POST',
        body: JSON.stringify({
          credentialId: values.credentialId,
          executionNodeIds: values.executionNodeIds,
          enabled: values.enabled,
          hosts: parsed.hosts,
        }),
      })
    },
    onSuccess: async (result) => {
      setImportForm(null)
      await refresh()
      showToast('success', `已导入 ${result.items.length} 台 SSH 主机`)
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
  const toggleImportNode = (id: string, checked: boolean) => {
    setImportForm((value) =>
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
  const submitImport = (event: FormEvent) => {
    event.preventDefault()
    if (importForm) importHosts.mutate(importForm)
  }
  const selectedProxy = items.find((item) => item.id === form?.proxyJumpHostId)
  const parsedImport = (() => {
    if (!importForm?.config.trim()) return { result: null, error: '' }
    try {
      return { result: parseSSHConfig(importForm.config), error: '' }
    } catch (error) {
      return {
        result: null,
        error: error instanceof Error ? error.message : 'SSH config 解析失败',
      }
    }
  })()

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
        {!form && !importForm && (
          <div className="flex flex-wrap gap-2">
            <button
              className="button-secondary"
              type="button"
              disabled={credentials.length === 0}
              title={credentials.length === 0 ? '请先添加登录凭证' : undefined}
              onClick={() =>
                setImportForm({
                  ...emptyImport,
                  credentialId:
                    credentials.find((item) => item.enabled)?.id ?? '',
                })
              }
            >
              导入 SSH config
            </button>
            <button
              className="button"
              type="button"
              disabled={credentials.length === 0}
              title={credentials.length === 0 ? '请先添加登录凭证' : undefined}
              onClick={() => setForm({ ...emptyHost })}
            >
              添加主机
            </button>
          </div>
        )}
      </div>

      {importForm && (
        <form className="panel mt-4" onSubmit={submitImport}>
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div>
              <h3 className="text-lg font-semibold">导入 SSH config</h3>
              <p className="muted mt-1 text-sm">
                粘贴多个具体的 Host 段，确认预览后会在一个事务中批量创建。
              </p>
            </div>
            <label className="flex cursor-pointer items-center gap-2 text-sm font-medium">
              <input
                className="size-4 accent-[var(--accent)]"
                type="checkbox"
                checked={importForm.enabled}
                onChange={(event) =>
                  setImportForm({
                    ...importForm,
                    enabled: event.target.checked,
                  })
                }
              />
              启用导入的主机
            </label>
          </div>

          <label className="mt-6 block text-sm font-medium">
            SSH config
            <textarea
              className="field mt-1 min-h-64 font-mono text-sm"
              value={importForm.config}
              required
              spellCheck={false}
              placeholder={`Host jump
  HostName 192.0.2.10
  User ubuntu

Host production
  HostName 10.0.0.8
  User deploy
  Port 2222
  ProxyJump jump`}
              onChange={(event) =>
                setImportForm({ ...importForm, config: event.target.value })
              }
            />
          </label>
          <p className="muted mt-2 text-xs">
            支持 Host、HostName、User、Port 和单级 ProxyJump；一个 Host
            段可以包含多个具体别名。
          </p>

          {parsedImport.error && (
            <div
              className="mt-3 rounded-lg border border-red-300 bg-red-50 p-3 text-sm text-red-800"
              role="alert"
            >
              {parsedImport.error}
            </div>
          )}

          {parsedImport.result && (
            <div className="mt-4 rounded-xl border border-[color:var(--border)] bg-[var(--surface-muted)] p-4">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <h4 className="font-semibold">
                  将导入 {parsedImport.result.hosts.length} 台主机
                </h4>
                <span className="muted text-xs">整批成功或整批失败</span>
              </div>
              <div className="mt-3 grid gap-2 md:grid-cols-2">
                {parsedImport.result.hosts.slice(0, 12).map((host) => (
                  <div
                    className="rounded-lg border border-[color:var(--border)] bg-[var(--surface)] px-3 py-2 text-sm"
                    key={host.alias}
                  >
                    <span className="font-semibold">{host.alias}</span>
                    <code className="muted ml-2 break-all text-xs">
                      {host.username}@{host.hostname}:{host.port}
                    </code>
                    {host.proxyJumpAlias && (
                      <span className="muted mt-1 block text-xs">
                        ProxyJump：{host.proxyJumpAlias}
                      </span>
                    )}
                  </div>
                ))}
              </div>
              {parsedImport.result.hosts.length > 12 && (
                <p className="muted mt-2 text-xs">
                  另有 {parsedImport.result.hosts.length - 12}{' '}
                  台主机未在预览中展开。
                </p>
              )}
              {parsedImport.result.identityFiles.length > 0 && (
                <p className="mt-3 text-xs text-amber-800">
                  config 中的 IdentityFile
                  只是本机路径，页面不会读取；以下选择的托管凭证会用于全部主机。
                </p>
              )}
              {parsedImport.result.ignoredDirectives.length > 0 && (
                <p className="muted mt-2 text-xs">
                  未导入的指令：
                  {parsedImport.result.ignoredDirectives.join('、')}
                </p>
              )}
            </div>
          )}

          <fieldset className="mt-6 border-t border-[color:var(--border)] pt-6">
            <legend className="font-semibold">认证凭证</legend>
            <label className="mt-3 block max-w-xl text-sm font-medium">
              全部主机使用
              <select
                className="field mt-1"
                value={importForm.credentialId}
                required
                onChange={(event) =>
                  setImportForm({
                    ...importForm,
                    credentialId: event.target.value,
                  })
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
          </fieldset>

          <fieldset className="mt-6 border-t border-[color:var(--border)] pt-6">
            <legend className="font-semibold">下发到执行节点</legend>
            <p className="muted mt-1 text-sm">
              选中的节点会同时接收本批主机。同批 ProxyJump
              会自动按依赖顺序创建。
            </p>
            {nodes.length === 0 ? (
              <div className="mt-3 rounded-lg border border-dashed border-[color:var(--border-strong)] p-4 text-sm">
                目前没有执行节点。你可以先导入主机，注册 Worker 后再回来分配。
              </div>
            ) : (
              <div className="mt-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                {nodes.map((node) => {
                  const checked = importForm.executionNodeIds.includes(node.id)
                  return (
                    <label
                      className={`flex items-start gap-3 rounded-xl border p-3 text-sm transition-colors ${
                        checked
                          ? 'border-[color:var(--accent)] bg-[var(--surface-muted)]'
                          : 'border-[color:var(--border)]'
                      } ${node.enabled ? 'cursor-pointer' : 'cursor-not-allowed opacity-50'}`}
                      key={node.id}
                    >
                      <input
                        className="mt-0.5 size-4 accent-[var(--accent)]"
                        type="checkbox"
                        aria-label={`批量导入到 ${node.name}`}
                        checked={checked}
                        disabled={!node.enabled}
                        onChange={(event) =>
                          toggleImportNode(node.id, event.target.checked)
                        }
                      />
                      <span>
                        <span className="block font-medium">{node.name}</span>
                        <span className="muted mt-0.5 block text-xs">
                          {node.enabled ? '将接收整批配置' : '节点已停用'}
                        </span>
                      </span>
                    </label>
                  )
                })}
              </div>
            )}
          </fieldset>

          <div className="mt-6 flex flex-wrap gap-2">
            <button
              className="button"
              disabled={
                importHosts.isPending ||
                !parsedImport.result ||
                !importForm.credentialId
              }
            >
              {importHosts.isPending
                ? '导入中…'
                : parsedImport.result
                  ? `导入 ${parsedImport.result.hosts.length} 台主机`
                  : '导入主机'}
            </button>
            <button
              type="button"
              className="button-secondary"
              disabled={importHosts.isPending}
              onClick={() => setImportForm(null)}
            >
              取消
            </button>
          </div>
        </form>
      )}

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
