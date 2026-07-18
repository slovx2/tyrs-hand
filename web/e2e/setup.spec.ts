import { expect, test } from '@playwright/test'
import { createHmac, generateKeyPairSync } from 'node:crypto'

test('首次安装页可以提交管理员资料', async ({ page }) => {
  await page.goto('/setup')
  await expect(
    page.getByRole('heading', { name: '初始化 tyrs-hand' }),
  ).toBeVisible()
  await page
    .getByLabel('一次性 Setup Token')
    .fill(process.env.E2E_SETUP_TOKEN ?? 'integration-setup-token')
  await page.getByLabel('管理员用户名').fill('admin')
  await page.getByLabel('管理员密码').fill('integration-password')
  await page.getByRole('button', { name: '创建管理员' }).click()
  await expect(
    page.getByRole('heading', { name: '管理员创建完成' }),
  ).toBeVisible()
  await expect(page.getByText('恢复码', { exact: true })).toBeVisible()
  const secret = (await page
    .locator('dt', { hasText: 'TOTP Secret' })
    .locator('..')
    .locator('dd')
    .textContent())!.trim()
  await page.getByRole('link', { name: '前往登录' }).click()
  await expect(
    page.getByRole('heading', { name: '登录 tyrs-hand' }),
  ).toBeVisible()
  await page.getByLabel('用户名').fill('admin')
  await page.getByLabel('密码').fill('integration-password')
  await page.getByLabel('TOTP 验证码').fill(totp(secret))
  const loginResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith('/api/v1/auth/login') &&
      response.request().method() === 'POST',
  )
  await page.getByRole('button', { name: '登录' }).click()
  expect((await loginResponse).status()).toBe(200)
  await expect(page.getByRole('heading', { name: '控制面概览' })).toBeVisible()

  const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
  await page.goto('/settings/github')
  await page.getByLabel('App ID').fill('12345')
  await page.getByLabel('Client ID').fill('client-id')
  await page.getByLabel('App Slug').fill('tyrs-hand-test')
  await page
    .getByLabel('Private Key（PEM）')
    .fill(privateKey.export({ type: 'pkcs1', format: 'pem' }).toString())
  await page.getByLabel('Webhook Secret').fill('integration-webhook-secret')
  await page.getByRole('button', { name: '保存 GitHub App' }).click()
  await expect(page.getByText(/已连接 tyrs-hand-test/)).toBeVisible()

  const csrfToken = await page.evaluate(
    () =>
      JSON.parse(sessionStorage.getItem('tyrs-hand-ui') ?? '{}').state
        ?.csrfToken as string,
  )
  const response = await page.request.post('/api/v1/repositories', {
    headers: { 'X-CSRF-Token': csrfToken },
    data: {
      installationExternalId: 11,
      accountLogin: 'example',
      repositoryExternalId: 22,
      owner: 'example',
      name: 'repository',
      defaultBranch: 'main',
      cloneUrl: 'https://github.com/example/repository.git',
    },
  })
  expect(response.status()).toBe(201)
  await page.goto('/repositories')
  await expect(page.getByText('repository')).toBeVisible()
})

function totp(secret: string): string {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = ''
  for (const character of secret.replace(/=+$/, '').toUpperCase()) {
    bits += alphabet.indexOf(character).toString(2).padStart(5, '0')
  }
  const bytes = Buffer.alloc(Math.floor(bits.length / 8))
  for (let index = 0; index < bytes.length; index++) {
    bytes[index] = Number.parseInt(bits.slice(index * 8, index * 8 + 8), 2)
  }
  const counter = Buffer.alloc(8)
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30_000)))
  const digest = createHmac('sha1', bytes).update(counter).digest()
  const offset = digest[digest.length - 1] & 0x0f
  const value = (digest.readUInt32BE(offset) & 0x7fffffff) % 1_000_000
  return value.toString().padStart(6, '0')
}
