import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it } from 'vitest'
import { server } from '../test/server'
import { LivePage } from './LivePage'

const conversationId = '11111111-1111-1111-1111-111111111111'

afterEach(() => {
  cleanup()
  window.localStorage.clear()
  window.history.replaceState(null, '', '/')
})

function conversationJson() {
  return {
    id: conversationId,
    model: 'gpt-live-1-codex',
    voice: 'cove',
    instructions: '',
    status: 'active',
    contextRevision: 1,
    createdAt: '2026-09-13T00:00:00Z',
    updatedAt: '2026-09-13T00:00:00Z',
  }
}

describe('LivePage', () => {
  it('展示产品控制，不展示调试入口', () => {
    render(<LivePage />)
    expect(screen.getByRole('heading', { name: 'Live 语音' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '连接' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '更多' })).toBeInTheDocument()
    expect(screen.getByText('连接后开始说话')).toBeInTheDocument()
    expect(screen.queryByText('使用验收录音')).not.toBeInTheDocument()
    expect(screen.queryByPlaceholderText('输入文本测试')).not.toBeInTheDocument()
    expect(screen.queryByText(/Conversation/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Session/)).not.toBeInTheDocument()
  })

  it('打开菜单后可重置且字幕仍在，清空后字幕消失', async () => {
    window.localStorage.setItem('tyrs-hand.live.conversationId', conversationId)
    server.use(
      http.get(`/api/v1/client/live-conversations/${conversationId}`, () =>
        HttpResponse.json(conversationJson()),
      ),
      http.get(
        `/api/v1/client/live-conversations/${conversationId}/messages`,
        () =>
          HttpResponse.json({
            items: [
              {
                sequence: 2,
                role: 'assistant',
                text: '已经切到 staging',
                createdAt: '2026-09-13T00:00:02Z',
              },
              {
                sequence: 1,
                role: 'user',
                text: '切到 staging',
                createdAt: '2026-09-13T00:00:01Z',
              },
            ],
          }),
      ),
    )
    const user = userEvent.setup()
    render(<LivePage />)
    expect(await screen.findByText('切到 staging')).toBeInTheDocument()
    expect(screen.getByText('已经切到 staging')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '更多' }))
    await user.click(screen.getByRole('menuitem', { name: '重置会话' }))
    expect(screen.getByText('切到 staging')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '更多' }))
    await user.click(screen.getByRole('menuitem', { name: '清空字幕' }))
    expect(screen.queryByText('切到 staging')).not.toBeInTheDocument()
    expect(screen.getByText('连接后开始说话')).toBeInTheDocument()
    expect(window.localStorage.getItem('tyrs-hand.live.conversationId')).toBeNull()
  })

  it('acceptanceAudio 不展示控件', () => {
    window.history.replaceState(null, '', '/live?acceptanceAudio=1')
    const { container } = render(<LivePage />)
    expect(screen.queryByText('使用验收录音')).not.toBeInTheDocument()
    const hidden = container.querySelectorAll('audio')
    expect(hidden.length).toBeGreaterThan(0)
    for (const element of hidden) {
      expect(element.hasAttribute('controls')).toBe(false)
    }
  })
})
