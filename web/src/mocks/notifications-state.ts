/**
 * Notification state of the mock hub (Admin → Notifications): channels, rules, the weekly digest and
 * the delivery history. The "last delivery" and queue depth of a channel are derived from its
 * deliveries, like the real hub, so a test message or a queued digest shows up everywhere at once.
 *
 * Dev-server hint: "Send test" fails for the `oncall-pager` channel, and for any channel whose URL
 * host contains "fail" (for example https://fail.example.com/hook).
 */
import type { components } from '@/api/schema'
import { hash, mulberry32, uuid } from './random'

type S = components['schemas']
export type NotificationChannel = S['NotificationChannel']
export type NotificationRule = S['NotificationRule']
export type NotificationDigest = S['NotificationDigest']
export type NotificationDelivery = S['NotificationDelivery']

export interface ChannelRec {
  id: string
  name: string
  type: S['NotificationChannelType']
  source: 'api' | 'config'
  enabled: boolean
  /** the URL masked to scheme and host, as the API returns it */
  url: string
  secretSet: boolean
  /** a destination that refuses test messages (the error the test result carries) */
  refuses?: { httpStatus: number; error: string }
  version: number
  createdAt: string
  createdBy?: S['Actor']
  updatedAt: string
  updatedBy?: S['Actor']
  deletedAt?: string
}

export interface RuleRec extends NotificationRule {
  deletedAt?: string
}

export interface NotificationModel {
  /** the hub has a secrets encryption key */
  secretsKey: boolean
  /** `notifications.baseURL` is set */
  links: boolean
  channels: ChannelRec[]
  rules: RuleRec[]
  digest: NotificationDigest
  /** newest first */
  deliveries: NotificationDelivery[]
}

const iso = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')

function zonedParts(ms: number, tz: string) {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: tz,
    hourCycle: 'h23',
    year: 'numeric',
    month: 'numeric',
    day: 'numeric',
    hour: 'numeric',
    minute: 'numeric',
  }).formatToParts(ms)
  const n = (t: string) => Number(parts.find((p) => p.type === t)!.value)
  return { y: n('year'), m: n('month'), d: n('day'), h: n('hour'), mi: n('minute') }
}

/** The instant at which the wall clock of `tz` reads this date and time. */
function zonedToUtc(y: number, m: number, d: number, h: number, mi: number, tz: string) {
  const guess = Date.UTC(y, m - 1, d, h, mi)
  const p = zonedParts(guess, tz)
  return guess - (Date.UTC(p.y, p.m - 1, p.d, p.h, p.mi) - guess)
}

/** The next (`dir` 1) or previous (`dir` -1) weekly occurrence of `weekday` (0 = Sunday) at `time` in `tz`. */
export function weeklyOccurrence(weekday: number, time: string, tz: string, from: number, dir: 1 | -1) {
  const [hh, mm] = time.split(':').map(Number) as [number, number]
  const p = zonedParts(from, tz)
  for (let i = 0; i <= 14; i++) {
    const day = new Date(Date.UTC(p.y, p.m - 1, p.d + i * dir))
    if (day.getUTCDay() !== weekday) continue
    const at = zonedToUtc(day.getUTCFullYear(), day.getUTCMonth() + 1, day.getUTCDate(), hh, mm, tz)
    if (dir === 1 ? at > from : at <= from) return at
  }
  return from
}

export function seedNotifications(now: number): NotificationModel {
  const r = mulberry32(hash('notifications-seed'))
  const ago = (min: number) => iso(now - min * 60_000)
  const alice = { type: 'user', name: 'alice', role: 'admin' } as S['Actor']
  const system = { type: 'system', name: 'system' } as S['Actor']
  const id = () => uuid(r)
  const channel = (
    c: Partial<ChannelRec> & Pick<ChannelRec, 'name' | 'type' | 'url'>,
    days: number,
  ): ChannelRec => ({
    id: id(),
    source: 'api',
    enabled: true,
    secretSet: false,
    version: 1,
    createdAt: iso(now - days * 86400_000),
    createdBy: alice,
    updatedAt: iso(now - days * 86400_000),
    updatedBy: alice,
    ...c,
  })
  const platform = channel({ name: 'platform-alerts', type: 'slack', url: 'https://hooks.slack.com' }, 40)
  const finops = channel(
    { name: 'finops-teams', type: 'teams', url: 'https://prod-12.westeurope.logic.azure.com' },
    33,
  )
  const sink = channel(
    { name: 'audit-sink', type: 'webhook', url: 'https://siem.example.com', secretSet: true },
    21,
  )
  const pager = channel(
    {
      name: 'oncall-pager',
      type: 'webhook',
      url: 'https://pager.example.com',
      refuses: {
        httpStatus: 404,
        error: 'destination answered 404 Not Found (the webhook was removed or revoked)',
      },
    },
    12,
  )
  const chart = channel(
    {
      name: 'chart-default',
      type: 'slack',
      url: 'https://hooks.slack.com',
      source: 'config',
      createdBy: system,
      updatedBy: system,
    },
    60,
  )
  const channels = [platform, finops, sink, pager, chart]

  const rules: RuleRec[] = [
    {
      id: id(),
      name: 'Rollbacks and failures in prod',
      enabled: true,
      eventTypes: ['rolled_back', 'failed'],
      clusters: ['prod-*'],
      namespaces: [],
      channelIds: [platform.id, pager.id],
      version: 2,
      createdAt: iso(now - 30 * 86400_000),
      createdBy: alice,
      updatedAt: iso(now - 9 * 86400_000),
      updatedBy: alice,
    },
    {
      id: id(),
      name: 'Applied changes in payments',
      enabled: true,
      eventTypes: ['applied'],
      clusters: [],
      namespaces: ['payments', 'checkout-*'],
      channelIds: [finops.id],
      version: 1,
      createdAt: iso(now - 14 * 86400_000),
      createdBy: alice,
      updatedAt: iso(now - 14 * 86400_000),
      updatedBy: alice,
    },
  ]

  const digest = {
    enabled: true,
    weekday: 1,
    time: '08:00',
    timezone: 'Europe/Berlin',
    channelIds: [finops.id],
    version: 3,
    updatedAt: iso(now - 20 * 86400_000),
    updatedBy: alice,
  }
  const model: NotificationModel = {
    secretsKey: true,
    links: true,
    channels,
    rules,
    digest: withSchedule(digest, now, weeklyOccurrence(1, '08:00', 'Europe/Berlin', now, -1)),
    deliveries: [],
  }

  const d = (
    channelRec: ChannelRec,
    minutesAgo: number,
    over: Partial<NotificationDelivery> & Pick<NotificationDelivery, 'summary'>,
  ): NotificationDelivery => ({
    id: id(),
    channelId: channelRec.id,
    channelName: channelRec.name,
    kind: 'event',
    status: 'sent',
    attempts: 1,
    createdAt: ago(minutesAgo),
    lastAttemptAt: ago(minutesAgo),
    sentAt: ago(minutesAgo),
    httpStatus: 200,
    ...over,
  })
  const list: NotificationDelivery[] = [
    d(finops, 4, {
      eventType: 'applied',
      summary: 'payments/checkout-api: applied new CPU and memory requests in prod-eu-west-1',
      status: 'pending',
      attempts: 2,
      sentAt: undefined,
      httpStatus: undefined,
      nextAttemptAt: iso(now + 6 * 60_000),
      lastError:
        'Post "https://prod-12.westeurope.logic.azure.com": context deadline exceeded (client timeout)',
    }),
    d(pager, 6, {
      eventType: 'rolled_back',
      summary: 'payments/ledger-writer: rolled back in prod-eu-west-1 (OOMKilled after the memory change)',
      status: 'failed',
      attempts: 7,
      sentAt: undefined,
      httpStatus: 404,
      lastError: 'destination answered 404 Not Found (the webhook was removed or revoked)',
    }),
    d(platform, 6, {
      eventType: 'rolled_back',
      summary: 'payments/ledger-writer: rolled back in prod-eu-west-1 (OOMKilled after the memory change)',
    }),
    d(platform, 12, {
      kind: 'test',
      summary: 'Test message from alice',
    }),
    d(finops, 55, {
      eventType: 'applied',
      summary: 'checkout-web/cart: applied new memory request in prod-us-east-1',
    }),
    d(platform, 190, {
      eventType: 'readiness_degraded',
      summary: 'prod-us-east-1: readiness went from Automation ready to Metrics OK',
    }),
    d(sink, 190, {
      eventType: 'readiness_degraded',
      summary: 'prod-us-east-1: readiness went from Automation ready to Metrics OK',
    }),
    d(pager, 260, {
      eventType: 'failed',
      summary: 'search/indexer: apply failed in prod-eu-west-1 (admission webhook denied the patch)',
      status: 'failed',
      attempts: 7,
      sentAt: undefined,
      httpStatus: 404,
      lastError: 'destination answered 404 Not Found (the webhook was removed or revoked)',
    }),
    d(platform, 260, {
      eventType: 'failed',
      summary: 'search/indexer: apply failed in prod-eu-west-1 (admission webhook denied the patch)',
    }),
    d(finops, 24 * 60 + 8 * 60, {
      kind: 'digest',
      summary: 'Weekly digest: 14 changes applied, 2 rolled back, about $1.9k per month saved',
    }),
    d(sink, 26 * 60, {
      eventType: 'automation_paused',
      summary: 'Automation paused for prod-eu-west-1 by bob (change freeze)',
    }),
    d(sink, 28 * 60, {
      eventType: 'automation_resumed',
      summary: 'Automation resumed for prod-eu-west-1 by bob',
    }),
    d(finops, 2 * 24 * 60, {
      eventType: 'applied',
      summary: 'payments/ledger-writer: applied new CPU request in prod-eu-west-1',
    }),
    d(chart, 2 * 24 * 60 + 30, {
      eventType: 'blocked',
      summary: 'payments/checkout-api: change blocked in prod-eu-west-1 (maintenance window closed)',
    }),
  ]
  list.sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  model.deliveries = list
  return model
}

/** Adds the schedule fields (`nextSendAt` while enabled, `lastSentAt`) to digest settings. */
export function withSchedule(
  d: Omit<NotificationDigest, 'nextSendAt' | 'lastSentAt'> & { lastSentAt?: string },
  now: number,
  lastSent?: number,
): NotificationDigest {
  return {
    ...d,
    ...(d.enabled ? { nextSendAt: iso(weeklyOccurrence(d.weekday, d.time, d.timezone, now, 1)) } : {}),
    ...(lastSent != null ? { lastSentAt: iso(lastSent) } : d.lastSentAt ? { lastSentAt: d.lastSentAt } : {}),
  }
}
