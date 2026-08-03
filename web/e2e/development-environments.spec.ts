import { expect, test, type Page } from '@playwright/test'

const environment = {
  id: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
  ownerDiscordUserId: '20',
  ownerName: 'Bob',
  status: 'running',
  imageRef: 'registry.example.invalid/development@sha256:aaaaaaaa',
  runtimeUser: 'developer',
  codexVersion: 'codex-cli 1.0.0',
  codexUserOverride: false,
  lastUsedAt: '2026-07-21T00:00:00Z',
  sshConfigRevision: 0,
  sshAppliedRevision: 0,
  daemonStatus: 'running',
  appServerStatus: 'running',
  sshStatus: 'disabled',
  hubStatus: 'running',
  projectsScannedAt: '2026-07-27T10:00:00Z',
  projects: [
    {
      id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      name: 'atlas',
      relativePath: 'workspaces/atlas',
      projectKind: 'git',
      availabilityStatus: 'available',
      branch: 'main',
      headSha: '0123456789abcdef',
      dirty: true,
      remoteUrl:
        'https://example.invalid/team/a-very-long-atlas-repository-name.git',
      lastSeenAt: '2026-07-27T10:00:00Z',
      forums: [],
    },
    {
      id: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
      name: 'notes',
      relativePath: 'workspaces/notes',
      projectKind: 'directory',
      availabilityStatus: 'available',
      dirty: false,
      lastSeenAt: '2026-07-27T10:00:00Z',
      forums: [
        {
          id: '22222222-2222-2222-2222-222222222222',
          name: 'bob-notes-old',
          discordId: '902',
          bindingStatus: 'inactive',
          collaborators: [],
        },
      ],
    },
    {
      id: 'cccccccc-cccc-cccc-cccc-cccccccccccc',
      name: 'archive',
      relativePath: 'workspaces/archive',
      projectKind: 'git',
      availabilityStatus: 'missing',
      dirty: false,
      lastSeenAt: '2026-07-20T10:00:00Z',
      forums: [
        {
          id: '33333333-3333-3333-3333-333333333333',
          name: 'bob-archive-old',
          discordId: '903',
          bindingStatus: 'inactive',
          collaborators: [],
        },
      ],
    },
  ],
}

async function mockAPI(page: Page) {
  const calls: Array<{ path: string; body?: unknown }> = []
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
    if (path === '/api/v1/development-environments') {
      if (request.method() === 'GET') {
        return route.fulfill({ json: { items: [environment] } })
      }
      calls.push({ path, body: request.postDataJSON() })
      return route.fulfill({
        status: 202,
        json: {
          id: 'dddddddd-dddd-dddd-dddd-dddddddddddd',
          operationId: 'ffffffff-ffff-ffff-ffff-ffffffffffff',
        },
      })
    }
    if (path === '/api/v1/discord/members') {
      return route.fulfill({
        json: [
          {
            guildId: 'guild',
            discordUserId: '10',
            username: 'alice',
            displayName: 'Alice',
            bound: false,
          },
          {
            guildId: 'guild',
            discordUserId: '20',
            username: 'bob',
            displayName: 'Bob',
            bound: false,
          },
        ],
      })
    }
    if (request.method() !== 'GET') {
      calls.push({
        path,
        body: request.postData() ? request.postDataJSON() : undefined,
      })
      return route.fulfill({ status: request.method() === 'PUT' ? 204 : 202 })
    }
    return route.fulfill({
      status: 404,
      json: { title: 'not mocked', status: 404 },
    })
  })
  return calls
}

test('按长期环境管理项目与 Forum 配对', async ({ page }) => {
  const calls = await mockAPI(page)
  await page.goto('/development-environments')

  await expect(page.getByRole('heading', { name: '开发环境' })).toBeVisible()
  await expect(page.getByText('workspaces/atlas')).toBeVisible()
  await expect(page.getByText('普通目录')).toBeVisible()
  await expect(page.getByText('缺失')).toBeVisible()
  await expect(page.getByLabel('普通项目名称')).toHaveCount(0)

  await page.getByRole('button', { name: '创建环境' }).click()
  await expect(
    page.getByLabel('Discord 成员').getByRole('option', { name: 'Bob' }),
  ).toHaveCount(0)
  await page.getByLabel('Discord 成员').selectOption('10')
  await page
    .getByRole('dialog')
    .getByRole('button', { name: '创建环境' })
    .click()
  expect(calls).toContainEqual({
    path: '/api/v1/development-environments',
    body: { ownerDiscordUserId: '10' },
  })

  await page.getByRole('button', { name: 'notes 管理 Forum 配对' }).click()
  await page
    .getByTestId('development-project-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb')
    .getByRole('button', { name: '恢复' })
    .click()
  expect(calls).toContainEqual({
    path: '/api/v1/development-projects/bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb/forums',
    body: {
      mode: 'restore',
      forumId: '22222222-2222-2222-2222-222222222222',
      name: '',
    },
  })

  await page.getByRole('button', { name: 'archive 管理 Forum 配对' }).click()
  const missingProject = page.getByTestId(
    'development-project-cccccccc-cccc-cccc-cccc-cccccccccccc',
  )
  await expect(
    missingProject.getByRole('button', { name: '恢复' }),
  ).toBeDisabled()
  await expect(
    missingProject.getByRole('button', { name: '创建 Forum' }),
  ).toBeDisabled()
})

test('项目表格在移动端转为纵向行且不溢出', async ({ page }) => {
  await mockAPI(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/development-environments')

  await expect(page.getByText('workspaces/atlas')).toBeVisible()
  const gitStatus = page
    .getByTestId('development-project-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa')
    .locator('[data-label="Git 状态"]')
  await expect(gitStatus).toBeVisible()
  expect(
    await gitStatus.evaluate((element) =>
      getComputedStyle(element, '::before').content.replaceAll('"', ''),
    ),
  ).toBe('Git 状态')
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true)
})
