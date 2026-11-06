/**
 * Opportunities and teams against the MSW mock hub: the Team facet synced to `?team=`, the Team
 * column, "Group by team", and team-scoped callers (only their teams, no unassigned).
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { MemoryRouter, useLocation } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { handlers, resetDb } from '@/mocks/handlers'
import OpportunitiesPage from './opportunities'

vi.setConfig({ testTimeout: 30_000 })

const server = setupServer(...handlers)
beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  Element.prototype.scrollIntoView ??= () => {}
})
afterAll(() => server.close())
beforeEach(() => {
  localStorage.clear()
  resetDb()
})
afterEach(cleanup)

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
  return <div data-testid="where">{l.search}</div>
}

function renderPage(url: string) {
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

const rows = () => screen.queryAllByTestId('opportunity-row')

describe('Opportunities: teams', () => {
  it('filters by ?team= and shows the team column and the facet', async () => {
    await signIn('viewer')
    renderPage('/opportunities?team=payments')
    await waitFor(() => expect(rows().length).toBeGreaterThan(0))
    for (const r of rows()) expect(within(r).getByTestId('team-chip')).toHaveTextContent('Payments')
    const filters = screen.getByRole('search', { name: 'Filter workloads' })
    expect(within(filters).getByRole('button', { name: /Team\s*Payments/ })).toBeInTheDocument()
  })

  it('adds the team to the URL from the facet, with Unassigned labelled', async () => {
    await signIn('viewer')
    renderPage('/opportunities')
    await waitFor(() => expect(rows().length).toBeGreaterThan(0))
    fireEvent.click(within(screen.getByRole('search')).getByRole('button', { name: /^Team/ }))
    fireEvent.click(await screen.findByRole('option', { name: /Unassigned/ }))
    await waitFor(() => expect(screen.getByTestId('where')).toHaveTextContent('team=unassigned'))
    await waitFor(() => {
      expect(rows().length).toBeGreaterThan(0)
      for (const r of rows()) expect(within(r).getByTestId('team-chip')).toHaveTextContent('Unassigned')
    })
  })

  it('groups the rows of the page by team, Unassigned last', async () => {
    await signIn('viewer')
    renderPage('/opportunities?size=100&group=team')
    await waitFor(() => expect(screen.getAllByTestId('team-group').length).toBeGreaterThan(1))
    const heads = screen.getAllByTestId('team-group').map((h) => h.textContent ?? '')
    expect(heads.at(-1)).toMatch(/^Unassigned/)
    expect(heads[0]).toMatch(/on this page · \$/)
    expect(screen.getByTestId('group-by-team')).toBeChecked()
  })

  it('shows no team controls while no team exists', async () => {
    await signIn('viewer')
    const { getAdmin } = await import('@/mocks/handlers')
    getAdmin().teams = []
    renderPage('/opportunities')
    await waitFor(() => expect(rows().length).toBeGreaterThan(0))
    expect(screen.queryByTestId('group-by-team')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Team/ })).not.toBeInTheDocument()
    expect(screen.queryByTestId('team-chip')).not.toBeInTheDocument()
  })

  it('lists only the workloads of a team-scoped user, without Unassigned', async () => {
    await signIn('paula')
    renderPage('/opportunities')
    await waitFor(() => expect(rows().length).toBeGreaterThan(0))
    for (const r of rows()) expect(within(r).getByTestId('team-chip')).toHaveTextContent('Payments')
    fireEvent.click(within(screen.getByRole('search')).getByRole('button', { name: /^Team/ }))
    expect(await screen.findByRole('option', { name: /Payments/ })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: /Unassigned/ })).not.toBeInTheDocument()
  })
})
