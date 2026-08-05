import { expect, test, type Page } from '@playwright/test'

const worker = {
  id: '11111111-1111-1111-1111-111111111111',
  name: 'song-ubuntu',
  roles: ['github', 'discord'],
  enabled: true,
  maxConcurrentJobs: 6,
  protocolVersion: 23,
  workerVersion: 'deploy-1.1',
  status: 'online',
  metadata: {
    host: { home: '/home/song', codexHome: '/home/song/.codex' },
    browser: { status: 'ready', tabCount: 1 },
  },
}

const workspace = {
  id: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
  ownerDiscordUserId: '20',
  ownerName: 'Bob',
  workerId: worker.id,
  projectsScannedAt: '2026-08-03T10:00:00Z',
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
      remoteUrl: 'https://example.invalid/team/atlas.git',
      lastSeenAt: '2026-08-03T10:00:00Z',
      forums: [],
    },
  ],
}

async function mockAPI(page: Page) {
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
    if (path === '/api/v1/workers') {
      return route.fulfill({ json: { items: [worker] } })
    }
    if (path === '/api/v1/settings/workers') {
      return route.fulfill({
        json: { githubWorkerId: worker.id, discordWorkerId: worker.id },
      })
    }
    if (path === '/api/v1/workspaces') {
      return route.fulfill({ json: { items: [workspace] } })
    }
    if (path === '/api/v1/discord/members') {
      return route.fulfill({ json: [] })
    }
    return route.fulfill({
      status: 404,
      json: { title: 'not mocked', status: 404 },
    })
  })
}

test('Workers 页面同时展示宿主状态与 Workspace 项目', async ({ page }) => {
  await mockAPI(page)
  await page.goto('/workers')

  await expect(
    page.getByRole('heading', { name: 'Worker', exact: true }),
  ).toBeVisible()
  await expect(page.getByRole('heading', { name: 'song-ubuntu' })).toBeVisible()
  await expect(page.getByText('/home/song/.codex')).toBeVisible()
  await expect(page.getByText('workspaces/atlas')).toBeVisible()
  await expect(page.getByText(/Chrome：ready/)).toBeVisible()
})

test('移动端 Worker 与 Workspace 不产生横向溢出', async ({ page }) => {
  await mockAPI(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/workers')

  await expect(page.getByText('workspaces/atlas')).toBeVisible()
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true)
})
