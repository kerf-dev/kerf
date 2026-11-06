/**
 * Contract and behaviour tests of the notification mocks: every response validates against
 * api/openapi/hub.yaml, and the mock enforces the roles, If-Match and conflict rules the page relies on.
 */
import { readFileSync } from 'node:fs'
import path from 'node:path'
import Ajv2020 from 'ajv/dist/2020'
import addFormats from 'ajv-formats'
import { setupServer } from 'msw/node'
import { parse } from 'yaml'
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { getAdmin, handlers, resetDb } from './handlers'

vi.setConfig({ testTimeout: 30_000 })

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

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Res = { status: number; json: any; headers: Headers }
async function call(
  method: string,
  url: string,
  opts: { body?: unknown; ifMatch?: string | number } = {},
): Promise<Res> {
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
  return { status: res.status, json: text ? JSON.parse(text) : undefined, headers: res.headers }
}

async function login(username: string) {
  const r = await call('POST', '/v1/auth/login', { body: { username, password: 'dev-password' } })
  expect(r.status).toBe(200)
  csrf = r.json.csrfToken
}

function expectProblem(r: Res, status: number, code: string) {
  expect(r.status, JSON.stringify(r.json)).toBe(status)
  expectValid('Problem', r.json)
  expect(r.json.code).toBe(code)
}

const base = '/v1/admin/notifications'
const reason = 'test'

describe('notification mocks', () => {
  it('serves status, channels, rules, digest and deliveries that match the spec', async () => {
    await login('admin')
    expectValid('NotificationStatus', (await call('GET', `${base}/status`)).json)
    const channels = await call('GET', `${base}/channels`)
    expectValid('NotificationChannelList', channels.json)
    expect(channels.json.items.map((c: { name: string }) => c.name)).toEqual([
      'audit-sink',
      'chart-default',
      'finops-teams',
      'oncall-pager',
      'platform-alerts',
    ])
    expect(JSON.stringify(channels.json)).not.toMatch(/secret"|\/services\//)
    expectValid('NotificationRuleList', (await call('GET', `${base}/rules`)).json)
    const digest = await call('GET', `${base}/digest`)
    expectValid('NotificationDigest', digest.json)
    expect(digest.headers.get('ETag')).toBe(`"${digest.json.version}"`)
    expect(Date.parse(digest.json.nextSendAt)).toBeGreaterThan(Date.now())
    expect(Date.parse(digest.json.lastSentAt)).toBeLessThan(Date.now())
    const deliveries = await call('GET', `${base}/deliveries`)
    expectValid('NotificationDeliveryList', deliveries.json)
    const times = deliveries.json.items.map((d: { createdAt: string }) => d.createdAt)
    expect([...times].sort().reverse()).toEqual(times)
  })

  it('pages and filters deliveries', async () => {
    await login('admin')
    const first = await call('GET', `${base}/deliveries?limit=4`)
    expect(first.json.items).toHaveLength(4)
    const second = await call(
      'GET',
      `${base}/deliveries?limit=4&continue=${encodeURIComponent(first.json.continue)}`,
    )
    expect(second.json.items[0].id).not.toBe(first.json.items[0].id)
    const failed = await call('GET', `${base}/deliveries?status=failed`)
    expect(failed.json.items.length).toBeGreaterThan(0)
    expect(failed.json.items.every((d: { status: string }) => d.status === 'failed')).toBe(true)
    expectProblem(await call('GET', `${base}/deliveries?status=nope`), 400, 'invalid_status')
  })

  it('creates, updates and deletes a channel; secrets and URLs are write-only', async () => {
    await login('admin')
    const created = await call('POST', `${base}/channels`, {
      body: {
        name: 'ops',
        type: 'webhook',
        url: 'https://example.com/hook/abc',
        secret: 'a-secret-of-16-chars',
        reason,
      },
    })
    expect(created.status).toBe(201)
    expectValid('NotificationChannel', created.json)
    expect(created.json).toMatchObject({
      url: 'https://example.com',
      secretSet: true,
      source: 'api',
      version: 1,
    })
    expect(created.headers.get('ETag')).toBe('"1"')
    expectProblem(
      await call('POST', `${base}/channels`, {
        body: { name: 'ops', type: 'slack', url: 'https://hooks.slack.com/x', reason },
      }),
      409,
      'already_exists',
    )
    expectProblem(
      await call('POST', `${base}/channels`, {
        body: { name: 'Bad Name', type: 'slack', url: 'https://hooks.slack.com/x', reason },
      }),
      400,
      'invalid_channel',
    )
    const id = created.json.id
    expectProblem(
      await call('PUT', `${base}/channels/${id}`, { body: { name: 'ops', type: 'webhook', reason } }),
      428,
      'precondition_required',
    )
    expectProblem(
      await call('PUT', `${base}/channels/${id}`, {
        ifMatch: 7,
        body: { name: 'ops', type: 'webhook', reason },
      }),
      412,
      'version_conflict',
    )
    const updated = await call('PUT', `${base}/channels/${id}`, {
      ifMatch: 1,
      body: { name: 'ops', type: 'webhook', clearSecret: true, enabled: false, reason },
    })
    expect(updated.json).toMatchObject({
      secretSet: false,
      enabled: false,
      url: 'https://example.com',
      version: 2,
    })
    expect((await call('DELETE', `${base}/channels/${id}`, { ifMatch: 2, body: { reason } })).status).toBe(
      204,
    )
    expectProblem(await call('GET', `${base}/channels/${id}`), 404, 'not_found')
  })

  it('answers the documented conflicts', async () => {
    await login('admin')
    const list = (await call('GET', `${base}/channels`)).json.items as {
      id: string
      name: string
      version: number
    }[]
    const byName = (n: string) => list.find((c) => c.name === n)!
    const chart = byName('chart-default')
    expectProblem(
      await call('PUT', `${base}/channels/${chart.id}`, {
        ifMatch: chart.version,
        body: { name: 'chart-default', type: 'slack', reason },
      }),
      409,
      'channel_read_only',
    )
    expectProblem(
      await call('DELETE', `${base}/channels/${chart.id}`, { ifMatch: chart.version, body: { reason } }),
      409,
      'channel_read_only',
    )
    const pager = byName('oncall-pager')
    expectProblem(
      await call('DELETE', `${base}/channels/${pager.id}`, { ifMatch: pager.version, body: { reason } }),
      409,
      'channel_in_use',
    )
    getAdmin().notifications.secretsKey = false
    expectProblem(
      await call('POST', `${base}/channels`, {
        body: { name: 'x', type: 'slack', url: 'https://hooks.slack.com/x', reason },
      }),
      409,
      'secrets_key_missing',
    )
  })

  it('sends test messages: ok, refused and recorded in the history', async () => {
    await login('admin')
    const list = (await call('GET', `${base}/channels`)).json.items as { id: string; name: string }[]
    const ok = await call(
      'POST',
      `${base}/channels/${list.find((c) => c.name === 'platform-alerts')!.id}/test`,
    )
    expectValid('NotificationTestResult', ok.json)
    expect(ok.json.ok).toBe(true)
    const bad = await call('POST', `${base}/channels/${list.find((c) => c.name === 'oncall-pager')!.id}/test`)
    expect(bad.status).toBe(200)
    expectValid('NotificationTestResult', bad.json)
    expect(bad.json).toMatchObject({ ok: false, httpStatus: 404 })
    expect(bad.json.error).toMatch(/404/)
    const history = (await call('GET', `${base}/deliveries?limit=2`)).json.items
    expect(history.map((d: { kind: string }) => d.kind)).toEqual(['test', 'test'])
    // A channel pointing at a host with "fail" in its name refuses tests too.
    const c = await call('POST', `${base}/channels`, {
      body: { name: 'broken', type: 'slack', url: 'https://fail.example.com/h', reason },
    })
    expect((await call('POST', `${base}/channels/${c.json.id}/test`)).json.ok).toBe(false)
  })

  it('creates, updates and deletes rules', async () => {
    await login('admin')
    const channel = (await call('GET', `${base}/channels`)).json.items[0].id
    expectProblem(
      await call('POST', `${base}/rules`, {
        body: { name: 'r', eventTypes: [], channelIds: [channel], reason },
      }),
      400,
      'invalid_rule',
    )
    expectProblem(
      await call('POST', `${base}/rules`, {
        body: { name: 'r', eventTypes: ['failed'], channelIds: [], reason },
      }),
      400,
      'invalid_rule',
    )
    const created = await call('POST', `${base}/rules`, {
      body: { name: 'Failures', eventTypes: ['failed'], clusters: ['prod-*'], channelIds: [channel], reason },
    })
    expect(created.status).toBe(201)
    expectValid('NotificationRule', created.json)
    expect(created.json).toMatchObject({ enabled: true, namespaces: [], version: 1 })
    const updated = await call('PUT', `${base}/rules/${created.json.id}`, {
      ifMatch: 1,
      body: {
        name: 'Failures',
        eventTypes: ['failed', 'blocked'],
        enabled: false,
        channelIds: [channel],
        reason,
      },
    })
    expectValid('NotificationRule', updated.json)
    expect(updated.json).toMatchObject({ enabled: false, version: 2, clusters: [] })
    expect(
      (await call('DELETE', `${base}/rules/${created.json.id}`, { ifMatch: 2, body: { reason } })).status,
    ).toBe(204)
    expectProblem(
      await call('DELETE', `${base}/rules/${created.json.id}`, { ifMatch: 3, body: { reason } }),
      404,
      'not_found',
    )
  })

  it('changes the digest and queues it on request', async () => {
    await login('admin')
    const d = (await call('GET', `${base}/digest`)).json
    expectProblem(
      await call('PUT', `${base}/digest`, {
        ifMatch: d.version,
        body: { enabled: true, weekday: 9, time: '25:00', timezone: 'Mars/Base', channelIds: [], reason },
      }),
      400,
      'invalid_digest',
    )
    const saved = await call('PUT', `${base}/digest`, {
      ifMatch: d.version,
      body: { enabled: false, weekday: 0, time: '09:15', timezone: 'UTC', channelIds: d.channelIds, reason },
    })
    expectValid('NotificationDigest', saved.json)
    expect(saved.json.nextSendAt).toBeUndefined()
    expect(saved.json.version).toBe(d.version + 1)
    const q = await call('POST', `${base}/digest/send`)
    expect(q.status).toBe(202)
    expectValid('NotificationDigestQueued', q.json)
    expect(q.json.queued).toBe(1)
    await call('PUT', `${base}/digest`, {
      ifMatch: saved.json.version,
      body: { enabled: false, weekday: 0, time: '09:15', timezone: 'UTC', channelIds: [], reason },
    })
    expectProblem(await call('POST', `${base}/digest/send`), 409, 'no_channels')
  })

  it('lets viewers and operators read but not write', async () => {
    await login('operator')
    expect((await call('GET', `${base}/channels`)).status).toBe(200)
    const channel = (await call('GET', `${base}/channels`)).json.items[0]
    expectProblem(await call('POST', `${base}/channels/${channel.id}/test`), 403, 'forbidden')
    expectProblem(await call('POST', `${base}/digest/send`), 403, 'forbidden')
    expectProblem(
      await call('POST', `${base}/rules`, {
        body: { name: 'r', eventTypes: ['failed'], channelIds: [channel.id], reason },
      }),
      403,
      'forbidden',
    )
  })
})
