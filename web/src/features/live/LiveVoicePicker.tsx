import {
  ChevronLeft,
  ChevronRight,
  Pause,
  Play,
  RotateCcw,
  X,
} from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { findLiveVoice, liveVoices, type LiveVoice } from './voices'

type PreviewStatus = 'idle' | 'playing' | 'error'

export function LiveVoicePicker({
  value,
  onChange,
}: {
  value: LiveVoice
  onChange: (value: LiveVoice) => void
}) {
  const [open, setOpen] = useState(false)
  const [previewStatus, setPreviewStatus] = useState<PreviewStatus>('idle')
  const audio = useRef<HTMLAudioElement>(null)
  const selected = findLiveVoice(value)
  const selectedIndex = liveVoices.findIndex(
    (voice) => voice.slug === selected.slug,
  )

  const stopPreview = () => {
    const element = audio.current
    if (element) {
      element.pause()
      element.currentTime = 0
    }
    setPreviewStatus('idle')
  }

  useEffect(() => {
    const element = audio.current
    return () => {
      element?.pause()
      if (element) element.currentTime = 0
    }
  }, [selected.slug])

  useEffect(() => {
    if (!open) return
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        audio.current?.pause()
        if (audio.current) audio.current.currentTime = 0
        setPreviewStatus('idle')
        setOpen(false)
        return
      }
      let nextIndex: number | undefined
      if (event.key === 'ArrowLeft' || event.key === 'ArrowUp')
        nextIndex = selectedIndex - 1
      if (event.key === 'ArrowRight' || event.key === 'ArrowDown')
        nextIndex = selectedIndex + 1
      if (event.key === 'Home') nextIndex = 0
      if (event.key === 'End') nextIndex = liveVoices.length - 1
      if (nextIndex === undefined) return
      event.preventDefault()
      const next =
        liveVoices[(nextIndex + liveVoices.length) % liveVoices.length]
      if (!next || next.slug === selected.slug) return
      audio.current?.pause()
      if (audio.current) audio.current.currentTime = 0
      setPreviewStatus('idle')
      onChange(next.slug)
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onChange, open, selected.slug, selectedIndex])

  const preview = () => {
    const element = audio.current
    if (!element) return
    if (previewStatus === 'playing') {
      element.pause()
      return
    }
    if (previewStatus === 'error' || element.ended) element.currentTime = 0
    if (typeof element.play !== 'function') {
      setPreviewStatus('error')
      return
    }
    void element.play().catch(() => setPreviewStatus('error'))
  }

  const choose = (index: number) => {
    const next = liveVoices[index]
    if (!next || next.slug === selected.slug) return
    stopPreview()
    onChange(next.slug)
  }
  const closePicker = () => {
    stopPreview()
    setOpen(false)
  }

  const previewLabel =
    previewStatus === 'playing'
      ? `暂停 ${selected.name} 试听`
      : previewStatus === 'error'
        ? `重试 ${selected.name} 试听`
        : `播放 ${selected.name} 试听`

  return (
    <>
      <button
        className="live-voice-trigger"
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={`音色：${selected.name}`}
        onClick={() => setOpen(true)}
      >
        <span className="live-picker-copy">
          <span>音色</span>
          <strong>{selected.name}</strong>
        </span>
        <ChevronRight aria-hidden size={17} />
      </button>
      {open && (
        <div
          className="live-voice-overlay"
          role="presentation"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) closePicker()
          }}
        >
          <div
            className="live-voice-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="live-voice-dialog-title"
            onMouseDown={(event) => event.stopPropagation()}
          >
            <div className="live-voice-dialog-head">
              <h2 id="live-voice-dialog-title">选择音色</h2>
              <button
                className="live-dialog-close"
                type="button"
                aria-label="关闭音色选择"
                onClick={closePicker}
              >
                <X aria-hidden size={18} />
              </button>
            </div>
            <div className="live-voice-preview">
              <button
                className="live-voice-arrow"
                type="button"
                aria-label="上一个音色"
                onClick={() =>
                  choose(
                    selectedIndex - 1 < 0
                      ? liveVoices.length - 1
                      : selectedIndex - 1,
                  )
                }
              >
                <ChevronLeft aria-hidden size={20} />
              </button>
              <div className="live-voice-preview-main">
                <button
                  className={`live-voice-orb${previewStatus === 'playing' ? ' is-playing' : ''}`}
                  type="button"
                  aria-label={previewLabel}
                  onClick={preview}
                >
                  {previewStatus === 'playing' ? (
                    <Pause aria-hidden size={25} />
                  ) : previewStatus === 'error' ? (
                    <RotateCcw aria-hidden size={25} />
                  ) : (
                    <Play aria-hidden size={25} />
                  )}
                </button>
                <strong>{selected.name}</strong>
                <span>{selected.description}</span>
                {previewStatus === 'error' && (
                  <small role="alert">试听暂时不可用</small>
                )}
              </div>
              <button
                className="live-voice-arrow"
                type="button"
                aria-label="下一个音色"
                onClick={() => choose((selectedIndex + 1) % liveVoices.length)}
              >
                <ChevronRight aria-hidden size={20} />
              </button>
            </div>
            <div
              className="live-voice-dots"
              role="radiogroup"
              aria-label="音色"
            >
              {liveVoices.map((voice) => (
                <button
                  key={voice.slug}
                  className={`live-voice-dot${voice.slug === selected.slug ? ' is-selected' : ''}`}
                  type="button"
                  role="radio"
                  aria-checked={voice.slug === selected.slug}
                  aria-label={`${voice.name}：${voice.description}`}
                  onClick={() => choose(liveVoices.indexOf(voice))}
                />
              ))}
            </div>
            <audio
              ref={audio}
              src={selected.previewUrl}
              preload="auto"
              className="sr-only"
              onPlay={() => setPreviewStatus('playing')}
              onPause={() => setPreviewStatus('idle')}
              onEnded={() => setPreviewStatus('idle')}
              onError={() => setPreviewStatus('error')}
            />
          </div>
        </div>
      )}
    </>
  )
}
