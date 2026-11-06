/**
 * Realized savings on the Overview and the workload page, against the MSW mock hub. Realized and
 * estimated amounts are never added together or shown with the same label.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { http, HttpResponse } from 'msw'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

vi.setConfig({ testTimeout: 30_000 })
import type { FleetSummary, RealizedSavings } from '@/api/types'
import { ThemeProvider } from '@/components/theme-provider'
import { TooltipProvider } from '@/components/ui/tooltip'
import { formatUSD } from '@/lib/format'
import { getDb, handlers, resetDb } from '@/mocks/handlers'
import { OverviewPage } from './overview'
import WorkloadPage from './workload'

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

const server = setupServer(...handlers)
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
beforeEach(async () => {
  localStorage.clear()
  resetDb()
  // Sign-in is required: read as a viewer.
  const r = await fetch(`${window.location.origin}/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: 'viewer', password: 'dev-password' }),
  })
  expect(r.status).toBe(200)
})
afterEach(() => {
  cleanup()
  server.resetHandlers()
})

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ThemeProvider>
        <TooltipProvider>
          <MemoryRouter initialEntries={[path]}>
            <Routes>
              <Route path="/" element={<OverviewPage />} />
              <Route path="/workloads/:cluster/:namespace/:kind/:name" element={<WorkloadPage />} />
            </Routes>
          </MemoryRouter>
        </TooltipProvider>
      </ThemeProvider>
    </QueryClientProvider>,
  )
}

const json = async <T,>(path: string) => (await (await fetch(`${window.location.origin}${path}`)).json()) as T

describe('Overview: realized savings', () => {
  it('shows realized and estimated amounts apart, never summed, never with the same label', async () => {
    renderAt('/')
    const card = await screen.findByTestId('realized-card')
    const label = await within(card).findByTestId('realized-label', {}, { timeout: 8000 })
    expect(label.textContent).toMatch(/^Realized since \d{1,2} [A-Z][a-z]{2} \d{4}$/)

    const realized = await json<RealizedSavings>(
      `/v1/savings/realized?step=month&from=${new Date(getDb().now - 365 * 86400_000).toISOString()}`,
    )
    const summary = await json<FleetSummary>('/v1/summary')
    const estimated = summary.fleet.monthlySavingsUSD.total
    expect(realized.total.total).toBeGreaterThan(0)
    expect(estimated).toBeGreaterThan(0)

    const total = within(card).getByTestId('realized-total')
    expect(total.textContent).toBe(formatUSD(realized.total.total, { cents: realized.total.total < 100 }))
    const kpi = await screen.findByTestId('kpi-savings', {}, { timeout: 8000 })
    expect(kpi.textContent).toBe(formatUSD(estimated))

    // Different labels: the estimate is "Estimated (if applied)", the realized card never says "Estimated".
    expect(screen.getByText('Estimated (if applied)')).toBeInTheDocument()
    expect(card.textContent).not.toMatch(/estimated/i)
    const tile = kpi.closest('[data-slot="card"]') ?? kpi.parentElement!.parentElement!.parentElement!
    expect(tile.textContent).not.toMatch(/realized since/i)

    // Never added together, anywhere on the page.
    const page = document.body.textContent ?? ''
    for (const sum of [estimated + realized.total.total, estimated + realized.inRange.total]) {
      expect(page).not.toContain(formatUSD(sum))
      expect(page).not.toContain(formatUSD(sum, { cents: true }))
    }

    expect(within(card).getByRole('img', { name: 'Realized savings by month' })).toBeInTheDocument()
    expect(within(card).getByTestId('released-now').textContent).toMatch(/cores/)
  })

  it('switches to 30 days by day', async () => {
    renderAt('/')
    const card = await screen.findByTestId('realized-card')
    await within(card).findByTestId('realized-label', {}, { timeout: 8000 })
    within(card).getByRole('radio', { name: 'Daily' }).click()
    expect(await within(card).findByRole('img', { name: 'Realized savings by day' })).toBeInTheDocument()
  })

  it('has an empty state before anything was applied', async () => {
    server.use(
      http.get('*/v1/savings/realized', ({ request }) => {
        const u = new URL(request.url)
        const body: RealizedSavings = {
          from: u.searchParams.get('from')!,
          to: u.searchParams.get('to')!,
          step: 'month',
          total: { cpu: 0, memory: 0, total: 0 },
          inRange: { cpu: 0, memory: 0, total: 0 },
          releasedNow: { cpuCores: 0, memoryBytes: 0 },
          points: [],
          coverage: {
            workloadHours: 0,
            replicasFromRecommendations: 0,
            withoutReplicas: 0,
            defaultPricing: 0,
          },
        }
        return HttpResponse.json(body)
      }),
    )
    renderAt('/')
    const card = await screen.findByTestId('realized-card')
    expect(await within(card).findByText('Nothing realized yet')).toBeInTheDocument()
    expect(within(card).queryByTestId('realized-total')).toBeNull()
  })

  it('labels the node cost trend as correlated, not caused', async () => {
    renderAt('/')
    const card = await screen.findByTestId('node-cost-card')
    expect(within(card).getByTestId('correlated-badge')).toHaveTextContent('correlated, not caused')
    expect(
      await within(card).findByRole(
        'img',
        { name: /Node cost per hour and released CPU requests for / },
        { timeout: 8000 },
      ),
    ).toBeInTheDocument()
    expect(await within(card).findByRole('combobox', { name: 'Cluster' })).toBeInTheDocument()
  })
})

describe('Workload page: realized savings', () => {
  it('shows the workload realized line apart from its estimate', async () => {
    const w = getDb().workloads.find((x) => x.appliedAt && x.phase === 'Applied' && x.recommendation)!
    renderAt(`/workloads/${w.cluster}/${w.namespace}/${w.kind}/${w.name}`)
    const box = await screen.findByTestId('workload-realized', {}, { timeout: 8000 })
    expect(box.textContent).toMatch(/^Realized since /)
    expect(box.textContent).toMatch(/in the last 30 days/)
    const est = screen.getByTestId('workload-estimated')
    expect(est.textContent).toMatch(/^Estimated \(if applied\)/)
    expect(est.textContent).not.toMatch(/realized/i)
    await waitFor(() => expect(box.textContent).not.toMatch(/estimated/i))
  })
})
