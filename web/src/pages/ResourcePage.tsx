import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo } from 'react'
import { api, type ListResponse } from '../api/client'
import { useUI } from '../state'

const hiddenColumns = new Set(['metadata', 'payload', 'config'])

export function ResourcePage({
  resource,
  title,
  description = '实时显示控制面的权威状态。',
}: {
  resource: string
  title: string
  description?: string
}) {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const query = useQuery({
    queryKey: [resource],
    queryFn: () => api<ListResponse>(`/${resource}`),
  })
  useEffect(() => {
    const source = new EventSource('/api/v1/events/stream', {
      withCredentials: true,
    })
    source.addEventListener(
      'update',
      () => void queryClient.invalidateQueries({ queryKey: [resource] }),
    )
    return () => source.close()
  }, [queryClient, resource])
  const columns = useMemo(() => {
    const found = new Set<string>()
    for (const item of query.data?.items ?? []) {
      Object.keys(item).forEach((key) => {
        if (!hiddenColumns.has(key)) found.add(key)
      })
    }
    return [...found].slice(0, 10)
  }, [query.data])

  return (
    <section>
      <div className="flex items-end justify-between gap-4">
        <div>
          <h1 className="text-3xl font-bold">{title}</h1>
          <p className="muted mt-2">{description}</p>
        </div>
        <button
          className="button-secondary"
          disabled={query.isFetching}
          onClick={async () => {
            const result = await query.refetch()
            if (result.error) {
              showToast('error', result.error.message)
              return
            }
            showToast('success', `${title}已刷新`)
          }}
        >
          {query.isFetching ? '刷新中…' : '刷新'}
        </button>
      </div>
      {query.isLoading && <p className="mt-8">正在加载…</p>}
      {query.error && (
        <p role="alert" className="error-text mt-8">
          {query.error.message}
        </p>
      )}
      {query.data && query.data.items.length === 0 && (
        <div className="panel muted mt-8">暂无数据</div>
      )}
      {query.data && query.data.items.length > 0 && (
        <div className="panel mt-8 overflow-x-auto p-0">
          <table className="w-full min-w-[760px] border-collapse text-left text-sm">
            <thead className="table-head border-b">
              <tr>
                {columns.map((column) => (
                  <th className="px-4 py-3 font-medium" key={column}>
                    {humanize(column)}
                  </th>
                ))}
                {resource === 'threads' && <th className="px-4 py-3">操作</th>}
              </tr>
            </thead>
            <tbody>
              {query.data.items.map((item, index) => (
                <tr
                  className="table-row border-b last:border-0"
                  key={String(item.id ?? index)}
                >
                  {columns.map((column) => (
                    <td
                      className="max-w-[320px] truncate px-4 py-3"
                      title={format(item[column])}
                      key={column}
                    >
                      {format(item[column])}
                    </td>
                  ))}
                  {resource === 'threads' && (
                    <td className="px-4 py-3">
                      <ControlActions
                        item={item}
                        onDone={() => query.refetch()}
                      />
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

function ControlActions({
  item,
  onDone,
}: {
  item: Record<string, unknown>
  onDone: () => void | Promise<unknown>
}) {
  const showToast = useUI((state) => state.showToast)
  const mutation = useMutation({
    mutationFn: (action: 'reconcile' | 'reset') =>
      api<void>(`/controls/${String(item.id)}/${action}`, { method: 'POST' }),
    onSuccess: async (_, action) => {
      await onDone()
      showToast('success', action === 'reconcile' ? '对账已完成' : '重置已完成')
    },
  })
  if (item.status !== 'error') return <span className="muted">—</span>
  return (
    <div className="flex gap-2">
      <button
        className="button-secondary"
        disabled={mutation.isPending}
        onClick={() => mutation.mutate('reconcile')}
      >
        {mutation.isPending && mutation.variables === 'reconcile'
          ? '对账中…'
          : '对账'}
      </button>
      <button
        className="button-secondary"
        disabled={mutation.isPending}
        onClick={() => mutation.mutate('reset')}
      >
        {mutation.isPending && mutation.variables === 'reset'
          ? '重置中…'
          : '重置'}
      </button>
    </div>
  )
}

function humanize(value: string): string {
  return value
    .replace(/([A-Z])/g, ' $1')
    .replace(/^./, (letter) => letter.toUpperCase())
}

function format(value: unknown): string {
  if (value === null || value === undefined || value === '') return '—'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}
