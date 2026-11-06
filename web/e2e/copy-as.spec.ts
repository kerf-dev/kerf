import { expect, test, type Page } from '@playwright/test'

/**
 * "Copy as…" on the workload page and the Opportunities bulk bar, against the MSW mock hub:
 * keyboard-only use, clipboard contents, the preview dialog, light and dark themes, and viewers
 * (the menu only reads what the page shows, so every role gets it).
 */

test.use({ permissions: ['clipboard-read', 'clipboard-write'] })

const clipboard = (page: Page) =>
  page.evaluate(() =>
    (navigator as unknown as { clipboard: { readText(): Promise<string> } }).clipboard.readText(),
  )

const WORKLOAD = '/workloads/prod-eu-west-1/payments/Deployment/checkout-api'

test.describe('copy as', () => {
  test('workload page: keyboard-only copy as a strategic merge patch', async ({ page }) => {
    await page.goto(WORKLOAD)
    const trigger = page.getByTestId('copy-as')
    await expect(trigger).toBeVisible()
    await trigger.focus()
    await page.keyboard.press('Enter')
    await expect(page.getByTestId('copy-as-menu')).toBeVisible()
    await page.keyboard.press('Enter') // the first item: strategic merge patch
    await expect(page.getByText('Copied as Strategic merge patch')).toBeVisible()
    const text = await clipboard(page)
    expect(text).toMatch(/^# Deployment payments\/checkout-api/)
    expect(text).toContain('apiVersion: apps/v1')
    expect(text).toContain('kind: Deployment')
    expect(text).toMatch(/containers:\n\s+- name: /)
    // The menu closes and focus returns to the button.
    await expect(page.getByTestId('copy-as-menu')).toHaveCount(0)
    await expect(trigger).toBeFocused()
  })

  test('workload page: every format copies', async ({ page }) => {
    await page.goto(WORKLOAD)
    const expected: Record<string, RegExp> = {
      patch: /kind: Deployment/,
      kubectl: /kubectl patch deployment 'checkout-api' -n 'payments' --type strategic -p '\{"spec"/,
      kustomize: /^patches:\n\s+# Deployment payments\/checkout-api[\s\S]*target:[\s\S]*kind: Deployment/m,
      helm: /\n[a-z][\w-]*:\n\s+resources:\n\s+requests:/,
    }
    for (const [id, re] of Object.entries(expected)) {
      await page.getByTestId('copy-as').click()
      await page.getByTestId(`copy-as-${id}`).click()
      await expect(page.getByTestId('toast').last()).toContainText('Copied as')
      await expect(page.getByTestId('copy-as-menu')).toHaveCount(0)
      expect(await clipboard(page), id).toMatch(re)
    }
  })

  for (const scheme of ['light', 'dark'] as const) {
    test(`workload page: preview dialog in ${scheme} mode`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme })
      await page.goto(WORKLOAD)
      await page.getByTestId('copy-as').click()
      await page.getByTestId('copy-as-preview').click()
      const dialog = page.getByTestId('copy-as-dialog')
      await expect(dialog).toBeVisible()
      const out = dialog.getByTestId('copy-as-output')
      await expect(out).toContainText('kind: Deployment')
      await dialog.getByRole('radio', { name: 'kubectl patch' }).click()
      await expect(out).toContainText('kubectl patch deployment')
      // The highlighter uses theme tokens, so keys render in both themes.
      await expect(out.locator('span.text-info').first()).toBeVisible()
      await dialog.getByTestId('copy-as-copy').click()
      expect(await clipboard(page)).toContain('kubectl patch deployment')
      await page.keyboard.press('Escape')
      await expect(dialog).toHaveCount(0)
    })
  }

  test('opportunities: bulk copy joins the selected workloads', async ({ page }) => {
    await page.goto('/opportunities?namespace=payments')
    const rows = page.getByTestId('select-row')
    await rows.nth(0).click()
    await rows.nth(1).click()
    const bar = page.getByTestId('bulk-bar')
    await expect(bar).toContainText('2 selected')
    await bar.getByTestId('bulk-copy-as').click()
    await page.getByTestId('bulk-copy-as-patch').click()
    await expect(page.getByText('Copied as Strategic merge patch')).toBeVisible()
    const text = await clipboard(page)
    expect(text.match(/^---$/gm)).toHaveLength(1)
    expect(text.match(/^apiVersion: /gm)).toHaveLength(2)
    // Kustomize merges them into one patches: list instead.
    await expect(page.getByTestId('bulk-copy-as-menu')).toHaveCount(0)
    await bar.getByTestId('bulk-copy-as').click()
    await page.getByTestId('bulk-copy-as-kustomize').click()
    const k = await clipboard(page)
    expect(k.match(/^patches:$/gm)).toHaveLength(1)
    expect(k.match(/^ {2}- target:$/gm)).toHaveLength(2)
  })

  test('opportunities: a viewer can select rows and copy but not change modes', async ({ page }) => {
    await page.goto('/login?next=%2Fopportunities%3Fnamespace%3Dpayments')
    await page.getByLabel('Username').fill('viewer')
    await page.getByLabel('Password', { exact: true }).fill('dev-password')
    await page.getByTestId('login-submit').click()
    await expect(page.getByTestId('user-menu')).toBeVisible()
    await page.getByTestId('select-row').first().click()
    const bar = page.getByTestId('bulk-bar')
    await expect(bar).toContainText('1 selected')
    await expect(bar.getByTestId('bulk-copy-as')).toBeEnabled()
    await expect(bar.getByTestId('bulk-set-mode')).toHaveCount(0)
    await expect(bar.getByTestId('bulk-exclude')).toHaveCount(0)
  })
})
