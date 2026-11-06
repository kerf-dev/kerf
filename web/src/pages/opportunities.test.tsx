/** Opportunities: the GPU filter (URL param, facet counts) and the GPU column. */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { MemoryRouter, useLocation } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { handlers, resetDb } from '@/mocks/handlers'
import OpportunitiesPage from './opportunities'

const server = setupServer(...handlers)
beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
  // cmdk (the filter list) measures and scrolls
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  Element.prototype.scrollIntoView ??= () => {}
})
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

/** The 400-row table renders slowly in jsdom, more so when the machine is busy. */
const SLOW = { timeout: 15_000 }

function Where() {
  const l = useLocation()
  return <span data-testid="search">{l.search}</span>
}

function renderPage(url = '/opportunities') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <MemoryRouter initialEntries={[url]}>
          <OpportunitiesPage />
          <Where />
        </MemoryRouter>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

const gpuHeader = () => screen.queryByRole('columnheader', { name: 'GPU' })
const rows = () => screen.queryAllByTestId('opportunity-row')

describe('Opportunities GPU filter', { timeout: 30_000 }, () => {
  it('offers "any" first, then every status, with facet counts', async () => {
    renderPage()
    await screen.findAllByTestId('opportunity-row', {}, SLOW)
    fireEvent.click(screen.getByRole('button', { name: /GPU/ }))
    const options = within(await screen.findByRole('listbox', {}, SLOW)).getAllByRole('option')
    expect(options.map((o) => o.textContent?.replace(/\d+$/, '').trim())).toEqual([
      'Any GPU workload',
      'Idle',
      'Under-used',
      'OK',
      'No data',
    ])
    await waitFor(() =>
      expect(options.map((o) => o.textContent?.match(/\d+$/)?.[0])).toEqual(['5', '1', '2', '1', '1']),
    )
  })

  it('selecting a value sets ?gpu= and narrows the table to those workloads', async () => {
    renderPage()
    await screen.findAllByTestId('opportunity-row', {}, SLOW)
    fireEvent.click(screen.getByRole('button', { name: /GPU/ }))
    fireEvent.click(await screen.findByRole('option', { name: /Idle/ }, SLOW))
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent('?gpu=idle'))
    await waitFor(() => expect(rows()).toHaveLength(1), SLOW)
    expect(within(rows()[0]!).getByTestId('gpu-status')).toHaveAttribute('data-status', 'idle')
    expect(screen.getByText(/match your filters/)).toBeInTheDocument()
  })

  it('reads the filter from the URL: several values match if any does', async () => {
    renderPage('/opportunities?gpu=idle&gpu=no-data')
    await waitFor(() => expect(rows()).toHaveLength(2), SLOW)
    expect(
      rows()
        .map((r) => within(r).getByTestId('gpu-status').getAttribute('data-status'))
        .sort(),
    ).toEqual(['idle', 'no-data'])
  })
})

describe('Opportunities GPU column', { timeout: 30_000 }, () => {
  it('shows status, p95 utilisation and monthly cost; "–" when unpriced', async () => {
    renderPage('/opportunities?gpu=any')
    await waitFor(() => expect(rows()).toHaveLength(5), SLOW)
    expect(gpuHeader()).toBeInTheDocument()
    const byStatus = (s: string) =>
      screen.getAllByTestId('gpu-cell').find((c) => within(c).getByTestId('gpu-status').dataset.status === s)!
    expect(byStatus('under-used')).toHaveTextContent(/p95 \d+(\.\d)? %/)
    expect(byStatus('idle')).toHaveTextContent('p95 1.8 %')
    expect(byStatus('idle')).toHaveTextContent(/\$[\d,]+ \/ mo/)
    expect(byStatus('no-data')).toHaveTextContent('–')
    expect(byStatus('no-data')).not.toHaveTextContent('$')
  })

  it('follows the data: shown while the workloads include GPU workloads, hidden otherwise', async () => {
    renderPage()
    await screen.findAllByTestId('opportunity-row', {}, SLOW)
    await waitFor(() => expect(gpuHeader()).toBeInTheDocument())
    cleanup()
    // a namespace without GPU workloads: no column
    renderPage('/opportunities?namespace=payments')
    await screen.findAllByTestId('opportunity-row', {}, SLOW)
    await waitFor(() => expect(screen.getByText(/workloads/)).toBeInTheDocument())
    expect(gpuHeader()).not.toBeInTheDocument()
  })

  it('an explicit choice wins over the data', async () => {
    localStorage.setItem('ro-opportunities-columns-v1', JSON.stringify({ gpu: false }))
    renderPage('/opportunities?gpu=any')
    await waitFor(() => expect(rows()).toHaveLength(5), SLOW)
    expect(gpuHeader()).not.toBeInTheDocument()
  })

  it('keeps the sort order and page size of the table', async () => {
    renderPage('/opportunities?gpu=any&sort=name&size=25')
    await waitFor(() => expect(rows()).toHaveLength(5), SLOW)
    const names = rows().map((r) => within(r).getAllByRole('link')[0]!.textContent!)
    expect([...names].sort((a, b) => a.localeCompare(b))).toEqual(names)
    expect(screen.getByTestId('search')).toHaveTextContent('gpu=any')
  })
})
