/**
 * Contract and behaviour tests of the team mocks: responses validate against api/openapi/hub.yaml,
 * ownership follows the selectors, and a team-scoped caller sees and changes only its teams.
 */
import { readFileSync } from 'node:fs'
import path from 'node:path'
import Ajv2020 from 'ajv/dist/2020'
import addFormats from 'ajv-formats'
import { setupServer } from 'msw/node'
import { parse } from 'yaml'
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { getAdmin, getDb, handlers, resetDb } from './handlers'

const spec = parse(readFileSync(path.resolve(__dirname, '../../../api/openapi/hub.yaml'), 'utf8'))
const ajv = new Ajv2020({ strict: false, allErrors: true })
addFormats(ajv)
ajv.addSchema({ ...spec, $id: 'hub' })

function expectValid(schema: string, data: unknown) {
  const validate = ajv.getSchema(`hub#/components/schemas/${schema}`)
  if (!validate) throw new Error(`no schema ${schema}`)
  if (!validate(data)) throw new Error(`${schema}: ${ajv.errorsText(validate.errors, { separator: '\n' })}`)
}

const server = setupServer(...handlers)
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterAll(() => server.close())

let csrf: string | undefined
beforeEach(() => {
  localStorage.clear()
  resetDb()
  csrf = undefined
})

async function call(method: string, url: string, opts: { body?: unknown; ifMatch?: number } = {}) {
  const headers: Record<string, string> = {}
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  if (csrf) headers['X-CSRF-Token'] = csrf
  if (opts.ifMatch !== undefined) headers['If-Match'] = `"${opts.ifMatch}"`
  const res = await fetch(`http://localhost${url}`, {
    method,
    headers,
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  })
  const text = await res.text()
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return { status: res.status, json: (text ? JSON.parse(text) : undefined) as any }
}

async function login(username: string) {
  const r = await call('POST', '/v1/auth/login', { body: { username, password: 'dev-password' } })
  expect(r.status).toBe(200)
  expectValid('Me', r.json)
  csrf = r.json.csrfToken
  return r.json
}

describe('teams', () => {
  it('lists teams with counts, the unassigned workloads and the owner label', async () => {
    await login('viewer')
    const r = await call('GET', '/v1/admin/teams')
    expect(r.status).toBe(200)
    expectValid('TeamList', r.json)
    expect(r.json.ownerLabel).toBe('team')
    expect(r.json.items.map((t: { id: string }) => t.id)).toEqual(['cards', 'payments', 'risk'])
    const all = getDb().workloads.length
    const counted =
      r.json.items.reduce((n: number, t: { workloads: number }) => n + t.workloads, 0) +
      r.json.unassigned.workloads
    expect(counted).toBe(all)
    expect(r.json.unassigned.workloads).toBeGreaterThan(0)
  })

  it('creates, updates and deletes a team (admin, If-Match, reason)', async () => {
    await login('admin')
    const body = { id: 'lending', displayName: 'Lending', namespaces: ['lending'], reason: 'new team' }
    const c = await call('POST', '/v1/admin/teams', { body })
    expect(c.status, JSON.stringify(c.json)).toBe(201)
    expectValid('Team', c.json)
    expect(c.json.workloads).toBeGreaterThan(0)
    expect((await call('POST', '/v1/admin/teams', { body })).status).toBe(409)
    expect((await call('POST', '/v1/admin/teams', { body: { ...body, id: 'unassigned' } })).status).toBe(400)
    expect((await call('POST', '/v1/admin/teams', { body: { ...body, id: 'Bad_Id' } })).status).toBe(400)

    const stale = await call('PUT', '/v1/admin/teams/lending', { body, ifMatch: 7 })
    expect(stale.status).toBe(412)
    const u = await call('PUT', '/v1/admin/teams/lending', {
      body: { ...body, displayName: 'Lending team', reason: 'rename' },
      ifMatch: 1,
    })
    expect(u.status).toBe(200)
    expect(u.json.displayName).toBe('Lending team')
    expect(u.json.version).toBe(2)

    expect(
      (await call('DELETE', '/v1/admin/teams/lending', { body: { reason: 'x' }, ifMatch: 2 })).status,
    ).toBe(204)
    expect((await call('GET', '/v1/admin/teams/lending')).status).toBe(404)
  })

  it('viewers read teams but cannot write them', async () => {
    await login('viewer')
    const r = await call('POST', '/v1/admin/teams', { body: { id: 'x', reason: 'x' } })
    expect(r.status).toBe(403)
  })

  it('deleting a team removes the bindings to it', async () => {
    await login('admin')
    const t = (await call('GET', '/v1/admin/teams/payments')).json
    await call('DELETE', '/v1/admin/teams/payments', { body: { reason: 'x' }, ifMatch: t.version })
    expect(getAdmin().users.get('paula')!.teamRoles).toEqual([])
  })
})

describe('team fields on the fleet', () => {
  it('rows, details, facets and the summary carry the team', async () => {
    await login('viewer')
    const list = await call('GET', '/v1/workloads?limit=500')
    expectValid('WorkloadList', list.json)
    expect(new Set(list.json.items.map((w: { team: string }) => w.team))).toContain('unassigned')
    const id = list.json.items.find((w: { team: string }) => w.team === 'payments').id
    const d = await call('GET', `/v1/workloads/${encodeURIComponent(id)}`)
    expectValid('WorkloadDetail', d.json)
    expect(d.json.team).toBe('payments')

    const facets = await call('GET', '/v1/workloads/facets')
    expectValid('WorkloadFacets', facets.json)
    expect(facets.json.teams.map((x: { value: string }) => x.value)).toEqual(
      expect.arrayContaining(['payments', 'cards', 'risk', 'unassigned']),
    )

    const only = await call('GET', '/v1/workloads?team=payments&team=unassigned&limit=500')
    expect(only.json.items.every((w: { team: string }) => ['payments', 'unassigned'].includes(w.team))).toBe(
      true,
    )

    const s = await call('GET', '/v1/summary')
    expectValid('FleetSummary', s.json)
    expect(s.json.teams.map((t: { team: string }) => t.team)).toEqual([
      'cards',
      'payments',
      'risk',
      'unassigned',
    ])
  })

  it('trend and realized savings take team and groupBy=team', async () => {
    await login('viewer')
    const t = await call('GET', '/v1/savings/trend?groupBy=team')
    expectValid('SavingsTrend', t.json)
    expect(t.json.series.map((x: { team: string }) => x.team)).toContain('payments')
    const one = await call('GET', '/v1/savings/trend?team=payments&groupBy=none')
    expectValid('SavingsTrend', one.json)
    const r = await call('GET', '/v1/savings/realized?step=month&team=payments')
    expectValid('RealizedSavings', r.json)
  })
})

describe('users with a scope', () => {
  it('lists scope and team roles; me carries them with the highest role', async () => {
    const me = await login('victor')
    expect(me.role).toBe('operator')
    expect(me.user.role).toBe('viewer')
    expect(me.scope).toBe('fleet')
    expect(me.teamRoles).toEqual([{ team: 'risk', role: 'operator' }])
    await login('admin')
    const users = await call('GET', '/v1/admin/users')
    expectValid('UserList', users.json)
    const paula = users.json.items.find((u: { username: string }) => u.username === 'paula')
    expect(paula).toMatchObject({ scope: 'teams', role: 'operator' })
  })

  it('validates scope and team roles on create and update', async () => {
    await login('admin')
    const base = { username: 'tess', password: 'a-long-password', mustChangePassword: false, reason: 'x' }
    const bad = async (extra: object) =>
      (await call('POST', '/v1/admin/users', { body: { ...base, role: 'viewer', ...extra } })).status
    expect(await bad({ scope: 'teams', teamRoles: [] })).toBe(400)
    expect(await bad({ scope: 'teams', teamRoles: [{ team: 'nope', role: 'viewer' }] })).toBe(400)
    expect(await bad({ role: 'admin', scope: 'teams', teamRoles: [{ team: 'cards', role: 'viewer' }] })).toBe(
      400,
    )
    const ok = await call('POST', '/v1/admin/users', {
      body: { ...base, role: 'viewer', scope: 'teams', teamRoles: [{ team: 'cards', role: 'operator' }] },
    })
    expect(ok.status, JSON.stringify(ok.json)).toBe(201)
    expectValid('User', ok.json)
    expect(ok.json.role).toBe('operator') // derived

    const idp = (await call('GET', '/v1/admin/users/idp-cards')).json
    const r = await call('PATCH', '/v1/admin/users/idp-cards', {
      body: { role: 'operator', reason: 'x' },
      ifMatch: idp.version,
    })
    expect(r.status).toBe(409)
    expect(r.json.code).toBe('idp_managed')
  })
})

describe('a team-scoped caller', () => {
  it('sees only its teams everywhere', async () => {
    await login('paula')
    const teams = await call('GET', '/v1/admin/teams')
    expect(teams.json.items.map((t: { id: string }) => t.id)).toEqual(['payments'])
    expect(teams.json.unassigned).toBeUndefined()

    const list = await call('GET', '/v1/workloads?limit=500')
    expect(list.json.items.length).toBeGreaterThan(0)
    expect(list.json.items.every((w: { team: string }) => w.team === 'payments')).toBe(true)
    const facets = await call('GET', '/v1/workloads/facets')
    expect(facets.json.teams.map((x: { value: string }) => x.value)).toEqual(['payments'])

    const s = await call('GET', '/v1/summary')
    expect(s.json.fleet.workloads).toBe(list.json.total)
    expect(s.json.teams.map((t: { team: string }) => t.team)).toEqual(['payments'])

    const other = getDb().workloads.find((w) => w.namespace === 'cards')!
    expect((await call('GET', `/v1/workloads/${encodeURIComponent(other.id)}`)).status).toBe(404)
  })

  it('gets 403 scope_forbidden from fleet-wide endpoints', async () => {
    await login('paula')
    for (const url of [
      '/v1/admin/users',
      '/v1/admin/policies',
      '/v1/admin/policy-inventory',
      '/v1/admin/tokens',
      '/v1/clusters/prod-eu-west-1/capacity',
      '/v1/clusters/prod-eu-west-1/readiness',
      '/v1/savings/realized/node-cost?cluster=prod-eu-west-1',
    ]) {
      const r = await call('GET', url)
      expect([url, r.status, r.json.code]).toEqual([url, 403, 'scope_forbidden'])
    }
    for (const url of ['/v1/admin/settings', '/v1/admin/automation', '/v1/admin/teams'])
      expect([url, (await call('GET', url)).status]).toEqual([url, 200])
  })

  it('may change workloads of a team it operates, not those of a team it views', async () => {
    // Workloads without an override (an update would need If-Match).
    const free = (ns: string) =>
      getDb().workloads.find(
        (w) =>
          w.namespace === ns &&
          w.cluster.startsWith('prod-') &&
          !getAdmin().policies.some((p) => p.workloadId === w.id && !p.deletedAt),
      )!
    const payments = free('payments')
    await login('paula')
    const own = await call('PUT', `/v1/workloads/${encodeURIComponent(payments.id)}/override`, {
      body: { spec: { cpu: { mode: 'off' } }, reason: 'x' },
    })
    expect(own.status, JSON.stringify(own.json)).toBe(200)

    // A fleet viewer who is operator on `risk` only.
    await login('victor')
    const risk = free('risk')
    const ok = await call('PUT', `/v1/workloads/${encodeURIComponent(risk.id)}/override`, {
      body: { spec: { cpu: { mode: 'off' } }, reason: 'x' },
    })
    expect(ok.status, JSON.stringify(ok.json)).toBe(200)
    const denied = await call('PUT', `/v1/workloads/${encodeURIComponent(payments.id)}/override`, {
      body: { spec: { cpu: { mode: 'off' } }, reason: 'x' },
    })
    expect([denied.status, denied.json.code]).toEqual([403, 'scope_forbidden'])
  })
})
