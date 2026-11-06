/**
 * Admin → Pricing against the MSW mock hub: the rate sources card (AWS Cost and Usage Report off,
 * in use and unreadable) and the per-cluster source chips.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, within } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { RouterProvider, createMemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import type { PricingRates } from '@/api/types'
import { formatPeriod } from '@/lib/rate-source'
import { TooltipProvider } from '@/components/ui/tooltip'
import { getAdmin, handlers, resetDb } from '@/mocks/handlers'
import PricingPage from './pricing'

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
  // Sign-in is required: read as a viewer.
  await signIn('viewer')
})
afterEach(cleanup)

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <RouterProvider router={createMemoryRouter([{ path: '/', element: <PricingPage /> }])} />
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

const setRates = (r: PricingRates) => {
  getAdmin().pricingRates = r
}

describe('Pricing page rate sources', () => {
  it('shows effective rates from the Cost and Usage Report and the sources per cluster', async () => {
    renderPage()
    const card = await screen.findByTestId('rate-sources-card')
    expect(card).toHaveTextContent('Effective rates from the AWS Cost and Usage Report')
    expect(card).toHaveTextContent('11 instance types')
    expect(card).toHaveTextContent("don't enter them as a discount")
    const table = within(card).getByTestId('rate-sources-table')
    const row = within(table).getByText('prod-us-east-1').closest('tr')!
    const chips = within(row).getByTestId('rate-sources')
    expect(chips).toHaveTextContent('CUR')
    expect(chips).toHaveTextContent('· 9')
    expect(chips).toHaveTextContent('list')
    expect(chips).toHaveTextContent('· 2')
    // The period is on the cur chip only.
    const period = formatPeriod(getAdmin().pricingRates.cur!.period)
    expect(period).not.toBe('')
    expect(chips).toHaveTextContent(period)
  })

  it('explains list prices when the reader is off', async () => {
    setRates({ clusters: [{ cluster: 'prod-eu-west-1', sources: [{ source: 'list', nodes: 5 }] }] })
    renderPage()
    const card = await screen.findByTestId('rate-sources-card')
    expect(card).toHaveTextContent('List prices')
    expect(card).toHaveTextContent('Turn on the AWS Cost and Usage Report')
    const chips = within(card).getByTestId('rate-sources')
    // A single source shows no count.
    expect(chips).toHaveTextContent(/^list$/)
  })

  it('warns when the report cannot be read', async () => {
    setRates({
      cur: { lastReadOK: false, rates: 0, lastSuccess: '2026-10-01T03:00:00Z' },
      clusters: [{ cluster: 'prod-eu-west-1', sources: [{ source: 'list', nodes: 5 }] }],
    })
    renderPage()
    const card = await screen.findByTestId('rate-sources-card')
    expect(card).toHaveTextContent('The AWS Cost and Usage Report cannot be read')
    expect(card).toHaveTextContent('priced at list prices again')
  })

  it('keeps working when the hub cannot read the pricing series', async () => {
    const st = getAdmin()
    // @ts-expect-error: the hub omits rates when its store is down
    st.pricingRates = undefined
    renderPage()
    const card = await screen.findByTestId('rate-sources-card')
    expect(card).toHaveTextContent('Rate sources are unavailable')
    expect(screen.getByText('Default discount')).toBeInTheDocument()
  })
})

describe('formatPeriod', () => {
  it('formats a period and ignores bad input', () => {
    expect(formatPeriod({ start: '2026-10-07T00:00:00Z', end: '2026-11-05T00:00:00Z' })).toMatch(/–/)
    expect(formatPeriod(undefined)).toBe('')
    expect(formatPeriod({ start: 'x', end: 'y' })).toBe('')
  })
})
