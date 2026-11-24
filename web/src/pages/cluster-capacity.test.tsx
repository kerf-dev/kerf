/**
 * Cluster page, Capacity tab, against the MSW mock hub: the estimate label and method link, blocker
 * links and kubectl text, and the empty states.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CapacityHistory, ClusterCapacity } from '@/api/types'
import { TooltipProvider } from '@/components/ui/tooltip'
import { getDb, handlers, resetDb } from '@/mocks/handlers'
import { capacityFor } from '@/mocks/capacity'
import { capacityHistoryFor } from '@/mocks/capacity-history'
import { kubectlGet } from '@/lib/capacity'
import { CapacityTab } from './cluster-capacity'

// ECharts needs a canvas. The stand-in lets a test click an event marker.
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
  Chart: ({ ariaLabel, onClick }: { ariaLabel: string; onClick?: (p: { data: unknown }) => void }) => (
    <div role="img" aria-label={ariaLabel}>
      {['applied', 'rolled_back', 'pool_added'].map((type) => (
        <button key={type} type="button" onClick={() => onClick?.({ data: { type } })}>
          marker {type}
        </button>
      ))}
    </div>
  ),
}))

const server = setupServer(...handlers)
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterAll(() => server.close())
beforeEach(() => {
  localStorage.clear()
  resetDb()
})
afterEach(cleanup)

async function signIn() {
  const r = await fetch(`${window.location.origin}/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'dev-password' }),
  })
  expect(r.status).toBe(200)
}

function Where() {
  const l = useLocation()
  return <div data-testid="where">{l.pathname + l.search}</div>
}

function renderTab(cluster: string, at = '/') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <MemoryRouter initialEntries={[at]}>
          <Routes>
            <Route path="*" element={<CapacityTab cluster={cluster} />} />
          </Routes>
          <Where />
        </MemoryRouter>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

const respondWith = (body: ClusterCapacity) =>
  server.use(http.get('*/v1/clusters/:cluster/capacity', () => HttpResponse.json(body)))

describe('Capacity tab', () => {
  it('labels the node estimate and links to the method', async () => {
    await signIn()
    renderTab('prod-eu-west-1')
    const card = await screen.findByTestId('estimate-card')
    expect(within(card).getByTestId('estimate-badge')).toHaveTextContent('Estimate')
    expect(within(card).getByTestId('estimate-nodes')).toHaveTextContent('24 → 18')
    expect(within(card).getByTestId('estimate-savings')).toHaveTextContent('$1,259')
    const link = within(card).getByRole('link', { name: /How this is estimated/ })
    expect(link).toHaveAttribute('href', expect.stringContaining('node-savings.md'))
    expect(link).toHaveAttribute('target', '_blank')
    expect(screen.getByTestId('reported-at')).toHaveTextContent(/Reported \d+ minutes ago/)
    expect(screen.getByTestId('unplaced')).toHaveTextContent('2 unplaced')
  })

  it('links a blocker to its workload, and offers kubectl for every object', async () => {
    await signIn()
    const base = capacityFor(getDb(), 'prod-eu-west-1')!
    const workload = base.blockers.find((b) => b.workloadId)!
    renderTab('prod-eu-west-1')
    const items = await screen.findAllByTestId('blocker')
    expect(items).toHaveLength(base.blockers.length)

    const linked = items.find((i) => i.dataset.type === 'pdb-zero-disruptions')!
    const a = within(linked).getByTestId('blocker-link')
    expect(a).toHaveAttribute('href', expect.stringMatching(/^\/workloads\/prod-eu-west-1\//))
    expect(within(linked).getByText('Budget allows no disruptions')).toBeInTheDocument()
    expect(within(linked).getByText(/How to fix/)).toBeInTheDocument()
    expect(workload.object.kind).toBe('PodDisruptionBudget')

    const pod = items.find((i) => i.dataset.type === 'do-not-disrupt-pod')!
    expect(within(pod).queryByTestId('blocker-link')).toBeNull()
    expect(within(pod).getByTestId('blocker-object')).toHaveTextContent('Pod demo/sample-worker-0')
    expect(kubectlGet({ apiVersion: 'v1', kind: 'Pod', namespace: 'demo', name: 'sample-worker-0' })).toBe(
      'kubectl get pod sample-worker-0 -n demo -o yaml',
    )
    expect(kubectlGet({ apiVersion: 'v1', kind: 'Node', name: 'node-sample-0a1b' })).toBe(
      'kubectl get node node-sample-0a1b -o yaml',
    )
    fireEvent.click(within(pod).getByRole('button', { name: /Copy kubectl/ }))
    await screen.findByText('Copied')
    expect(kubectlGet({ apiVersion: 'karpenter.sh/v1', kind: 'NodePool', name: 'general' })).toBe(
      'kubectl get nodepool general -o yaml',
    )
    expect(kubectlGet({ kind: 'NodeGroup', name: 'eks-workers' })).toBeUndefined()
    expect(within(pod).getByText('Copied')).toBeInTheDocument()
    expect(screen.getByTestId('blockers-omitted')).toHaveTextContent('12 more')
  })

  it('shows what is missing when the agent sent no report', async () => {
    await signIn()
    renderTab('dr-eu-central-1')
    const empty = await screen.findByTestId('capacity-empty')
    expect(empty).toHaveTextContent('No capacity report yet')
    expect(empty).toHaveTextContent(/Karpenter/)
    expect(screen.queryByTestId('estimate-card')).toBeNull()
  })

  it('says so when nothing blocks consolidation, and warns on a truncated report', async () => {
    await signIn()
    renderTab('prod-us-east-1')
    expect(await screen.findByTestId('no-blockers')).toHaveTextContent(
      'Nothing found that blocks consolidation',
    )
    expect(screen.getByTestId('truncated')).toBeInTheDocument()
    expect(screen.getByTestId('estimate-card')).toHaveTextContent('More nodes would be needed')
  })

  it('explains a missing estimate', async () => {
    await signIn()
    const { estimate: _drop, ...rest } = capacityFor(getDb(), 'prod-eu-west-1')!
    respondWith(rest)
    renderTab('prod-eu-west-1')
    expect(await screen.findByTestId('estimate-missing')).toBeInTheDocument()
    expect(screen.getByTestId('estimate-badge')).toBeInTheDocument()
  })

  it('shows a node group as text without a kubectl command', async () => {
    await signIn()
    const base = capacityFor(getDb(), 'prod-us-east-1')!
    respondWith({
      ...base,
      blockers: [
        {
          type: 'scale-down-disabled',
          object: { kind: 'NodeGroup', name: 'sample-group' },
          message: 'Scale-down is disabled for this node group.',
          fix: 'Enable scale-down on the node group.',
        },
      ],
    })
    renderTab('prod-us-east-1')
    const item = await screen.findByTestId('blocker')
    expect(within(item).getByTestId('blocker-object')).toHaveTextContent('Node group sample-group')
    expect(within(item).queryByRole('button', { name: /Copy kubectl/ })).toBeNull()
    expect(within(item).queryByRole('link')).toBeNull()
  })

  describe('Trend', () => {
    const history = (over: Partial<CapacityHistory> = {}) =>
      server.use(
        http.get('*/v1/clusters/:cluster/capacity/history', ({ request }) => {
          const u = new URL(request.url)
          const base = capacityHistoryFor('30d', u.searchParams.get('nodePool') || undefined)
          return HttpResponse.json({ ...base, ...over })
        }),
      )

    it('shows the section with its footer and keeps other query params', async () => {
      await signIn()
      renderTab('prod-eu-west-1', '/?tab=capacity')
      const card = await screen.findByTestId('capacity-trend')
      expect(within(card).getByText('Trend')).toBeInTheDocument()
      expect(await within(card).findByTestId('trend-since')).toHaveTextContent(/^History starts .+\.$/)
      fireEvent.click(within(card).getByRole('radio', { name: '7d' }))
      await waitFor(() => expect(screen.getByTestId('where')).toHaveTextContent('/?tab=capacity&trend=7d'))
    })

    it('explains an empty history', async () => {
      await signIn()
      history({ points: [], events: [], since: undefined, nodePools: [] })
      renderTab('prod-eu-west-1')
      expect(
        await screen.findByText('Kerf records capacity every hour. The first point appears within an hour.'),
      ).toBeInTheDocument()
    })

    it('warns when the totals may be low', async () => {
      await signIn()
      history({ truncated: true })
      renderTab('prod-eu-west-1')
      expect(await screen.findByTestId('trend-truncated')).toHaveTextContent(
        'Some capacity reports were truncated, so the totals may be low.',
      )
    })

    it('puts the chosen pool in the URL and refetches', async () => {
      await signIn()
      const pools: (string | null)[] = []
      server.use(
        http.get('*/v1/clusters/:cluster/capacity/history', ({ request }) => {
          const p = new URL(request.url).searchParams.get('nodePool')
          pools.push(p)
          return HttpResponse.json(capacityHistoryFor('30d', p || undefined))
        }),
      )
      renderTab('prod-eu-west-1')
      const select = await screen.findByRole('combobox', { name: 'Node pool' })
      fireEvent.change(select, { target: { value: 'general' } })
      await waitFor(() => expect(screen.getByTestId('where')).toHaveTextContent('/?pool=general'))
      await waitFor(() => expect(pools).toEqual([null, 'general']))
    })

    it('opens the Activity page from an event marker, except for pool changes', async () => {
      await signIn()
      renderTab('prod-eu-west-1', '/?trend=90d')
      fireEvent.click(await screen.findByText('marker pool_added'))
      expect(screen.getByTestId('where')).toHaveTextContent('/?trend=90d')
      fireEvent.click(screen.getByText('marker rolled_back'))
      await waitFor(() =>
        expect(screen.getByTestId('where')).toHaveTextContent(
          '/activity?cluster=prod-eu-west-1&type=rolled_back&range=30d',
        ),
      )
    })
  })
})
