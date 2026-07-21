import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { useUI } from '../state'
import { ToastViewport } from './ToastViewport'

afterEach(cleanup)

describe('ToastViewport', () => {
  it('在右上角显示不同颜色语义并允许关闭', () => {
    const { showToast } = useUI.getState()
    showToast('success', '保存成功', 60_000)
    showToast('error', '保存失败', 60_000)
    showToast('warning', '请先确认', 60_000)
    showToast('info', '任务已提交', 60_000)

    const { container } = render(<ToastViewport />)

    expect(container.querySelector('.toast-success')).toHaveTextContent(
      '保存成功',
    )
    expect(container.querySelector('.toast-error')).toHaveTextContent(
      '保存失败',
    )
    expect(container.querySelector('.toast-warning')).toHaveTextContent(
      '请先确认',
    )
    expect(container.querySelector('.toast-info')).toHaveTextContent(
      '任务已提交',
    )

    fireEvent.click(screen.getByRole('button', { name: '关闭成功通知' }))
    expect(screen.queryByText('保存成功')).not.toBeInTheDocument()
  })
})
