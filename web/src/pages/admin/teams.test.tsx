/**
 * Admin → Teams against the MSW mock hub: the list with the Unassigned row and the ownership
 * explanation, creating, editing and deleting a team, the id validation and the read-only view.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { getAdmin, handlers, resetDb } from '@/mocks/handlers'
import { parseLabels, parseList } from '@/lib/teams'
import TeamsPage from './teams'

vi.setConfig({ testTimeout: 30_000 })

const server = setupServer(...handlers)
beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
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

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <MemoryRouter>
          <TeamsPage />
        </MemoryRouter>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

async function confirmDialog(dialog: HTMLElement, reason = 'because we said so') {
  fireEvent.change(within(dialog).getByTestId('reason'), { target: { value: reason } })
  fireEvent.click(within(dialog).getByTestId('confirm-write'))
}

describe('list parsing', () => {
  it('splits lists and key=value lines', () => {
    expect(parseList('a, b\nb  c')).toEqual(['a', 'b', 'c'])
    expect(parseLabels('env=prod\n\n tier = web ').labels).toEqual({ env: 'prod', tier: 'web' })
    expect(parseLabels('env').bad).toBe('env')
  })
})

describe('Teams page', () => {
  it('lists teams with selectors, owner values, workloads and savings, plus Unassigned', async () => {
    await signIn('admin')
    renderPage()
    expect(await screen.findByRole('heading', { level: 1, name: 'Teams' })).toBeInTheDocument()
    const payments = await screen.findByTestId('team-payments')
    expect(within(payments).getByText('Payments')).toBeInTheDocument()
    expect(within(payments).getByTestId('team-selectors')).toHaveTextContent('namespaces payments')
    expect(within(payments).getByText('payments-squad')).toBeInTheDocument()
    expect(within(payments).getByRole('link', { name: /workloads of Payments/ })).toHaveAttribute(
      'href',
      '/opportunities?team=payments',
    )
    expect(within(screen.getByTestId('team-risk')).getByTestId('team-selectors')).toHaveTextContent(
      'in clusters prod-*',
    )
    const un = screen.getByTestId('team-unassigned')
    expect(within(un).getByRole('link')).toHaveAttribute('href', '/opportunities?team=unassigned')
  })

  it('explains how ownership resolves with the owner label', async () => {
    await signIn('admin')
    getAdmin().ownerLabel = 'owner.example/team'
    renderPage()
    const help = await screen.findByTestId('ownership-help')
    await waitFor(() => expect(help).toHaveTextContent('owner.example/team'))
    expect(help).toHaveTextContent('first team (by id)')
    expect(help).toHaveTextContent('unassigned')
  })

  it('creates a team and shows it with its workloads', async () => {
    await signIn('admin')
    renderPage()
    await screen.findByTestId('team-payments')
    fireEvent.click(screen.getByTestId('new-team'))
    const dialog = await screen.findByTestId('team-dialog')
    fireEvent.change(within(dialog).getByTestId('team-id'), { target: { value: 'lending' } })
    fireEvent.change(within(dialog).getByTestId('team-display-name'), { target: { value: 'Lending' } })
    fireEvent.change(within(dialog).getByTestId('team-namespaces'), { target: { value: 'lending' } })
    await confirmDialog(dialog)
    const row = await screen.findByTestId('team-lending')
    expect(within(row).getByText('Lending')).toBeInTheDocument()
    expect(within(row).getByTestId('team-selectors')).toHaveTextContent('namespaces lending')
    expect(getAdmin().teams.some((t) => t.id === 'lending')).toBe(true)
    expect(Number(within(row).getByRole('link').textContent)).toBeGreaterThan(0)
  })

  it('rejects an invalid or reserved id before sending', async () => {
    await signIn('admin')
    renderPage()
    await screen.findByTestId('team-payments')
    fireEvent.click(screen.getByTestId('new-team'))
    const dialog = await screen.findByTestId('team-dialog')
    for (const [id, msg] of [
      ['Bad_ID', /Lowercase letters/],
      ['unassigned', /reserved/],
    ] as const) {
      fireEvent.change(within(dialog).getByTestId('team-id'), { target: { value: id } })
      await confirmDialog(dialog)
      expect(await within(dialog).findByText(msg)).toBeInTheDocument()
    }
    expect(getAdmin().teams).toHaveLength(3)
  })

  it('edits a team (the id is fixed) and deletes it with a reason', async () => {
    await signIn('admin')
    renderPage()
    await screen.findByTestId('team-cards')
    fireEvent.pointerDown(screen.getByRole('button', { name: 'Actions for cards' }), {
      button: 0,
      ctrlKey: false,
    })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Edit' }))
    const dialog = await screen.findByTestId('team-dialog')
    expect(within(dialog).getByTestId('team-id')).toBeDisabled()
    fireEvent.change(within(dialog).getByTestId('team-owner-values'), { target: { value: 'cards-squad' } })
    await confirmDialog(dialog)
    await waitFor(() =>
      expect(getAdmin().teams.find((t) => t.id === 'cards')!.ownerValues).toEqual(['cards-squad']),
    )
    expect(getAdmin().teams.find((t) => t.id === 'cards')!.version).toBe(2)

    fireEvent.pointerDown(await screen.findByRole('button', { name: 'Actions for cards' }), { button: 0 })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete' }))
    await confirmDialog(await screen.findByTestId('delete-team-dialog'))
    await waitFor(() => expect(screen.queryByTestId('team-cards')).not.toBeInTheDocument())
    expect(getAdmin().teams.map((t) => t.id)).toEqual(['payments', 'risk'])
  })

  it('is read-only for viewers', async () => {
    await signIn('viewer')
    renderPage()
    await screen.findByTestId('team-payments')
    expect(screen.queryByTestId('new-team')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Actions for/ })).not.toBeInTheDocument()
    expect(screen.getByText(/Changes need the admin role/)).toBeInTheDocument()
  })

  it('shows a team-scoped user only its own teams and no Unassigned row', async () => {
    await signIn('paula')
    renderPage()
    await screen.findByTestId('team-payments')
    expect(screen.queryByTestId('team-cards')).not.toBeInTheDocument()
    expect(screen.queryByTestId('team-unassigned')).not.toBeInTheDocument()
    expect(screen.queryByTestId('new-team')).not.toBeInTheDocument()
  })
})
