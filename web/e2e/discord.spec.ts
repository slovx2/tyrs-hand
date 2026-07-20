import { expect, test } from '@playwright/test'

test('管理员配置 Discord、初始化并创建仓库开发 Forum', async ({ page }) => {
  let initializationBody: Record<string, unknown> | undefined
  let forumCreated = false
  let forumBody: Record<string, unknown> | undefined
  let accessBody: Record<string, unknown> | undefined
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
        json: { username: 'admin', expiresAt: '2030-01-01T00:00:00Z' },
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
    if (path === '/api/v1/discord/members') {
      return route.fulfill({
        json: [
          {
            guildId: '123',
            discordUserId: '10',
            username: 'alice',
            displayName: 'Alice',
            bound: true,
            githubLogin: 'alice',
          },
          {
            guildId: '123',
            discordUserId: '20',
            username: 'bob',
            displayName: 'Bob',
            bound: true,
            githubLogin: 'bob',
          },
        ],
      })
    }
    if (path === '/api/v1/repositories') {
      return route.fulfill({
        json: {
          items: [
            {
              id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
              owner: 'datawake-ai',
              name: 'tyrs-hand',
              enabled: true,
            },
          ],
        },
      })
    }
    if (path === '/api/v1/discord/development-environments') {
      return route.fulfill({
        json: [
          {
            id: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
            ownerDiscordUserId: '20',
            ownerName: 'Bob',
            buildRepositoryId: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
            buildRepository: 'datawake-ai/tyrs-hand',
            status: 'running',
            lastUsedAt: '2026-07-21T00:00:00Z',
            forums: [
              {
                id: '11111111-1111-1111-1111-111111111111',
                name: 'bob-dev',
                discordId: '999',
                repositoryId: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
                repository: 'datawake-ai/tyrs-hand',
                status: 'ready',
                branch: 'tyrs-hand/discord/bob',
                dirty: false,
              },
            ],
          },
        ],
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
    if (path === '/api/v1/discord/members/10/forum') {
      forumCreated = true
      forumBody = request.postDataJSON() as Record<string, unknown>
      return route.fulfill({
        status: 202,
        json: { id: '33333333-3333-3333-3333-333333333333' },
      })
    }
    if (path.includes('/discord/forums/') && request.method() === 'PUT') {
      accessBody = request.postDataJSON() as Record<string, unknown>
      return route.fulfill({ status: 204 })
    }
    return route.fulfill({
      status: 404,
      json: { title: 'not mocked', status: 404 },
    })
  })

  await page.goto('/settings/discord')
  await expect(page.getByRole('heading', { name: 'Discord' })).toBeVisible()
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

  await page
    .getByLabel('Alice 开发仓库')
    .selectOption('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa')
  await page.getByLabel('Alice Forum 名称').fill('alice-dev')
  await page.getByRole('button', { name: '创建开发 Forum' }).first().click()
  expect(forumCreated).toBe(true)
  expect(forumBody).toEqual({
    repositoryId: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
    name: 'alice-dev',
  })
  await page.getByLabel('bob-dev 授权成员').selectOption('10')
  await page.getByLabel('bob-dev 权限').selectOption('operator')
  await page.getByRole('button', { name: '授权' }).click()
  expect(accessBody).toEqual({ accessLevel: 'operator' })
})

test('初始化冲突、模式切换和危险确认在移动端保持安全', async ({ page }) => {
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
        json: { username: 'admin', expiresAt: '2030-01-01T00:00:00Z' },
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
    if (path === '/api/v1/discord/members') {
      return route.fulfill({ json: [] })
    }
    if (path === '/api/v1/repositories') {
      return route.fulfill({ json: { items: [] } })
    }
    if (path === '/api/v1/discord/development-environments') {
      return route.fulfill({ json: [] })
    }
    if (path === '/api/v1/discord/initializations/preflight') {
      const body = request.postDataJSON() as { mode: string }
      preflightModes.push(body.mode)
      return route.fulfill({
        json:
          body.mode === 'incremental'
            ? {
                guildId,
                mode: 'incremental',
                creates: [],
                updates: [],
                deletes: [],
                conflicts: [
                  {
                    name: '系统状态',
                    reason: '存在未受管的同名频道',
                  },
                ],
                missingPermissions: [],
                channelCount: 1,
                safe: false,
              }
            : {
                guildId,
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
    return route.fulfill({
      status: 404,
      json: { title: 'not mocked', status: 404 },
    })
  })

  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/settings/discord')
  await page.getByRole('button', { name: '执行预检' }).click()
  await expect(page.getByText('预检存在冲突')).toBeVisible()
  await expect(page.getByText('存在未受管的同名频道')).toBeVisible()

  await page.getByRole('button', { name: '全新初始化' }).click()
  await expect(page.getByText('预检存在冲突')).toHaveCount(0)
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
