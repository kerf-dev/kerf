/**
 * Visual capture of every page in light and dark mode.
 * The admin area is captured signed in as admin (operator for workload actions and bulk edits). Skipped unless SCREENSHOTS_DIR is set:
 *   make web-screenshots   (or: SCREENSHOTS_DIR=$PWD/../docs/screenshots npx playwright test screenshots)
 */
import { expect, test } from '@playwright/test'
import { seedSession } from './session'

const dir = process.env.SCREENSHOTS_DIR

// Every page but the sign-in page needs a session: a viewer unless a shot signs in as someone else.
test.beforeEach(async ({ page }, info) => {
  if (!info.title.startsWith('login')) await seedSession(page, 'viewer')
})
const pages = [
  ['overview', '/'],
  ['opportunities', '/opportunities'],
  ['workload', '/workloads/prod-eu-west-1/payments/Deployment/checkout-api'],
  ['workload-rolled-back', ''],
  ['clusters', '/clusters'],
  ['activity', '/activity'],
] as const

for (const theme of ['light', 'dark'] as const) {
  for (const [name, path] of pages) {
    test(`${name} (${theme})`, async ({ page }) => {
      test.skip(!dir, 'set SCREENSHOTS_DIR to capture')
      test.slow()
      await page.emulateMedia({ colorScheme: theme })
      let target: string = path
      if (name === 'workload-rolled-back') {
        // pick a workload that was rolled back, to show the status banner and the timeline
        await page.goto('/opportunities')
        await expect(page.getByTestId('opportunity-row').first()).toBeVisible()
        const res = (await page.evaluate(async () => (await fetch('/v1/workloads?limit=500')).json())) as {
          items: { id: string; phase: string }[]
        }
        const w = res.items.find((x) => x.phase === 'RolledBack')
        target = `/workloads/${w!.id}`
      }
      await page.goto(target)
      await expect(page.locator('main')).toBeVisible()
      await page.waitForLoadState('networkidle')
      await expect(page.locator('[aria-busy=true], .animate-pulse')).toHaveCount(0, { timeout: 30_000 })
      await page.waitForTimeout(900) // let chart animations settle
      await page.screenshot({ path: `${dir}/${name}-${theme}.png`, fullPage: true })
    })
  }
}

// Tablet width: the sidebar collapses to an icon rail and grids stack.
for (const [name, path] of [
  ['overview', '/'],
  ['opportunities', '/opportunities'],
  ['workload', '/workloads/prod-eu-west-1/payments/Deployment/checkout-api'],
  ['clusters', '/clusters'],
  ['activity', '/activity'],
] as const) {
  test(`${name} (tablet)`, async ({ page }) => {
    test.skip(!dir, 'set SCREENSHOTS_DIR to capture')
    test.slow()
    await page.setViewportSize({ width: 820, height: 1180 })
    await page.goto(path)
    await expect(page.locator('main')).toBeVisible()
    await page.waitForLoadState('networkidle')
    await expect(page.locator('[aria-busy=true], .animate-pulse')).toHaveCount(0, { timeout: 30_000 })
    await page.waitForTimeout(900)
    await page.screenshot({ path: `${dir}/${name}-tablet.png`, fullPage: true })
  })
}

// ---------------------------------------------------------------- admin area

async function signIn(page: import('@playwright/test').Page, user: string) {
  await page.goto('/login')
  await page.evaluate(() => localStorage.removeItem('ro-mock-session'))
  await page.goto('/login')
  await page.getByLabel('Username').fill(user)
  await page.getByLabel('Password', { exact: true }).fill('dev-password')
  await page.getByTestId('login-submit').click()
  await expect(page.getByTestId('user-menu')).toBeVisible()
}

async function policyId(page: import('@playwright/test').Page, name: string) {
  return page.evaluate(async (n) => {
    const r = (await (await fetch('/v1/admin/policies')).json()) as { items: { id: string; name: string }[] }
    return r.items.find((p) => p.name === n)!.id
  }, name)
}

async function settle(page: import('@playwright/test').Page) {
  await expect(page.locator('main')).toBeVisible()
  await page.waitForLoadState('networkidle')
  await expect(page.locator('main [aria-busy=true], main .animate-pulse')).toHaveCount(0, { timeout: 30_000 })
  await page.waitForTimeout(700)
}

type Shot = {
  name: string
  user?: string
  path: string | ((page: import('@playwright/test').Page) => Promise<string>)
  act?: (page: import('@playwright/test').Page) => Promise<void>
  themes?: readonly ('light' | 'dark')[]
  tablet?: boolean
  full?: boolean
}

const adminShots: Shot[] = [
  { name: 'login', path: '/login' },
  {
    // Both identity providers offered: OpenID Connect and SAML, above the password form.
    name: 'login-saml',
    path: '/login',
    act: async (p) => {
      await p.evaluate(() => {
        localStorage.setItem('ro-mock-oidc', 'on')
        localStorage.setItem('ro-mock-saml', 'on')
      })
      await p.reload()
      await expect(p.getByTestId('saml-sign-in')).toBeVisible()
    },
  },
  { name: 'admin-automation', user: 'admin', path: '/admin', tablet: true },
  { name: 'admin-policies', user: 'admin', path: '/admin/policies' },
  {
    name: 'admin-policy-editor',
    user: 'admin',
    path: async (p) => `/admin/policies/${await policyId(p, 'payments-autopilot')}`,
    tablet: true,
  },
  {
    name: 'admin-policy-history',
    user: 'admin',
    path: async (p) => `/admin/policies/${await policyId(p, 'payments-autopilot')}?tab=history`,
  },
  {
    name: 'admin-simulator',
    user: 'admin',
    path: '/admin/policies?tab=simulator&workload=prod-eu-west-1%2Fpayments%2FDeployment%2Fcheckout-api',
  },
  { name: 'admin-defaults', user: 'admin', path: '/admin/defaults' },
  {
    name: 'admin-defaults-preview',
    user: 'admin',
    path: '/admin/defaults',
    themes: ['light'],
    full: false,
    act: async (p) => {
      await p.getByTestId('field-memory-mode').getByRole('radio', { name: 'Off' }).click()
      await p.getByTestId('save').click()
      await expect(p.getByTestId('impact-preview')).toBeVisible()
    },
  },
  { name: 'admin-recommender', user: 'admin', path: '/admin/recommender' },
  { name: 'admin-pricing', user: 'admin', path: '/admin/pricing' },
  { name: 'admin-datasources', user: 'admin', path: '/admin/datasources' },
  { name: 'admin-access', user: 'admin', path: '/admin/access' },
  { name: 'admin-audit', user: 'admin', path: '/admin/audit' },
  { name: 'admin-maintenance', user: 'admin', path: '/admin/maintenance', tablet: true },
  { name: 'admin-notifications', user: 'admin', path: '/admin/notifications', tablet: true },
  { name: 'cluster-readiness', user: 'admin', path: '/clusters/prod-us-east-1?tab=readiness', tablet: true },
  { name: 'cluster-agent', user: 'admin', path: '/clusters/dr-eu-central-1?tab=agent' },
  {
    name: 'cluster-wizard',
    user: 'admin',
    path: '/clusters/new',
    act: async (p) => {
      await p.getByLabel('Cluster name').fill('staging-eu-west-1')
      await p.getByTestId('wizard-next').click()
      await p.getByTestId('test-datasource').click()
      await expect(p.getByTestId('datasource-test-result')).toBeVisible()
    },
  },
  {
    name: 'workload-automation',
    user: 'operator',
    path: '/workloads/prod-eu-west-1/payments/Deployment/checkout-api',
  },
  {
    name: 'opportunities-bulk',
    user: 'operator',
    path: '/opportunities?namespace=payments',
    themes: ['light'],
    full: false,
    act: async (p) => {
      for (const i of [0, 1, 2]) await p.getByTestId('select-row').nth(i).click()
      await p.getByTestId('bulk-set-mode').click()
      await p.getByTestId('bulk-cpu').getByRole('radio', { name: 'Auto-patch' }).click()
      await expect(p.getByTestId('impact-preview')).toBeVisible()
    },
  },
]

for (const s of adminShots) {
  for (const theme of s.themes ?? (['light', 'dark'] as const)) {
    test(`${s.name} (${theme})`, async ({ page }) => {
      test.skip(!dir, 'set SCREENSHOTS_DIR to capture')
      test.slow()
      await page.emulateMedia({ colorScheme: theme })
      if (s.user) await signIn(page, s.user)
      await page.goto(typeof s.path === 'string' ? s.path : await s.path(page))
      if (s.path !== '/login') await settle(page)
      else await page.waitForTimeout(500)
      if (s.act) {
        await s.act(page)
        await page.waitForTimeout(600)
      }
      await page.screenshot({ path: `${dir}/${s.name}-${theme}.png`, fullPage: s.full ?? true })
    })
  }
  if (s.tablet)
    test(`${s.name} (tablet)`, async ({ page }) => {
      test.skip(!dir, 'set SCREENSHOTS_DIR to capture')
      test.slow()
      await page.setViewportSize({ width: 820, height: 1180 })
      if (s.user) await signIn(page, s.user)
      await page.goto(typeof s.path === 'string' ? s.path : await s.path(page))
      await settle(page)
      await page.screenshot({ path: `${dir}/${s.name}-tablet.png`, fullPage: true })
    })
}
