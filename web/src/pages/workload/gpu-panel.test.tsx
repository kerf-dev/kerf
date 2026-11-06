/** GPU panel of the workload page: every status, costs, suggestions and the no-data explanation. */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import { setupServer } from 'msw/node'
import { MemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import type { GPUUsage } from '@/api/types'
import { TooltipProvider } from '@/components/ui/tooltip'
import { getDb, handlers, resetDb } from '@/mocks/handlers'
import { GpuPanel } from './gpu-panel'

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

const gpu = (patch: Partial<GPUUsage> = {}): GPUUsage => ({
  status: 'ok',
  gpusPerPod: 2,
  gpus: 6,
  pods: 3,
  model: 'NVIDIA A10G',
  window: '336h0m0s',
  samples: 40320,
  utilizationP95: 82.4,
  utilizationAvg: 61.3,
  memoryUsedP95Bytes: 18 * 1024 ** 3,
  memoryTotalBytes: 24 * 1024 ** 3,
  memoryUtilizationP95: 75,
  priced: true,
  monthlyCostUSD: 4400,
  perPodMonthlyCostUSD: 1466.67,
  unusedMonthlyCostUSD: 1700,
  suggestions: [],
  ...patch,
})

function renderPanel(g: GPUUsage, cluster = 'prod-eu-west-1') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <MemoryRouter>
          <GpuPanel gpu={g} cluster={cluster} />
        </MemoryRouter>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

describe('GpuPanel', () => {
  it('ok: facts, gauges, costs and the empty suggestion state', () => {
    renderPanel(gpu())
    const panel = screen.getByTestId('gpu-panel')
    expect(within(panel).getByTestId('gpu-status')).toHaveAttribute('data-status', 'ok')
    expect(within(panel).getByTestId('gpu-per-pod')).toHaveTextContent('2')
    expect(within(panel).getByTestId('gpu-total')).toHaveTextContent('6in 3 pods')
    expect(within(panel).getByText('NVIDIA A10G')).toBeInTheDocument()
    expect(within(panel).getByRole('meter', { name: 'p95' })).toHaveAttribute('aria-valuetext', '82 %')
    expect(within(panel).getByRole('meter', { name: 'Average' })).toHaveAttribute('aria-valuenow', '61.3')
    expect(within(panel).getByRole('meter', { name: 'Used p95 of total' })).toHaveAttribute(
      'aria-valuetext',
      '18 GiB of 24 GiB',
    )
    expect(within(panel).getByTestId('gpu-cost')).toHaveTextContent('$4,400')
    expect(within(panel).getByTestId('gpu-cost-pod')).toHaveTextContent('$1,467')
    expect(within(panel).getByTestId('gpu-cost-unused')).toHaveTextContent('$1,700')
    expect(within(panel).queryByTestId('gpu-suggestions')).not.toBeInTheDocument()
    expect(within(panel).getByText(/Nothing to suggest/)).toBeInTheDocument()
  })

  it('idle: scale-to-zero shows its savings', () => {
    renderPanel(
      gpu({
        status: 'idle',
        utilizationP95: 1.8,
        utilizationAvg: 0.6,
        suggestions: [
          { kind: 'scale-to-zero', message: 'Scale it to zero.', monthlySavingsUSD: 4400 },
          { kind: 'time-slicing', message: 'Share the GPU.' },
        ],
      }),
    )
    const panel = screen.getByTestId('gpu-panel')
    expect(within(panel).getByTestId('gpu-status')).toHaveAttribute('data-status', 'idle')
    expect(within(panel).getByRole('meter', { name: 'p95' })).toHaveAttribute('aria-valuetext', '1.8 %')
    const items = within(panel).getAllByTestId('gpu-suggestion')
    expect(items.map((i) => i.getAttribute('data-kind'))).toEqual(['scale-to-zero', 'time-slicing'])
    expect(items[0]).toHaveTextContent('Scale to zero')
    expect(items[0]).toHaveTextContent('saves $4,400 / mo')
    expect(items[1]).not.toHaveTextContent('saves')
  })

  it('under-used: smaller-gpu names the target model; mig and time-slicing are listed', () => {
    renderPanel(
      gpu({
        status: 'under-used',
        memoryUsedP95Bytes: 4 * 1024 ** 3,
        memoryUtilizationP95: 16.7,
        suggestions: [
          {
            kind: 'smaller-gpu',
            targetModel: 'NVIDIA T4',
            message: 'A 16 GiB GPU fits.',
            monthlySavingsUSD: 700,
          },
          { kind: 'mig', message: 'Partition the GPU.' },
          { kind: 'time-slicing', message: 'Share the GPU.' },
        ],
      }),
    )
    const panel = screen.getByTestId('gpu-panel')
    expect(within(panel).getByTestId('gpu-status')).toHaveAttribute('data-status', 'under-used')
    expect(within(panel).getByText('NVIDIA T4')).toBeInTheDocument()
    expect(within(panel).getAllByTestId('gpu-suggestion')).toHaveLength(3)
    expect(within(panel).getByText('MIG partitions')).toBeInTheDocument()
  })

  it('unpriced: shows "–" instead of $0 and says why', () => {
    renderPanel(gpu({ priced: false, monthlyCostUSD: 0, perPodMonthlyCostUSD: 0, unusedMonthlyCostUSD: 0 }))
    for (const id of ['gpu-cost', 'gpu-cost-pod', 'gpu-cost-unused'])
      expect(screen.getByTestId(id)).toHaveTextContent(/^–$/)
    expect(screen.getByTestId('gpu-unpriced')).toBeInTheDocument()
  })

  it('no-data: explains the missing DCGM series and links to the readiness checks', async () => {
    const r = await fetch(`${window.location.origin}/v1/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'admin', password: 'dev-password' }),
    })
    expect(r.status).toBe(200)
    const noData = [...getDb().byId.values()].find((w) => w.gpu?.status === 'no-data')!
    renderPanel(noData.gpu!, noData.cluster)
    const panel = screen.getByTestId('gpu-panel')
    expect(within(panel).getByTestId('gpu-status')).toHaveAttribute('data-status', 'no-data')
    const note = within(panel).getByTestId('gpu-no-data')
    expect(note).toHaveTextContent('NVIDIA DCGM exporter has no series')
    expect(within(note).getByRole('link')).toHaveAttribute(
      'href',
      `/clusters/${encodeURIComponent(noData.cluster)}?tab=readiness`,
    )
    // gauges and suggestions make no sense without data
    expect(within(panel).queryByRole('meter')).not.toBeInTheDocument()
    expect(within(panel).queryByText('Suggestions')).not.toBeInTheDocument()
    // the readiness check of the cluster names the missing series
    await waitFor(() =>
      expect(within(panel).getByTestId('gpu-dcgm-check')).toHaveTextContent(
        'DCGM exporter series missing: DCGM_FI_DEV_GPU_UTIL, DCGM_FI_DEV_FB_USED, DCGM_FI_DEV_FB_FREE',
      ),
    )
  })
})
