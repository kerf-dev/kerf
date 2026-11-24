/**
 * Mock `GET /v1/clusters/{cluster}/capacity/history`: two node pools, a 6-hour gap, one pool that
 * appears and two days with applied changes. The data ends at a fixed time so it is deterministic.
 */
import { http, HttpResponse } from 'msw'
import type { CapacityHistory, CapacityHistoryEvent, CapacityHistoryPoint } from '@/api/types'
import { gate, problem } from './core'

const GiB = 1024 ** 3
const HOUR = 3600_000
const DAY = 24 * HOUR

/** The last stored hour of every mock history. */
export const HISTORY_END = Date.parse('2026-06-30T23:00:00Z')

const DAYS = { '7d': 7, '30d': 30, '90d': 90, '1y': 365 } as const
const POOLS: Record<string, { nodes: number; cpu: number; gib: number }> = {
  general: { nodes: 8, cpu: 8, gib: 32 },
  batch: { nodes: 4, cpu: 16, gib: 64 },
}

/** Pools that exist at a time: `batch` appears 10 days before the end. */
const BATCH_ADDED = Date.parse('2026-06-20T00:00:00Z')

function point(t: number, pools: string[], daily: boolean): CapacityHistoryPoint {
  const wave = daily ? 0.75 : 0.65 + 0.15 * Math.sin(((t % DAY) / DAY) * 2 * Math.PI)
  const out = { nodes: 0, cpuRequested: 0, cpuAllocatable: 0, memRequested: 0, memAllocatable: 0 }
  for (const name of pools) {
    if (name === 'batch' && t < BATCH_ADDED) continue
    const p = POOLS[name]!
    out.nodes += p.nodes
    out.cpuAllocatable += p.nodes * p.cpu
    out.cpuRequested += p.nodes * p.cpu * wave
    out.memAllocatable += p.nodes * p.gib * GiB
    out.memRequested += p.nodes * p.gib * GiB * (wave - 0.1)
  }
  return { hour: new Date(t).toISOString(), ...out }
}

export function capacityHistoryFor(range: keyof typeof DAYS, nodePool: string | undefined): CapacityHistory {
  const daily = range === '90d' || range === '1y'
  const step = daily ? DAY : HOUR
  const end = daily ? Math.floor(HISTORY_END / DAY) * DAY : HISTORY_END
  const start = end - DAYS[range] * DAY + step
  const gapStart = Date.parse('2026-06-15T06:00:00Z')
  const gapEnd = gapStart + 6 * HOUR
  const pools = nodePool ? [nodePool] : Object.keys(POOLS)
  const points: CapacityHistoryPoint[] = []
  for (let t = start; t <= end; t += step) {
    if (!daily && t >= gapStart && t < gapEnd) continue
    if (daily && t === Math.floor(gapStart / DAY) * DAY) continue
    if (pools.length === 1 && pools[0] === 'batch' && t < BATCH_ADDED) continue
    points.push(point(t, pools, daily))
  }
  const events: CapacityHistoryEvent[] = [
    {
      day: '2026-06-12',
      type: 'applied',
      count: 14,
      topWorkloads: [
        { namespace: 'payments', kind: 'Deployment', name: 'api' },
        { namespace: 'gateway', kind: 'Deployment', name: 'edge' },
      ],
    },
    {
      day: '2026-06-24',
      type: 'applied',
      count: 3,
      topWorkloads: [{ namespace: 'payments', kind: 'Deployment', name: 'api' }],
    },
    { day: '2026-06-20', type: 'pool_added', count: 1, nodePool: 'batch' },
  ].filter(
    (e) => Date.parse(e.day) >= start - DAY && (!nodePool || !('nodePool' in e) || e.nodePool === nodePool),
  ) as CapacityHistoryEvent[]
  return {
    range,
    since: points.length ? points[0]!.hour : undefined,
    nodePools: Object.keys(POOLS),
    points,
    events,
  }
}

export const capacityHistoryHandlers = [
  http.get('*/v1/clusters/:cluster/capacity/history', async ({ request, params }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const cluster = String(params.cluster)
    if (!g.db.clusterSpecs.some((c) => c.name === cluster)) {
      return problem(404, 'not_found', 'Not Found', `cluster ${cluster} not found`)
    }
    const url = new URL(request.url)
    const r = url.searchParams.get('range') ?? '30d'
    if (!(r in DAYS)) return problem(400, 'invalid_request', 'Bad Request', `unknown range ${r}`)
    return HttpResponse.json(
      capacityHistoryFor(r as keyof typeof DAYS, url.searchParams.get('nodePool') || undefined),
    )
  }),
]
