/**
 * Visual capture of every page in light and dark mode. Skipped unless SCREENSHOTS_DIR is set:
 *   SCREENSHOTS_DIR=../docs/screenshots npx playwright test screenshots
 */
import { expect, test } from '@playwright/test'

const dir = process.env.SCREENSHOTS_DIR
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
      await expect(page.locator('[aria-busy=true], .animate-pulse')).toHaveCount(0, { timeout: 15_000 })
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
] as const) {
  test(`${name} (tablet)`, async ({ page }) => {
    test.skip(!dir, 'set SCREENSHOTS_DIR to capture')
    test.slow()
    await page.setViewportSize({ width: 820, height: 1180 })
    await page.goto(path)
    await expect(page.locator('main')).toBeVisible()
    await page.waitForLoadState('networkidle')
    await expect(page.locator('[aria-busy=true], .animate-pulse')).toHaveCount(0, { timeout: 15_000 })
    await page.waitForTimeout(900)
    await page.screenshot({ path: `${dir}/${name}-tablet.png`, fullPage: true })
  })
}
