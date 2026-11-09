/**
 * Screenshots of the Dynatrace datasource for the install guide: the datasource form, the Test
 * result with its metric mapping and the readiness of a cluster read from Dynatrace. Skipped unless
 * SCREENSHOTS_DIR is set:
 *   SCREENSHOTS_DIR=$PWD/../docs/screenshots npx playwright test screenshots-dynatrace
 */
import { expect, test, type Page } from '@playwright/test'

const dir = process.env.SCREENSHOTS_DIR

test.use({ viewport: { width: 1280, height: 1100 }, deviceScaleFactor: 2 })

async function signInAdmin(page: Page) {
  await page.goto('/login')
  await page.evaluate(() => localStorage.removeItem('ro-mock-session'))
  await page.goto('/login')
  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password', { exact: true }).fill('dev-password')
  await page.getByTestId('login-submit').click()
  await expect(page.getByTestId('user-menu')).toBeVisible()
}

async function settle(page: Page) {
  await expect(page.locator('main')).toBeVisible()
  await page.waitForLoadState('networkidle')
  await expect(page.locator('main [aria-busy=true], main .animate-pulse')).toHaveCount(0, { timeout: 30_000 })
  await page.waitForTimeout(700)
}

for (const theme of ['light', 'dark'] as const) {
  test.describe(theme, () => {
    test.beforeEach(async ({ page }) => {
      test.skip(!dir, 'set SCREENSHOTS_DIR to capture')
      test.slow()
      await page.emulateMedia({ colorScheme: theme })
      await signInAdmin(page)
    })

    test(`dynatrace datasource form (${theme})`, async ({ page }) => {
      await page.goto('/admin/datasources')
      await settle(page)
      await page.getByTestId('new-datasource').click()
      const dialog = page.getByTestId('datasource-dialog')
      await dialog.getByLabel('Type').selectOption('dynatrace')
      await dialog.getByLabel('Name', { exact: true }).fill('dynatrace-prod')
      await dialog.getByLabel('Environment URL').fill('https://abc12345.apps.dynatrace.com')
      await dialog.getByLabel('Secret name').fill('dynatrace')
      await dialog.getByLabel('ActiveGate URL').fill('https://activegate.example.internal:9999')
      await page.waitForTimeout(600)
      await dialog.screenshot({ path: `${dir}/dynatrace-datasource-form-${theme}.png` })
    })

    test(`dynatrace test result (${theme})`, async ({ page }) => {
      // Tall enough to hold the card without scrolling past the sticky header.
      await page.setViewportSize({ width: 1280, height: 2400 })
      await page.goto('/admin/datasources')
      await settle(page)
      const card = page.getByTestId('datasource-dynatrace-prod')
      await card.getByRole('button', { name: /Test/ }).click()
      await expect(card.getByTestId('metric-mapping')).toBeVisible()
      await page.waitForTimeout(600)
      await card.screenshot({ path: `${dir}/dynatrace-test-result-${theme}.png` })
    })

    test(`dynatrace readiness (${theme})`, async ({ page }) => {
      // Tall enough to hold the group without scrolling past the sticky header.
      await page.setViewportSize({ width: 1280, height: 3200 })
      await page.goto('/clusters/dt-prod-eu1?tab=readiness')
      await settle(page)
      for (const id of ['metrics.hpa']) await page.getByTestId(`check-${id}`).getByRole('button').click()
      await page.waitForTimeout(600)
      // The Metrics group of the checklist: the part that differs for a Dynatrace datasource.
      const metrics = page.locator('section', { has: page.getByTestId('check-metrics.node-labels') })
      await metrics.screenshot({ path: `${dir}/dynatrace-readiness-${theme}.png` })
    })
  })
}
