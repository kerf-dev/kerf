/**
 * Realized savings for the mock hub, computed like the real one (docs/realized-savings.md) from the
 * mock audit log: every applied / rolled_back event moves the released requests by (from - to), a
 * released event ends the claim; released x replicas x the cluster's rates, per hour.
 */
import { parseQuantity } from '@/lib/format'
import type { MockDb } from './data'
import type { NodeCostTrend, RealizedSavings } from './types'

const HOUR = 3600_000
const GIB = 2 ** 30

interface Step {
  at: number
  cpu: number
  mem: number
}

/** Step function of the released requests per replica of one workload (cores, bytes). */
function ledger(db: MockDb, cluster: string, ns: string, kind: string, name: string): Step[] {
  const evs = db.events
    .filter(
      (e) =>
        e.cluster === cluster &&
        e.workload.namespace === ns &&
        e.workload.kind === kind &&
        e.workload.name === name &&
        (e.type === 'applied' || e.type === 'rolled_back' || e.type === 'released'),
    )
    .sort((a, b) => a.time.localeCompare(b.time))
  const state = new Map<string, { cpu: number; mem: number }>()
  const steps: Step[] = []
  for (const e of evs) {
    if (e.type === 'released') {
      const all = !e.resources?.length
      for (const s of state.values()) {
        if (all || e.resources!.includes('cpu')) s.cpu = 0
        if (all || e.resources!.includes('memory')) s.mem = 0
      }
    } else {
      for (const c of e.containers ?? []) {
        const s = state.get(c.name) ?? { cpu: 0, mem: 0 }
        const f = c.from?.requests
        const t = c.to?.requests
        const fc = parseQuantity(f?.cpu)
        const tc = parseQuantity(t?.cpu)
        const fm = parseQuantity(f?.memory)
        const tm = parseQuantity(t?.memory)
        if (fc != null && tc != null) s.cpu += fc - tc
        if (fm != null && tm != null) s.mem += fm - tm
        state.set(c.name, s)
      }
    }
    let cpu = 0
    let mem = 0
    for (const s of state.values()) {
      cpu += s.cpu
      mem += s.mem
    }
    steps.push({
      at: Date.parse(e.time),
      cpu: Math.abs(cpu) < 1e-9 ? 0 : cpu,
      mem: Math.abs(mem) < 1 ? 0 : mem,
    })
  }
  return steps
}

/** Time-weighted released over [a, b). */
function integrate(steps: Step[], a: number, b: number) {
  let cpu = 0
  let mem = 0
  steps.forEach((s, i) => {
    const start = Math.max(a, s.at)
    const end = Math.min(b, steps[i + 1]?.at ?? Infinity)
    if (end > start) {
      cpu += (s.cpu * (end - start)) / HOUR
      mem += (s.mem * (end - start)) / HOUR
    }
  })
  return { cpuHours: cpu, memHours: mem } // core-hours, byte-hours
}

function bucketStart(t: number, step: 'day' | 'month' | 'hour') {
  const d = new Date(t)
  if (step === 'month') return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1)
  if (step === 'day') return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate())
  return Math.floor(t / HOUR) * HOUR
}

function nextBucket(t: number, step: 'day' | 'month' | 'hour') {
  const d = new Date(t)
  if (step === 'month') return Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 1)
  if (step === 'day') return t + 24 * HOUR
  return t + HOUR
}

const iso = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')
const r4 = (n: number) => Math.round(n * 1e4) / 1e4

export interface RealizedScope {
  clusters?: string[]
  namespaces?: string[]
  workload?: { cluster: string; namespace: string; kind: string; name: string }
}

export function realizedSavings(
  db: MockDb,
  scope: RealizedScope,
  from: number,
  to: number,
  step: 'day' | 'month',
): RealizedSavings {
  const through = Math.floor(db.now / HOUR) * HOUR
  const ws = db.workloads.filter((w) =>
    scope.workload
      ? w.cluster === scope.workload.cluster &&
        w.namespace === scope.workload.namespace &&
        w.kind === scope.workload.kind &&
        w.name === scope.workload.name
      : (!scope.clusters?.length || scope.clusters.includes(w.cluster)) &&
        (!scope.namespaces?.length || scope.namespaces.includes(w.namespace)),
  )
  const buckets: number[] = []
  for (let b = bucketStart(from, step); b < to; b = nextBucket(b, step)) buckets.push(b)
  const pts = buckets.map((b) => ({ time: iso(b), cpu: 0, memory: 0, cores: 0, bytes: 0 }))
  const total = { cpu: 0, memory: 0 }
  const inRange = { cpu: 0, memory: 0 }
  const now = { cpu: 0, mem: 0 }
  let since: number | undefined
  let hours = 0
  for (const w of ws) {
    const steps = ledger(db, w.cluster, w.namespace, w.kind, w.name)
    if (!steps.some((s) => s.cpu || s.mem)) continue
    const spec = db.clusterSpecs.find((s) => s.name === w.cluster)!
    const first = steps.find((s) => s.cpu || s.mem)!.at
    since = Math.min(since ?? Infinity, Math.floor(first / HOUR) * HOUR)
    const usd = (a: number, b: number) => {
      const x = integrate(steps, a, b)
      return {
        cpu: x.cpuHours * w.replicas * spec.cpuPrice,
        memory: (x.memHours / GIB) * w.replicas * spec.memPrice,
        x,
      }
    }
    const all = usd(0, through)
    total.cpu += all.cpu
    total.memory += all.memory
    const lo = Math.floor(from / HOUR) * HOUR
    const hi = Math.min(Math.ceil(to / HOUR) * HOUR, through)
    const r = usd(lo, hi)
    inRange.cpu += r.cpu
    inRange.memory += r.memory
    hours += Math.max(0, Math.ceil((hi - Math.max(lo, first)) / HOUR))
    buckets.forEach((b, i) => {
      const a = Math.max(b, lo)
      const e = Math.min(nextBucket(b, step), hi)
      if (e <= a) return
      const u = usd(a, e)
      const h = (e - a) / HOUR
      pts[i]!.cpu += u.cpu
      pts[i]!.memory += u.memory
      pts[i]!.cores += (u.x.cpuHours * w.replicas) / h
      pts[i]!.bytes += (u.x.memHours * w.replicas) / h
    })
    const last = integrate(steps, through - HOUR, through)
    now.cpu += last.cpuHours * w.replicas
    now.mem += last.memHours * w.replicas
  }
  return {
    ...(since != null ? { since: iso(since) } : {}),
    computedThrough: iso(through),
    from: iso(from),
    to: iso(to),
    step,
    total: { cpu: r4(total.cpu), memory: r4(total.memory), total: r4(total.cpu + total.memory) },
    inRange: { cpu: r4(inRange.cpu), memory: r4(inRange.memory), total: r4(inRange.cpu + inRange.memory) },
    releasedNow: { cpuCores: r4(now.cpu), memoryBytes: Math.round(now.mem) },
    points: pts.map((p) => ({
      time: p.time,
      cpu: r4(p.cpu),
      memory: r4(p.memory),
      total: r4(p.cpu + p.memory),
      released: { cpuCores: r4(p.cores), memoryBytes: Math.round(p.bytes) },
    })),
    coverage: { workloadHours: hours, replicasFromRecommendations: 0, withoutReplicas: 0, defaultPricing: 0 },
  }
}

/** Node cost trend of one mock cluster: a fleet of nodes that shrinks a little as releases pile up. */
export function nodeCostTrend(
  db: MockDb,
  cluster: string,
  from: number,
  to: number,
  step: 'hour' | 'day',
): NodeCostTrend {
  const spec = db.clusterSpecs.find((s) => s.name === cluster)
  const through = Math.floor(db.now / HOUR) * HOUR
  const points: NodeCostTrend['points'] = []
  if (spec) {
    const ws = db.workloads.filter((w) => w.cluster === cluster)
    const ledgers = ws.map((w) => ({ w, steps: ledger(db, w.cluster, w.namespace, w.kind, w.name) }))
    const baseNodes = Math.max(3, Math.round(ws.length / 14))
    const nodePrice = spec.cpuPrice * 8 + spec.memPrice * 32
    for (let b = bucketStart(from, step); b < Math.min(to, through); b = nextBucket(b, step)) {
      const e = Math.min(nextBucket(b, step), through)
      const h = (e - b) / HOUR
      let cores = 0
      let bytes = 0
      for (const { w, steps } of ledgers) {
        const x = integrate(steps, b, e)
        cores += (x.cpuHours * w.replicas) / h
        bytes += (x.memHours * w.replicas) / h
      }
      const day = b / (24 * HOUR)
      // Daily rhythm plus the capacity the released requests let the autoscaler give back.
      const nodes = Math.max(2, baseNodes + Math.sin(day * 2 * Math.PI) * 0.6 - cores / 8)
      const requested = Math.min(0.95, 0.62 + 0.05 * Math.sin(day * 1.3) - cores / (nodes * 8) / 3)
      points.push({
        time: iso(b),
        nodeCostUSDPerHour: r4(nodes * nodePrice),
        nodes: r4(nodes),
        cpuRequestedRatio: r4(requested),
        memoryRequestedRatio: r4(Math.min(0.95, requested + 0.07)),
        released: { cpuCores: r4(cores), memoryBytes: Math.round(bytes) },
      })
    }
  }
  return {
    cluster,
    from: iso(from),
    to: iso(to),
    step,
    relation: 'correlated',
    computedThrough: iso(through),
    points,
  }
}
