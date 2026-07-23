import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { SSHCredentialSection } from './SSHCredentialSection'
import { SSHHostSection } from './SSHHostSection'
import type { SSHCredential, SSHExecutionNode, SSHHost } from './sshTypes'

export function SSHPage() {
  const credentials = useQuery({
    queryKey: ['ssh-credentials'],
    queryFn: () => api<{ items: SSHCredential[] }>('/ssh/credentials'),
  })
  const hosts = useQuery({
    queryKey: ['ssh-hosts'],
    queryFn: () => api<{ items: SSHHost[] }>('/ssh/hosts'),
  })
  const nodes = useQuery({
    queryKey: ['execution-nodes'],
    queryFn: () => api<{ items: SSHExecutionNode[] }>('/execution-nodes'),
  })
  const isLoading = credentials.isLoading || hosts.isLoading || nodes.isLoading
  const error = credentials.error ?? hosts.error ?? nodes.error
  const retry = () => {
    void Promise.all([credentials.refetch(), hosts.refetch(), nodes.refetch()])
  }

  return (
    <section className="mx-auto max-w-6xl pb-16">
      <h1 className="text-3xl font-bold">SSH</h1>
      <p className="muted mt-2 max-w-3xl leading-6">
        集中管理 Codex 可以访问的远程主机。私钥由 Control
        加密保存，只会下发给你指定的执行节点。
      </p>

      <ol className="mt-6 grid gap-3 md:grid-cols-3" aria-label="SSH 配置步骤">
        <SetupStep number="1" title="添加凭证">
          上传或粘贴私钥，保存后不会再次显示。
        </SetupStep>
        <SetupStep number="2" title="添加主机">
          填写连接地址，并选择用于登录的凭证。
        </SetupStep>
        <SetupStep number="3" title="选择执行节点">
          指定哪些 Worker 可以收到并使用这份配置。
        </SetupStep>
      </ol>

      {isLoading ? (
        <div className="panel muted mt-8 text-sm">正在加载 SSH 配置…</div>
      ) : error ? (
        <div className="danger-note mt-8 flex flex-wrap items-center justify-between gap-4">
          <span>SSH 配置加载失败：{(error as Error).message}</span>
          <button className="button-secondary" type="button" onClick={retry}>
            重新加载
          </button>
        </div>
      ) : (
        <>
          <SSHCredentialSection items={credentials.data?.items ?? []} />
          <SSHHostSection
            items={hosts.data?.items ?? []}
            credentials={credentials.data?.items ?? []}
            nodes={nodes.data?.items ?? []}
          />
        </>
      )}
    </section>
  )
}

function SetupStep({
  number,
  title,
  children,
}: {
  number: string
  title: string
  children: string
}) {
  return (
    <li className="rounded-xl border border-[color:var(--border)] bg-[var(--surface-raised)] p-4">
      <div className="flex items-center gap-3">
        <span className="grid size-7 place-items-center rounded-full bg-[var(--accent)] text-xs font-bold text-[var(--on-accent)]">
          {number}
        </span>
        <h2 className="font-semibold">{title}</h2>
      </div>
      <p className="muted mt-2 pl-10 text-sm leading-5">{children}</p>
    </li>
  )
}
