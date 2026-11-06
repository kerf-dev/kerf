import type { Page } from '@playwright/test'

/**
 * Signing in is required. Starts the page with a mock-API session for `user` (admin, operator or
 * viewer), once per test: a later sign-out or sign-in in the test is kept across navigations.
 */
export async function seedSession(page: Page, user = 'viewer') {
  await page.addInitScript((username) => {
    if (sessionStorage.getItem('e2e-session-seeded')) return
    sessionStorage.setItem('e2e-session-seeded', '1')
    localStorage.setItem(
      'ro-mock-session',
      JSON.stringify({
        username,
        csrfToken: 'e2e-csrf-token',
        expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
      }),
    )
  }, user)
}
