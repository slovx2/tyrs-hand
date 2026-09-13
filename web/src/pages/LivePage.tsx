import { useEffect, useRef, useState } from 'react'
import {
  closeLiveSession,
  createLiveConversation,
  createLiveSession,
  getLiveConversation,
  listLiveMessages,
  recoverLiveSession,
  type LiveConversation,
  type LiveMessage,
} from '../api/live'
import {
  initialLiveTranscriptState,
  reduceLiveTranscript,
  visibleLiveTranscript,
  type LiveTranscriptState,
} from '../features/live/transcriptReducer'
import liveAcceptanceAudioUrl from '../assets/live-acceptance.wav?url'

const liveConversationStorageKey = 'tyrs-hand.live.conversationId'
const liveIceConfiguration: RTCConfiguration = {
  iceServers: [{ urls: 'stun:stun.l.google.com:19302' }],
}

type CapturableAudioElement = HTMLAudioElement & {
  captureStream?: () => MediaStream
  mozCaptureStream?: () => MediaStream
}

function shouldUseAcceptanceAudio(): boolean {
  const value = new URLSearchParams(window.location.search).get(
    'acceptanceAudio',
  )
  return value === '1' || value === 'true'
}

function readStoredConversationId(): string | null {
  return window.localStorage.getItem(liveConversationStorageKey)?.trim() || null
}

function writeStoredConversationId(id: string | null): void {
  if (id) window.localStorage.setItem(liveConversationStorageKey, id)
  else window.localStorage.removeItem(liveConversationStorageKey)
}

function transcriptFromMessages(items: LiveMessage[]): LiveTranscriptState {
  return {
    items: [...items].reverse().map((item) => ({
      role: item.role === 'user' ? 'user' : 'assistant',
      text: item.text,
    })),
    partial: {},
    seenEventIds: {},
    finalized: {},
  }
}

async function waitForIceGathering(connection: RTCPeerConnection): Promise<void> {
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

function captureAudioElement(element: HTMLAudioElement): MediaStream {
  const capturable = element as CapturableAudioElement
  const capture = capturable.captureStream ?? capturable.mozCaptureStream
  if (!capture) throw new Error('当前浏览器不支持本地录音 WebRTC 输入')
  return capture.call(element)
}

async function primeAudioCapture(element: HTMLAudioElement): Promise<void> {
  if (element.readyState < HTMLMediaElement.HAVE_CURRENT_DATA) {
    await new Promise<void>((resolve, reject) => {
      const timeout = window.setTimeout(() => {
        cleanup()
        reject(new Error('本地录音尚未准备好'))
      }, 5000)
      const cleanup = () => {
        window.clearTimeout(timeout)
        element.removeEventListener('canplay', onReady)
        element.removeEventListener('error', onError)
      }
      const onReady = () => {
        cleanup()
        resolve()
      }
      const onError = () => {
        cleanup()
        reject(new Error('无法读取本地录音'))
      }
      element.addEventListener('canplay', onReady, { once: true })
      element.addEventListener('error', onError, { once: true })
    })
  }
  await element.play()
  element.pause()
  element.currentTime = 0
}

function LiveMark({ connected }: { connected: boolean }) {
  return (
    <svg
      className={`live-mark${connected ? ' is-connected' : ''}`}
      viewBox="0 0 120 120"
      aria-hidden="true"
    >
      <circle className="live-ring live-ring-outer" cx="60" cy="60" r="50" />
      <circle className="live-ring live-ring-mid" cx="60" cy="60" r="36" />
      <circle className="live-core" cx="60" cy="60" r="22" />
      <circle className="live-ring live-ring-inner" cx="60" cy="60" r="22" />
      <path className="live-wave" d="M38 60q8-16 16 0t16 0t16 0" />
    </svg>
  )
}

export function LivePage() {
  const [conversation, setConversation] = useState<LiveConversation | null>(
    null,
  )
  const [sessionId, setSessionId] = useState<string>()
  const [transcript, setTranscript] = useState<LiveTranscriptState>(
    initialLiveTranscriptState,
  )
  const [status, setStatus] = useState('未连接')
  const [error, setError] = useState('')
  const [menuOpen, setMenuOpen] = useState(false)
  const peer = useRef<RTCPeerConnection | null>(null)
  const channel = useRef<RTCDataChannel | null>(null)
  const audio = useRef<HTMLAudioElement>(null)
  const recording = useRef<HTMLAudioElement>(null)
  const stream = useRef<MediaStream | null>(null)
  const menu = useRef<HTMLDivElement>(null)
  const resetPending = useRef(false)
  const acceptanceAudio = shouldUseAcceptanceAudio()

  const closePeer = () => {
    channel.current?.close()
    channel.current = null
    recording.current?.pause()
    stream.current?.getTracks().forEach((track) => track.stop())
    stream.current = null
    peer.current?.close()
    peer.current = null
  }

  useEffect(
    () => () => {
      closePeer()
    },
    [],
  )
  useEffect(() => {
    if (!menuOpen) return
    const onPointer = (event: PointerEvent) => {
      if (!(event.target instanceof Node)) return
      if (!menu.current?.contains(event.target)) setMenuOpen(false)
    }
    document.addEventListener('pointerdown', onPointer)
    return () => document.removeEventListener('pointerdown', onPointer)
  }, [menuOpen])
  useEffect(() => {
    const id = readStoredConversationId()
    if (!id) return
    let cancelled = false
    void (async () => {
      try {
        const current = await getLiveConversation(id)
        const history = await listLiveMessages(id)
        if (cancelled) return
        setConversation(current)
        setTranscript(transcriptFromMessages(history.items))
      } catch {
        if (!cancelled) writeStoredConversationId(null)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  const handleEvent = (event: Record<string, unknown>) => {
    const type = String(event.type ?? '')
    if (type === 'session.started') {
      setStatus('已连接')
    }
    if (type === 'session.closed') {
      setStatus('未连接')
      setSessionId(undefined)
    }
    setTranscript((current) => reduceLiveTranscript(current, event))
    if (type === 'error')
      setError(
        String(
          (event.error as { message?: string } | undefined)?.message ??
            'Live 服务返回错误',
        ),
      )
  }

  const connect = async () => {
    setError('')
    setStatus('连接中')
    try {
      const shouldRecover = Boolean(conversation) && !resetPending.current
      let current = conversation
      closePeer()
      const recordingElement = acceptanceAudio ? recording.current : null
      if (recordingElement) await primeAudioCapture(recordingElement)
      if (!current) {
        current = await createLiveConversation({})
        setConversation(current)
        writeStoredConversationId(current.id)
      }
      const connection = new RTCPeerConnection(liveIceConfiguration)
      peer.current = connection
      connection.onconnectionstatechange = () => {
        if (
          connection.connectionState === 'disconnected' ||
          connection.connectionState === 'failed'
        ) {
          setStatus('未连接')
        }
      }
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
      events.onopen = () => setStatus('连接中')
      if (recordingElement) {
        const localStream = captureAudioElement(recordingElement)
        if (localStream.getAudioTracks().length === 0)
          throw new Error('本地录音没有可用的音频轨道')
        stream.current = localStream
        localStream
          .getAudioTracks()
          .forEach((track) => connection.addTrack(track, localStream))
      } else {
        try {
          const localStream = await navigator.mediaDevices.getUserMedia({
            audio: true,
          })
          stream.current = localStream
          localStream
            .getTracks()
            .forEach((track) => connection.addTrack(track, localStream))
        } catch {
          setError('未获得麦克风权限')
        }
      }
      if (stream.current === null)
        connection.addTransceiver('audio', { direction: 'recvonly' })
      const offer = await connection.createOffer()
      await connection.setLocalDescription(offer)
      await waitForIceGathering(connection)
      const description = connection.localDescription?.sdp
      if (!description) throw new Error('无法生成 SDP offer')
      const result = shouldRecover
        ? await recoverLiveSession(current.id, description)
        : await createLiveSession(current.id, description)
      resetPending.current = false
      setSessionId(result.sessionId)
      await connection.setRemoteDescription({
        type: 'answer',
        sdp: result.transport.answerSdp,
      })
      if (recordingElement) {
        recordingElement.currentTime = 0
        await recordingElement.play().catch(() => {
          setError('WebRTC 已连接，但验收录音未能自动播放')
        })
      }
      setStatus('连接中')
    } catch (reason) {
      closePeer()
      setStatus('未连接')
      setError(reason instanceof Error ? reason.message : 'Live 连接失败')
    }
  }

  const disconnect = async () => {
    if (sessionId) {
      try {
        await closeLiveSession(sessionId)
      } catch (reason) {
        setError(reason instanceof Error ? reason.message : '关闭失败')
      }
    }
    setSessionId(undefined)
    setStatus('未连接')
    closePeer()
  }

  const resetSession = async () => {
    await disconnect()
    resetPending.current = true
  }

  const clearCaptions = async () => {
    await disconnect()
    resetPending.current = false
    setConversation(null)
    setTranscript(initialLiveTranscriptState)
    writeStoredConversationId(null)
  }

  const visibleTranscript = visibleLiveTranscript(transcript)
  const connected = status === '已连接'
  const connecting = status === '连接中'
  return (
    <section className="live-page">
      <div className="live-head">
        <h1>Live 语音</h1>
        <div className="live-more" ref={menu}>
          <button
            className="live-more-button"
            type="button"
            aria-label="更多"
            aria-expanded={menuOpen}
            onClick={() => setMenuOpen((open) => !open)}
          >
            ⋯
          </button>
          {menuOpen && (
            <div className="live-menu" role="menu">
              <button
                type="button"
                role="menuitem"
                onClick={() => {
                  setMenuOpen(false)
                  void resetSession()
                }}
              >
                重置会话
              </button>
              <button
                type="button"
                role="menuitem"
                onClick={() => {
                  setMenuOpen(false)
                  void clearCaptions()
                }}
              >
                清空字幕
              </button>
            </div>
          )}
        </div>
      </div>
      {error && <div className="danger-note">{error}</div>}
      <div className="live-transcript">
        {visibleTranscript.length === 0 ? (
          <span className="muted">连接后开始说话</span>
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
      <div className="live-dock">
        <LiveMark connected={connected} />
        <div className="live-dock-copy">
          <strong>{connected ? '已连接' : connecting ? '连接中' : '未连接'}</strong>
          <span>{connected ? '正在听' : connecting ? '正在建立会话' : '点击连接开始'}</span>
        </div>
        {connected ? (
          <button className="button" type="button" onClick={() => void disconnect()}>
            断开
          </button>
        ) : (
          <button
            className="button"
            type="button"
            onClick={() => void connect()}
            disabled={connecting}
          >
            连接
          </button>
        )}
      </div>
      {acceptanceAudio && (
        <audio
          ref={recording}
          src={liveAcceptanceAudioUrl}
          preload="auto"
          className="sr-only"
        />
      )}
      <audio ref={audio} autoPlay className="sr-only" />
    </section>
  )
}
