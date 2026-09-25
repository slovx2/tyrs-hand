import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import type { components } from '../api/schema'
import { StatusBadge } from './workerUI'

type WorkerRuntime = components['schemas']['WorkerRuntime']

const runtimeLabels: Record<WorkerRuntime['status'], string> = {
  running: '运行中',
  unavailable: '暂不可用',
  stopped: '已停止',
  disabled: '未启用',
  offline: '离线',
  incompatible: '协议不匹配',
}

export function WorkerRuntimes({ workerId }: { workerId: string }) {
  const runtimes = useQuery({
    queryKey: ['worker-runtimes', workerId],
    queryFn: () => api<WorkerRuntime[]>(`/workers/${workerId}/runtimes`),
    refetchInterval: 15_000,
  })
  const active = runtimes.data?.filter((runtime) => runtime.enabled) ?? []
  const partiallyUnavailable =
    active.some((runtime) => runtime.status === 'running') &&
    active.some((runtime) => runtime.status !== 'running')

  return (
    <section className="panel" aria-label="运行时入口">
      <h2 className="text-xl font-semibold">运行时入口</h2>
      {partiallyUnavailable && (
        <p role="status" className="muted mt-2">
          部分运行时不可用，其他入口仍可使用。
        </p>
      )}
      {runtimes.isLoading && <p className="muted mt-2">正在读取运行时…</p>}
      {runtimes.isError && <p role="alert">{runtimes.error.message}</p>}
      {runtimes.data?.length === 0 && (
        <p className="muted mt-2">Worker 尚未上报运行时。</p>
      )}
      {runtimes.data?.map((runtime) => (
        <div
          key={runtime.engine}
          className="mt-5"
          aria-label={
            runtime.engine === 'codex' ? 'Codex 入口' : 'Claude Code 入口'
          }
        >
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="font-semibold">
              {runtime.engine === 'codex' ? 'Codex' : 'Claude Code'}
            </h3>
            <StatusBadge
              tone={runtime.status === 'running' ? 'success' : 'muted'}
            >
              {runtimeLabels[runtime.status]}
            </StatusBadge>
          </div>
          <dl className="runtime-status-grid mt-3">
            <div>
              <dt>SSH 入口</dt>
              <dd>{runtime.sshListenAddress}</dd>
            </div>
            <div>
              <dt>CLI 版本</dt>
              <dd>{runtime.build.cliBuild || '尚未上报'}</dd>
            </div>
            <div>
              <dt>协议版本</dt>
              <dd>{runtime.protocolVersion || '尚未上报'}</dd>
            </div>
            <div>
              <dt>Host Key 指纹</dt>
              <dd className="break-all">
                {runtime.sshHostKeyFingerprint || '尚未上报'}
              </dd>
            </div>
          </dl>
        </div>
      ))}
    </section>
  )
}
