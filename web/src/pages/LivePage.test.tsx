import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { server } from '../test/server'
import { LivePage } from './LivePage'

const conversationId = '11111111-1111-1111-1111-111111111111'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  window.localStorage.clear()
  window.history.replaceState(null, '', '/')
})

function conversationJson() {
  return {
    id: conversationId,
    workerId: '22222222-2222-2222-2222-222222222222',
    projectId: '33333333-3333-3333-3333-333333333333',
    workspaceSessionId: '44444444-4444-4444-4444-444444444444',
    model: 'gpt-live-1-codex',
    voice: 'cove',
    instructions: '',
    status: 'active',
    contextRevision: 1,
    createdAt: '2026-09-13T00:00:00Z',
    updatedAt: '2026-09-13T00:00:00Z',
  }
}

class FakePeerConnection {
  iceGatheringState = 'complete'
  connectionState = 'new'
  localDescription: { sdp?: string } | null = null
  onconnectionstatechange: (() => void) | null = null
  ontrack: ((event: { streams: MediaStream[] }) => void) | null = null
  readonly dataChannel = {
    onmessage: null as ((event: { data: string }) => void) | null,
    onopen: null as (() => void) | null,
    close: vi.fn(),
  }

  createDataChannel() {
    queueMicrotask(() => this.dataChannel.onopen?.())
    return this.dataChannel
  }

  addTransceiver = vi.fn()
  createOffer = vi.fn(async () => ({ type: 'offer', sdp: 'offer-sdp' }))
  async setLocalDescription(description: { sdp?: string }) {
    this.localDescription = description
  }
  setRemoteDescription = vi.fn(async () => undefined)
  close = vi.fn()
}

describe('LivePage', () => {
  it('展示产品控制，不展示调试入口', () => {
    render(<LivePage />)
    expect(
      screen.getByRole('heading', { name: 'Live 语音' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '连接' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '更多' })).toBeInTheDocument()
    expect(screen.getByText('连接后开始说话')).toBeInTheDocument()
    expect(screen.queryByText('使用验收录音')).not.toBeInTheDocument()
    expect(
      screen.queryByPlaceholderText('输入文本测试'),
    ).not.toBeInTheDocument()
    expect(screen.queryByText(/Conversation/)).not.toBeInTheDocument()
    expect(screen.getByText('Worker')).toBeInTheDocument()
    expect(
      screen.getByRole('option', { name: '选择 Worker' }),
    ).toBeInTheDocument()
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
      http.patch(
        `/api/v1/client/live-conversations/${conversationId}`,
        async ({ request }) => {
          const body = (await request.json()) as { voice: string }
          return HttpResponse.json({ ...conversationJson(), voice: body.voice })
        },
      ),
      http.post(
        `/api/v1/client/live-conversations/${conversationId}/reset-history`,
        () => HttpResponse.json(conversationJson()),
      ),
      http.post(
        `/api/v1/client/live-conversations/${conversationId}/clear-messages`,
        () => HttpResponse.json(conversationJson()),
      ),
    )
    const user = userEvent.setup()
    render(<LivePage />)
    expect(await screen.findByText('切到 staging')).toBeInTheDocument()
    expect(screen.getByText('已经切到 staging')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '音色：Cove' }))
    await user.click(screen.getByRole('radio', { name: 'Ember：自信乐观' }))
    expect(
      await screen.findByRole('button', { name: '音色：Ember' }),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '更多' }))
    await user.click(screen.getByRole('menuitem', { name: '重置会话' }))
    expect(await screen.findByText('切到 staging')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '更多' }))
    await user.click(screen.getByRole('menuitem', { name: '清空字幕' }))
    await waitFor(() => {
      expect(screen.queryByText('切到 staging')).not.toBeInTheDocument()
    })
    expect(screen.getByText('连接后开始说话')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: '音色：Ember' }),
    ).toBeInTheDocument()
    expect(window.localStorage.getItem('tyrs-hand.live.conversationId')).toBe(
      conversationId,
    )
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

  it('音色选择器支持试听和切换，不阻断连接操作', async () => {
    const play = vi
      .spyOn(HTMLMediaElement.prototype, 'play')
      .mockResolvedValue(undefined)
    const pause = vi
      .spyOn(HTMLMediaElement.prototype, 'pause')
      .mockImplementation(() => undefined)
    const user = userEvent.setup()
    render(<LivePage />)

    await user.click(screen.getByRole('button', { name: '音色：Cove' }))
    expect(screen.getByRole('dialog', { name: '选择音色' })).toBeInTheDocument()
    expect(screen.getAllByRole('radio')).toHaveLength(9)
    await user.click(screen.getByRole('button', { name: '播放 Cove 试听' }))
    expect(play).toHaveBeenCalledTimes(1)
    await user.click(screen.getByRole('radio', { name: 'Breeze：活泼真挚' }))
    expect(
      screen.getByRole('button', { name: '音色：Breeze' }),
    ).toBeInTheDocument()
    expect(pause).toHaveBeenCalled()
  })

  it('已有 conversation 切换音色会保存，但不会自动重置', async () => {
    window.localStorage.setItem('tyrs-hand.live.conversationId', conversationId)
    let receivedVoice = ''
    server.use(
      http.get(`/api/v1/client/live-conversations/${conversationId}`, () =>
        HttpResponse.json(conversationJson()),
      ),
      http.get(
        `/api/v1/client/live-conversations/${conversationId}/messages`,
        () => HttpResponse.json({ items: [] }),
      ),
      http.patch(
        `/api/v1/client/live-conversations/${conversationId}`,
        async ({ request }) => {
          receivedVoice = ((await request.json()) as { voice: string }).voice
          return HttpResponse.json({
            ...conversationJson(),
            voice: receivedVoice,
          })
        },
      ),
    )
    const user = userEvent.setup()
    render(<LivePage />)

    await user.click(await screen.findByRole('button', { name: '音色：Cove' }))
    await user.click(screen.getByRole('radio', { name: 'Ember：自信乐观' }))
    await waitFor(() => expect(receivedVoice).toBe('ember'))
    expect(
      screen.getByRole('button', { name: '音色：Ember' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '连接' })).toBeInTheDocument()
  })

  it('新建 conversation 时把所选音色带入连接请求', async () => {
    vi.stubGlobal('RTCPeerConnection', FakePeerConnection)
    const workerId = '22222222-2222-2222-2222-222222222222'
    const sessionId = '44444444-4444-4444-4444-444444444444'
    const createdConversation = {
      ...conversationJson(),
      id: '55555555-5555-5555-5555-555555555555',
      workerId,
      workspaceSessionId: sessionId,
      voice: 'ember',
    }
    let createBody: Record<string, unknown> | undefined
    let sessionCreated = false
    server.use(
      http.get('/api/v1/workers', () =>
        HttpResponse.json({ items: [{ id: workerId, name: 'Live Worker' }] }),
      ),
      http.get(`/api/v1/client/live-workers/${workerId}/sessions`, () =>
        HttpResponse.json({
          sessions: [{ id: sessionId, title: 'Live Session' }],
        }),
      ),
      http.get(`/api/v1/client/live-workers/${workerId}/projects`, () =>
        HttpResponse.json({ projects: [] }),
      ),
      http.post('/api/v1/client/live-conversations', async ({ request }) => {
        createBody = (await request.json()) as Record<string, unknown>
        return HttpResponse.json(createdConversation, { status: 201 })
      }),
      http.post(
        `/api/v1/client/live-conversations/${createdConversation.id}/sessions`,
        () => {
          sessionCreated = true
          return HttpResponse.json(
            {
              conversationId: createdConversation.id,
              sessionId: '66666666-6666-6666-6666-666666666666',
              transport: { type: 'webrtc', answerSdp: 'answer-sdp' },
              session: { status: 'starting' },
            },
            { status: 201 },
          )
        },
      ),
    )
    const user = userEvent.setup()
    render(<LivePage />)

    await user.selectOptions(await screen.findByLabelText('Worker'), workerId)
    await user.selectOptions(await screen.findByLabelText('Session'), sessionId)
    await user.click(screen.getByRole('button', { name: '音色：Cove' }))
    await user.click(screen.getByRole('radio', { name: 'Ember：自信乐观' }))
    await user.click(screen.getByRole('button', { name: '连接' }))

    await waitFor(() => expect(createBody?.voice).toBe('ember'))
    await waitFor(() => expect(sessionCreated).toBe(true))
    expect(
      screen.getByRole('button', { name: '音色：Ember' }),
    ).toBeInTheDocument()
    expect(screen.queryByText(/重置会话后生效/)).not.toBeInTheDocument()
  })
})
