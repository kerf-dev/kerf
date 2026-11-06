/**
 * Workload actions and team roles: operator on the workload's team enables them (a fleet viewer who
 * is operator on one team included); otherwise they are disabled with the reason. Fleet-wide pages
 * explain themselves to team-scoped users.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, unwrap } from '@/api/client'
import { RequireRole } from '@/components/admin/require-role'
import { TooltipProvider } from '@/components/ui/tooltip'
import { getDb, handlers, resetDb } from '@/mocks/handlers'
import { AutomationActions } from './automation-actions'

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
  await unwrap(api.POST('/v1/auth/login', { body: { username, password: 'dev-password' } }))
}

async function detail(namespace: string) {
  const w = getDb().workloads.find((x) => x.namespace === namespace && x.recommendation)!
  return unwrap(api.GET('/v1/workloads/{id}', { params: { path: { id: w.id } } }))
}

function renderWrapped(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <MemoryRouter>{ui}</MemoryRouter>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

describe('workload actions by team role', () => {
  it('a team operator can act on its team, a fleet viewer on the team it operates', async () => {
    await signIn('victor')
    renderWrapped(<AutomationActions w={await detail('risk')} />)
    const btn = await screen.findByTestId('set-override')
    expect(btn).toBeEnabled()
    expect(screen.queryByTestId('actions-no-team-role')).not.toBeInTheDocument()
  })

  it('disables the actions on another team with the reason', async () => {
    await signIn('victor')
    const w = await detail('payments')
    expect(w.team).toBe('payments')
    renderWrapped(<AutomationActions w={w} />)
    const box = await screen.findByTestId('actions-no-team-role')
    for (const b of box.querySelectorAll('button')) expect(b).toBeDisabled()
    expect(screen.queryByTestId('set-override')).not.toBeInTheDocument()
    box.focus()
    await waitFor(() =>
      expect(screen.getAllByText(/not an operator on team payments/).length).toBeGreaterThan(0),
    )
  })

  it('an admin keeps every action', async () => {
    await signIn('admin')
    renderWrapped(<AutomationActions w={await detail('cards')} />)
    expect(await screen.findByTestId('set-override')).toBeEnabled()
    expect(screen.getByTestId('approve-once')).toBeInTheDocument()
  })

  it('a plain viewer still sees the operator-role note', async () => {
    await signIn('viewer')
    renderWrapped(<AutomationActions w={await detail('cards')} />)
    expect(await screen.findByText('Changing automation needs the operator role.')).toBeInTheDocument()
  })
})

describe('fleet-wide pages for team-scoped users', () => {
  it('explains instead of rendering', async () => {
    await signIn('paula')
    renderWrapped(
      <RequireRole min="viewer">
        <div>secret fleet page</div>
      </RequireRole>,
    )
    expect(await screen.findByText('This page is for fleet-wide users')).toBeInTheDocument()
    expect(screen.queryByText('secret fleet page')).not.toBeInTheDocument()
  })

  it('lets them open pages marked as for everyone (Teams)', async () => {
    await signIn('paula')
    renderWrapped(
      <RequireRole min="viewer" fleet={false}>
        <div>teams page</div>
      </RequireRole>,
    )
    expect(await screen.findByText('teams page')).toBeInTheDocument()
  })
})
