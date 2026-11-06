import { expect, test, type Page } from '@playwright/test'

/**
 * Sign-in through the web proxy against a real hub: nothing is mocked. Users are the dev users of the
 * hub chart (passwords from E2E_<ROLE>_PASSWORD, defaults: deploy/charts/hub/values-dev.yaml).
 */
const password = (user: string) => process.env[`E2E_${user.toUpperCase()}_PASSWORD`] ?? `ro-dev-${user}-pass`

/** Paths of /v1 responses that carried data (anything but the session check and sign-in). */
function watchData(page: Page) {
  const seen: string[] = []
  page.on('response', (r) => {
    const u = new URL(r.url())
    if (
      !u.pathname.startsWith('/v1/') ||
      ['/v1/me', '/v1/auth/login', '/v1/auth/logout'].includes(u.pathname)
    )
      return
    if (r.status() < 400) seen.push(`${r.status()} ${u.pathname}`)
  })
  return seen
}

async function signIn(page: Page, user: string) {
  await expect(page.getByTestId('login-form')).toBeVisible()
  await page.getByLabel('Username').fill(user)
  await page.getByLabel('Password', { exact: true }).fill(password(user))
  await page.getByTestId('login-submit').click()
  await expect(page.getByTestId('user-menu')).toBeVisible()
}

async function signOut(page: Page) {
  await page.getByTestId('user-menu').click()
  await page.getByTestId('sign-out').click()
  await expect(page).toHaveURL(/\/login$/)
}

test('an anonymous visit sees only the sign-in page and gets no data', async ({ page, request }) => {
  const data = watchData(page)
  for (const path of ['/', '/opportunities', '/clusters', '/activity', '/admin/access']) {
    await page.goto(path)
    await expect(page.getByTestId('login-form')).toBeVisible()
    await expect(page.locator('main')).toHaveCount(0)
    await expect(page.getByTestId('fleet-savings')).toHaveCount(0)
  }
  expect(data).toEqual([])
  // The API itself: 401 without a session.
  for (const path of ['/v1/workloads', '/v1/summary', '/v1/me']) {
    const r = await request.get(path)
    expect(r.status(), path).toBe(401)
  }
})

test('sign-in returns to the page first asked for; sign-out ends the session', async ({ page }) => {
  await page.goto('/opportunities?sort=name')
  await expect(page).toHaveURL(/\/login\?next=%2Fopportunities%3Fsort%3Dname$/)
  await signIn(page, 'operator')
  await expect(page).toHaveURL(/\/opportunities\?sort=name$/)
  await expect(
    page
      .getByTestId('opportunity-row')
      .first()
      .or(page.getByText(/no workloads/i).first()),
  ).toBeVisible()

  await signOut(page)
  // Leave the app first, so answers to requests made before sign-out are not counted.
  await page.goto('about:blank')
  const data = watchData(page)
  await page.goto('/opportunities')
  await expect(page.getByTestId('login-form')).toBeVisible()
  expect(data).toEqual([])
  expect((await page.request.get('/v1/me')).status()).toBe(401)
})

test('a next= pointing to another origin is ignored', async ({ page, baseURL }) => {
  for (const next of ['https://evil.example/', '//evil.example/', '/\\evil.example/']) {
    await page.goto(`/login?next=${encodeURIComponent(next)}`)
    await signIn(page, 'viewer')
    expect(page.url()).toBe(new URL('/', baseURL).toString())
    await signOut(page)
  }
})

test('an ended session goes back to sign-in from any page, with a notice', async ({ page, context }) => {
  await page.goto('/clusters')
  await signIn(page, 'viewer')
  await expect(page).toHaveURL(/\/clusters$/)
  // End the session behind the tab's back: sign out with the same cookie from another tab.
  const other = await context.newPage()
  await other.goto('/activity')
  await expect(other.getByTestId('user-menu')).toBeVisible()
  await signOut(other)
  await other.close()

  await page.getByRole('link', { name: 'Opportunities' }).first().click()
  await expect(page).toHaveURL(/\/login\?next=%2Fopportunities$/)
  await expect(page.getByTestId('session-expired')).toContainText('Your session ended')
  await signIn(page, 'viewer')
  await expect(page).toHaveURL(/\/opportunities$/)
})
