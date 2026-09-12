import { Mic, MicOff, PhoneOff, RefreshCw, Send } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import {
  createLiveConversation,
  createLiveSession,
  closeLiveSession,
  recoverLiveSession,
  listLiveMessages,
  type LiveConversation,
  type LiveMessage,
} from '../api/live'
import {
  initialLiveTranscriptState,
  reduceLiveTranscript,
  visibleLiveTranscript,
  type LiveTranscriptState,
} from '../features/live/transcriptReducer'

async function waitForIceGathering(
  connection: RTCPeerConnection,
): Promise<void> {
  if (connection.iceGatheringState === 'complete') return
  await new Promise<void>((resolve) => {
    const timeout = window.setTimeout(() => {
      connection.onicegatheringstatechange = null
      resolve()
    }, 5000)
    connection.onicegatheringstatechange = () => {
      if (connection.iceGatheringState === 'complete') {
        window.clearTimeout(timeout)
        connection.onicegatheringstatechange = null
        resolve()
      }
    }
  })
}

export function LivePage() {
  const [conversation, setConversation] = useState<LiveConversation | null>(
    null,
  )
  const [sessionId, setSessionId] = useState<string>()
  const [transcript, setTranscript] = useState<LiveTranscriptState>(
    initialLiveTranscriptState,
  )
  const [text, setText] = useState('')
  const [status, setStatus] = useState('未连接')
  const [error, setError] = useState('')
  const peer = useRef<RTCPeerConnection | null>(null)
  const channel = useRef<RTCDataChannel | null>(null)
  const audio = useRef<HTMLAudioElement>(null)
  const stream = useRef<MediaStream | null>(null)

  useEffect(
    () => () => {
      peer.current?.close()
    },
    [],
  )
  const closePeer = () => {
    channel.current?.close()
    channel.current = null
    stream.current?.getTracks().forEach((track) => track.stop())
    stream.current = null
    peer.current?.close()
    peer.current = null
  }
  const handleEvent = (event: Record<string, unknown>) => {
    const type = String(event.type ?? '')
    if (type === 'session.started') setStatus('已连接')
    setTranscript((current) =>
      reduceLiveTranscript(
        current,
        event as {
          type?: string
          id?: string
          item_id?: string
          response_id?: string
          delta?: string
          text?: string
          transcript?: string
        },
      ),
    )
    if (type === 'error')
      setError(
        String(
          (event.error as { message?: string } | undefined)?.message ??
            'Live 服务返回错误',
        ),
      )
  }
  const connect = async (recover = false) => {
    setError('')
    setStatus('连接中')
    try {
      let current = conversation
      const shouldRecover = recover || Boolean(current)
      if (!current) {
        current = await createLiveConversation({})
        setConversation(current)
      }
      closePeer()
      const connection = new RTCPeerConnection()
      peer.current = connection
      connection.ontrack = (event) => {
        if (audio.current && event.streams[0]) {
          audio.current.srcObject = event.streams[0]
          void audio.current.play().catch(() => undefined)
        }
      }
      const events = connection.createDataChannel('oai-events')
      channel.current = events
      events.onmessage = (event) => {
        try {
          handleEvent(JSON.parse(event.data) as Record<string, unknown>)
        } catch {
          /* ignore malformed provider frames */
        }
      }
      events.onopen = () => setStatus('数据通道已连接')
      const localStream = await navigator.mediaDevices.getUserMedia({
        audio: true,
      })
      stream.current = localStream
      localStream
        .getTracks()
        .forEach((track) => connection.addTrack(track, localStream))
      const offer = await connection.createOffer()
      await connection.setLocalDescription(offer)
      await waitForIceGathering(connection)
      const description = connection.localDescription?.sdp
      if (!description) throw new Error('无法生成 SDP offer')
      const result = shouldRecover
        ? await recoverLiveSession(current.id, description)
        : await createLiveSession(current.id, description)
      setSessionId(result.sessionId)
      await connection.setRemoteDescription({
        type: 'answer',
        sdp: result.transport.answerSdp,
      })
      setStatus('等待 Live session')
    } catch (reason) {
      closePeer()
      setStatus('连接失败')
      setError(reason instanceof Error ? reason.message : 'Live 连接失败')
    }
  }
  const sendText = () => {
    const value = text.trim()
    if (!value || channel.current?.readyState !== 'open') return
    channel.current.send(
      JSON.stringify({
        type: 'session.commentary.append',
        event_id: `typed-live-${crypto.randomUUID()}`,
        delegation_id: null,
        content: value,
      }),
    )
    setTranscript((current) => ({
      ...current,
      items: [...current.items, { role: 'user', text: value }],
    }))
    setText('')
  }
  const close = async () => {
    if (sessionId) {
      try {
        await closeLiveSession(sessionId)
        setStatus('已关闭')
      } catch (reason) {
        setError(reason instanceof Error ? reason.message : '关闭失败')
      }
    }
    closePeer()
  }
  const loadHistory = async () => {
    if (!conversation) return
    const result = await listLiveMessages(conversation.id)
    setTranscript({
      items: result.items.reverse().map((item: LiveMessage) => ({
        role: item.role === 'user' ? 'user' : 'assistant',
        text: item.text,
      })),
      partial: {},
    })
  }
  const visibleTranscript = visibleLiveTranscript(transcript)
  return (
    <section className="live-page">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-3xl font-bold">Live Voice</h1>
          <p className="muted mt-2">通过 Control 建立 WebRTC Live 会话。</p>
        </div>
        <span className="status-pill">{status}</span>
      </div>
      {error && <div className="danger-note mt-4">{error}</div>}
      <div className="live-toolbar mt-6">
        <button
          className="button button-primary"
          onClick={() => void connect()}
          disabled={status === '连接中'}
        >
          {status === '已连接' ? <MicOff size={16} /> : <Mic size={16} />}{' '}
          {conversation ? '重新连接' : '新建会话'}
        </button>
        <button
          className="button"
          onClick={() => void connect(true)}
          disabled={!conversation}
        >
          <RefreshCw size={16} />
          恢复
        </button>
        <button
          className="button"
          onClick={() => void close()}
          disabled={!sessionId}
        >
          <PhoneOff size={16} />
          关闭
        </button>
        <button
          className="button"
          onClick={() => void loadHistory()}
          disabled={!conversation}
        >
          <RefreshCw size={16} />
          加载历史
        </button>
      </div>
      <div className="panel mt-5">
        <div className="live-meta">
          <span>Conversation {conversation?.id ?? '—'}</span>
          <span>Session {sessionId ?? '—'}</span>
        </div>
        <div className="live-transcript">
          {visibleTranscript.length === 0 ? (
            <span className="muted">
              暂无文本消息。连接后可用下方输入框测试。
            </span>
          ) : (
            visibleTranscript.map((item, index) => (
              <div
                className={`live-line live-line-${item.role}`}
                key={`${index}-${item.text}`}
              >
                <strong>{item.role === 'user' ? '你' : 'Live'}</strong>
                <span>{item.text}</span>
              </div>
            ))
          )}
        </div>
        <div className="live-composer">
          <input
            value={text}
            onChange={(event) => setText(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') sendText()
            }}
            placeholder="输入文本测试"
          />
          <button
            className="button button-primary"
            onClick={sendText}
            disabled={!text.trim()}
            aria-label="发送"
          >
            <Send size={16} />
          </button>
        </div>
      </div>
      <audio ref={audio} autoPlay controls className="mt-4 w-full" />
    </section>
  )
}
