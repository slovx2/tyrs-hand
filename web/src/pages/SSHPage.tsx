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

  return (
    <section>
      <h1 className="text-3xl font-bold">SSH</h1>
      <p className="muted mt-2">
        私钥由 Control 加密保存，仅向分配到的 Execution Node 下发。
      </p>
      <SSHCredentialSection items={credentials.data?.items ?? []} />
      <SSHHostSection
        items={hosts.data?.items ?? []}
        credentials={credentials.data?.items ?? []}
        nodes={nodes.data?.items ?? []}
      />
    </section>
  )
}
