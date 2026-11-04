import { describe, expect, it } from 'vitest'
import type { AuditEvent, WorkloadRecommendation } from '@/api/types'
import { collapseEvents, collapseRecommendations, groupBursts } from './collapse'

const rec = (id: string, at: string, cpu: string): WorkloadRecommendation => ({
  id,
  workload: { namespace: 'ns', kind: 'Deployment', name: 'w' },
  generatedAt: at,
  settings: {},
  containers: [{ name: 'app', current: {}, recommended: { requests: { cpu } } }],
  confidence: 0.9,
  riskFlags: [],
  monthlySavingsUSD: { cpu: 1, memory: 0, total: 1 },
  dataCoverage: { ratio: 1, window: '336h0m0s' },
})

const ev = (id: string, at: string, reason = 'ModeAudit', workload = 'w'): AuditEvent => ({
  id,
  type: 'skipped',
  time: at,
  workload: { namespace: 'ns', kind: 'Deployment', name: workload },
  reason,
  cluster: 'c',
  receivedAt: at,
  workloadId: `c/ns/Deployment/${workload}`,
})

describe('collapseRecommendations', () => {
  it('merges runs with equal values and remembers the previous run', () => {
    // newest first: 3 × 100m (latest), then 2 × 200m
    const runs = collapseRecommendations([
      rec('e', '2026-11-04T05:00:00Z', '100m'),
      rec('d', '2026-11-04T04:00:00Z', '100m'),
      rec('c', '2026-11-04T03:00:00Z', '100m'),
      rec('b', '2026-11-04T02:00:00Z', '200m'),
      rec('a', '2026-11-04T01:00:00Z', '200m'),
    ])
    expect(runs.map((r) => [r.latest.id, r.first.id, r.count, r.previous?.id])).toEqual([
      ['e', 'c', 3, 'b'],
      ['b', 'a', 2, undefined],
    ])
  })
})

describe('collapseEvents', () => {
  it('merges consecutive identical events only', () => {
    const runs = collapseEvents([
      ev('4', '2026-11-04T04:00:00Z'),
      ev('3', '2026-11-04T03:00:00Z'),
      ev('2', '2026-11-04T02:00:00Z', 'Cooldown'),
      ev('1', '2026-11-04T01:00:00Z'),
    ])
    expect(runs.map((r) => [r.latest.id, r.first.id, r.count])).toEqual([
      ['4', '3', 2],
      ['2', '2', 1],
      ['1', '1', 1],
    ])
  })
})

describe('groupBursts', () => {
  it('groups same decision across workloads within the window, splitting small groups', () => {
    const g = groupBursts([
      ev('a', '2026-11-04T04:10:30Z', 'ModeAudit', 'w1'),
      ev('b', '2026-11-04T04:10:20Z', 'ModeAudit', 'w2'),
      ev('c', '2026-11-04T04:10:10Z', 'ModeAudit', 'w3'),
      ev('d', '2026-11-04T04:05:00Z', 'ModeAudit', 'w4'),
      ev('e', '2026-11-04T04:04:50Z', 'Cooldown', 'w5'),
    ])
    expect(g.map((x) => x.map((e) => e.id))).toEqual([['a', 'b', 'c'], ['d'], ['e']])
  })
})
