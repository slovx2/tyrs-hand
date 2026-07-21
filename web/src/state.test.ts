import { describe, expect, it } from 'vitest'
import { useUI } from './state'

describe('UI state', () => {
  it('切换语言、主题和 CSRF', () => {
    useUI.getState().setLocale('en-US')
    useUI.getState().setTheme('dark')
    useUI.getState().setCSRFToken('csrf')
    expect(useUI.getState()).toMatchObject({
      locale: 'en-US',
      theme: 'dark',
      csrfToken: 'csrf',
    })
  })

  it('创建并关闭不同性质的全局通知', () => {
    const state = useUI.getState()
    const success = state.showToast('success', '保存成功', 60_000)
    state.showToast('error', '保存失败', 60_000)
    state.showToast('warning', '需要确认', 60_000)
    state.showToast('info', '任务已提交', 60_000)

    expect(useUI.getState().toasts.map((toast) => toast.type)).toEqual([
      'success',
      'error',
      'warning',
      'info',
    ])
    state.dismissToast(success)
    expect(useUI.getState().toasts.map((toast) => toast.message)).not.toContain(
      '保存成功',
    )
  })
})
