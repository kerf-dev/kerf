/**
 * Contract and behaviour tests of the admin-area mocks (WO-22): every response validates against
 * api/openapi/hub.yaml, and the mock enforces the contract's auth, CSRF, If-Match, role and D-025
 * gating rules, so the UI built on it handles the real hub's answers.
 */
import { readFileSync } from 'node:fs'
import path from 'node:path'
import Ajv2020 from 'ajv/dist/2020'
import addFormats from 'ajv-formats'
import { setupServer } from 'msw/node'
import { parse } from 'yaml'
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { getAdmin, getDb, handlers, resetDb, timing } from './handlers'

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

const DEFAULT_TIMING = { ...timing }
let csrf: string | undefined
beforeEach(async () => {
  localStorage.clear()
  resetDb()
  Object.assign(timing, DEFAULT_TIMING)
  csrf = undefined
  // Sign-in is required: every test starts as a signed-in viewer (signOut() for none).
  await login('viewer')
})

async function signOut() {
  expect((await call('POST', '/v1/auth/logout')).status).toBe(204)
  csrf = undefined
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
interface Res<T = any> {
  status: number
  json: T
  headers: Headers
}
// eslint-disable-next-line @typescript-eslint/no-explicit-any
async function call<T = any>(
  method: string,
  url: string,
  opts: { body?: unknown; ifMatch?: string | number; noCsrf?: boolean } = {},
): Promise<Res<T>> {
  const headers: Record<string, string> = {}
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  if (csrf && !opts.noCsrf) headers['X-CSRF-Token'] = csrf
  if (opts.ifMatch !== undefined) headers['If-Match'] = `"${opts.ifMatch}"`
  const res = await fetch(`http://localhost${url}`, {
    method,
    headers,
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  })
  const text = await res.text()
  return { status: res.status, json: text ? JSON.parse(text) : undefined, headers: res.headers }
}

function expectProblem(r: Res, status: number, code: string) {
  expect(r.status, JSON.stringify(r.json)).toBe(status)
  expectValid('Problem', r.json)
  expect(r.json.code).toBe(code)
}

async function login(username: string, password = 'dev-password') {
  const r = await call('POST', '/v1/auth/login', { body: { username, password } })
  expect(r.status, JSON.stringify(r.json)).toBe(200)
  expectValid('Me', r.json)
  csrf = r.json.csrfToken
  return r.json
}

const enc = (id: string) => encodeURIComponent(id)
const P = (id: string) => `/v1/admin/policies/${id}`
const policy = (name: string) => getAdmin().policies.find((p) => p.name === name && !p.deletedAt)!

/** A workload on prod-eu-west-1 in audit (both resources) with a recommendation and no approval. */
function approvable() {
  const st = getAdmin()
  return getDb().workloads.find(
    (w) =>
      w.cluster === 'prod-eu-west-1' &&
      w.recommendation &&
      w.modes.cpu === 'audit' &&
      w.modes.memory === 'audit' &&
      !st.annotations.has(w.id) &&
      !st.approvals.some((a) => a.workloadId === w.id) &&
      !st.policies.some((p) => p.workloadId === w.id && !p.deletedAt),
  )!
}

describe('auth and session', () => {
  it('without a session everything but sign-in answers 401', async () => {
    await signOut()
    expectProblem(await call('GET', '/v1/me'), 401, 'unauthorized')
    expectProblem(await call('GET', '/v1/summary'), 401, 'unauthorized')
    expectProblem(await call('GET', '/v1/workloads'), 401, 'unauthorized')
    await login('viewer')
    expect((await call('GET', '/v1/summary')).status).toBe(200)
  })

  it('login, me, logout with CSRF', async () => {
    await signOut()
    expectProblem(
      await call('POST', '/v1/auth/login', { body: { username: 'admin', password: 'nope' } }),
      401,
      'invalid_credentials',
    )
    const me = await login('admin')
    expect(me.authMethod).toBe('session')
    expect(me.role).toBe('admin')
    expect(me.user.username).toBe('admin')
    const again = await call('GET', '/v1/me')
    expectValid('Me', again.json)
    expect(again.json.csrfToken).toBe(me.csrfToken)
    expectProblem(await call('POST', '/v1/auth/logout', { noCsrf: true }), 403, 'csrf_invalid')
    expect((await call('POST', '/v1/auth/logout')).status).toBe(204)
    expectProblem(await call('GET', '/v1/me'), 401, 'unauthorized')
    // Signing out again (no session left) still answers 204.
    expect((await call('POST', '/v1/auth/logout')).status).toBe(204)
  })

  it('locks after 5 failures with answers that never reveal whether a user exists', async () => {
    const attempt = (username: string, password: string) =>
      call('POST', '/v1/auth/login', { body: { username, password } })
    for (const username of ['bob', 'nobody-at-all']) {
      for (let i = 0; i < 5; i++) expectProblem(await attempt(username, 'x'), 401, 'invalid_credentials')
      const r = await attempt(username, 'dev-password')
      expectProblem(r, 429, 'rate_limited')
      expect(Number(r.headers.get('Retry-After'))).toBeGreaterThan(0)
      expect(r.json.detail).toBe('Too many sign-in attempts. Try again later.')
    }
    // A disabled user gets the answer of a wrong password.
    const dana = await attempt('dana', 'dev-password')
    const ghost = await attempt('ghost', 'dev-password')
    expectProblem(dana, 401, 'invalid_credentials')
    expect(dana.json).toEqual(ghost.json)
  })

  it('forced password change', async () => {
    const me = await login('sam')
    expect(me.user.mustChangePassword).toBe(true)
    expectProblem(
      await call('POST', '/v1/me/password', {
        body: { currentPassword: 'wrong', newPassword: 'new-password-1' },
      }),
      400,
      'invalid_credentials',
    )
    expect(
      (
        await call('POST', '/v1/me/password', {
          body: { currentPassword: 'dev-password', newPassword: 'new-password-1' },
        })
      ).status,
    ).toBe(204)
    expect((await call('GET', '/v1/me')).json.user.mustChangePassword).toBe(false)
  })
})

describe('settings', () => {
  it('reads and versions', async () => {
    const all = await call('GET', '/v1/admin/settings')
    expectValid('Settings', all.json)
    for (const section of ['recommender', 'pricing']) {
      const doc = await call('GET', `/v1/admin/settings/${section}`)
      expectValid(section === 'recommender' ? 'RecommenderSettingsDoc' : 'PricingSettingsDoc', doc.json)
      expect(doc.headers.get('ETag')).toBe(`"${doc.json.version}"`)
      const v = await call('GET', `/v1/admin/settings/${section}/versions`)
      expectValid('SettingsVersionList', v.json)
      expect(v.json.items.length).toBeGreaterThan(0)
    }
  })

  it('PUT needs admin, CSRF, If-Match and valid values', async () => {
    const doc = (await call('GET', '/v1/admin/settings/recommender')).json
    const body = { value: { ...doc.value, interval: '2h' }, reason: 'less load' }
    expectProblem(
      await call('PUT', '/v1/admin/settings/recommender', { body, ifMatch: doc.version }),
      403,
      'forbidden',
    )
    await login('operator')
    expectProblem(
      await call('PUT', '/v1/admin/settings/recommender', { body, ifMatch: doc.version }),
      403,
      'forbidden',
    )
    await login('admin')
    expectProblem(await call('PUT', '/v1/admin/settings/recommender', { body }), 428, 'precondition_required')
    expectProblem(
      await call('PUT', '/v1/admin/settings/recommender', { body, ifMatch: 1 }),
      412,
      'version_conflict',
    )
    expectProblem(
      await call('PUT', '/v1/admin/settings/recommender', { body, ifMatch: doc.version, noCsrf: true }),
      403,
      'csrf_invalid',
    )
    expectProblem(
      await call('PUT', '/v1/admin/settings/recommender', {
        body: { ...body, value: { ...body.value, interval: 'soon' } },
        ifMatch: doc.version,
      }),
      400,
      'invalid_settings',
    )
    const ok = await call('PUT', '/v1/admin/settings/recommender', { body, ifMatch: doc.version })
    expect(ok.status).toBe(200)
    expectValid('RecommenderSettingsDoc', ok.json)
    expect(ok.json.version).toBe(doc.version + 1)
    expect(ok.headers.get('ETag')).toBe(`"${doc.version + 1}"`)
    const pricing = (await call('GET', '/v1/admin/settings/pricing')).json
    const p = await call('PUT', '/v1/admin/settings/pricing', {
      body: { value: { ...pricing.value, defaultDiscountPercent: 5 }, reason: 'EDP' },
      ifMatch: pricing.version,
    })
    expectValid('PricingSettingsDoc', p.json)
    const audit = await call('GET', '/v1/audit/config?targetType=settings&order=desc&limit=2')
    expectValid('ConfigChangeList', audit.json)
    expect(audit.json.items[0].actor.name).toBe('admin')
  })
})

describe('policies', () => {
  it('lists, reads, versions and inventory validate', async () => {
    const list = await call('GET', '/v1/admin/policies')
    expectValid('AdminPolicyList', list.json)
    const types = new Set(list.json.items.map((p: { type: string }) => p.type))
    expect([...types].sort()).toEqual(['cluster-default', 'global-default', 'policy', 'workload-override'])
    expect(list.json.items.some((p: { deletedAt?: string }) => p.deletedAt)).toBe(false)
    const withDeleted = await call('GET', '/v1/admin/policies?includeDeleted=true')
    expect(withDeleted.json.items.some((p: { deletedAt?: string }) => p.deletedAt)).toBe(true)
    for (const p of list.json.items) {
      const one = await call('GET', P(p.id))
      expectValid('AdminPolicy', one.json)
      expect(one.headers.get('ETag')).toBe(`"${p.version}"`)
      expectValid('PolicyVersionList', (await call('GET', `${P(p.id)}/versions`)).json)
    }
    const payments = policy('payments-autopilot')
    expect((await call('GET', `${P(payments.id)}/versions`)).json.items.length).toBeGreaterThanOrEqual(3)
    const inv = await call('GET', '/v1/admin/policy-inventory')
    expectValid('PolicyInventoryList', inv.json)
    const eu = inv.json.items.find((c: { cluster: string }) => c.cluster === 'prod-eu-west-1')
    expect(eu.items.some((i: { source: string }) => i.source === 'git')).toBe(true)
    expect(
      eu.items.find((i: { name: string }) => i.name === 'payments-autopilot').matchedWorkloads,
    ).toBeGreaterThan(0)
    expectProblem(await call('GET', P('00000000-0000-4000-8000-000000000000')), 404, 'not_found')
  })

  it('create, validate, gate, update, delete, revert', async () => {
    const draft = {
      type: 'policy',
      name: 'cards-cpu-auto',
      clusters: ['prod-eu-west-1'],
      spec: { priority: 120, selector: { namespaces: ['cards'] }, cpu: { mode: 'auto-patch' } },
      reason: 'FinOps Q4',
    }
    await login('operator')
    expectProblem(await call('POST', '/v1/admin/policies', { body: draft }), 403, 'forbidden')
    await login('admin')
    expectProblem(
      await call('POST', '/v1/admin/policies', { body: { ...draft, name: 'ro-x' } }),
      400,
      'invalid_policy',
    )
    const bad = await call('POST', '/v1/admin/policies', {
      body: { ...draft, spec: { ...draft.spec, cpu: { mode: 'auto-patch', limits: 'ratio' } } },
    })
    expectProblem(bad, 400, 'invalid_policy')
    expect(bad.json.detail).toContain('spec.cpu.limitRatio')
    expectProblem(
      await call('POST', '/v1/admin/policies', { body: { ...draft, name: 'payments-autopilot' } }),
      409,
      'already_exists',
    )
    // auto-patch for every cluster: prod-us-east-1 and dr-eu-central-1 are not AutomationReady
    const all = await call('POST', '/v1/admin/policies', { body: { ...draft, clusters: [] } })
    expectProblem(all, 409, 'cluster_not_automation_ready')
    expect(all.json.detail).toContain('prod-us-east-1 (ReadyAudit)')
    const forced = await call('POST', '/v1/admin/policies', {
      body: { ...draft, name: 'cards-everywhere', clusters: [], overrideReadiness: true },
    })
    expect(forced.status).toBe(201)

    const created = await call('POST', '/v1/admin/policies', { body: draft })
    expect(created.status).toBe(201)
    expectValid('AdminPolicy', created.json)
    expect(created.headers.get('ETag')).toBe('"1"')
    const id = created.json.id
    // the mock fleet follows: cards workloads on prod-eu-west-1 are now CPU auto-patch (unless annotated)
    const cards = getDb().workloads.filter(
      (w) =>
        w.cluster === 'prod-eu-west-1' &&
        w.policy === 'cards-cpu-auto' &&
        w.modes.cpuSource === 'policy' &&
        !getAdmin().policies.some((p) => p.workloadId === w.id && !p.deletedAt),
    )
    expect(cards.length).toBeGreaterThan(0)
    expect(cards.every((w) => w.modes.cpu === 'auto-patch')).toBe(true)

    const upd = {
      ...draft,
      spec: { ...draft.spec, guardrails: { maxChangePercent: 20 } },
      reason: 'smaller steps',
    }
    expectProblem(await call('PUT', P(id), { body: upd }), 428, 'precondition_required')
    expectProblem(await call('PUT', P(id), { body: upd, ifMatch: 7 }), 412, 'version_conflict')
    expectProblem(
      await call('PUT', P(id), { body: { ...upd, type: 'cluster-default' }, ifMatch: 1 }),
      400,
      'type_immutable',
    )
    const v2 = await call('PUT', P(id), { body: upd, ifMatch: 1 })
    expectValid('AdminPolicy', v2.json)
    expect(v2.json.version).toBe(2)
    const del = await call('DELETE', P(id), { body: { reason: 'replaced' }, ifMatch: 2 })
    expect(del.status).toBe(200)
    expectValid('AdminPolicy', del.json)
    expect(del.json.deletedAt).toBeTruthy()
    const rev = await call('POST', `${P(id)}/revert`, {
      body: { version: 1, reason: 'rollback' },
      ifMatch: 3,
    })
    expect(rev.status).toBe(200)
    expectValid('AdminPolicy', rev.json)
    expect(rev.json.version).toBe(4)
    expect(rev.json.deletedAt).toBeUndefined()
    expect(rev.json.spec.guardrails).toBeUndefined()
    const versions = (await call('GET', `${P(id)}/versions`)).json
    expectValid('PolicyVersionList', versions)
    expect(versions.items.map((v: { operation: string }) => v.operation)).toEqual([
      'revert',
      'delete',
      'update',
      'create',
    ])
    expect(versions.items[0].revertedFrom).toBe(1)
    const audit = await call('GET', `/v1/audit/config?targetId=${id}`)
    expectValid('ConfigChangeList', audit.json)
    expect(audit.json.items.map((c: { action: string }) => c.action)).toEqual([
      'PolicyCreated',
      'PolicyUpdated',
      'PolicyDeleted',
      'PolicyReverted',
    ])
  })

  it('global defaults: second create conflicts, update changes modes', async () => {
    await login('admin')
    expectProblem(
      await call('POST', '/v1/admin/policies', { body: { type: 'global-default', spec: {}, reason: 'x' } }),
      409,
      'already_exists',
    )
    const g = policy('ro-global-defaults')
    const r = await call('PUT', P(g.id), {
      body: {
        type: 'global-default',
        spec: { ...g.spec, memory: { mode: 'off' } },
        reason: 'memory hands-off',
      },
      ifMatch: g.version,
    })
    expect(r.status).toBe(200)
    const ws = getDb().workloads.filter((w) => w.modes.memorySource === 'global-default')
    expect(ws.length).toBeGreaterThan(0)
    expect(ws.every((w) => w.modes.memory === 'off')).toBe(true)
  })
})

describe('preview and effective policy', () => {
  it('preview numbers are consistent', async () => {
    await login('operator')
    const req = {
      changes: [
        {
          op: 'upsert',
          policy: {
            type: 'policy',
            name: 'cards-cpu-auto',
            clusters: ['prod-eu-west-1'],
            spec: { priority: 100, selector: { namespaces: ['cards'] }, cpu: { mode: 'auto-patch' } },
          },
        },
      ],
      limit: 1000,
    }
    const r = await call('POST', '/v1/admin/preview', { body: req })
    expect(r.status).toBe(200)
    expectValid('ImpactPreview', r.json)
    expect(r.json.workloadsEvaluated).toBe(getDb().workloads.length)
    expect(r.json.workloadsChanged).toBe(r.json.items.length)
    expect(r.json.workloadsChanged).toBeGreaterThan(3)
    const cpu = r.json.items
      .filter(
        (i: { before: { cpu: string }; after: { cpu: string } }) =>
          i.after.cpu === 'auto-patch' && i.before.cpu !== 'auto-patch',
      )
      .reduce(
        (s: number, i: { monthlySavingsUSD?: { cpu: number } }) => s + (i.monthlySavingsUSD?.cpu ?? 0),
        0,
      )
    expect(r.json.savingsIntoAutoPatchUSD.cpu).toBeCloseTo(cpu, 1)
    expect(r.json.savingsOutOfAutoPatchUSD.total).toBe(0)
    for (const i of r.json.items) {
      expect(i.namespace).toBe('cards')
      expect(i.policyAfter).toBe('cards-cpu-auto')
    }
    const small = await call('POST', '/v1/admin/preview', { body: { ...req, limit: 2 } })
    expect(small.json.items).toHaveLength(2)
    expect(small.json.truncated).toBe(true)
    // bulk "Set mode…": override drafts per workload
    const w = approvable()
    const bulk = await call('POST', '/v1/admin/preview', {
      body: {
        changes: [
          {
            op: 'upsert',
            policy: {
              type: 'workload-override',
              workloadId: w.id,
              spec: { cpu: { mode: 'off' }, memory: { mode: 'off' } },
            },
          },
        ],
      },
    })
    expectValid('ImpactPreview', bulk.json)
    expect(bulk.json.workloadsChanged).toBe(1)
    expect(bulk.json.items[0].after).toMatchObject({ cpu: 'off', memory: 'off', cpuSource: 'policy' })
    // matchLabels is unknown to the hub → warning
    const labels = await call('POST', '/v1/admin/preview', {
      body: {
        changes: [
          {
            op: 'upsert',
            policy: {
              type: 'policy',
              name: 'l',
              spec: { priority: 5, selector: { matchLabels: { team: 'x' } }, cpu: { mode: 'off' } },
            },
          },
        ],
      },
    })
    expect(labels.json.warnings?.[0]).toContain('matchLabels')
    // viewers cannot preview
    await login('viewer')
    expectProblem(await call('POST', '/v1/admin/preview', { body: req }), 403, 'forbidden')
  })

  it('effective policy matches the workload modes', async () => {
    const ws = getDb().workloads.filter((_, i) => i % 37 === 0)
    for (const w of ws) {
      const e = await call('GET', `/v1/workloads/${enc(w.id)}/effective-policy`)
      expect(e.status).toBe(200)
      expectValid('EffectivePolicy', e.json)
      const d = await call('GET', `/v1/workloads/${enc(w.id)}`)
      expect(e.json.modes).toEqual(d.json.modes)
      expect(e.json.layers[0].source).toBe('built-in')
    }
    const pay = getDb().workloads.find(
      (w) => w.cluster === 'prod-eu-west-1' && w.namespace === 'payments' && w.kind === 'CronJob',
    )
    if (pay) {
      const e = (await call('GET', `/v1/workloads/${enc(pay.id)}/effective-policy`)).json
      const loser = e.layers.find((l: { name?: string }) => l.name === 'batch-jobs-rollout')
      expect(loser.applied).toBe(false)
      expect(loser.note).toContain('payments-autopilot')
    }
  })
})

describe('workload override and approve once', () => {
  it('override lifecycle', async () => {
    const w = approvable()
    const url = `/v1/workloads/${enc(w.id)}/override`
    expectProblem(await call('GET', url), 404, 'not_found')
    expectProblem(
      await call('PUT', url, { body: { spec: { cpu: { mode: 'off' } }, reason: 'x' } }),
      403,
      'forbidden',
    )
    await login('operator')
    expectProblem(
      await call('PUT', url, { body: { spec: { priority: 5, cpu: { mode: 'off' } }, reason: 'x' } }),
      400,
      'invalid_override',
    )
    expectProblem(
      await call('PUT', url, { body: { spec: { cpu: { mode: 'auto-patch' } }, reason: 'x' } }),
      403,
      'admin_required',
    )
    expectProblem(await call('PUT', url, { body: { spec: { cpu: { mode: 'off' } } } }), 400, 'invalid_reason')
    const put = await call('PUT', url, {
      body: { spec: { cpu: { mode: 'off' }, memory: { mode: 'off' } }, reason: 'Exclude: batch job' },
    })
    expect(put.status).toBe(200)
    expectValid('AdminPolicy', put.json)
    expect(put.json.name).toMatch(/^ro-override-[0-9a-f]{16}$/)
    expect(put.json.spec.selector.workloads).toEqual([{ kind: w.kind, namespace: w.namespace, name: w.name }])
    expect(put.headers.get('ETag')).toBe('"1"')
    const d = await call('GET', `/v1/workloads/${enc(w.id)}`)
    expectValid('WorkloadDetail', d.json)
    expect(d.json.modes).toMatchObject({ cpu: 'off', memory: 'off', cpuSource: 'policy' })
    expect(d.json.override.id).toBe(put.json.id)
    expect(d.json.status.override).toBe(put.json.name)
    expectValid('AdminPolicy', (await call('GET', url)).json)
    expectProblem(
      await call('PUT', url, { body: { spec: { cpu: { mode: 'audit' } }, reason: 'y' } }),
      428,
      'precondition_required',
    )
    const upd = await call('PUT', url, {
      body: { spec: { cpu: { mode: 'audit' } }, reason: 'y' },
      ifMatch: 1,
    })
    expect(upd.json.version).toBe(2)
    expectProblem(await call('DELETE', url, { body: { reason: 'z' } }), 428, 'precondition_required')
    expect((await call('DELETE', url, { body: { reason: 'back to team policy' }, ifMatch: 2 })).status).toBe(
      204,
    )
    expectProblem(await call('GET', url), 404, 'not_found')
    const h = await call('GET', `/v1/workloads/${enc(w.id)}/history`)
    expectValid('WorkloadHistory', h.json)
    expect(h.json.configChanges.map((c: { action: string }) => c.action)).toEqual([
      'OverrideCleared',
      'OverrideSet',
      'OverrideSet',
    ])
  })

  it('approve once: stale, not in audit, gating, consumed', async () => {
    const w = approvable()
    const url = `/v1/workloads/${enc(w.id)}/approve`
    await login('operator')
    expectProblem(
      await call('POST', url, {
        body: { recommendationID: '00000000-0000-4000-8000-000000000000', reason: 'ok' },
      }),
      409,
      'recommendation_stale',
    )
    const auto = getDb().workloads.find(
      (x) =>
        x.cluster === 'prod-eu-west-1' &&
        x.recommendation &&
        x.modes.cpu !== 'audit' &&
        x.modes.memory !== 'audit',
    )!
    expectProblem(
      await call('POST', `/v1/workloads/${enc(auto.id)}/approve`, {
        body: { recommendationID: auto.recommendation!.id, reason: 'ok' },
      }),
      409,
      'not_in_audit',
    )
    const us = getDb().workloads.find(
      (x) => x.cluster === 'prod-us-east-1' && x.recommendation && x.modes.cpu === 'audit',
    )!
    const usUrl = `/v1/workloads/${enc(us.id)}/approve`
    expectProblem(
      await call('POST', usUrl, { body: { recommendationID: us.recommendation!.id, reason: 'ok' } }),
      409,
      'cluster_not_automation_ready',
    )
    expectProblem(
      await call('POST', usUrl, {
        body: { recommendationID: us.recommendation!.id, reason: 'ok', overrideReadiness: true },
      }),
      403,
      'admin_required',
    )
    timing.approvalMs = 0
    const ok = await call('POST', url, {
      body: { recommendationID: w.recommendation!.id, reason: 'reviewed with owner' },
    })
    expect(ok.status).toBe(201)
    expectValid('Approval', ok.json)
    expect(ok.json.state).toBe('pending')
    const d = await call('GET', `/v1/workloads/${enc(w.id)}`)
    expectValid('WorkloadDetail', d.json)
    expect(d.json.approval).toMatchObject({ id: ok.json.id, state: 'consumed' })
    const ev = await call(
      'GET',
      `/v1/audit?workload=${w.name}&namespace=${w.namespace}&cluster=${w.cluster}&order=desc&limit=1`,
    )
    expectValid('AuditEventList', ev.json)
    expect(ev.json.items[0]).toMatchObject({
      type: 'applied',
      reason: 'ApprovedOnce',
      details: { approvalId: ok.json.id },
    })
    await login('admin')
    const forced = await call('POST', usUrl, {
      body: { recommendationID: us.recommendation!.id, reason: 'ok', overrideReadiness: true },
    })
    expect(forced.status).toBe(201)
  })
})

describe('clusters, automation, readiness', () => {
  it('admin cluster views, automation, fleet', async () => {
    for (const c of ['prod-eu-west-1', 'prod-us-east-1', 'dr-eu-central-1']) {
      const r = await call('GET', `/v1/admin/clusters/${c}`)
      expectValid('ClusterAdmin', r.json)
      expect(r.headers.get('ETag')).toBe(`"${r.json.version}"`)
    }
    const list = await call('GET', '/v1/clusters')
    expectValid('ClusterList', list.json)
    expect(list.json.items[0]).toMatchObject({
      environment: 'prod',
      owner: 'platform',
      policySource: 'both',
      paused: false,
    })
    expect(list.json.items.map((c: { readiness: { state: string } }) => c.readiness.state)).toEqual([
      'AutomationReady',
      'ReadyAudit',
      'Registered',
    ])

    await login('operator')
    const pause = await call('PUT', '/v1/clusters/prod-eu-west-1/automation', {
      body: {
        paused: true,
        reason: 'incident INC-4800',
        resumeAt: new Date(Date.now() + 3600_000).toISOString(),
      },
    })
    expect(pause.status).toBe(200)
    expectValid('Automation', pause.json)
    expect(pause.headers.get('ETag')).toBe(`"${pause.json.version}"`)
    expect((await call('GET', '/v1/clusters')).json.items[0].paused).toBe(true)
    expectProblem(
      await call('PUT', '/v1/clusters/prod-eu-west-1/automation', { body: { paused: false, reason: 'ok' } }),
      403,
      'admin_required',
    )
    expectProblem(
      await call('PUT', '/v1/clusters/prod-eu-west-1/automation', {
        body: { paused: true, reason: 'x' },
        ifMatch: 0,
      }),
      412,
      'version_conflict',
    )
    // the non-prod cluster can be resumed by an operator
    await call('PUT', '/v1/clusters/dr-eu-central-1/automation', { body: { paused: true, reason: 'drill' } })
    expect(
      (
        await call('PUT', '/v1/clusters/dr-eu-central-1/automation', {
          body: { paused: false, reason: 'drill over' },
        })
      ).status,
    ).toBe(200)
    const fleet = await call('GET', '/v1/admin/automation')
    expectValid('FleetAutomation', fleet.json)
    const fp = await call('PUT', '/v1/admin/automation', { body: { paused: true, reason: 'quarter close' } })
    expectValid('Automation', fp.json)
    expectProblem(
      await call('PUT', '/v1/admin/automation', { body: { paused: false, reason: 'done' } }),
      403,
      'admin_required',
    )
    await login('admin')
    expect(
      (await call('PUT', '/v1/admin/automation', { body: { paused: false, reason: 'done' } })).status,
    ).toBe(200)
    expect(
      (
        await call('PUT', '/v1/clusters/prod-eu-west-1/automation', {
          body: { paused: false, reason: 'INC-4800 resolved' },
        })
      ).status,
    ).toBe(200)
    const audit = await call('GET', '/v1/audit/config?targetType=automation&order=desc&limit=3')
    expectValid('ConfigChangeList', audit.json)
    expect(audit.json.items[0]).toMatchObject({ action: 'AutomationResumed', actor: { name: 'admin' } })

    const c = (await call('GET', '/v1/admin/clusters/prod-us-east-1')).json
    expectProblem(
      await call('PATCH', '/v1/admin/clusters/prod-us-east-1', { body: { owner: 'x', reason: 'y' } }),
      428,
      'precondition_required',
    )
    const patched = await call('PATCH', '/v1/admin/clusters/prod-us-east-1', {
      body: { owner: 'cards-sre', reason: 'reorg' },
      ifMatch: c.version,
    })
    expectValid('ClusterAdmin', patched.json)
    expect(patched.json.owner).toBe('cards-sre')
  })

  it('config sync catches up after a change', async () => {
    await login('admin')
    timing.syncMs = 60_000
    const g = policy('ro-global-defaults')
    await call('PUT', P(g.id), {
      body: { type: 'global-default', spec: g.spec, reason: 'touch' },
      ifMatch: g.version,
    })
    const before = (await call('GET', '/v1/admin/clusters/prod-eu-west-1')).json
    expect(before.agent.inSync).toBe(false)
    timing.syncMs = 0
    const after = (await call('GET', '/v1/admin/clusters/prod-eu-west-1')).json
    expect(after.agent.inSync).toBe(true)
    expect(after.agent.appliedConfigVersion).toBe(after.agent.configVersion)
  })

  it('readiness, recheck and history', async () => {
    const states: Record<string, string> = {}
    for (const c of ['prod-eu-west-1', 'prod-us-east-1', 'dr-eu-central-1']) {
      const r = await call('GET', `/v1/clusters/${c}/readiness`)
      expectValid('ClusterReadiness', r.json)
      // Connected agents report every check; without an agent, its checks have no row (hub semantics)
      // and the blocking ones are listed in blockedBy.
      if (c === 'dr-eu-central-1') {
        expect(r.json.checks.length).toBeLessThan(33)
        expect(r.json.blockedBy).toEqual(
          expect.arrayContaining(['agent.connected', 'agent.version', 'rbac.read']),
        )
        expect(r.json.checks.map((x: { id: string }) => x.id)).not.toContain('agent.version')
        expect(r.json.summary.unknown).toBeGreaterThan(0)
      } else expect(r.json.checks).toHaveLength(34)
      states[c] = r.json.state
      const h = await call('GET', `/v1/clusters/${c}/readiness/history`)
      expectValid('ReadinessHistory', h.json)
      expect(h.json.items.length).toBeGreaterThan(0)
    }
    expect(states).toEqual({
      'prod-eu-west-1': 'AutomationReady',
      'prod-us-east-1': 'ReadyAudit',
      'dr-eu-central-1': 'Registered',
    })
    const us = (await call('GET', '/v1/clusters/prod-us-east-1/readiness')).json
    expect(us.blockedBy).toEqual(['rbac.resize', 'rbac.policies'])
    expect(us.capabilities.find((c: { name: string }) => c.name === 'resize')).toMatchObject({
      granted: false,
    })
    expect(us.checks.find((c: { id: string }) => c.id === 'metrics.node-labels')).toMatchObject({
      status: 'warn',
      remediationType: 'yaml',
    })
    expectProblem(await call('POST', '/v1/clusters/prod-us-east-1/readiness/recheck'), 403, 'forbidden')
    await login('operator')
    timing.recheckMs = 60_000
    const re = await call('POST', '/v1/clusters/prod-us-east-1/readiness/recheck')
    expect(re.status).toBe(202)
    expectValid('RecheckAccepted', re.json)
    expect((await call('GET', '/v1/clusters/prod-us-east-1/readiness')).json.recheckRequestedAt).toBe(
      re.json.requestedAt,
    )
    timing.recheckMs = 0
    const done = (await call('GET', '/v1/clusters/prod-us-east-1/readiness')).json
    expect(done.recheckRequestedAt).toBeUndefined()
    expectProblem(await call('GET', '/v1/clusters/nope/readiness'), 404, 'not_found')
  })

  it('register a cluster: token once, install snippets, agent connects, delete', async () => {
    await login('admin')
    timing.connectMs = 60_000
    const body = {
      name: 'staging-eu-west-1',
      environment: 'non-prod',
      owner: 'platform',
      policySource: 'both',
      provider: 'aws',
      region: 'eu-west-1',
      metrics: { datasourceId: 'local', clusterLabel: 'cluster', clusterLabelValue: 'staging-eu-west-1' },
      createAgentToken: true,
      reason: 'new staging cluster',
    }
    expectProblem(
      await call('POST', '/v1/admin/clusters', { body: { ...body, name: 'Bad_Name' } }),
      400,
      'invalid_cluster',
    )
    // Hub codes for bindings (WO-25): unknown datasource vs. an invalid binding.
    expectProblem(
      await call('POST', '/v1/admin/clusters', {
        body: { ...body, metrics: { ...body.metrics, datasourceId: 'nope' } },
      }),
      400,
      'unknown_datasource',
    )
    expectProblem(
      await call('POST', '/v1/admin/clusters', {
        body: { ...body, metrics: { ...body.metrics, clusterLabel: 'bad-label' } },
      }),
      400,
      'invalid_metrics',
    )
    expectProblem(await call('GET', '/v1/admin/datasources/nope'), 404, 'not_found')
    const r = await call('POST', '/v1/admin/clusters', { body })
    expect(r.status).toBe(201)
    expectValid('ClusterCreated', r.json)
    expect(r.json.agentToken.value).toMatch(/^ro-/)
    expect(r.json.cluster.readiness.state).toBe('Registered')
    expectProblem(await call('POST', '/v1/admin/clusters', { body }), 409, 'already_exists')
    const list = await call('GET', '/v1/clusters')
    expectValid('ClusterList', list.json)
    expect(list.json.items.map((c: { name: string }) => c.name)).toContain('staging-eu-west-1')
    const rd = (await call('GET', '/v1/clusters/staging-eu-west-1/readiness')).json
    expectValid('ClusterReadiness', rd)
    expect(rd.checks.find((c: { id: string }) => c.id === 'agent.connected').status).toBe('fail')
    expect(rd.checks.find((c: { id: string }) => c.id === 'metrics.cluster-label').status).toBe('pass')
    for (const format of ['helm', 'values', 'argocd']) {
      const s = await call(
        'GET',
        `/v1/admin/clusters/staging-eu-west-1/install?format=${format}&webhook=true`,
      )
      expectValid('InstallSnippet', s.json)
      expect(s.json.content).toContain('kerf-agent-token')
      expect(s.json.content).not.toContain(r.json.agentToken.value)
    }
    expectProblem(
      await call('GET', '/v1/admin/clusters/staging-eu-west-1/install?format=zip'),
      400,
      'invalid_format',
    )
    timing.connectMs = 0
    const connected = (await call('GET', '/v1/clusters/staging-eu-west-1/readiness')).json
    expect(connected.state).toBe('MetricsOK')
    // an unknown label value fails the metrics checks
    const other = await call('POST', '/v1/admin/clusters', {
      body: {
        name: 'eks-new',
        environment: 'prod',
        metrics: { datasourceId: 'local', clusterLabel: 'cluster' },
        reason: 'x',
      },
    })
    expect(other.status).toBe(201)
    expect(other.json.agentToken).toBeUndefined()
    const o = (await call('GET', '/v1/clusters/eks-new/readiness')).json
    expect(o.checks.find((c: { id: string }) => c.id === 'metrics.cluster-label')).toMatchObject({
      status: 'fail',
    })
    const v = (await call('GET', '/v1/admin/clusters/staging-eu-west-1')).json.version
    expect(
      (await call('DELETE', '/v1/admin/clusters/staging-eu-west-1', { body: { reason: 'gone' }, ifMatch: v }))
        .status,
    ).toBe(204)
    expectProblem(await call('GET', '/v1/admin/clusters/staging-eu-west-1'), 404, 'not_found')
    const toks = (await call('GET', '/v1/admin/tokens?includeRevoked=true&cluster=staging-eu-west-1')).json
    expect(toks.items[0].revokedAt).toBeTruthy()
  })
})

describe('tokens, users, audit, datasources', () => {
  it('tokens: admin only, value once, revoke', async () => {
    expectProblem(await call('GET', '/v1/admin/tokens'), 403, 'forbidden')
    await login('operator')
    expectProblem(await call('GET', '/v1/admin/tokens'), 403, 'forbidden')
    await login('admin')
    const list = await call('GET', '/v1/admin/tokens')
    expectValid('TokenList', list.json)
    expect(list.json.items.some((t: { revokedAt?: string }) => t.revokedAt)).toBe(false)
    expectValid('TokenList', (await call('GET', '/v1/admin/tokens?includeRevoked=true')).json)
    expectProblem(
      await call('POST', '/v1/admin/tokens', { body: { role: 'agent', description: 'x', reason: 'y' } }),
      400,
      'invalid_token',
    )
    const c = await call('POST', '/v1/admin/tokens', {
      body: {
        role: 'ui',
        description: 'grafana',
        expiresAt: new Date(Date.now() + 86400_000).toISOString(),
        reason: 'dashboards',
      },
    })
    expect(c.status).toBe(201)
    expectValid('TokenCreated', c.json)
    expect((await call('GET', '/v1/admin/tokens')).json.items.some((t: { value?: string }) => t.value)).toBe(
      false,
    )
    const rv = await call('DELETE', `/v1/admin/tokens/${c.json.token.id}`, { body: { reason: 'leaked' } })
    expectValid('TokenInfo', rv.json)
    expect(rv.json.revokedAt).toBeTruthy()
  })

  it('users: create, last admin, reset password, delete', async () => {
    await login('admin')
    const list = await call('GET', '/v1/admin/users')
    expectValid('UserList', list.json)
    expect(list.json.items.map((u: { username: string }) => u.username)).toEqual(
      expect.arrayContaining(['admin', 'alice', 'bob', 'operator', 'viewer', 'dana', 'sam']),
    )
    const c = await call('POST', '/v1/admin/users', {
      body: {
        username: 'carol',
        displayName: 'Carol',
        role: 'operator',
        password: 'longenough',
        reason: 'new SRE',
      },
    })
    expect(c.status).toBe(201)
    expectValid('User', c.json)
    expect(c.json.mustChangePassword).toBe(true)
    expectProblem(
      await call('POST', '/v1/admin/users', {
        body: { username: 'carol', role: 'viewer', password: 'longenough', reason: 'x' },
      }),
      409,
      'already_exists',
    )
    expectProblem(
      await call('POST', '/v1/admin/users', {
        body: { username: 'X', role: 'viewer', password: 'short', reason: 'x' },
      }),
      400,
      'invalid_user',
    )
    const alice = (await call('GET', '/v1/admin/users/alice')).json
    expectValid('User', alice)
    const demoted = await call('PATCH', '/v1/admin/users/alice', {
      body: { role: 'operator', reason: 'moved team' },
      ifMatch: alice.version,
    })
    expectValid('User', demoted.json)
    const admin = (await call('GET', '/v1/admin/users/admin')).json
    expectProblem(
      await call('PATCH', '/v1/admin/users/admin', {
        body: { disabled: true, reason: 'x' },
        ifMatch: admin.version,
      }),
      409,
      'last_admin',
    )
    expectProblem(
      await call('DELETE', '/v1/admin/users/admin', { body: { reason: 'x' }, ifMatch: admin.version }),
      409,
      'last_admin',
    )
    const reset = await call('POST', '/v1/admin/users/carol/reset-password', { body: { reason: 'forgot' } })
    expectValid('PasswordResetResult', reset.json)
    expect(reset.json.temporaryPassword).toBeTruthy()
    const carol = (await call('GET', '/v1/admin/users/carol')).json
    expect(
      (await call('DELETE', '/v1/admin/users/carol', { body: { reason: 'left' }, ifMatch: carol.version }))
        .status,
    ).toBe(204)
    expectProblem(await call('GET', '/v1/admin/users/nobody'), 404, 'not_found')
  })

  it('config audit: filters and paging', async () => {
    const all = await call('GET', '/v1/audit/config?limit=1000')
    expectValid('ConfigChangeList', all.json)
    expect(all.json.items.length).toBeGreaterThanOrEqual(25)
    const actors = new Set(all.json.items.map((c: { actor: { name: string } }) => c.actor.name))
    for (const a of ['alice', 'bob', 'admin', 'system']) expect(actors).toContain(a)
    const pg = await call('GET', '/v1/audit/config?order=desc&limit=10')
    expectValid('ConfigChangeList', pg.json)
    expect(pg.json.continue).toBeTruthy()
    const pg2 = await call('GET', `/v1/audit/config?order=desc&limit=10&continue=${pg.json.continue}`)
    expect(pg2.json.items[0].time <= pg.json.items[9].time).toBe(true)
    const pol = await call('GET', '/v1/audit/config?targetType=policy&actor=alice')
    expect(
      pol.json.items.every(
        (c: { target: { type: string }; actor: { name: string } }) =>
          c.target.type === 'policy' && c.actor.name === 'alice',
      ),
    ).toBe(true)
    const cl = await call('GET', '/v1/audit/config?cluster=prod-us-east-1')
    expect(cl.json.items.length).toBeGreaterThan(0)
  })

  it('datasources: CRUD, secrets write-only, test, in use', async () => {
    const list = await call('GET', '/v1/admin/datasources')
    expectValid('DatasourceList', list.json)
    const local = list.json.items.find((d: { id: string }) => d.id === 'local')
    expect(local.usedBy).toEqual(['prod-eu-west-1', 'prod-us-east-1'])
    const amp = list.json.items.find((d: { name: string }) => d.name === 'amp-eu1')
    expect(amp.usedBy).toEqual(['dr-eu-central-1'])
    expectValid('Datasource', (await call('GET', `/v1/admin/datasources/${amp.id}`)).json)
    await login('admin')
    const c = await call('POST', '/v1/admin/datasources', {
      body: {
        name: 'mimir-prod',
        type: 'mimir',
        url: 'https://mimir.example.com/prometheus',
        auth: { type: 'bearer', token: 's3cret' },
        headers: { 'X-Scope-OrgID': 'prod' },
        reason: 'tenant prod',
      },
    })
    expect(c.status).toBe(201)
    expectValid('Datasource', c.json)
    expect(c.json.auth.secretConfigured).toBe(true)
    expect(JSON.stringify(c.json)).not.toContain('s3cret')
    expectProblem(
      await call('POST', '/v1/admin/datasources', {
        body: { name: 'x', type: 'mimir', url: 'nope', auth: { type: 'bearer' }, reason: 'x' },
      }),
      400,
      'invalid_datasource',
    )
    const upd = await call('PUT', `/v1/admin/datasources/${c.json.id}`, {
      body: {
        name: 'mimir-prod',
        type: 'mimir',
        url: 'https://mimir.example.com/prometheus',
        auth: { type: 'bearer' },
        timeout: '45s',
        reason: 'longer timeout',
      },
      ifMatch: 1,
    })
    expectValid('Datasource', upd.json)
    expect(upd.json.auth.secretConfigured).toBe(true)
    const okTest = await call('POST', '/v1/admin/datasources/local/test', {
      body: {
        binding: { datasourceId: 'local', clusterLabel: 'cluster', clusterLabelValue: 'staging-eu-west-1' },
      },
    })
    expectValid('DatasourceTestResult', okTest.json)
    expect(okTest.json.ok).toBe(true)
    expect(okTest.json.found.pods).toBeGreaterThan(0)
    const bad = await call('POST', '/v1/admin/datasources/local/test', {
      body: { binding: { datasourceId: 'local', clusterLabel: 'cluster', clusterLabelValue: 'prod-eu1' } },
    })
    expectValid('DatasourceTestResult', bad.json)
    expect(bad.json.ok).toBe(false)
    expect(
      bad.json.checks.find((x: { id: string }) => x.id === 'metrics.cluster-label').details.suggestions,
    ).toContain('prod-eu-west-1')
    const bare = await call('POST', '/v1/admin/datasources/local/test')
    expectValid('DatasourceTestResult', bare.json)
    expect(bare.json.checks.map((x: { id: string }) => x.id)).toEqual(['datasource.reachable'])
    expectProblem(
      await call('DELETE', `/v1/admin/datasources/${amp.id}`, {
        body: { reason: 'x' },
        ifMatch: amp.version,
      }),
      409,
      'datasource_in_use',
    )
    expectProblem(
      await call('DELETE', '/v1/admin/datasources/local', { body: { reason: 'x' }, ifMatch: local.version }),
      409,
      'builtin',
    )
    expectProblem(
      await call('PUT', '/v1/admin/datasources/local', {
        body: { name: 'local', type: 'victoriametrics', url: 'http://x:8428', reason: 'x' },
        ifMatch: local.version,
      }),
      409,
      'builtin',
    )
    expect(
      (await call('DELETE', `/v1/admin/datasources/${c.json.id}`, { body: { reason: 'x' }, ifMatch: 2 }))
        .status,
    ).toBe(204)
  })
})

describe('maintenance (WO-33)', () => {
  it('lists every component with its compat against the hub', async () => {
    const r = await call('GET', '/v1/admin/maintenance')
    expect(r.status).toBe(200)
    expectValid('Maintenance', r.json)
    const m = r.json as import('@/api/types').Maintenance
    const hubs = m.components.filter((c) => c.component === 'hub')
    expect(hubs.length).toBe(2)
    expect(hubs.filter((h) => h.leader).length).toBe(1)
    expect(m.components.some((c) => c.component === 'pricing')).toBe(true)
    const agent = (cl: string) => m.components.find((c) => c.component === 'agent' && c.cluster === cl)
    expect(agent('prod-eu-west-1')?.compat).toBe('same')
    expect(agent('prod-us-east-1')?.compat).toBe('older')
    expect(agent('prod-us-east-1')?.message).toMatch(/upgrade/i)
    expect(agent('dr-eu-central-1')?.compat).toBe('unknown')
    const hooks = (cl: string) => m.components.filter((c) => c.component === 'webhook' && c.cluster === cl)
    expect(hooks('prod-eu-west-1').length).toBe(2)
    expect(hooks('prod-us-east-1').length).toBe(0)
    expect(m.update.state).toBe('update_available')
    expect(m.upgrade?.hubCommand).toContain(`--version ${m.update.latestVersion}`)
  })

  it('check now needs admin and CSRF, and 409 when the check is off', async () => {
    await signOut()
    expectProblem(await call('POST', '/v1/admin/maintenance/update-check'), 401, 'unauthorized')
    await login('viewer')
    expectProblem(await call('POST', '/v1/admin/maintenance/update-check'), 403, 'forbidden')
    await login('operator')
    expectProblem(await call('POST', '/v1/admin/maintenance/update-check'), 403, 'forbidden')
    await login('admin')
    expectProblem(
      await call('POST', '/v1/admin/maintenance/update-check', { noCsrf: true }),
      403,
      'csrf_invalid',
    )
    getAdmin().maintenance.update.checkedAt = new Date(Date.now() - 3600_000).toISOString()
    const r = await call('POST', '/v1/admin/maintenance/update-check')
    expect(r.status).toBe(200)
    expectValid('UpdateStatus', r.json)
    expect(r.json.state).toBe('update_available')
    expect(Date.now() - Date.parse(r.json.checkedAt)).toBeLessThan(5_000)
    getAdmin().maintenance.update = { ...getAdmin().maintenance.update, enabled: false, state: 'disabled' }
    expectProblem(await call('POST', '/v1/admin/maintenance/update-check'), 409, 'update_check_disabled')
    expectValid('Maintenance', (await call('GET', '/v1/admin/maintenance')).json)
  })

  it('a fresh hub has not checked yet and offers no upgrade', async () => {
    localStorage.setItem('ro-mock-scenario', 'empty')
    const r = await call('GET', '/v1/admin/maintenance')
    expectValid('Maintenance', r.json)
    expect(r.json.update.state).toBe('pending')
    expect(r.json.upgrade).toBeUndefined()
  })
})

describe('scenarios', () => {
  it('error and empty apply to the admin endpoints', async () => {
    localStorage.setItem('ro-mock-scenario', 'error')
    expectProblem(await call('GET', '/v1/admin/policies'), 500, 'internal')
    localStorage.setItem('ro-mock-scenario', 'empty')
    const p = await call('GET', '/v1/admin/policies')
    expectValid('AdminPolicyList', p.json)
    expect(p.json.items).toEqual([])
    expectValid('PolicyInventoryList', (await call('GET', '/v1/admin/policy-inventory')).json)
    expectValid('FleetAutomation', (await call('GET', '/v1/admin/automation')).json)
    expectValid('ConfigChangeList', (await call('GET', '/v1/audit/config')).json)
    await login('admin')
    expectValid('TokenList', (await call('GET', '/v1/admin/tokens')).json)
  })
})

describe('seed ids for the UI and e2e', () => {
  it('has the documented demo objects', () => {
    const st = getAdmin()
    expect(
      st.policies.filter((p) => p.type === 'workload-override' && !p.deletedAt).length,
    ).toBeGreaterThanOrEqual(3)
    expect(approvable()).toBeTruthy()
  })
})
