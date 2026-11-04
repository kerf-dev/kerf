import { expect, test, type Page } from '@playwright/test'

/**
 * Admin area flows (WO-22) against the MSW mock hub: login per role, defaults with impact preview,
 * create a policy, pause a cluster, approve once, revert, bulk exclude, onboarding, conflicts.
 * Mock users: admin, operator, viewer (password dev-password).
 */

async function login(page: Page, user: string, next = '/') {
  await page.goto(`/login?next=${encodeURIComponent(next)}`)
  await page.getByLabel('Username').fill(user)
  await page.getByLabel('Password', { exact: true }).fill('dev-password')
  await page.getByTestId('login-submit').click()
  await expect(page).toHaveURL(new RegExp(`${next.replace(/[?]/g, '\\?')}$`))
  await expect(page.getByTestId('user-menu')).toBeVisible()
}

/** Fills every "type the cluster name" box, the reason, and confirms. */
async function confirmWrite(page: Page, dialog: string, reason = 'e2e test') {
  const d = page.getByTestId(dialog)
  await expect(d).toBeVisible()
  for (const input of await d.getByTestId('prod-confirm').all()) {
    const label = await d.locator(`label[for="${await input.getAttribute('id')}"]`).innerText()
    await input.fill(
      label
        .replace(/^Type\s+/, '')
        .replace(/\s+to confirm$/, '')
        .trim(),
    )
  }
  await d.getByTestId('reason').fill(reason)
  await d.getByTestId('confirm-write').click()
  await expect(d).toBeHidden()
}

test.beforeEach(async ({ page }) => {
  // Fresh mock state: no session, anonymous viewer allowed.
  await page.addInitScript(() => {
    if (!sessionStorage.getItem('e2e-init')) {
      localStorage.removeItem('ro-mock-session')
      localStorage.removeItem('ro-mock-anonymous')
      sessionStorage.setItem('e2e-init', '1')
    }
  })
})

test.describe('login and roles', () => {
  test('admin sees the whole admin area; sign out returns to the login', async ({ page }) => {
    await login(page, 'admin')
    const nav = page.getByTestId('admin-nav')
    for (const item of [
      'Automation',
      'Policies',
      'Defaults',
      'Recommender',
      'Pricing',
      'Metrics sources',
      'Access',
      'Audit',
    ])
      await expect(nav.getByRole('link', { name: item })).toBeVisible()
    await page.getByTestId('user-menu').click()
    await page.getByTestId('sign-out').click()
    await expect(page).toHaveURL(/\/login$/)
  })

  test('operator sees automation, policies and audit only', async ({ page }) => {
    await login(page, 'operator')
    const nav = page.getByTestId('admin-nav')
    await expect(nav.getByRole('link', { name: 'Automation' })).toBeVisible()
    await expect(nav.getByRole('link', { name: 'Policies' })).toBeVisible()
    await expect(nav.getByRole('link', { name: 'Audit' })).toBeVisible()
    await expect(nav.getByRole('link', { name: 'Defaults' })).toHaveCount(0)
    await expect(nav.getByRole('link', { name: 'Access' })).toHaveCount(0)
    await page.goto('/admin/defaults')
    await expect(page.getByText('This page needs the admin role')).toBeVisible()
  })

  test('viewer has no admin area; anonymous visitors can sign in', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByTestId('sign-in')).toBeVisible()
    await expect(page.getByTestId('admin-nav')).toHaveCount(0)
    await login(page, 'viewer')
    await expect(page.getByTestId('admin-nav')).toHaveCount(0)
    await page.goto('/admin')
    await expect(page.getByText('This page needs the operator role')).toBeVisible()
  })

  test('wrong password is explained; login required without anonymous access', async ({ page }) => {
    await page.goto('/login')
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Password', { exact: true }).fill('wrong-password')
    await page.getByTestId('login-submit').click()
    await expect(page.getByText('Wrong username or password.')).toBeVisible()

    await page.evaluate(() => localStorage.setItem('ro-mock-anonymous', 'none'))
    await page.goto('/opportunities')
    await expect(page).toHaveURL(/\/login\?next=%2Fopportunities$/)
    await expect(page.getByText('Continue without signing in')).toHaveCount(0)
  })
})

test.describe('admin writes', () => {
  test('change global defaults with the impact preview', async ({ page }) => {
    await login(page, 'admin', '/admin/defaults')
    await page.getByTestId('field-memory-mode').getByRole('radio', { name: 'Off' }).click()
    await expect(page.getByTestId('save-bar')).toContainText('Unsaved changes')
    await page.getByTestId('save').click()
    const d = page.getByTestId('defaults-dialog')
    await expect(d.getByTestId('impact-preview')).toBeVisible()
    await expect(d.getByTestId('impact-changed')).toContainText(/[1-9]\d* /)
    await confirmWrite(page, 'defaults-dialog', 'memory off fleet-wide (e2e)')
    await expect(page.getByTestId('toast')).toContainText('Global defaults saved')
    await expect(page.getByText(/ro-global-defaults · v3/)).toBeVisible()
  })

  test('create a policy with the selector builder', async ({ page }) => {
    await login(page, 'admin', '/admin/policies')
    await page.getByTestId('new-policy').click()
    await page.getByTestId('policy-name').fill('payments-e2e')
    await page
      .getByRole('group', { name: 'Clusters' })
      .getByRole('button', { name: /prod-eu-west-1/ })
      .click()
    const ns = page.getByTestId('selector-namespaces').getByRole('combobox')
    await ns.fill('payments')
    await ns.press('Enter')
    await expect(page.getByTestId('match-count')).toContainText(/Matches [1-9]\d* workloads?/)
    await page.getByTestId('field-cpu-mode').getByRole('radio', { name: 'Auto-patch' }).click()
    await page.getByTestId('save').click()
    await expect(page.getByTestId('policy-save-dialog').getByTestId('impact-preview')).toBeVisible()
    await confirmWrite(page, 'policy-save-dialog', 'CPU auto-patch for payments (e2e)')
    await expect(page.getByTestId('toast')).toContainText('Created payments-e2e')
    await expect(page).toHaveURL(/\/admin\/policies\/[0-9a-f-]{36}$/)
    await expect(page.getByRole('heading', { name: 'payments-e2e' })).toBeVisible()
  })

  test('revert a policy to an earlier version', async ({ page }) => {
    await login(page, 'admin', '/admin/policies')
    await page.getByTestId('policy-row-payments-autopilot').click()
    await page.getByTestId('tab-history').click()
    await page.getByTestId('version-1').click()
    await expect(page.getByTestId('version-diff')).toBeVisible()
    await page.getByTestId('revert').click()
    await confirmWrite(page, 'revert-dialog', 'rollback (e2e)')
    await expect(page.getByTestId('toast')).toContainText('Reverted payments-autopilot to v1')
    await expect(page.getByTestId('version-list')).toContainText(/revert to v1/i)
  })

  test('a stale page gets a version conflict with a reload option', async ({ page }) => {
    await login(page, 'admin', '/admin/policies')
    await page.getByTestId('policy-row-payments-autopilot').click()
    await expect(page.getByRole('heading', { name: 'payments-autopilot' })).toBeVisible()
    // Someone else saves the policy in the meantime.
    await page.evaluate(async () => {
      const me = (await (await fetch('/v1/me')).json()) as { csrfToken: string }
      const list = (await (await fetch('/v1/admin/policies?type=policy')).json()) as {
        items: { id: string; name: string; version: number; clusters: string[]; spec: object }[]
      }
      const p = list.items.find((x) => x.name === 'payments-autopilot')!
      await fetch(`/v1/admin/policies/${p.id}`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          'If-Match': `"${p.version}"`,
          'X-CSRF-Token': me.csrfToken,
        },
        body: JSON.stringify({
          type: 'policy',
          name: p.name,
          clusters: p.clusters,
          spec: { ...p.spec, profile: 'dev' },
          reason: 'concurrent edit',
        }),
      })
    })
    await page.getByTestId('field-memory-mode').getByRole('radio', { name: 'Off' }).click()
    await page.getByTestId('save').click()
    await expect(page.getByTestId('policy-save-dialog').getByTestId('impact-preview')).toBeVisible()
    const d = page.getByTestId('policy-save-dialog')
    for (const input of await d.getByTestId('prod-confirm').all()) {
      const label = await d.locator(`label[for="${await input.getAttribute('id')}"]`).innerText()
      await input.fill(
        label
          .replace(/^Type\s+/, '')
          .replace(/\s+to confirm$/, '')
          .trim(),
      )
    }
    await d.getByTestId('reason').fill('stale edit')
    await d.getByTestId('confirm-write').click()
    await expect(d.getByText('Someone else changed this in the meantime')).toBeVisible()
    await expect(d.getByTestId('reload-after-conflict')).toBeVisible()
  })

  test('onboard a cluster: metrics test, token once, install snippet', async ({ page }) => {
    await login(page, 'admin', '/clusters')
    await page.getByTestId('add-cluster').click()
    await page.getByLabel('Cluster name').fill('staging-eu-west-1')
    await page.getByTestId('wizard-next').click()
    await page.getByTestId('test-datasource').click()
    await expect(page.getByTestId('datasource-test-result')).toContainText(
      'The datasource has what recommendations need',
    )
    await page.getByTestId('wizard-next').click()
    await page.getByTestId('register-cluster').click()
    await confirmWrite(page, 'write-dialog', 'new staging cluster (e2e)')
    await expect(page.getByTestId('secret-value')).toHaveText(/\S{12,}/)
    await expect(page.getByTestId('install-content')).toContainText('resource-optimizer-agent-token')
    await page.getByTestId('wizard-next').click()
    await expect(page.getByTestId('verify-step')).toBeVisible()
    await expect(page.getByTestId('readiness-stepper')).toBeVisible()
  })
})

test.describe('operator writes', () => {
  test('pause a cluster', async ({ page }) => {
    await login(page, 'operator', '/admin')
    const card = page.getByTestId('automation-prod-eu-west-1')
    await card.getByTestId('pause-cluster').click()
    await confirmWrite(page, 'pause-dialog', 'incident INC-4711 (e2e)')
    await expect(page.getByTestId('toast')).toContainText('Automation paused for prod-eu-west-1')
    await expect(card.getByTestId('paused-badge')).toBeVisible()
    // Resuming a prod cluster needs an admin.
    await expect(card.getByTestId('resume-cluster')).toBeDisabled()
  })

  test('approve a recommendation once', async ({ page }) => {
    await login(page, 'operator', '/workloads/prod-eu-west-1/cards/Deployment/card-issuing-api')
    await page.getByTestId('approve-once').click()
    await confirmWrite(page, 'approve-dialog', 'reviewed with the owner (e2e)')
    await expect(page.getByTestId('approval')).toContainText('Approved once, waiting for the agent')
    await expect(page.getByTestId('approve-once')).toBeDisabled()
  })

  test('exclude a workload, then clear the override', async ({ page }) => {
    await login(page, 'operator', '/workloads/prod-eu-west-1/cards/Deployment/card-issuing-api')
    await page.getByTestId('exclude').click()
    await expect(page.getByTestId('override-dialog').getByTestId('impact-preview')).toBeVisible()
    await confirmWrite(page, 'override-dialog', 'owner asked (e2e)')
    await expect(page.getByTestId('override-active')).toContainText('CPU off')
    await page.getByTestId('clear-override').click()
    await confirmWrite(page, 'clear-dialog', 'back to policy (e2e)')
    await expect(page.getByTestId('override-active')).toHaveCount(0)
  })

  test('bulk exclude from opportunities shows count and savings impact', async ({ page }) => {
    await login(page, 'operator', '/opportunities?namespace=payments')
    const rows = page.getByTestId('select-row')
    await rows.nth(0).click()
    await rows.nth(1).click()
    await expect(page.getByTestId('bulk-bar')).toContainText('2 selected')
    await page.getByTestId('bulk-exclude').click()
    const d = page.getByTestId('bulk-dialog')
    await expect(d).toContainText('Exclude 2 workloads')
    await expect(d.getByTestId('impact-preview')).toBeVisible()
    await confirmWrite(page, 'bulk-dialog', 'payments hands off (e2e)')
    await expect(page.getByTestId('toast')).toContainText('Excluded 2 workloads')
    await expect(page.getByTestId('bulk-bar')).toHaveCount(0)
  })
})

test.describe('readiness', () => {
  test('banner on the overview links to the cluster checklist with remediation', async ({ page }) => {
    await page.goto('/')
    const banner = page.getByTestId('readiness-banner')
    await expect(banner).toContainText('prod-us-east-1')
    await banner.getByRole('link', { name: /prod-us-east-1/ }).click()
    await expect(page).toHaveURL(/\/clusters\/prod-us-east-1\?tab=readiness/)
    await expect(page.getByTestId('check-rbac.resize')).toContainText('ClusterRole rules to add')
  })
})
