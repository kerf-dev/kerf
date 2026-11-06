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
  // Fresh mock state: no session.
  await page.addInitScript(() => {
    if (!sessionStorage.getItem('e2e-init')) {
      localStorage.removeItem('ro-mock-session')
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
      'Maintenance',
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
    await expect(nav.getByRole('link', { name: 'Maintenance' })).toBeVisible()
    await expect(nav.getByRole('link', { name: 'Defaults' })).toHaveCount(0)
    await expect(nav.getByRole('link', { name: 'Access' })).toHaveCount(0)
    await page.goto('/admin/defaults')
    await expect(page.getByText('This page needs the admin role')).toBeVisible()
  })

  test('viewer has no admin area', async ({ page }) => {
    await login(page, 'viewer')
    await expect(page.getByTestId('admin-nav')).toHaveCount(0)
    await page.goto('/admin')
    await expect(page.getByText('This page needs the operator role')).toBeVisible()
  })

  test('without a session only the sign-in page shows, then it returns to the page asked for', async ({
    page,
  }) => {
    const data: string[] = []
    page.on('response', (r) => {
      const u = new URL(r.url())
      if (u.pathname.startsWith('/v1/') && u.pathname !== '/v1/me' && r.ok()) data.push(u.pathname)
    })
    await page.goto('/opportunities?kind=StatefulSet')
    await expect(page).toHaveURL(/\/login\?next=%2Fopportunities%3Fkind%3DStatefulSet$/)
    await expect(page.getByTestId('login-form')).toBeVisible()
    await expect(page.locator('main')).toHaveCount(0)
    await expect(page.getByText('Continue without signing in')).toHaveCount(0)
    expect(data).toEqual([])

    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Password', { exact: true }).fill('wrong-password')
    await page.getByTestId('login-submit').click()
    await expect(page.getByText('Wrong username or password.')).toBeVisible()

    await page.getByLabel('Password', { exact: true }).fill('dev-password')
    await page.getByTestId('login-submit').click()
    await expect(page).toHaveURL(/\/opportunities\?kind=StatefulSet$/)
    await expect(page.getByTestId('user-menu')).toBeVisible()
  })

  test('a next= pointing to another origin is ignored', async ({ page }) => {
    for (const next of ['https://evil.example/x', '//evil.example/x', '/\\evil.example/x']) {
      await page.goto(`/login?next=${encodeURIComponent(next)}`)
      await page.getByLabel('Username').fill('viewer')
      await page.getByLabel('Password', { exact: true }).fill('dev-password')
      await page.getByTestId('login-submit').click()
      await expect(page.getByTestId('user-menu')).toBeVisible()
      await expect(page).toHaveURL(/^http:\/\/localhost:\d+\/$/)
      await page.getByTestId('user-menu').click()
      await page.getByTestId('sign-out').click()
      await expect(page).toHaveURL(/\/login$/)
    }
  })

  test('an ended session goes back to sign-in with a notice and returns to the page', async ({ page }) => {
    await login(page, 'operator', '/clusters')
    // The session ends behind the tab's back (expired or revoked).
    await page.evaluate(() => localStorage.removeItem('ro-mock-session'))
    await page.getByRole('link', { name: 'Opportunities' }).first().click()
    await expect(page).toHaveURL(/\/login\?next=%2Fopportunities$/)
    await expect(page.getByTestId('session-expired')).toContainText('Your session ended')
    await page.getByLabel('Username').fill('operator')
    await page.getByLabel('Password', { exact: true }).fill('dev-password')
    await page.getByTestId('login-submit').click()
    await expect(page).toHaveURL(/\/opportunities$/)
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
    await expect(page.getByTestId('install-content')).toContainText('kerf-agent-token')
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

test.describe('maintenance (WO-33)', () => {
  test('versions by component, the update and Check now', async ({ page }) => {
    await login(page, 'admin')
    await page.getByTestId('admin-nav').getByRole('link', { name: 'Maintenance' }).click()
    await expect(page).toHaveURL(/\/admin\/maintenance$/)
    const card = page.getByTestId('updates-card')
    await expect(card.getByTestId('update-state')).toHaveAttribute('data-state', 'update_available')
    await expect(card.getByTestId('latest-version')).toHaveText('0.5.0')
    await expect(card.getByTestId('hub-command')).toContainText('--version 0.5.0')
    await expect(page.getByTestId('leader-badge')).toHaveCount(1)
    await expect(page.getByTestId('cluster-prod-us-east-1')).toHaveAttribute('data-compat', 'older')
    await expect(page.getByTestId('cluster-prod-us-east-1').getByTestId('webhook-missing')).toBeVisible()
    await page.getByTestId('check-now').click()
    await expect(card).toContainText('Checked just now')
  })

  test('operators see the page but cannot run the check', async ({ page }) => {
    await login(page, 'operator', '/admin/maintenance')
    await expect(page.getByTestId('updates-card')).toBeVisible()
    await expect(page.getByTestId('check-now')).toBeDisabled()
  })
})

test.describe('readiness', () => {
  test('banner on the overview links to the cluster checklist with remediation', async ({ page }) => {
    await login(page, 'viewer')
    const banner = page.getByTestId('readiness-banner')
    await expect(banner).toContainText('prod-us-east-1')
    await banner.getByRole('link', { name: /prod-us-east-1/ }).click()
    await expect(page).toHaveURL(/\/clusters\/prod-us-east-1\?tab=readiness/)
    await expect(page.getByTestId('check-rbac.resize')).toContainText('ClusterRole rules to add')
  })
})

test.describe('WO-22b polish', () => {
  test('mode controls fit at ~800 px content width', async ({ page }) => {
    await page.setViewportSize({ width: 1060, height: 900 })
    await login(page, 'admin', '/admin/defaults')
    for (const [id, name] of [
      ['field-cpu-mode', 'CPU mode'],
      ['field-memory-mode', 'Memory mode'],
    ] as const) {
      const group = page.getByTestId(id).getByRole('radiogroup', { name })
      const g = (await group.boundingBox())!
      const auto = (await page.getByTestId(id).getByRole('radio', { name: 'Auto-patch' }).boundingBox())!
      expect(auto.x + auto.width).toBeLessThanOrEqual(g.x + g.width + 0.5)
      expect(auto.height).toBeLessThan(34) // one line, not wrapped
    }
    const cpu = (await page.getByTestId('field-cpu-mode').boundingBox())!
    const mem = (await page.getByTestId('field-memory-mode').boundingBox())!
    // side by side without overlap, or stacked
    expect(cpu.x + cpu.width <= mem.x + 1 || mem.y >= cpu.y + cpu.height - 1).toBe(true)
  })

  test('/admin/clusters redirects to the clusters pages', async ({ page }) => {
    await login(page, 'admin')
    await page.goto('/admin/clusters')
    await expect(page).toHaveURL(/\/clusters$/)
    await page.goto('/admin/clusters/prod-eu-west-1')
    await expect(page).toHaveURL(/\/clusters\/prod-eu-west-1$/)
  })

  test('Git-only clusters are read-only for policy edits', async ({ page }) => {
    await login(page, 'admin', '/clusters/dr-eu-central-1?tab=settings')
    await expect(page.getByText(/Not sent to the Git-only cluster dr-eu-central-1/)).toBeVisible()
    await expect(page.getByTestId('field-cpu-mode').getByRole('radio', { name: 'Off' })).toBeDisabled()
    await page.goto('/admin/policies/new')
    await expect(
      page.getByRole('group', { name: 'Clusters' }).getByRole('button', { name: /dr-eu-central-1/ }),
    ).toBeDisabled()
  })
})

test.describe('WO-22c readiness alignment', () => {
  test('checks without a result show as pending, not failures', async ({ page }) => {
    await login(page, 'viewer')
    await expect(page.getByTestId('readiness-banner-pending').first()).toContainText(
      /\d+ checks? not reported yet/,
    )
    await page.evaluate(() => localStorage.removeItem('ro-mock-session'))
    await login(page, 'admin', '/clusters/dr-eu-central-1?tab=readiness')
    const row = page.getByTestId('check-agent.version')
    await expect(row).toContainText('Not reported yet')
    await expect(row).toContainText('pending')
  })

  test('the built-in datasource offers no edit or delete', async ({ page }) => {
    await login(page, 'admin', '/admin/datasources')
    const local = page.getByTestId('datasource-local')
    await expect(local.getByTestId('builtin-note')).toBeVisible()
    await expect(local.getByRole('button', { name: 'Edit local' })).toHaveCount(0)
    await expect(
      page.getByTestId('datasource-amp-eu1').getByRole('button', { name: 'Edit amp-eu1' }),
    ).toBeVisible()
  })

  test('install snippets not configured on the hub name the missing chart values', async ({ page }) => {
    await login(page, 'admin', '/')
    await page.evaluate(() => localStorage.setItem('ro-mock-install', 'unconfigured'))
    await page.goto('/clusters/dr-eu-central-1?tab=agent')
    const box = page.getByTestId('install-not-configured')
    await expect(box).toContainText('config.onboarding.chart')
    await expect(box).toContainText('config.onboarding.hubURL')
    await page.evaluate(() => localStorage.removeItem('ro-mock-install'))
  })
})
