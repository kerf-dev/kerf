/**
 * Admin → Maintenance against the MSW mock hub: versions per component, mismatch highlight,
 * every update state, and "Check now" for admins only.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import type { UpdateStatus } from '@/api/types'
import { TooltipProvider } from '@/components/ui/tooltip'
import { getAdmin, handlers, resetDb } from '@/mocks/handlers'
import MaintenancePage from './maintenance'

const server = setupServer(...handlers)
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterAll(() => server.close())
async function signIn(username: string) {
  const r = await fetch(`${window.location.origin}/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password: 'dev-password' }),
  })
  expect(r.status).toBe(200)
}

beforeEach(async () => {
  localStorage.clear()
  resetDb()
  // Sign-in is required: read as a viewer unless a test signs in as someone else.
  await signIn('viewer')
})
afterEach(cleanup)

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <MemoryRouter>
          <MaintenancePage />
        </MemoryRouter>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

const setUpdate = (patch: Partial<UpdateStatus>) => {
  const m = getAdmin().maintenance
  m.update = { ...m.update, ...patch }
}

describe('Maintenance page', () => {
  it('shows versions by component and highlights mismatches', async () => {
    await signIn('admin')
    renderPage()
    const hub = await screen.findByTestId('hub-components')
    expect(within(hub).getAllByTestId('leader-badge')).toHaveLength(1)
    expect(within(hub).getAllByText('hub')).toHaveLength(2)
    expect(within(hub).getByText('pricing')).toBeInTheDocument()
    // This UI's bundle is a dev build in tests: not compared.
    expect(within(hub).getByTestId('component-web')).toHaveAttribute('data-compat', 'unknown')

    const clusters = screen.getByTestId('cluster-components')
    expect(within(clusters).getByTestId('cluster-prod-eu-west-1')).toHaveAttribute('data-compat', 'same')
    const older = within(clusters).getByTestId('cluster-prod-us-east-1')
    expect(older).toHaveAttribute('data-compat', 'older')
    expect(older.className).toMatch(/bg-warning/)
    expect(within(older).getByTestId('compat-message')).toHaveTextContent(/Older than the hub \(0\.4\.2\)/)
    expect(within(older).getByTestId('webhook-missing')).toHaveTextContent('not installed')
    expect(within(clusters).getByTestId('cluster-prod-eu-west-1')).toHaveTextContent('(2 pods)')
    expect(within(clusters).getByTestId('cluster-dr-eu-central-1')).toHaveAttribute('data-compat', 'unknown')
  })

  it('shows the update and the upgrade commands', async () => {
    renderPage()
    const card = await screen.findByTestId('updates-card')
    expect(within(card).getByTestId('update-state')).toHaveAttribute('data-state', 'update_available')
    expect(within(card).getByTestId('current-version')).toHaveTextContent('0.4.2')
    expect(within(card).getByTestId('latest-version')).toHaveTextContent('0.5.0')
    expect(within(card).getByTestId('release-notes')).toHaveAttribute(
      'href',
      'https://github.com/kerf-dev/kerf/releases/tag/v0.5.0',
    )
    expect(within(card).getByTestId('hub-command')).toHaveTextContent('--version 0.5.0')
    expect(within(card).getByTestId('spoke-command')).toHaveTextContent('values-spoke-<cluster>.yaml')
    expect(within(card).getByText(/sends nothing about this installation/)).toBeInTheDocument()
  })

  it.each([
    [{ state: 'up_to_date', latestVersion: '0.4.2' }, /latest stable release/],
    [{ state: 'failed', error: 'dial tcp: lookup api.github.com: no such host' }, /no such host/],
    [{ state: 'disabled', enabled: false }, /hub\.updateCheck\.enabled: false/],
    [
      { state: 'pending', checkedAt: undefined, latestVersion: undefined, lastSuccessAt: undefined },
      /first check/,
    ],
    [{ state: 'dev_build', currentVersion: 'dev-3f2a9c1' }, /development build \(dev-3f2a9c1\)/],
  ] as [Partial<UpdateStatus>, RegExp][])('renders state %o', async (patch, text) => {
    setUpdate(patch)
    renderPage()
    const card = await screen.findByTestId('updates-card')
    expect(within(card).getByTestId('update-state')).toHaveAttribute('data-state', patch.state)
    expect(within(card).getByText(text)).toBeInTheDocument()
    expect(within(card).queryByTestId('upgrade-commands')).toBeNull()
  })

  it('says when no stable release is published yet', async () => {
    setUpdate({
      state: 'up_to_date',
      latestVersion: undefined,
      releaseName: undefined,
      releaseURL: undefined,
      publishedAt: undefined,
    })
    renderPage()
    const card = await screen.findByTestId('updates-card')
    expect(within(card).getByTestId('no-release')).toHaveTextContent('No stable release published yet.')
    expect(within(card).queryByTestId('latest-version')).toBeNull()
    expect(within(card).queryByTestId('hub-command')).toBeNull()
  })

  it('offers Check now to admins only and refreshes the result', async () => {
    await signIn('operator')
    renderPage()
    expect(await screen.findByTestId('check-now')).toBeDisabled()
    cleanup()

    await signIn('admin')
    setUpdate({ checkedAt: new Date(Date.now() - 2 * 3600_000).toISOString() })
    renderPage()
    // The tooltip (role hint) goes away once /v1/me says admin, which remounts the button.
    await waitFor(() => expect(screen.getByTestId('check-now')).toBeEnabled())
    screen.getByTestId('check-now').click()
    await waitFor(() =>
      expect(Date.now() - Date.parse(getAdmin().maintenance.update.checkedAt!)).toBeLessThan(5000),
    )
    await waitFor(() => expect(screen.getByTestId('updates-card')).toHaveTextContent(/Checked just now/))
  })

  it('disables Check now when the check is off', async () => {
    await signIn('admin')
    setUpdate({ enabled: false, state: 'disabled' })
    renderPage()
    await screen.findByTestId('updates-card')
    expect(screen.getByTestId('check-now')).toBeDisabled()
  })
})
