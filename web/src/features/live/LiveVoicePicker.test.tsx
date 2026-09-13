import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useState } from 'react'
import { LiveVoicePicker } from './LiveVoicePicker'
import { defaultLiveVoice, type LiveVoice } from './voices'

function PickerHarness({ initial = defaultLiveVoice }: { initial?: LiveVoice }) {
  const [value, setValue] = useState<LiveVoice>(initial)
  return <LiveVoicePicker value={value} onChange={setValue} />
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('LiveVoicePicker', () => {
  it('支持播放、暂停、重播，并在切换音色时停止预览', () => {
    const play = vi
      .spyOn(HTMLMediaElement.prototype, 'play')
      .mockResolvedValue(undefined)
    const pause = vi
      .spyOn(HTMLMediaElement.prototype, 'pause')
      .mockImplementation(() => undefined)
    render(<PickerHarness />)

    fireEvent.click(screen.getByRole('button', { name: '音色：Cove' }))
    const dialog = screen.getByRole('dialog', { name: '选择音色' })
    const audio = dialog.querySelector('audio') as HTMLAudioElement
    fireEvent.click(screen.getByRole('button', { name: '播放 Cove 试听' }))
    fireEvent.play(audio)
    expect(screen.getByRole('button', { name: '暂停 Cove 试听' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '暂停 Cove 试听' }))
    fireEvent.pause(audio)
    expect(pause).toHaveBeenCalled()
    Object.defineProperty(audio, 'ended', { configurable: true, value: true })
    fireEvent.click(screen.getByRole('button', { name: '播放 Cove 试听' }))
    expect(play).toHaveBeenCalledTimes(2)

    fireEvent.play(audio)
    fireEvent.click(screen.getByRole('radio', { name: 'Breeze：活泼真挚' }))
    expect(screen.getByRole('button', { name: '音色：Breeze' })).toBeInTheDocument()
    expect(pause).toHaveBeenCalled()
  })

  it('展示试听失败并支持重试，关闭和卸载时释放音频', async () => {
    const play = vi
      .spyOn(HTMLMediaElement.prototype, 'play')
      .mockRejectedValueOnce(new Error('audio unavailable'))
      .mockResolvedValue(undefined)
    const pause = vi
      .spyOn(HTMLMediaElement.prototype, 'pause')
      .mockImplementation(() => undefined)
    const { unmount } = render(<PickerHarness />)

    fireEvent.click(screen.getByRole('button', { name: '音色：Cove' }))
    const audio = screen.getByRole('dialog').querySelector('audio') as HTMLAudioElement
    fireEvent.click(screen.getByRole('button', { name: '播放 Cove 试听' }))
    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent('试听暂时不可用'),
    )
    fireEvent.click(screen.getByRole('button', { name: '重试 Cove 试听' }))
    fireEvent.play(audio)
    expect(play).toHaveBeenCalledTimes(2)
    fireEvent.error(audio)
    expect(screen.getByRole('alert')).toHaveTextContent('试听暂时不可用')

    fireEvent.click(screen.getByRole('button', { name: '关闭音色选择' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '音色：Cove' }))
    unmount()
    expect(pause).toHaveBeenCalled()
  })

  it('支持音色键盘导航、点位选择和遮罩关闭', () => {
    render(<PickerHarness />)
    fireEvent.click(screen.getByRole('button', { name: '音色：Cove' }))
    const overlay = screen.getByRole('presentation')
    const dialog = screen.getByRole('dialog')
    const dialogQueries = within(dialog)
    fireEvent.mouseDown(dialog)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'ArrowRight' })
    expect(dialogQueries.getByText('Ember')).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'ArrowLeft' })
    expect(dialogQueries.getByText('Cove')).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'Home' })
    expect(dialogQueries.getByText('Arbor')).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'End' })
    expect(dialogQueries.getByText('Vale')).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'End' })
    fireEvent.keyDown(document, { key: 'ArrowDown' })
    expect(dialogQueries.getByText('Arbor')).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'x' })
    fireEvent.mouseDown(overlay)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '音色：Arbor' }))
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})
