/**
 * Cluster page, Capacity tab, against the MSW mock hub: the estimate label and method link, blocker
 * links and kubectl text, and the empty states.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import type { ClusterCapacity } from '@/api/types'
import { TooltipProvider } from '@/components/ui/tooltip'
import { getDb, handlers, resetDb } from '@/mocks/handlers'
import { capacityFor } from '@/mocks/capacity'
import { kubectlGet } from '@/lib/capacity'
import { CapacityTab } from './cluster-capacity'

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

function renderTab(cluster: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <MemoryRouter>
          <CapacityTab cluster={cluster} />
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
})
