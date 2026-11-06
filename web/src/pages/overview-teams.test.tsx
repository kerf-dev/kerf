/**
 * Overview and teams against the MSW mock hub: "Savings by team" (largest first, Unassigned last,
 * linking to the filtered list), absent while no team exists, and no fleet-only panels (node cost)
 * or requests for them for a team-scoped user.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { MemoryRouter, useLocation } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { ThemeProvider } from '@/components/theme-provider'
import { TooltipProvider } from '@/components/ui/tooltip'
import { getAdmin, handlers, resetDb } from '@/mocks/handlers'
import { OverviewPage } from './overview'

vi.setConfig({ testTimeout: 30_000 })

// ECharts needs a canvas; the charts' data is not what these tests check.
vi.mock('@/components/charts/theme', async (importOriginal) => {
  const orig = await importOriginal<typeof import('@/components/charts/theme')>()
  const rgb = 'rgb(128, 128, 128)'
  const colors = new Proxy(
    { series: [rgb, rgb, rgb, rgb, rgb, rgb] },
    { get: (t, k) => (k in t ? t[k as 'series'] : rgb) },
  )
  return { ...orig, useChartColors: () => colors }
})
vi.mock('@/components/charts/chart', () => ({
  Chart: ({ ariaLabel }: { ariaLabel: string }) => <div role="img" aria-label={ariaLabel} />,
}))

const requested: string[] = []
const server = setupServer(...handlers)
server.events.on('request:start', ({ request }) => {
  requested.push(new URL(request.url).pathname)
})
beforeAll(() => {
  server.listen({ onUnhandledRequest: 'bypass' })
  window.matchMedia ??= ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    onchange: null,
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})
afterAll(() => server.close())
beforeEach(() => {
  localStorage.clear()
  resetDb()
  requested.length = 0
})
afterEach(() => {
  cleanup()
  server.resetHandlers()
})

async function signIn(username: string) {
  const r = await fetch(`${window.location.origin}/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password: 'dev-password' }),
  })
  expect(r.status).toBe(200)
}

function Where() {
  const l = useLocation()
  return <div data-testid="where">{l.pathname + l.search}</div>
}

function renderOverview() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ThemeProvider>
        <TooltipProvider>
          <MemoryRouter>
            <OverviewPage />
            <Where />
          </MemoryRouter>
        </TooltipProvider>
      </ThemeProvider>
    </QueryClientProvider>,
  )
}

describe('Overview: savings by team', () => {
  it('lists teams largest first with Unassigned last, linking to the filtered list', async () => {
    await signIn('viewer')
    renderOverview()
    const card = await screen.findByTestId('team-savings')
    const keys = within(card)
      .getAllByRole('button')
      .map((b) => b.getAttribute('data-testid'))
    expect(keys.at(-1)).toBe('team-savings-unassigned')
    expect(keys).toHaveLength(4)
    const amounts = within(card)
      .getAllByRole('button')
      .slice(0, -1)
      .map((b) => Number((b.textContent ?? '').match(/\$([\d,]+)/)?.[1]?.replace(/,/g, '')))
    expect(amounts).toEqual([...amounts].sort((a, b) => b - a))
    fireEvent.click(within(card).getByTestId('team-savings-payments'))
    await waitFor(() => expect(screen.getByTestId('where')).toHaveTextContent('/opportunities?team=payments'))
  })

  it('is absent while no team exists', async () => {
    await signIn('viewer')
    getAdmin().teams = []
    renderOverview()
    await screen.findByTestId('realized-card')
    await screen.findByText('Estimated savings by cluster')
    expect(screen.queryByTestId('team-savings')).not.toBeInTheDocument()
  })

  it('a team-scoped user sees only its team, no node cost, and never asks for it', async () => {
    await signIn('paula')
    renderOverview()
    const card = await screen.findByTestId('team-savings')
    expect(within(card).getAllByRole('button')).toHaveLength(1)
    expect(screen.queryByTestId('team-savings-unassigned')).not.toBeInTheDocument()
    await screen.findByTestId('realized-card')
    expect(screen.queryByTestId('node-cost-card')).not.toBeInTheDocument()
    expect(requested.filter((p) => /node-cost|capacity|readiness|\/v1\/admin\/(?!teams)/.test(p))).toEqual([])
  })
})
