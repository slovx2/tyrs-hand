import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import { useUI } from '../state'

interface GlobalAgentsSettings {
  content: string
  revision: number
}

export function SettingsPage() {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const globalAgents = useQuery({
    queryKey: ['global-agents'],
    queryFn: () => api<GlobalAgentsSettings>('/settings/global-agents'),
  })
  const save = useMutation({
    mutationFn: (content: string) =>
      api<void>('/settings/global-agents', {
        method: 'PUT',
        body: JSON.stringify({ content }),
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['global-agents'] })
      showToast('success', '全局 AGENTS.md 已保存')
    },
  })

  return (
    <section>
      <h1 className="text-3xl font-bold">系统设置</h1>
      <div className="danger-note mt-6">
        Codex Provider、登录态、Base URL、代理和其他 Codex 配置仅从 Worker
        宿主用户的真实 CODEX_HOME 读取，Control 不保存也不下发这些配置。
      </div>
      <form
        className="panel mt-6"
        onSubmit={(event) => {
          event.preventDefault()
          const formData = new FormData(event.currentTarget)
          save.mutate(String(formData.get('content') ?? ''))
        }}
      >
        <h2 className="text-xl font-semibold">全局 Agent 指令</h2>
        <p className="muted mt-2 text-sm">
          该内容作为每个任务的 developer instructions 注入，不会写入或修改机器
          Codex Home。
        </p>
        <textarea
          className="field mt-4 min-h-80 font-mono text-sm"
          name="content"
          maxLength={262144}
          defaultValue={globalAgents.data?.content ?? ''}
          key={globalAgents.data?.revision ?? 0}
          spellCheck={false}
          aria-label="全局 Agent 指令"
        />
        <button className="button mt-5" disabled={save.isPending}>
          {save.isPending ? '保存中…' : '保存全局 Agent 指令'}
        </button>
      </form>
    </section>
  )
}
