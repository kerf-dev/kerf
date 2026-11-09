/**
 * Admin → Access with teams: scope and team roles in the table, the scope choice and team-role
 * editor when adding or editing a local user, admins forced to the whole fleet, IdP users read-only.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { getAdmin, handlers, resetDb } from '@/mocks/handlers'
import AccessPage from './access'

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
          <AccessPage />
        </MemoryRouter>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

const choose = (el: HTMLElement, value: string) => fireEvent.change(el, { target: { value } })

describe('Access page: teams', () => {
  it('shows scope and team roles per user', async () => {
    await signIn('admin')
    renderPage()
    const paula = await screen.findByTestId('user-paula')
    expect(within(paula).getByText('Some teams')).toBeInTheDocument()
    expect(within(paula).getByTestId('team-role-badge')).toHaveTextContent('payments · operator')
    const victor = screen.getByTestId('user-victor')
    expect(within(victor).getByText('Whole fleet')).toBeInTheDocument()
    expect(within(victor).getByTestId('team-role-badge')).toHaveTextContent('risk · operator')
    expect(within(screen.getByTestId('user-viewer')).getByText('Whole fleet')).toBeInTheDocument()
  })

  it('adds a team-scoped user with team roles', async () => {
    await signIn('admin')
    renderPage()
    await screen.findByTestId('user-paula')
    fireEvent.click(screen.getByTestId('add-user'))
    const dialog = await screen.findByTestId('write-dialog')
    choose(within(dialog).getByTestId('new-username'), 'tess')
    choose(within(dialog).getByTestId('new-password'), 'a-good-long-passphrase-1')
    // No team yet: the scope needs at least one.
    choose(within(dialog).getByTestId('user-scope'), 'teams')
    expect(within(dialog).getByTestId('derived-role')).toHaveTextContent('viewer')
    fireEvent.click(within(dialog).getByTestId('add-team-role'))
    choose(within(dialog).getByLabelText('Team'), 'cards')
    choose(within(dialog).getByLabelText('Role on the team'), 'operator')
    expect(within(dialog).getByTestId('derived-role')).toHaveTextContent('operator')
    choose(within(dialog).getByTestId('reason'), 'new cards operator')
    fireEvent.click(within(dialog).getByTestId('confirm-write'))
    const row = await screen.findByTestId('user-tess')
    expect(within(row).getByText('Some teams')).toBeInTheDocument()
    expect(getAdmin().users.get('tess')).toMatchObject({
      scope: 'teams',
      role: 'operator',
      teamRoles: [{ team: 'cards', role: 'operator' }],
    })
  })

  it('asks for a team when the scope is "only some teams"', async () => {
    await signIn('admin')
    renderPage()
    await screen.findByTestId('user-paula')
    fireEvent.click(screen.getByTestId('add-user'))
    const dialog = await screen.findByTestId('write-dialog')
    choose(within(dialog).getByTestId('new-username'), 'tess')
    choose(within(dialog).getByTestId('new-password'), 'a-good-long-passphrase-1')
    choose(within(dialog).getByTestId('user-scope'), 'teams')
    choose(within(dialog).getByTestId('reason'), 'x')
    fireEvent.click(within(dialog).getByTestId('confirm-write'))
    expect(await within(dialog).findByText(/Add at least one team/)).toBeInTheDocument()
    expect(getAdmin().users.has('tess')).toBe(false)
  })

  it('the admin role forces the whole fleet and hides the team roles', async () => {
    await signIn('admin')
    renderPage()
    await screen.findByTestId('user-paula')
    fireEvent.click(screen.getByTestId('add-user'))
    const dialog = await screen.findByTestId('write-dialog')
    choose(within(dialog).getByTestId('user-scope'), 'teams')
    expect(within(dialog).getByTestId('team-roles')).toBeInTheDocument()
    // The role select is replaced by the derived role while scoped to teams; go back first.
    choose(within(dialog).getByTestId('user-scope'), 'fleet')
    choose(within(dialog).getByTestId('user-role'), 'admin')
    expect(within(dialog).queryByTestId('user-scope')).not.toBeInTheDocument()
    expect(within(dialog).queryByTestId('team-roles')).not.toBeInTheDocument()
    expect(within(dialog).getByText(/Admins always see the whole fleet/)).toBeInTheDocument()
  })

  it('edits the scope and team roles of a local user', async () => {
    await signIn('admin')
    renderPage()
    await screen.findByTestId('user-bob')
    fireEvent.pointerDown(screen.getByRole('button', { name: 'Actions for bob' }), { button: 0 })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Edit scope and teams' }))
    const dialog = await screen.findByTestId('access-dialog')
    choose(within(dialog).getByTestId('user-scope'), 'teams')
    fireEvent.click(within(dialog).getByTestId('add-team-role'))
    choose(within(dialog).getByLabelText('Team'), 'payments')
    choose(within(dialog).getByTestId('reason'), 'bob moves to payments')
    fireEvent.click(within(dialog).getByTestId('confirm-write'))
    await waitFor(() =>
      expect(getAdmin().users.get('bob')).toMatchObject({
        scope: 'teams',
        role: 'viewer',
        teamRoles: [{ team: 'payments', role: 'viewer' }],
      }),
    )
  })

  it('shows identity-provider users read-only', async () => {
    await signIn('admin')
    renderPage()
    const idp = await screen.findByTestId('user-idp-cards')
    expect(within(idp).getByTestId('idp-user')).toBeInTheDocument()
    expect(within(idp).getByText('Some teams')).toBeInTheDocument()
    fireEvent.pointerDown(screen.getByRole('button', { name: 'Actions for idp-cards' }), { button: 0 })
    await screen.findByRole('menuitem', { name: 'Disable' })
    expect(screen.queryByRole('menuitem', { name: 'Edit scope and teams' })).not.toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: /Make / })).not.toBeInTheDocument()
  })

  it('shows SAML users as identity-provider users, read-only, without a password', async () => {
    await signIn('admin')
    renderPage()
    const priya = await screen.findByTestId('user-priya.shah@example.com')
    expect(within(priya).getByTestId('idp-user')).toHaveTextContent('identity provider · SAML')
    expect(within(await screen.findByTestId('user-idp-cards')).getByTestId('idp-user')).toHaveTextContent(
      'OIDC',
    )
    fireEvent.pointerDown(screen.getByRole('button', { name: 'Actions for priya.shah@example.com' }), {
      button: 0,
    })
    await screen.findByRole('menuitem', { name: 'Disable' })
    expect(screen.queryByRole('menuitem', { name: /Reset password/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: /Make / })).not.toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: 'Edit scope and teams' })).not.toBeInTheDocument()
  })
})
