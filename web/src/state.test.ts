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
})
