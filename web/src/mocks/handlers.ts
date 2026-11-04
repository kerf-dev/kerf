import { http, HttpResponse } from 'msw'
import type {
  AuditEventList,
  ClusterList,
  EventType,
  FleetSummary,
  Mode,
  RiskFlag,
  SavingsTrend,
  UsageSeries,
  WorkloadHistory,
  WorkloadKind,
  WorkloadFacets,
  WorkloadList,
  WorkloadModel,
} from './types'
import {
  chooseStep,
  goDuration,
  parseStep,
  savingsTrend,
  summarize,
  toDetail,
  toRow,
  usageSeries,
  type MockDb,
} from './data'
import { adminHandlers, decorateClusters, decorateDetail, decorateHistory } from './admin'
import { dec, enc, gate, problem } from './core'

export { currentScenario, getDb, getAdmin, resetDb, timing, type Scenario } from './core'

function range(url: URL, db: MockDb, defaultDays = 7) {
  const to = url.searchParams.get('to') ? Date.parse(url.searchParams.get('to')!) : db.now
  const from = url.searchParams.get('from')
    ? Date.parse(url.searchParams.get('from')!)
    : to - defaultDays * 86400_000
  return { from, to }
}

function decodeId(raw: string | readonly string[] | undefined): string {
  const s = Array.isArray(raw) ? raw.join('/') : String(raw ?? '')
  return s.includes('%') ? decodeURIComponent(s) : s
}

function findWorkload(db: MockDb, raw: string | readonly string[] | undefined): WorkloadModel | Response {
  const id = decodeId(raw)
  if (
    !/^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?\/[^/]+\/(Deployment|StatefulSet|DaemonSet|CronJob)\/[^/]+$/.test(id)
  ) {
    return problem(400, 'invalid_workload_id', 'Bad Request', `invalid workload id ${JSON.stringify(id)}`)
  }
  return db.byId.get(id) ?? problem(404, 'workload_not_found', 'Not Found', `workload ${id} not found`)
}

interface Filters {
  clusters: string[]
  namespaces: string[]
  kinds: WorkloadKind[]
  modes: Mode[]
  riskFlags: RiskFlag[]
  minSavings?: number
  q?: string
}

function parseFilters(sp: URLSearchParams): Filters | Response {
  const minSavings = sp.get('minSavings') != null ? Number(sp.get('minSavings')) : undefined
  if (minSavings != null && Number.isNaN(minSavings))
    return problem(400, 'invalid_min_savings', 'Bad Request', 'minSavings must be a number')
  return {
    clusters: sp.getAll('cluster'),
    namespaces: sp.getAll('namespace'),
    kinds: sp.getAll('kind') as WorkloadKind[],
    modes: sp.getAll('mode') as Mode[],
    riskFlags: sp.getAll('riskFlag') as RiskFlag[],
    minSavings,
    q: sp.get('q')?.toLowerCase() || undefined,
  }
}

function filterWorkloads(db: MockDb, f: Filters): WorkloadModel[] {
  return db.workloads.filter(
    (w) =>
      (!f.clusters.length || f.clusters.includes(w.cluster)) &&
      (!f.namespaces.length || f.namespaces.includes(w.namespace)) &&
      (!f.kinds.length || f.kinds.includes(w.kind)) &&
      (!f.modes.length || f.modes.includes(w.modes.cpu) || f.modes.includes(w.modes.memory)) &&
      (!f.riskFlags.length || f.riskFlags.some((x) => w.riskFlags.includes(x))) &&
      (f.minSavings == null || (w.hasRecommendation && w.savings.total >= f.minSavings)) &&
      (!f.q || w.namespace.toLowerCase().includes(f.q) || w.name.toLowerCase().includes(f.q)),
  )
}

export const handlers = [
  ...adminHandlers,

  http.get('*/v1/summary', async () => {
    const g = await gate()
    if (g instanceof Response) return g
    const { db } = g
    const body: FleetSummary = {
      generatedAt: new Date(db.now).toISOString(),
      fleet: summarize(db.workloads, db.lastRunAt),
      clusters: db.clusters.map((c) => ({
        cluster: c.name,
        ...summarize(
          db.workloads.filter((w) => w.cluster === c.name),
          db.lastRunAt,
        ),
      })),
    }
    return HttpResponse.json(body)
  }),

  http.get('*/v1/clusters', async () => {
    const g = await gate()
    if (g instanceof Response) return g
    const body: ClusterList = { items: decorateClusters(g.st, g.db, g.db.clusters) }
    return HttpResponse.json(body)
  }),

  http.get('*/v1/workloads', async ({ request }) => {
    const g = await gate()
    if (g instanceof Response) return g
    const url = new URL(request.url)
    const sp = url.searchParams
    const sort = (sp.get('sort') ?? 'savings') as 'savings' | 'confidence' | 'name'
    if (!['savings', 'confidence', 'name'].includes(sort))
      return problem(400, 'invalid_sort', 'Bad Request', `invalid sort ${sort}`)
    const order = sp.get('order') ?? (sort === 'name' ? 'asc' : 'desc')
    const limit = Number(sp.get('limit') ?? 50)
    if (!(limit >= 1 && limit <= 500))
      return problem(400, 'invalid_limit', 'Bad Request', 'limit must be 1..500')
    const offset = dec(sp.get('continue'))
    if (offset < 0) return problem(400, 'invalid_continue', 'Bad Request', 'invalid continue token')

    const f = parseFilters(sp)
    if (f instanceof Response) return f
    let ws = filterWorkloads(g.db, f)
    const dir = order === 'asc' ? 1 : -1
    ws = [...ws].sort((a, b) => {
      let c: number
      if (sort === 'name')
        c =
          a.name.localeCompare(b.name) ||
          a.namespace.localeCompare(b.namespace) ||
          a.cluster.localeCompare(b.cluster)
      else if (sort === 'confidence')
        c = (a.hasRecommendation ? a.confidence : -1) - (b.hasRecommendation ? b.confidence : -1)
      else
        c =
          (a.hasRecommendation ? a.savings.total : -Infinity) -
          (b.hasRecommendation ? b.savings.total : -Infinity)
      return c * dir || a.id.localeCompare(b.id)
    })
    // Workloads without a recommendation always sort last.
    if (sort !== 'name')
      ws = [...ws.filter((w) => w.hasRecommendation), ...ws.filter((w) => !w.hasRecommendation)]
    const page = ws.slice(offset, offset + limit)
    const body: WorkloadList = {
      items: page.map(toRow),
      total: ws.length,
      ...(offset + limit < ws.length ? { continue: enc(offset + limit) } : {}),
    }
    return HttpResponse.json(body)
  }),

  http.get('*/v1/workloads/facets', async ({ request }) => {
    const g = await gate()
    if (g instanceof Response) return g
    const f = parseFilters(new URL(request.url).searchParams)
    if (f instanceof Response) return f
    // Each facet ignores its own filter so a multi-select keeps offering the other values.
    const count = (key: keyof Filters, values: (w: WorkloadModel) => string[]) => {
      const m = new Map<string, number>()
      for (const w of filterWorkloads(g.db, {
        ...f,
        [key]: key === 'minSavings' || key === 'q' ? undefined : [],
      }))
        for (const v of new Set(values(w))) m.set(v, (m.get(v) ?? 0) + 1)
      return [...m.entries()]
        .sort((a, b) => a[0].localeCompare(b[0]))
        .map(([value, n]) => ({ value, count: n }))
    }
    const body: WorkloadFacets = {
      total: filterWorkloads(g.db, f).length,
      clusters: count('clusters', (w) => [w.cluster]),
      namespaces: count('namespaces', (w) => [w.namespace]),
      kinds: count('kinds', (w) => [w.kind]),
      modes: count('modes', (w) => [w.modes.cpu, w.modes.memory]),
      riskFlags: count('riskFlags', (w) => w.riskFlags),
    }
    return HttpResponse.json(body)
  }),

  http.get('*/v1/workloads/:id/usage', async ({ request, params }) => {
    const g = await gate()
    if (g instanceof Response) return g
    const w = findWorkload(g.db, params.id)
    if (w instanceof Response) return w
    const url = new URL(request.url)
    const resource = url.searchParams.get('resource')
    if (resource !== 'cpu' && resource !== 'memory')
      return problem(400, 'invalid_resource', 'Bad Request', 'resource must be cpu or memory')
    const container = url.searchParams.get('container')
    const { from, to } = range(url, g.db)
    if (!(from < to)) return problem(400, 'invalid_range', 'Bad Request', 'from must be before to')
    const rangeSec = (to - from) / 1000
    const step = parseStep(url.searchParams.get('step')) ?? chooseStep(rangeSec)
    const cs = w.containers.filter((c) => !container || c.name === container)
    const body: UsageSeries = {
      workloadId: w.id,
      resource,
      unit: resource === 'cpu' ? 'cores' : 'bytes',
      from: new Date(from).toISOString(),
      to: new Date(to).toISOString(),
      step: goDuration(step),
      containers: cs.map((c) => usageSeries(w, c, resource, from / 1000, to / 1000, step)),
    }
    return HttpResponse.json(body)
  }),

  http.get('*/v1/workloads/:id/history', async ({ request, params }) => {
    const g = await gate()
    if (g instanceof Response) return g
    const w = findWorkload(g.db, params.id)
    if (w instanceof Response) return w
    const url = new URL(request.url)
    const limit = Number(url.searchParams.get('limit') ?? 100)
    const { from, to } = range(url, g.db, 30)
    const inRange = (t: string) => Date.parse(t) >= from && Date.parse(t) < to
    const body: WorkloadHistory = decorateHistory(g.st, {
      workloadId: w.id,
      recommendations: w.history.filter((h) => inRange(h.generatedAt)).slice(0, limit),
      events: g.db.events
        .filter((e) => e.workloadId === w.id && inRange(e.time))
        .reverse()
        .slice(0, limit),
    })
    return HttpResponse.json(body)
  }),

  http.get('*/v1/workloads/:id', async ({ params }) => {
    const g = await gate()
    if (g instanceof Response) return g
    const w = findWorkload(g.db, params.id)
    if (w instanceof Response) return w
    return HttpResponse.json(decorateDetail(g.st, toDetail(w, g.db)))
  }),

  http.get('*/v1/savings/trend', async ({ request }) => {
    const g = await gate()
    if (g instanceof Response) return g
    const url = new URL(request.url)
    const groupBy = url.searchParams.get('groupBy') ?? 'none'
    const clusters = url.searchParams.getAll('cluster')
    const { from, to } = range(url, g.db)
    const step = parseStep(url.searchParams.get('step')) ?? chooseStep((to - from) / 1000)
    const names = g.db.clusters.map((c) => c.name).filter((n) => !clusters.length || clusters.includes(n))
    const per = savingsTrend(g.db, names, from / 1000, to / 1000, step)
    let series: SavingsTrend['series']
    if (groupBy === 'cluster') series = per
    else {
      const pts = new Map<string, { time: string; cpu: number; memory: number; total: number }>()
      for (const s of per)
        for (const p of s.points) {
          const cur = pts.get(p.time) ?? { time: p.time, cpu: 0, memory: 0, total: 0 }
          cur.cpu += p.cpu
          cur.memory += p.memory
          cur.total += p.total
          pts.set(p.time, cur)
        }
      const r2 = (n: number) => Math.round(n * 100) / 100
      series = [
        {
          points: [...pts.values()]
            .sort((a, b) => a.time.localeCompare(b.time))
            .map((p) => ({ time: p.time, cpu: r2(p.cpu), memory: r2(p.memory), total: r2(p.total) })),
        },
      ]
    }
    const body: SavingsTrend = {
      from: new Date(from).toISOString(),
      to: new Date(to).toISOString(),
      step: goDuration(step),
      series,
    }
    return HttpResponse.json(body)
  }),

  http.get('*/v1/audit', async ({ request }) => {
    const g = await gate()
    if (g instanceof Response) return g
    const sp = new URL(request.url).searchParams
    const cluster = sp.get('cluster')
    const ns = sp.get('namespace')
    const kind = sp.get('kind')
    const workload = sp.get('workload')
    const types = sp.getAll('type') as EventType[]
    const from = sp.get('from') ? Date.parse(sp.get('from')!) : -Infinity
    const to = sp.get('to') ? Date.parse(sp.get('to')!) : Infinity
    const order = sp.get('order') ?? 'asc'
    const limit = Number(sp.get('limit') ?? 100)
    if (!(limit >= 1 && limit <= 1000))
      return problem(400, 'invalid_limit', 'Bad Request', 'limit must be 1..1000')
    const offset = dec(sp.get('continue'))
    if (offset < 0) return problem(400, 'invalid_continue', 'Bad Request', 'invalid continue token')
    let items = g.db.events.filter(
      (e) =>
        (!cluster || e.cluster === cluster) &&
        (!ns || e.workload.namespace === ns) &&
        (!kind || e.workload.kind === kind) &&
        (!workload || e.workload.name === workload) &&
        (!types.length || types.includes(e.type)) &&
        Date.parse(e.time) >= from &&
        Date.parse(e.time) < to,
    )
    if (order === 'desc') items = [...items].reverse()
    const page = items.slice(offset, offset + limit)
    const body: AuditEventList = {
      items: page,
      ...(offset + limit < items.length ? { continue: enc(offset + limit) } : {}),
    }
    return HttpResponse.json(body)
  }),
]
