import { describe, expect, it } from 'vitest'
import { t } from './i18n'

describe('i18n', () => {
  it('在中英文之间稳定映射管理菜单', () => {
    expect(t('zh-CN', 'repositories')).toBe('仓库')
    expect(t('en-US', 'repositories')).toBe('Repositories')
  })
})
