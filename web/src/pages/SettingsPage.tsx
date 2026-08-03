import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import { useUI } from '../state'

interface GitHubAgentInstructions {
  content: string
  revision: number
}

export function SettingsPage() {
  const queryClient = useQueryClient()
  const showToast = useUI((state) => state.showToast)
  const instructions = useQuery({
    queryKey: ['github-agent-instructions'],
    queryFn: () =>
      api<GitHubAgentInstructions>('/settings/github-agent-instructions'),
  })
  const save = useMutation({
    mutationFn: (content: string) =>
      api<void>('/settings/github-agent-instructions', {
        method: 'PUT',
        body: JSON.stringify({ content }),
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ['github-agent-instructions'],
      })
      showToast('success', 'GitHub Agent 指令已保存')
    },
  })

  return (
    <section>
      <h1 className="text-3xl font-bold">GitHub Agent 指令</h1>
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
        <h2 className="text-xl font-semibold">GitHub Work Item 指令</h2>
        <p className="muted mt-2 text-sm">
          仅作为 GitHub Work Item 的 developer instructions
          注入。Desktop、Discord 和 Mobile 会话不会读取该内容，也不会写入机器
          Codex Home。
        </p>
        <textarea
          className="field mt-4 min-h-80 font-mono text-sm"
          name="content"
          maxLength={262144}
          defaultValue={instructions.data?.content ?? ''}
          key={instructions.data?.revision ?? 0}
          spellCheck={false}
          aria-label="GitHub Agent 指令"
        />
        <button className="button mt-5" disabled={save.isPending}>
          {save.isPending ? '保存中…' : '保存 GitHub Agent 指令'}
        </button>
      </form>
    </section>
  )
}
