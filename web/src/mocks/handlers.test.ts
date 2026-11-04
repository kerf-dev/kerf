/**
 * Contract test: every mock response must validate against api/openapi/hub.yaml, so the UI built on
 * the mocks works unchanged against the real hub (WO-04).
 */
import { readFileSync } from 'node:fs'
import path from 'node:path'
import Ajv2020 from 'ajv/dist/2020'
import addFormats from 'ajv-formats'
import { setupServer } from 'msw/node'
import { parse } from 'yaml'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { api, unwrap } from '@/api/client'
import { handlers, getDb } from './handlers'

const spec = parse(readFileSync(path.resolve(__dirname, '../../../api/openapi/hub.yaml'), 'utf8'))
const ajv = new Ajv2020({ strict: false, allErrors: true })
addFormats(ajv)
ajv.addSchema({ ...spec, $id: 'hub' })

function expectValid(schema: string, data: unknown) {
  const validate = ajv.getSchema(`hub#/components/schemas/${schema}`)
  if (!validate) throw new Error(`no schema ${schema}`)
  const ok = validate(data)
  if (!ok) throw new Error(`${schema}: ${ajv.errorsText(validate.errors, { separator: '\n' })}`)
  expect(ok).toBe(true)
}

const server = setupServer(...handlers)
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterAll(() => server.close())

describe('mock fleet', () => {
  it('has two connected clusters with ~200 workloads each', () => {
    const db = getDb()
    const counts = db.clusters.map((c) => c.counts.workloads)
    expect(counts.filter((n) => n >= 180).length).toBe(2)
    expect(db.events.length).toBeGreaterThan(300)
  })
})

describe('mock responses match hub.yaml', () => {
  it('GET /v1/summary', async () => {
    expectValid('FleetSummary', await unwrap(api.GET('/v1/summary')))
  })
  it('GET /v1/clusters', async () => {
    expectValid('ClusterList', await unwrap(api.GET('/v1/clusters')))
  })
  it('GET /v1/workloads (all pages, filters, sort)', async () => {
    const first = await unwrap(api.GET('/v1/workloads', { params: { query: { limit: 500 } } }))
    expectValid('WorkloadList', first)
    expect(first.total).toBeGreaterThan(380)
    const filtered = await unwrap(
      api.GET('/v1/workloads', {
        params: {
          query: {
            namespace: ['payments', 'cards'],
            riskFlag: ['throttled', 'oom-killed'],
            sort: 'confidence',
            order: 'asc',
          },
        },
      }),
    )
    expectValid('WorkloadList', filtered)
    for (const w of filtered.items) {
      expect(['payments', 'cards']).toContain(w.namespace)
      expect(w.riskFlags?.some((f) => f === 'throttled' || f === 'oom-killed')).toBe(true)
    }
    const conf = filtered.items.map((w) => w.confidence ?? -1)
    expect([...conf].sort((a, b) => a - b)).toEqual(conf)
    const page2 = await unwrap(
      api.GET('/v1/workloads', {
        params: {
          query: {
            limit: 10,
            continue: (await unwrap(api.GET('/v1/workloads', { params: { query: { limit: 10 } } }))).continue,
          },
        },
      }),
    )
    expect(page2.items).toHaveLength(10)
  })
  it('GET /v1/workloads/{id} and sub-resources with an encoded id', async () => {
    const list = await unwrap(api.GET('/v1/workloads', { params: { query: { limit: 500 } } }))
    // check a sample of shapes: applied, blocked, no recommendation, sidecars, cronjobs
    const sample = [
      ...list.items.filter((w) => w.phase === 'Applied').slice(0, 2),
      ...list.items.filter((w) => w.phase === 'Blocked').slice(0, 2),
      ...list.items.filter((w) => w.phase === 'RolledBack').slice(0, 1),
      ...list.items.filter((w) => !w.recommendationID).slice(0, 1),
      ...list.items.filter((w) => w.kind === 'CronJob').slice(0, 1),
      ...list.items.filter((w) => w.riskFlags?.includes('sidecar')).slice(0, 1),
    ]
    expect(sample.length).toBeGreaterThan(5)
    for (const w of sample) {
      const id = w.id
      const detail = await unwrap(api.GET('/v1/workloads/{id}', { params: { path: { id } } }))
      expectValid('WorkloadDetail', detail)
      for (const resource of ['cpu', 'memory'] as const) {
        const usage = await unwrap(
          api.GET('/v1/workloads/{id}/usage', { params: { path: { id }, query: { resource } } }),
        )
        expectValid('UsageSeries', usage)
        expect(usage.containers.length).toBeGreaterThan(0)
      }
      const hist = await unwrap(api.GET('/v1/workloads/{id}/history', { params: { path: { id } } }))
      expectValid('WorkloadHistory', hist)
    }
  })
  it('GET /v1/workloads/facets matches list totals and ignores its own filter', async () => {
    const all = await unwrap(api.GET('/v1/workloads/facets'))
    expectValid('WorkloadFacets', all)
    expect(all.namespaces.length).toBeGreaterThan(10)
    const query = { namespace: ['payments'], kind: ['Deployment' as const] }
    const f = await unwrap(api.GET('/v1/workloads/facets', { params: { query } }))
    expectValid('WorkloadFacets', f)
    const list = await unwrap(api.GET('/v1/workloads', { params: { query: { ...query, limit: 1 } } }))
    expect(f.total).toBe(list.total)
    // the namespace facet still offers other namespaces (own filter not applied)
    expect(f.namespaces.length).toBeGreaterThan(1)
    expect(f.kinds.map((k) => k.value)).toContain('CronJob')
    // facets without their own filter set sum to the total
    expect(f.clusters.reduce((n, c) => n + c.count, 0)).toBe(f.total)
  })
  it('detail carries pricing and confidence factors that add up', async () => {
    const list = await unwrap(api.GET('/v1/workloads', { params: { query: { limit: 20 } } }))
    for (const row of list.items.filter((w) => w.recommendationID).slice(0, 5)) {
      const d = await unwrap(api.GET('/v1/workloads/{id}', { params: { path: { id: row.id } } }))
      expect(d.recommendation?.pricing?.source).toBeDefined()
      const confs = d.recommendation!.containers.map((c) => {
        const f = c.explanation!.confidenceFactors!
        const sum =
          f.coverage.score * f.coverage.weight +
          f.samples.score * f.samples.weight +
          f.variance.score * f.variance.weight
        expect(Math.abs(sum - f.confidence)).toBeLessThan(0.011)
        return f.confidence
      })
      expect(d.recommendation!.confidence).toBe(Math.min(...confs))
    }
  })
  it('unknown workload → 404 problem', async () => {
    const res = await api.GET('/v1/workloads/{id}', {
      params: { path: { id: 'prod-eu-west-1/x/Deployment/nope' } },
    })
    expect(res.response.status).toBe(404)
    expect(res.response.headers.get('content-type')).toContain('application/problem+json')
    expectValid('Problem', res.error)
  })
  it('GET /v1/savings/trend', async () => {
    const to = new Date(getDb().now).toISOString()
    const from = new Date(getDb().now - 30 * 86400_000).toISOString()
    expectValid(
      'SavingsTrend',
      await unwrap(api.GET('/v1/savings/trend', { params: { query: { from, to } } })),
    )
    const byCluster = await unwrap(
      api.GET('/v1/savings/trend', { params: { query: { groupBy: 'cluster', from, to } } }),
    )
    expectValid('SavingsTrend', byCluster)
    expect(byCluster.series.length).toBe(3)
  })
  it('GET /v1/audit with filters and paging', async () => {
    const page = await unwrap(
      api.GET('/v1/audit', {
        params: { query: { type: ['applied', 'rolled_back'], order: 'desc', limit: 50 } },
      }),
    )
    expectValid('AuditEventList', page)
    expect(page.items.every((e) => e.type === 'applied' || e.type === 'rolled_back')).toBe(true)
    expect(page.continue).toBeTruthy()
    const all = await unwrap(api.GET('/v1/audit', { params: { query: { limit: 1000 } } }))
    expectValid('AuditEventList', all)
  })
})
