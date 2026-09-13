import { Mic, MicOff, PhoneOff, RefreshCw, Send, Upload } from 'lucide-react'
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
import liveAcceptanceAudioUrl from '../assets/live-acceptance.wav?url'

const liveAcceptanceAudioName = 'live-acceptance.wav'
const liveIceConfiguration: RTCConfiguration = {
  // STUN only helps the peers discover candidates. Media remains a direct
  // WebRTC connection between this browser and the Live media endpoint.
  iceServers: [{ urls: 'stun:stun.l.google.com:19302' }],
}

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

type CapturableAudioElement = HTMLAudioElement & {
  captureStream?: () => MediaStream
  mozCaptureStream?: () => MediaStream
}

function shouldUseAcceptanceAudio(): boolean {
  const value = new URLSearchParams(window.location.search).get('acceptanceAudio')
  return value === '1' || value === 'true'
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

  // Start and immediately pause once while the connect button still owns the
  // user gesture. Some browsers expose the captured audio track only after
  // playback has started; reset the position so negotiation cannot consume
  // the recording before the SDP answer arrives.
  await element.play()
  element.pause()
  element.currentTime = 0
}

export function LivePage() {
  const [conversation, setConversation] = useState<LiveConversation | null>(
    null,
  )
  const [sessionId, setSessionId] = useState<string>()
  const [sessionStatus, setSessionStatus] = useState('')
  const [transcript, setTranscript] = useState<LiveTranscriptState>(
    initialLiveTranscriptState,
  )
  const [text, setText] = useState('')
  const [status, setStatus] = useState('未连接')
  const [error, setError] = useState('')
  const peer = useRef<RTCPeerConnection | null>(null)
  const channel = useRef<RTCDataChannel | null>(null)
  const audio = useRef<HTMLAudioElement>(null)
  const recording = useRef<HTMLAudioElement>(null)
  const recordingObjectUrl = useRef<string | undefined>(undefined)
  const stream = useRef<MediaStream | null>(null)
  const [recordingUrl, setRecordingUrl] = useState('')
  const [recordingName, setRecordingName] = useState('')

  const loadAcceptanceAudio = () => {
    if (recordingObjectUrl.current) {
      URL.revokeObjectURL(recordingObjectUrl.current)
      recordingObjectUrl.current = undefined
    }
    setRecordingUrl(liveAcceptanceAudioUrl)
    setRecordingName(liveAcceptanceAudioName)
    setError('')
  }

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
      if (recordingObjectUrl.current)
        URL.revokeObjectURL(recordingObjectUrl.current)
    },
    [],
  )
  useEffect(() => {
    if (!recordingUrl || !recording.current) return
    recording.current.load()
  }, [recordingUrl])
  useEffect(() => {
    if (!shouldUseAcceptanceAudio()) return
    if (recordingObjectUrl.current) {
      URL.revokeObjectURL(recordingObjectUrl.current)
      recordingObjectUrl.current = undefined
    }
    setRecordingUrl(liveAcceptanceAudioUrl)
    setRecordingName(liveAcceptanceAudioName)
  }, [])
  const handleEvent = (event: Record<string, unknown>) => {
    const type = String(event.type ?? '')
    if (type === 'session.started') {
      setStatus('已连接')
      setSessionStatus('active')
    }
    if (type === 'session.closed') {
      setStatus('已关闭')
      setSessionStatus('closed')
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
  const connect = async (recover = false) => {
    setError('')
    setStatus('连接中')
    try {
      const shouldRecover =
        recover ||
        (Boolean(sessionId) &&
          sessionStatus !== 'closed' &&
          sessionStatus !== 'failed')
      let current = conversation
      closePeer()
      const recordingElement = recordingUrl ? recording.current : null
      if (recordingElement) await primeAudioCapture(recordingElement)
      if (!current) {
        current = await createLiveConversation({})
        setConversation(current)
      }
      const connection = new RTCPeerConnection(liveIceConfiguration)
      peer.current = connection
      connection.onconnectionstatechange = () => {
        if (
          connection.connectionState === 'disconnected' ||
          connection.connectionState === 'failed'
        ) {
          setStatus('已断开，可恢复')
          setSessionStatus('sideband_disconnected')
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
      events.onopen = () => setStatus('数据通道已连接')
      if (recordingUrl) {
        const localRecording = recordingElement
        if (!localRecording) throw new Error('本地录音播放器尚未就绪')
        const localStream = captureAudioElement(localRecording)
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
          setError('未获得麦克风权限，将继续使用文本测试通道')
        }
      }
      if (stream.current === null)
        connection.addTransceiver('audio', { direction: 'recvonly' })
      const offer = await connection.createOffer()
      await connection.setLocalDescription(offer)
      await waitForIceGathering(connection)
      const description = connection.localDescription?.sdp
      if (!description) throw new Error('无法生成 SDP offer')
      if (!/^a=candidate:/m.test(description))
        throw new Error('无法生成包含 ICE candidate 的 SDP offer')
      const result = shouldRecover
        ? await recoverLiveSession(current.id, description)
        : await createLiveSession(current.id, description)
      setSessionId(result.sessionId)
      setSessionStatus(result.session.status)
      await connection.setRemoteDescription({
        type: 'answer',
        sdp: result.transport.answerSdp,
      })
      if (recordingElement) {
        recordingElement.currentTime = 0
        await recordingElement.play().catch(() => {
          setError('WebRTC 已连接，请点击录音播放器的播放按钮开始发送本地录音')
        })
      }
      setStatus('等待 Live session')
    } catch (reason) {
      closePeer()
      setStatus('连接失败')
      setError(reason instanceof Error ? reason.message : 'Live 连接失败')
    }
  }
  const selectRecording = (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    if (!file) return
    const isAudio =
      file.type.startsWith('audio/') ||
      /\.(aac|m4a|mp3|ogg|wav|webm)$/i.test(file.name)
    if (!isAudio) {
      setError('请选择音频录音文件')
      event.target.value = ''
      return
    }
    if (recordingObjectUrl.current)
      URL.revokeObjectURL(recordingObjectUrl.current)
    const url = URL.createObjectURL(file)
    recordingObjectUrl.current = url
    setRecordingUrl(url)
    setRecordingName(file.name)
    setError('')
  }
  const sendText = () => {
    const value = text.trim()
    if (!value || channel.current?.readyState !== 'open') return
    const eventId = `typed-live-${crypto.randomUUID()}`
    channel.current.send(
      JSON.stringify({
        type: 'session.context.append',
        event_id: eventId,
          channel: 'speakable',
        content: [{ type: 'input_text', text: value }],
      }),
    )
    setText('')
  }
  const close = async () => {
    if (sessionId) {
      try {
        await closeLiveSession(sessionId)
        setStatus('已关闭')
        setSessionStatus('closed')
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
      seenEventIds: {},
      finalized: {},
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
      <div className="live-recording mt-4">
        <label className="button">
          <Upload size={16} />
          <span>{recordingName ? '更换本地录音' : '选择本地录音'}</span>
          <input
            className="sr-only"
            type="file"
            accept="audio/*,.m4a"
            onChange={selectRecording}
          />
        </label>
        <button className="button" type="button" onClick={loadAcceptanceAudio}>
          使用验收录音
        </button>
        {recordingName && (
          <span className="muted" title="录音仅作为本地 WebRTC 音频轨道输入">
            {recordingName}
          </span>
        )}
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
      {recordingUrl && (
        <audio
          ref={recording}
          src={recordingUrl}
          preload="auto"
          controls
          className="mt-3 w-full"
        />
      )}
      <audio ref={audio} autoPlay controls className="mt-4 w-full" />
    </section>
  )
}
