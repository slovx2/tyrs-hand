import { expect, test } from '@playwright/test'

test('管理员配置 Discord 并执行 Server 初始化', async ({ page }) => {
  let initializationBody: Record<string, unknown> | undefined
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/setup/status') {
      return route.fulfill({
        json: { setupRequired: false, githubConfigured: true },
      })
    }
    if (path === '/api/v1/auth/me') {
      return route.fulfill({
        json: {
          username: 'admin',
          csrfToken: 'test-csrf',
          expiresAt: '2030-01-01T00:00:00Z',
        },
      })
    }
    if (path === '/api/v1/settings/discord' && request.method() === 'GET') {
      return route.fulfill({
        json: {
          guildId: '123',
          enabled: false,
          communityEnabled: true,
          applicationId: '456',
          botUserId: '789',
          tokenConfigured: true,
        },
      })
    }
    if (path === '/api/v1/settings/discord' && request.method() === 'PUT') {
      return route.fulfill({ status: 204 })
    }
    if (path === '/api/v1/discord/status') {
      return route.fulfill({
        json: {
          configured: true,
          enabled: false,
          gatewayStatus: 'disabled',
          pendingOutbox: 0,
          failedOutbox: 0,
          pendingInitializationOperations: 0,
        },
      })
    }
    if (path === '/api/v1/discord/initializations/preflight') {
      return route.fulfill({
        json: {
          guildId: '123',
          mode: 'fresh',
          creates: ['系统'],
          updates: [],
          deletes: ['旧频道'],
          conflicts: [],
          missingPermissions: [],
          channelCount: 1,
          safe: true,
        },
      })
    }
    if (path === '/api/v1/discord/initializations') {
      initializationBody = request.postDataJSON() as Record<string, unknown>
      return route.fulfill({
        status: 202,
        json: { id: '22222222-2222-2222-2222-222222222222' },
      })
    }
    return route.fulfill({
      status: 404,
      json: { title: 'not mocked', status: 404 },
    })
  })

  await page.goto('/settings/discord')
  await expect(page.getByRole('heading', { name: 'Discord' })).toBeVisible()
  await expect(page.getByText('普通项目')).toHaveCount(0)
  await expect(page.getByText('长期开发环境')).toHaveCount(0)
  await page.getByLabel('启用 Discord 常驻服务').check()
  await page.getByRole('button', { name: '保存 Discord 设置' }).click()

  await page.getByRole('button', { name: '全新初始化' }).click()
  await page.getByLabel(/输入确认指令/).fill('DELETE ALL CHANNELS 123')
  await page.getByRole('button', { name: '执行预检' }).click()
  await expect(page.getByText('预检通过')).toBeVisible()
  await page.getByRole('button', { name: '开始初始化' }).click()
  await expect(page.getByText(/初始化操作已创建/)).toBeVisible()
  expect(initializationBody).toEqual({
    mode: 'fresh',
    confirmation: 'DELETE ALL CHANNELS 123',
  })
})

test('初始化冲突和危险确认在移动端保持安全', async ({ page }) => {
  const guildId = '123456789012345678'
  const preflightModes: string[] = []
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/setup/status') {
      return route.fulfill({
        json: { setupRequired: false, githubConfigured: true },
      })
    }
    if (path === '/api/v1/auth/me') {
      return route.fulfill({
        json: {
          username: 'admin',
          csrfToken: 'test-csrf',
          expiresAt: '2030-01-01T00:00:00Z',
        },
      })
    }
    if (path === '/api/v1/settings/discord') {
      return route.fulfill({
        json: {
          guildId,
          enabled: true,
          communityEnabled: true,
          applicationId: '456',
          botUserId: '789',
          tokenConfigured: true,
        },
      })
    }
    if (path === '/api/v1/discord/status') {
      return route.fulfill({
        json: {
          configured: true,
          enabled: true,
          gatewayStatus: 'connected',
          pendingOutbox: 0,
          failedOutbox: 0,
          pendingInitializationOperations: 0,
        },
      })
    }
    if (path === '/api/v1/discord/initializations/preflight') {
      const body = request.postDataJSON() as { mode: string }
      preflightModes.push(body.mode)
      const safe = body.mode === 'fresh'
      return route.fulfill({
        json: {
          guildId,
          mode: body.mode,
          creates: safe ? ['系统'] : [],
          updates: [],
          deletes: safe ? ['旧频道'] : [],
          conflicts: safe
            ? []
            : [{ name: '系统状态', reason: '存在未受管的同名频道' }],
          missingPermissions: [],
          channelCount: 1,
          safe,
        },
      })
    }
    return route.fulfill({
      status: 404,
      json: { title: 'not mocked', status: 404 },
    })
  })

  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/settings/discord')
  await page.getByRole('button', { name: '执行预检' }).click()
  await expect(page.getByText('存在未受管的同名频道')).toBeVisible()

  await page.getByRole('button', { name: '全新初始化' }).click()
  await page.getByLabel(/输入确认指令/).fill('DELETE ALL CHANNELS wrong')
  await page.getByRole('button', { name: '执行预检' }).click()
  await expect(page.getByText('预检通过')).toBeVisible()
  await expect(page.getByRole('button', { name: '开始初始化' })).toBeDisabled()

  await page.getByLabel(/输入确认指令/).fill(`DELETE ALL CHANNELS ${guildId}`)
  await expect(page.getByRole('button', { name: '开始初始化' })).toBeEnabled()
  expect(preflightModes).toEqual(['incremental', 'fresh'])
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true)
})
