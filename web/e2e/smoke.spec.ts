import { expect, test, type Page } from '@playwright/test'
import { seedSession } from './session'

async function open(page: Page, path: string) {
  await page.goto(path)
  await expect(page.locator('main')).toBeVisible()
}

test.describe('smoke', () => {
  // Signed in as a viewer: every page needs a session.
  test.beforeEach(({ page }) => seedSession(page, 'viewer'))

  test('overview renders KPIs, charts and top opportunities', async ({ page }) => {
    await open(page, '/')
    await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
    await expect(page.getByTestId('kpi-savings')).toHaveText(/\$\d/)
    await expect(page.getByText('Top 10 opportunities')).toBeVisible()
    await expect(page.locator('[data-testid=chart] canvas').first()).toBeVisible()
    expect(await page.locator('[data-testid=chart] canvas').count()).toBeGreaterThanOrEqual(2)
    // fleet savings stay visible in the top bar on every page
    await expect(page.getByTestId('fleet-savings')).toContainText(/\$[\d,]+\/mo/)
  })

  test('opportunities: filter by kind and sort by name', async ({ page }) => {
    await open(page, '/opportunities')
    const rows = page.getByTestId('opportunity-row')
    await expect(rows.first()).toBeVisible()
    await expect(
      page
        .getByText(/^\d+ workloads?$/)
        .or(page.getByText(/workloads$/))
        .first(),
    ).toBeVisible()

    // Filter: kind = StatefulSet
    await page.getByRole('button', { name: 'Kind' }).click()
    await page.getByRole('option', { name: 'StatefulSet' }).click()
    await page.keyboard.press('Escape')
    await expect(page).toHaveURL(/kind=StatefulSet/)
    await expect(page.getByText(/match your filters/)).toBeVisible()
    await expect(page.locator('[data-testid=opportunities-table][data-loading=false]')).toBeVisible()
    const kinds = await rows.locator('td:first-child').allInnerTexts()
    expect(kinds.length).toBeGreaterThan(0)
    for (const k of kinds) expect(k.toLowerCase()).toContain('sts')

    // Sort by workload name ascending
    await page.getByRole('button', { name: /^Workload/ }).click()
    await expect(page).toHaveURL(/sort=name/)
    await expect(page.locator('th[aria-sort=ascending]')).toBeVisible()
    await expect(page.locator('[data-testid=opportunities-table][data-loading=false]')).toBeVisible()
    const names = await rows.locator('td:first-child a').allInnerTexts()
    expect(names).toEqual([...names].sort((a, b) => a.localeCompare(b)))
  })

  test('opportunities: facet filters show counts from the facets endpoint', async ({ page }) => {
    await open(page, '/opportunities')
    await page.getByRole('button', { name: 'Namespace', exact: true }).click()
    const payments = page.getByRole('option', { name: /^payments\s+\d+$/ })
    await expect(payments).toBeVisible()
  })

  test('opportunities: search narrows results and URL keeps state', async ({ page }) => {
    await open(page, '/opportunities?q=ledger')
    const rows = page.getByTestId('opportunity-row')
    await expect(page.locator('[data-testid=opportunities-table][data-loading=false]')).toBeVisible()
    await expect(rows.first()).toBeVisible()
    for (const t of await rows.locator('td:first-child').allInnerTexts())
      expect(t.toLowerCase()).toContain('ledger')
  })

  test('workload detail: explanation and usage charts render', async ({ page }) => {
    await open(page, '/opportunities')
    const first = page.getByTestId('opportunity-row').first()
    await expect(first).toBeVisible()
    await first.focus()
    await page.keyboard.press('Enter')
    await expect(page).toHaveURL(/\/workloads\/[^/]+\/[^/]+\/[^/]+\/[^/]+$/)
    await expect(page.getByText('Why this recommendation')).toBeVisible()
    await expect(page.getByText('Recommended resources')).toBeVisible()
    // Confidence formula and pricing source
    await expect(page.getByTestId('confidence-factors')).toContainText('Coverage')
    await expect(page.getByTestId('pricing')).toContainText(/core·h/)
    const usage = page.locator('[data-testid=chart] canvas').first()
    await expect(usage).toBeVisible()
    await page.getByRole('radio', { name: /Memory/ }).click()
    await expect(page.locator('[data-testid=chart] canvas').first()).toBeVisible()
    await page.getByRole('radio', { name: '30d' }).click()
    await expect(page.getByTestId('history-timeline')).toBeVisible()
  })

  test('workload detail: usage chart shows the recommendation history', async ({ page }) => {
    await open(page, '/workloads/prod-eu-west-1/payments/Deployment/checkout-api')
    await expect(page.locator('[data-testid=chart] canvas').first()).toBeVisible()
    await expect(page.getByText(/^Recommendation \(changed \d+× in range\)$/)).toBeVisible()
  })

  test('opportunities: tablet width keeps the request columns in view', async ({ page }) => {
    await page.setViewportSize({ width: 820, height: 1180 })
    await open(page, '/opportunities')
    await expect(page.getByTestId('opportunity-row').first()).toBeVisible()
    await expect(page.getByRole('columnheader', { name: /CPU request/i })).toBeInViewport()
    await expect(page.getByRole('columnheader', { name: /Confidence/i })).toHaveCount(0)
  })

  test('workload detail: unknown workload shows not-found state', async ({ page }) => {
    await open(page, '/workloads/prod-eu-west-1/nope/Deployment/missing')
    await expect(page.getByText('Workload not found')).toBeVisible()
  })

  test('clusters page shows agent health', async ({ page }) => {
    await open(page, '/clusters')
    await expect(page.getByTestId('cluster-cards')).toBeVisible()
    await expect(page.getByText('Connected').first()).toBeVisible()
    await expect(page.getByText('Never connected').first()).toBeVisible()
    await expect(page.getByTestId('cluster-cards')).toContainText('Estimated savings')
  })

  test('activity page lists audit events and filters by type', async ({ page }) => {
    await open(page, '/activity')
    await expect(page.getByTestId('activity-list')).toBeVisible()
    await page.getByRole('button', { name: 'Blocked', pressed: true }).click()
    await expect(page).toHaveURL(/type=applied/)
    await expect(page).not.toHaveURL(/type=blocked/)
    await expect(page.getByTestId('activity-list')).toBeVisible()
  })

  test('theme toggle switches to dark mode', async ({ page }) => {
    await open(page, '/')
    await page.getByRole('button', { name: 'Theme' }).click()
    await page.getByRole('menuitemradio', { name: 'Dark' }).click()
    await expect(page.locator('html')).toHaveClass(/dark/)
  })
})
