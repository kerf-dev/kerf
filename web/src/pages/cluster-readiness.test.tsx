/**
 * Cluster page, Readiness tab, against the MSW mock hub: the Re-check now button (spinner and
 * disabled while the request runs, then the pending state), its absence for viewers, and the age
 * of each check.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { delay, http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import type { ClusterReadiness } from '@/api/types'
import { CheckAge } from '@/components/admin/readiness'
import { TooltipProvider } from '@/components/ui/tooltip'
import { handlers, resetDb } from '@/mocks/handlers'
import { ReadinessTab } from './cluster-detail'

const server = setupServer(...handlers)
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterAll(() => server.close())
beforeEach(() => {
  localStorage.clear()
  resetDb()
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

function renderTab(cluster: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <MemoryRouter>
          <ReadinessTab cluster={cluster} />
        </MemoryRouter>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

const ago = (ms: number) => new Date(Date.now() - ms).toISOString()

/** A cluster whose hub checks ran 23 minutes ago and whose agent checks ran 2 minutes ago. */
function readiness(over: Partial<ClusterReadiness> = {}): ClusterReadiness {
  return {
    cluster: 'lab',
    state: 'AgentConnected',
    stateSince: ago(3_600_000),
    checkedAt: ago(120_000),
    summary: { blocking: 1, warnings: 0, unknown: 0 },
    nextState: 'MetricsOK',
    blockedBy: ['metrics.freshness'],
    checks: [
      {
        id: 'metrics.freshness',
        category: 'metrics',
        severity: 'blocking',
        source: 'hub',
        gates: 'MetricsOK',
        status: 'fail',
        message: 'no samples of kube_pod_owner found in the last 1h',
        remediation: 'Install kube-state-metrics.',
        remediationType: 'text',
        checkedAt: ago(23 * 60_000),
        details: { kube_pod_owner: 'absent' },
      },
      {
        id: 'agent.connected',
        category: 'agent',
        severity: 'blocking',
        source: 'hub',
        gates: 'AgentConnected',
        status: 'pass',
        message: 'last pull 20s ago',
        checkedAt: ago(120_000),
      },
    ],
    ...over,
  }
}

describe('Readiness tab', () => {
  it('shows how old each check is', async () => {
    await signIn('operator')
    server.use(http.get('*/v1/clusters/:cluster/readiness', () => HttpResponse.json(readiness())))
    renderTab('lab')
    const fresh = await screen.findByTestId('check-metrics.freshness')
    expect(within(fresh).getByTestId('check-age')).toHaveTextContent('checked 23 minutes ago')
    expect(within(screen.getByTestId('check-agent.connected')).getByTestId('check-age')).toHaveTextContent(
      'checked 2 minutes ago',
    )
    expect(screen.getByTestId('recheck-hint')).toHaveTextContent('every hour')
  })

  it('re-checks now: spinner and disabled while the request runs, then pending', async () => {
    await signIn('operator')
    let pending = false
    let posts = 0
    server.use(
      http.get('*/v1/clusters/:cluster/readiness', () =>
        HttpResponse.json(readiness(pending ? { recheckRequestedAt: new Date().toISOString() } : {})),
      ),
      http.post('*/v1/clusters/:cluster/readiness/recheck', async () => {
        posts++
        await delay(150)
        pending = true
        return HttpResponse.json(
          { requestedAt: new Date().toISOString(), hubChecks: true, agentRecheck: true },
          { status: 202 },
        )
      }),
    )
    renderTab('lab')
    const button = await screen.findByRole('button', { name: /Re-check now/ })
    expect(button).toBeEnabled()
    fireEvent.click(button)
    await waitFor(() => expect(button).toBeDisabled())
    expect(button).toHaveAttribute('aria-busy', 'true')
    expect(button.querySelector('svg')).toHaveClass('animate-spin')
    expect(await screen.findByText(/Re-checking/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Re-check now/ })).not.toBeInTheDocument()
    expect(posts).toBe(1)
  })

  it('re-checks against the mock hub', async () => {
    await signIn('operator')
    renderTab('prod-eu-west-1')
    fireEvent.click(await screen.findByRole('button', { name: /Re-check now/ }))
    expect(await screen.findByText(/Re-checking/)).toBeInTheDocument()
  })

  it('has no re-check for viewers', async () => {
    await signIn('viewer')
    server.use(http.get('*/v1/clusters/:cluster/readiness', () => HttpResponse.json(readiness())))
    renderTab('lab')
    await screen.findByTestId('check-metrics.freshness')
    expect(screen.queryByRole('button', { name: /Re-check now/ })).not.toBeInTheDocument()
  })
})

describe('CheckAge', () => {
  it('renders the relative age with the exact time on hover, and nothing without a result', () => {
    const now = Date.parse('2026-11-08T12:00:00Z')
    const { container, rerender } = render(<CheckAge checkedAt="2026-11-08T11:37:00Z" now={now} />)
    const el = screen.getByTestId('check-age')
    expect(el).toHaveTextContent('checked 23 minutes ago')
    expect(el.getAttribute('title')).toMatch(/08 Nov 2026/)
    rerender(<CheckAge checkedAt="2026-11-08T11:59:50Z" now={now} />)
    expect(screen.getByTestId('check-age')).toHaveTextContent('checked just now')
    rerender(<CheckAge checkedAt="" now={now} />)
    expect(container).toBeEmptyDOMElement()
  })
})

describe('Readiness of a cluster read from Dynatrace', () => {
  it('words the remediation for Dynatrace and mentions no Prometheus setup', async () => {
    await signIn('operator')
    const res = await fetch(`${window.location.origin}/v1/clusters/dt-prod-eu1/readiness`)
    const body = (await res.json()) as ClusterReadiness
    const remediations = body.checks.filter((c) => c.remediation && c.id.startsWith('metrics.'))
    expect(remediations.map((c) => c.id).sort()).toEqual(['metrics.hpa', 'metrics.node-labels'])
    for (const c of body.checks.filter(
      (x) => x.id.startsWith('metrics.') || x.id === 'datasource.reachable' || x.id === 'pricing.coverage',
    ))
      expect(`${c.message} ${c.remediation ?? ''}`).not.toMatch(
        /kube-state-metrics|scrape|vmagent|Prometheus|cAdvisor|helm upgrade/i,
      )

    renderTab('dt-prod-eu1')
    const labels = await screen.findByTestId('check-metrics.node-labels')
    expect(labels).toHaveTextContent('node labels lack capacity type on 2 of 24 nodes')
    expect(labels).toHaveTextContent('Karpenter sets karpenter.sh/capacity-type')
    expect(screen.getByTestId('check-metrics.recommendations')).toHaveTextContent(
      'recommendations for 410 of 412 workloads',
    )
    const hpa = screen.getByTestId('check-metrics.hpa')
    expect(hpa).toHaveTextContent('Dynatrace records no HorizontalPodAutoscaler metrics')
    fireEvent.click(within(hpa).getByRole('button'))
    expect(hpa).toHaveTextContent('Kerf does not guess HPA coupling for a Dynatrace datasource.')
    expect(screen.getByTestId('check-metrics.throttling')).toHaveTextContent(
      'derived throttling ratio (dt.containers.cpu.throttling_ratio) for 412 containers',
    )
    expect(screen.getByTestId('check-metrics.dcgm')).toHaveTextContent(
      'GPU metrics are not read from a Dynatrace datasource',
    )
  })
})
