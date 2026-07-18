import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo } from 'react'
import { api, type ListResponse } from '../api/client'

const hiddenColumns = new Set(['metadata', 'payload', 'config'])

export function ResourcePage({
  resource,
  title,
}: {
  resource: string
  title: string
}) {
  const queryClient = useQueryClient()
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
          <p className="mt-2 text-slate-500">实时显示控制面的权威状态。</p>
        </div>
        <button
          className="button-secondary"
          onClick={() => void query.refetch()}
        >
          刷新
        </button>
      </div>
      {query.isLoading && <p className="mt-8">正在加载…</p>}
      {query.error && (
        <p role="alert" className="mt-8 text-red-700">
          {query.error.message}
        </p>
      )}
      {query.data && query.data.items.length === 0 && (
        <div className="panel mt-8 text-slate-500">暂无数据</div>
      )}
      {query.data && query.data.items.length > 0 && (
        <div className="panel mt-8 overflow-x-auto p-0">
          <table className="w-full min-w-[760px] border-collapse text-left text-sm">
            <thead className="border-b border-slate-200 bg-slate-50 dark:border-white/10 dark:bg-white/5">
              <tr>
                {columns.map((column) => (
                  <th className="px-4 py-3 font-medium" key={column}>
                    {humanize(column)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {query.data.items.map((item, index) => (
                <tr
                  className="border-b border-slate-100 last:border-0 dark:border-white/5"
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
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
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
