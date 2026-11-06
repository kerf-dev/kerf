/**
 * MSW handlers for Admin → Notifications (`/v1/admin/notifications/*` in api/openapi/hub.yaml):
 * status, channels (with the last delivery and the queue depth), test messages, rules, the weekly
 * digest and the delivery history. Responses are validated against the spec in notifications.test.ts.
 */
import { http, HttpResponse } from 'msw'
import type { components } from '@/api/schema'
import { audit } from './admin-state'
import { badRequest, body, conflict, dec, enc, gate, ifMatch, json, notFound, reasonError } from './core'
import { iso } from './data'
import { withSchedule, type ChannelRec, type NotificationModel, type RuleRec } from './notifications-state'
import { mulberry32, uuid } from './random'

type S = components['schemas']

const now = () => iso(Date.now())
const newId = () => uuid(mulberry32((Date.now() ^ Math.floor(Math.random() * 2 ** 31)) >>> 0))
const live = <T extends { deletedAt?: string }>(x: T) => !x.deletedAt

const EVENT_TYPES: S['NotificationEventType'][] = [
  'applied',
  'rolled_back',
  'failed',
  'blocked',
  'readiness_degraded',
  'automation_paused',
  'automation_resumed',
]
const NAME = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/
const TIME = /^([01][0-9]|2[0-3]):[0-5][0-9]$/
const RETRY_LIMIT = 7

function invalid(code: string, errs: string[]) {
  return badRequest(code, errs.join('; '))
}

// ---------------------------------------------------------------------------------------------
// Delivery queue: queued deliveries are attempted when their time has come.
// ---------------------------------------------------------------------------------------------

function settle(m: NotificationModel) {
  const t = Date.now()
  for (const d of m.deliveries) {
    if (d.status !== 'pending') continue
    const due = d.nextAttemptAt ? Date.parse(d.nextAttemptAt) : Date.parse(d.createdAt) + 3_000
    if (due > t) continue
    const c = m.channels.find((x) => x.id === d.channelId)
    d.attempts++
    d.lastAttemptAt = iso(t)
    if (c?.refuses) {
      d.lastError = c.refuses.error
      d.httpStatus = c.refuses.httpStatus
      if (d.attempts >= RETRY_LIMIT) {
        d.status = 'failed'
        delete d.nextAttemptAt
      } else d.nextAttemptAt = iso(t + Math.min(2 ** d.attempts, 60) * 60_000)
    } else {
      d.status = 'sent'
      d.sentAt = iso(t)
      d.httpStatus = 200
      delete d.nextAttemptAt
      delete d.lastError
    }
  }
}

function channelOut(m: NotificationModel, c: ChannelRec): S['NotificationChannel'] {
  const mine = m.deliveries.filter((d) => d.channelId === c.id)
  const attempted = mine.find((d) => d.attempts > 0)
  const lastSuccess = mine.find((d) => d.sentAt)
  const status = (d: S['NotificationDelivery']) =>
    d.status === 'sent' ? 'sent' : d.status === 'failed' ? 'failed' : 'retrying'
  return {
    id: c.id,
    name: c.name,
    type: c.type,
    source: c.source,
    enabled: c.enabled,
    url: c.url,
    secretSet: c.secretSet,
    pending: mine.filter((d) => d.status === 'pending').length,
    ...(attempted
      ? {
          lastDelivery: {
            status: status(attempted),
            at: attempted.lastAttemptAt ?? attempted.createdAt,
            ...(attempted.status !== 'sent' && attempted.lastError ? { error: attempted.lastError } : {}),
          },
        }
      : {}),
    ...(lastSuccess?.sentAt ? { lastSuccessAt: lastSuccess.sentAt } : {}),
    version: c.version,
    createdAt: c.createdAt,
    ...(c.createdBy ? { createdBy: c.createdBy } : {}),
    updatedAt: c.updatedAt,
    ...(c.updatedBy ? { updatedBy: c.updatedBy } : {}),
  }
}

const ruleOut = ({ deletedAt: _deleted, ...r }: RuleRec): S['NotificationRule'] => r
const liveChannel = (m: NotificationModel, id: string) => m.channels.find((c) => c.id === id && live(c))

/** Rules and the digest that send to a channel. */
function usedBy(m: NotificationModel, id: string) {
  return [
    ...m.rules.filter((r) => live(r) && r.channelIds.includes(id)).map((r) => `rule ${r.name}`),
    ...(m.digest.channelIds.includes(id) ? ['the weekly digest'] : []),
  ]
}

function maskURL(url: string) {
  const u = new URL(url)
  return `${u.protocol}//${u.host}`
}

// ---------------------------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------------------------

function validateChannel(m: NotificationModel, b: S['NotificationChannelInput'], existing?: ChannelRec) {
  const errs: string[] = []
  if (!NAME.test(b.name ?? '')) errs.push('name: must be a DNS-style label')
  if (!['slack', 'teams', 'webhook'].includes(b.type)) errs.push('type: must be slack, teams or webhook')
  if (b.url == null && !existing) errs.push('url: Required value')
  if (b.url != null) {
    try {
      const u = new URL(b.url)
      if (u.protocol !== 'https:') errs.push('url: must use https')
    } catch {
      errs.push('url: must be a URL')
    }
  }
  if (b.secret != null && b.type !== 'webhook') errs.push('secret: only webhook channels are signed')
  if (b.secret != null && b.secret.length < 16) errs.push('secret: at least 16 characters')
  if (errs.length) return invalid('invalid_channel', errs)
  if (m.channels.some((c) => live(c) && c.name === b.name && c.id !== existing?.id))
    return conflict('already_exists', `channel ${b.name} exists`)
  return undefined
}

function validateRule(m: NotificationModel, b: S['NotificationRuleInput']) {
  const errs: string[] = []
  if (!b.name?.trim() || b.name.length > 100) errs.push('name: 1–100 characters')
  if (!b.eventTypes?.length) errs.push('eventTypes: at least one')
  else if (b.eventTypes.some((e) => !EVENT_TYPES.includes(e))) errs.push('eventTypes: unknown event type')
  if (!b.channelIds?.length) errs.push('channelIds: at least one')
  else if (b.channelIds.some((id) => !liveChannel(m, id))) errs.push('channelIds: unknown channel')
  return errs.length ? invalid('invalid_rule', errs) : undefined
}

function validateDigest(m: NotificationModel, b: S['NotificationDigestInput']) {
  const errs: string[] = []
  if (!Number.isInteger(b.weekday) || b.weekday < 0 || b.weekday > 6) errs.push('weekday: 0..6')
  if (!TIME.test(b.time ?? '')) errs.push('time: HH:MM')
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: b.timezone })
  } catch {
    errs.push('timezone: unknown IANA time zone')
  }
  if (b.channelIds.some((id) => !liveChannel(m, id))) errs.push('channelIds: unknown channel')
  return errs.length ? invalid('invalid_digest', errs) : undefined
}

// ---------------------------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------------------------

export const notificationHandlers = [
  http.get('*/v1/admin/notifications/status', async ({ request }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const m = g.st.notifications
    return json<S['NotificationStatus']>({
      secretsKey: m.secretsKey,
      links: m.links,
      rateLimitPerMinute: 30,
      retryWindowSeconds: 3600,
    })
  }),

  http.get('*/v1/admin/notifications/channels', async ({ request }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const m = g.st.notifications
    settle(m)
    return json<S['NotificationChannelList']>({
      items: m.channels
        .filter(live)
        .sort((a, b) => a.name.localeCompare(b.name))
        .map((c) => channelOut(m, c)),
    })
  }),

  http.post('*/v1/admin/notifications/channels', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const m = g.st.notifications
    const b = await body<S['NotificationChannelInput']>(request)
    if (!b) return badRequest('invalid_channel', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    const v = validateChannel(m, b)
    if (v) return v
    if (!m.secretsKey)
      return conflict(
        'secrets_key_missing',
        'the hub has no secrets encryption key, so webhook URLs cannot be stored',
      )
    const t = now()
    const c: ChannelRec = {
      id: newId(),
      name: b.name,
      type: b.type,
      source: 'api',
      enabled: b.enabled ?? true,
      url: maskURL(b.url!),
      secretSet: !!b.secret,
      ...(new URL(b.url!).host.includes('fail')
        ? { refuses: { httpStatus: 500, error: 'destination answered 500 Internal Server Error' } }
        : {}),
      version: 1,
      createdAt: t,
      createdBy: g.who.actor,
      updatedAt: t,
      updatedBy: g.who.actor,
    }
    m.channels.push(c)
    const out = channelOut(m, c)
    audit(g.st, {
      time: t,
      actor: g.who.actor,
      action: 'NotificationChannelCreated',
      target: { type: 'notification', id: c.id, name: c.name },
      reason: b.reason,
      version: 1,
      after: out,
    })
    return json(out, { status: 201, version: 1 })
  }),

  http.get('*/v1/admin/notifications/channels/:channelId', async ({ request, params }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const m = g.st.notifications
    settle(m)
    const c = liveChannel(m, String(params.channelId))
    if (!c) return notFound('not_found', `channel ${String(params.channelId)} not found`)
    return json(channelOut(m, c), { version: c.version })
  }),

  http.put('*/v1/admin/notifications/channels/:channelId', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const m = g.st.notifications
    const c = liveChannel(m, String(params.channelId))
    if (!c) return notFound('not_found', `channel ${String(params.channelId)} not found`)
    if (c.source === 'config')
      return conflict('channel_read_only', `channel ${c.name} is defined in the chart values`)
    const pre = ifMatch(request, c.version)
    if (pre) return pre
    const b = await body<S['NotificationChannelInput']>(request)
    if (!b) return badRequest('invalid_channel', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    const v = validateChannel(m, b, c)
    if (v) return v
    const before = channelOut(m, c)
    c.name = b.name
    c.type = b.type
    c.enabled = b.enabled ?? true
    if (b.url != null) {
      c.url = maskURL(b.url)
      if (new URL(b.url).host.includes('fail'))
        c.refuses = { httpStatus: 500, error: 'destination answered 500 Internal Server Error' }
      else if (c.name !== 'oncall-pager') delete c.refuses
    }
    if (b.clearSecret) c.secretSet = false
    if (b.secret) c.secretSet = true
    if (c.type !== 'webhook') c.secretSet = false
    c.version++
    c.updatedAt = now()
    c.updatedBy = g.who.actor
    const out = channelOut(m, c)
    audit(g.st, {
      time: c.updatedAt,
      actor: g.who.actor,
      action: 'NotificationChannelUpdated',
      target: { type: 'notification', id: c.id, name: c.name },
      reason: b.reason,
      version: c.version,
      before,
      after: out,
    })
    return json(out, { version: c.version })
  }),

  http.delete('*/v1/admin/notifications/channels/:channelId', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const m = g.st.notifications
    const c = liveChannel(m, String(params.channelId))
    if (!c) return notFound('not_found', `channel ${String(params.channelId)} not found`)
    const pre = ifMatch(request, c.version)
    if (pre) return pre
    const b = await body<S['ChangeReason']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    if (c.source === 'config')
      return conflict('channel_read_only', `channel ${c.name} is defined in the chart values`)
    const used = usedBy(m, c.id)
    if (used.length) return conflict('channel_in_use', `sent to by ${used.join(' and ')}`)
    const before = channelOut(m, c)
    c.deletedAt = now()
    c.version++
    // Queued deliveries of a deleted channel are dropped; the history of sent ones stays.
    m.deliveries = m.deliveries.filter((d) => !(d.channelId === c.id && d.status === 'pending'))
    audit(g.st, {
      time: c.deletedAt,
      actor: g.who.actor,
      action: 'NotificationChannelDeleted',
      target: { type: 'notification', id: c.id, name: c.name },
      reason: b!.reason,
      version: c.version,
      before,
      after: null,
    })
    return new HttpResponse(null, { status: 204 })
  }),

  http.post('*/v1/admin/notifications/channels/:channelId/test', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const m = g.st.notifications
    const c = liveChannel(m, String(params.channelId))
    if (!c) return notFound('not_found', `channel ${String(params.channelId)} not found`)
    const latencyMs = 90 + Math.floor(Math.random() * 240)
    const ok = !c.refuses
    const t = now()
    m.deliveries.unshift({
      id: newId(),
      channelId: c.id,
      channelName: c.name,
      kind: 'test',
      summary: `Test message from ${g.who.actor.name}`,
      status: ok ? 'sent' : 'failed',
      attempts: 1,
      createdAt: t,
      lastAttemptAt: t,
      ...(ok
        ? { sentAt: t, httpStatus: 200 }
        : { httpStatus: c.refuses!.httpStatus, lastError: c.refuses!.error }),
    })
    return json<S['NotificationTestResult']>({
      ok,
      latencyMs,
      httpStatus: ok ? 200 : c.refuses!.httpStatus,
      ...(ok ? {} : { error: c.refuses!.error }),
    })
  }),

  http.get('*/v1/admin/notifications/rules', async ({ request }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    return json<S['NotificationRuleList']>({
      items: g.st.notifications.rules
        .filter(live)
        .sort((a, b) => a.name.localeCompare(b.name))
        .map(ruleOut),
    })
  }),

  http.post('*/v1/admin/notifications/rules', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const m = g.st.notifications
    const b = await body<S['NotificationRuleInput']>(request)
    if (!b) return badRequest('invalid_rule', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    const v = validateRule(m, b)
    if (v) return v
    const t = now()
    const rule: RuleRec = {
      id: newId(),
      name: b.name.trim(),
      enabled: b.enabled ?? true,
      eventTypes: b.eventTypes,
      clusters: b.clusters ?? [],
      namespaces: b.namespaces ?? [],
      channelIds: b.channelIds,
      version: 1,
      createdAt: t,
      createdBy: g.who.actor,
      updatedAt: t,
      updatedBy: g.who.actor,
    }
    m.rules.push(rule)
    audit(g.st, {
      time: t,
      actor: g.who.actor,
      action: 'NotificationRuleCreated',
      target: { type: 'notification', id: rule.id, name: rule.name },
      reason: b.reason,
      version: 1,
      after: ruleOut(rule),
    })
    return json(ruleOut(rule), { status: 201, version: 1 })
  }),

  http.put('*/v1/admin/notifications/rules/:ruleId', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const m = g.st.notifications
    const rule = m.rules.find((x) => x.id === String(params.ruleId) && live(x))
    if (!rule) return notFound('not_found', `rule ${String(params.ruleId)} not found`)
    const pre = ifMatch(request, rule.version)
    if (pre) return pre
    const b = await body<S['NotificationRuleInput']>(request)
    if (!b) return badRequest('invalid_rule', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    const v = validateRule(m, b)
    if (v) return v
    const before = ruleOut(rule)
    Object.assign(rule, {
      name: b.name.trim(),
      enabled: b.enabled ?? true,
      eventTypes: b.eventTypes,
      clusters: b.clusters ?? [],
      namespaces: b.namespaces ?? [],
      channelIds: b.channelIds,
      updatedAt: now(),
      updatedBy: g.who.actor,
    })
    rule.version++
    audit(g.st, {
      time: rule.updatedAt,
      actor: g.who.actor,
      action: 'NotificationRuleUpdated',
      target: { type: 'notification', id: rule.id, name: rule.name },
      reason: b.reason,
      version: rule.version,
      before,
      after: ruleOut(rule),
    })
    return json(ruleOut(rule), { version: rule.version })
  }),

  http.delete('*/v1/admin/notifications/rules/:ruleId', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const m = g.st.notifications
    const rule = m.rules.find((x) => x.id === String(params.ruleId) && live(x))
    if (!rule) return notFound('not_found', `rule ${String(params.ruleId)} not found`)
    const pre = ifMatch(request, rule.version)
    if (pre) return pre
    const b = await body<S['ChangeReason']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    const before = ruleOut(rule)
    rule.deletedAt = now()
    rule.version++
    audit(g.st, {
      time: rule.deletedAt,
      actor: g.who.actor,
      action: 'NotificationRuleDeleted',
      target: { type: 'notification', id: rule.id, name: rule.name },
      reason: b!.reason,
      version: rule.version,
      before,
      after: null,
    })
    return new HttpResponse(null, { status: 204 })
  }),

  http.get('*/v1/admin/notifications/digest', async ({ request }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const m = g.st.notifications
    m.digest = withSchedule(m.digest, Date.now())
    return json(m.digest, { version: m.digest.version })
  }),

  http.put('*/v1/admin/notifications/digest', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const m = g.st.notifications
    const pre = ifMatch(request, m.digest.version)
    if (pre) return pre
    const b = await body<S['NotificationDigestInput']>(request)
    if (!b) return badRequest('invalid_digest', 'body: Required value')
    const r = reasonError(b.reason)
    if (r) return r
    const v = validateDigest(m, b)
    if (v) return v
    const before = m.digest
    const { nextSendAt: _next, ...rest } = before
    m.digest = withSchedule(
      {
        ...rest,
        enabled: b.enabled,
        weekday: b.weekday,
        time: b.time,
        timezone: b.timezone,
        channelIds: b.channelIds,
        version: before.version + 1,
        updatedAt: now(),
        updatedBy: g.who.actor,
      },
      Date.now(),
    )
    audit(g.st, {
      time: m.digest.updatedAt,
      actor: g.who.actor,
      action: 'NotificationDigestUpdated',
      target: { type: 'notification', id: 'digest', name: 'weekly digest' },
      reason: b.reason,
      version: m.digest.version,
      before,
      after: m.digest,
    })
    return json(m.digest, { version: m.digest.version })
  }),

  http.post('*/v1/admin/notifications/digest/send', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const m = g.st.notifications
    const targets = m.digest.channelIds.map((id) => liveChannel(m, id)).filter((c) => !!c)
    if (!targets.length) return conflict('no_channels', 'the digest has no channel to send to')
    const t = now()
    for (const c of targets)
      m.deliveries.unshift({
        id: newId(),
        channelId: c.id,
        channelName: c.name,
        kind: 'digest',
        summary: 'Weekly digest (sent on request)',
        status: 'pending',
        attempts: 0,
        createdAt: t,
        nextAttemptAt: t,
      })
    return json<S['NotificationDigestQueued']>({ queued: targets.length }, { status: 202 })
  }),

  http.get('*/v1/admin/notifications/deliveries', async ({ request }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const m = g.st.notifications
    settle(m)
    const sp = new URL(request.url).searchParams
    const status = sp.get('status')
    if (status && !['pending', 'sent', 'failed'].includes(status))
      return badRequest('invalid_status', 'status must be pending, sent or failed')
    const channelId = sp.get('channelId')
    const limit = Number(sp.get('limit') ?? 100)
    if (!(Number.isInteger(limit) && limit >= 1 && limit <= 1000))
      return badRequest('invalid_limit', 'limit must be 1..1000')
    const offset = dec(sp.get('continue'))
    const items = m.deliveries.filter(
      (d) => (!status || d.status === status) && (!channelId || d.channelId === channelId),
    )
    return json<S['NotificationDeliveryList']>({
      items: items.slice(offset, offset + limit),
      ...(offset + limit < items.length ? { continue: enc(offset + limit) } : {}),
    })
  }),
]
