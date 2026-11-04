import type { AuditEvent, WorkloadRecommendation } from '@/api/types'

/**
 * The hub recommends on every run (every few minutes in dev) and agents may repeat the same
 * skipped/blocked event, so raw histories are mostly repeats. These helpers collapse runs of identical
 * entries so the timeline shows changes, with a count for the repeats.
 */

const recValues = (r: WorkloadRecommendation) =>
  JSON.stringify(r.containers.map((c) => [c.name, c.recommended, c.removeLimits ?? []]))

export interface RecRun {
  /** First recommendation of the run: when these values appeared. */
  first: WorkloadRecommendation
  /** Latest recommendation of the run. */
  latest: WorkloadRecommendation
  count: number
  /** The run before this one (older values), if any. */
  previous?: WorkloadRecommendation
}

/** `recs` newest first; returns runs newest first. */
export function collapseRecommendations(recs: WorkloadRecommendation[]): RecRun[] {
  const runs: RecRun[] = []
  for (let i = recs.length - 1; i >= 0; i--) {
    const r = recs[i]!
    const cur = runs[runs.length - 1]
    if (cur && recValues(cur.latest) === recValues(r)) {
      cur.latest = r
      cur.count++
    } else {
      runs.push({ first: r, latest: r, count: 1, previous: cur?.latest })
    }
  }
  return runs.reverse()
}

const eventKey = (e: AuditEvent) =>
  JSON.stringify([
    e.type,
    e.reason,
    e.message ?? '',
    e.resources ?? [],
    e.containers ?? [],
    e.workloadId ?? '',
  ])

export interface EventRun {
  /** Newest event of the run. */
  latest: AuditEvent
  /** Oldest event of the run. */
  first: AuditEvent
  count: number
}

/** `events` newest first (one workload); consecutive identical events become one run. */
export function collapseEvents(events: AuditEvent[]): EventRun[] {
  const runs: EventRun[] = []
  for (const e of events) {
    const cur = runs[runs.length - 1]
    if (cur && eventKey(cur.first) === eventKey(e)) {
      cur.first = e
      cur.count++
    } else runs.push({ latest: e, first: e, count: 1 })
  }
  return runs
}

/**
 * Groups bursts across workloads: consecutive events (newest first) with the same type, reason and
 * message within `windowMs` of the group's newest event. Groups smaller than `min` are split back into
 * single events.
 */
export function groupBursts(events: AuditEvent[], windowMs = 3 * 60_000, min = 3): AuditEvent[][] {
  const key = (e: AuditEvent) => `${e.type}|${e.reason}|${e.message ?? ''}`
  const groups: AuditEvent[][] = []
  for (const e of events) {
    const g = groups[groups.length - 1]
    if (g && key(g[0]!) === key(e) && Date.parse(g[0]!.time) - Date.parse(e.time) <= windowMs) g.push(e)
    else groups.push([e])
  }
  return groups.flatMap((g) => (g.length >= min ? [g] : g.map((e) => [e])))
}
