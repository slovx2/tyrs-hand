import { expect, test, type Page } from '@playwright/test'

const worker = {
  id: '11111111-1111-1111-1111-111111111111',
  name: 'worker-primary',
  roles: ['github', 'discord'],
  enabled: true,
  maxConcurrentJobs: 6,
  protocolVersion: 23,
  workerVersion: 'deploy-1.1',
  status: 'online',
  metadata: {
    host: { home: '/home/worker', codexHome: '/home/worker/.codex' },
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

async function mockAPI(page: Page, bound = true) {
  let scans = 0
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
          role: 'admin',
          csrfToken: 'test-csrf',
          expiresAt: '2030-01-01T00:00:00Z',
        },
      })
    }
    if (path === `/api/v1/workers/${worker.id}`) {
      return route.fulfill({ json: worker })
    }
    if (path === `/api/v1/workers/${worker.id}/workspace`) {
      return route.fulfill({ json: { workspace: bound ? workspace : null } })
    }
    if (path === `/api/v1/workers/${worker.id}/workspace/scan`) {
      scans += 1
      return route.fulfill({
        json: {
          workspace: bound ? workspace : null,
          scan: {
            projects: [
              {
                name: 'atlas',
                relativePath: 'workspaces/atlas',
                hostPath: '/srv/atlas',
                projectSource: 'workspace_child',
                projectKind: 'git',
                branch: 'main',
                dirty: true,
                available: true,
              },
            ],
          },
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
  return () => scans
}

test('Worker 详情展示绑定后的正式项目', async ({ page }) => {
  await mockAPI(page)
  await page.goto(`/workers/${worker.id}/workspace`)

  await expect(
    page.getByRole('heading', { name: 'worker-primary' }),
  ).toBeVisible()
  await expect(page.getByText('workspaces/atlas')).toBeVisible()
})

test('未绑定 Worker 在移动端发现和刷新项目', async ({ page }) => {
  const scans = await mockAPI(page, false)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto(`/workers/${worker.id}/workspace`)

  await expect(page.getByText('/srv/atlas')).toBeVisible()
  await expect(page.getByText('尚未绑定 Workspace')).toBeVisible()
  await expect(page.getByRole('button', { name: '创建 Forum' })).toHaveCount(0)
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await expect.poll(scans).toBe(2)
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true)
})
