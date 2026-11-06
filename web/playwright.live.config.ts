import { defineConfig, devices } from '@playwright/test'

/**
 * Browser tests through a real web proxy and hub (no mocks), e.g. the k3d dev fleet. Run by the Go e2e
 * suite (test/e2e, TestWebSignInBrowser) or by hand:
 *   E2E_WEB_URL=http://localhost:19090 npx playwright test -c playwright.live.config.ts
 */
export default defineConfig({
  testDir: './e2e-live',
  timeout: 60_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: process.env.E2E_WEB_URL ?? 'http://localhost:19090',
    viewport: { width: 1440, height: 900 },
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 900 } } },
  ],
})
