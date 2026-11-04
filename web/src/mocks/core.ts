/**
 * Shared plumbing of the mock hub: scenarios, the seeded databases, problem responses, the mock
 * session (D-023) and the lazy "background" work of the simulated agent and hub.
 */
import { delay, HttpResponse } from 'msw'
import type { Problem } from '@/api/types'
import { buildDb, iso, type MockDb } from './data'
import { audit, seedAdmin, type Actor, type AdminState, type UserRec, type UserRole } from './admin-state'
import { mulberry32, uuid } from './random'

/**
 * Mock scenarios for checking UI states by hand: set `localStorage['ro-mock-scenario']` to
 * `empty` (a fleet with no clusters), `error` (every call fails with 500) or `slow` (2 s latency).
 * The mock-mode menu in the top bar sets it.
 */
export type Scenario = 'normal' | 'empty' | 'error' | 'slow'
export function currentScenario(): Scenario {
  return (readLocal('ro-mock-scenario') as Scenario | null) ?? 'normal'
}

/** `localStorage['ro-mock-install'] = 'unconfigured'`: the hub chart lacks the onboarding values (409). */
export const installUnconfigured = () => readLocal('ro-mock-install') === 'unconfigured'

function readLocal(key: string): string | null {
  try {
    return globalThis.localStorage?.getItem(key) ?? null
  } catch {
    return null
  }
}
function writeLocal(key: string, value: string | null) {
  try {
    if (value == null) globalThis.localStorage?.removeItem(key)
    else globalThis.localStorage?.setItem(key, value)
  } catch {
    /* ignore */
  }
}

// ---------------------------------------------------------------------------------------------
// Databases
// ---------------------------------------------------------------------------------------------

let db: MockDb | undefined
let emptyDb: MockDb | undefined
let admin: AdminState | undefined
let emptyAdmin: AdminState | undefined

export function getDb(): MockDb {
  db ??= buildDb()
  return db
}
export function getEmptyDb(): MockDb {
  emptyDb ??= { ...getDb(), clusters: [], workloads: [], byId: new Map(), events: [] }
  return emptyDb
}
export function getAdmin(): AdminState {
  admin ??= seedAdmin(getDb())
  return admin
}
export function getEmptyAdmin(): AdminState {
  emptyAdmin ??= seedAdmin(getEmptyDb())
  return emptyAdmin
}
/** Rebuilds the fleet and the admin state (tests). */
export function resetDb(now?: number) {
  db = buildDb(now)
  emptyDb = undefined
  admin = undefined
  emptyAdmin = undefined
  memSession = null
  loginFailures.clear()
}

/** Delays of the simulated background work (tests set them to 0). */
export const timing = {
  /** the agent applies a new config version */
  syncMs: 5000,
  /** a readiness re-check finishes */
  recheckMs: 3000,
  /** the agent applies an approve-once grant */
  approvalMs: 10_000,
  /** the agent of a newly registered cluster connects */
  connectMs: 15_000,
}

// ---------------------------------------------------------------------------------------------
// Responses
// ---------------------------------------------------------------------------------------------

export function problem(
  status: number,
  code: string,
  title: string,
  detail?: string,
  headers?: Record<string, string>,
) {
  const body: Problem = { type: 'about:blank', title, status, code, ...(detail ? { detail } : {}) }
  return HttpResponse.json(body, {
    status,
    headers: { 'Content-Type': 'application/problem+json', ...(headers ?? {}) },
  })
}

export const badRequest = (code: string, detail: string) => problem(400, code, 'Bad Request', detail)
export const notFound = (code: string, detail: string) => problem(404, code, 'Not Found', detail)
export const conflict = (code: string, detail: string) => problem(409, code, 'Conflict', detail)
export const forbidden = (code: string, detail: string) => problem(403, code, 'Forbidden', detail)

export const enc = (n: number) => btoa(`o:${n}`)
export function dec(s: string | null): number {
  if (!s) return 0
  try {
    const m = /^o:(\d+)$/.exec(atob(s))
    return m ? Number(m[1]) : -1
  } catch {
    return -1
  }
}

export function json<T>(body: T, init: { status?: number; version?: number } = {}) {
  return HttpResponse.json(body as object, {
    status: init.status ?? 200,
    headers: init.version != null ? { ETag: `"${init.version}"` } : undefined,
  })
}

// ---------------------------------------------------------------------------------------------
// Session (D-023). The mock keeps it in localStorage (a service worker cannot hold an HttpOnly
// cookie for the page), so reloads stay signed in.
// ---------------------------------------------------------------------------------------------

export interface MockSession {
  username: string
  csrfToken: string
  expiresAt: string
}
const SESSION_KEY = 'ro-mock-session'
let memSession: MockSession | null = null
export const loginFailures = new Map<string, number>()

export function readSession(): MockSession | null {
  const raw = readLocal(SESSION_KEY)
  if (raw) {
    try {
      return JSON.parse(raw) as MockSession
    } catch {
      return null
    }
  }
  return globalThis.localStorage ? null : memSession
}
export function writeSession(s: MockSession | null) {
  memSession = s
  writeLocal(SESSION_KEY, s ? JSON.stringify(s) : null)
}

/** `ro-mock-anonymous`: `viewer` (default, like nginx's ui token) or `none` (login required). */
export const anonymousMode = () => (readLocal('ro-mock-anonymous') === 'none' ? 'none' : 'viewer')

export function newSession(username: string): MockSession {
  const r = mulberry32((Date.now() ^ Math.floor(Math.random() * 2 ** 31)) >>> 0)
  return {
    username,
    csrfToken: uuid(r).replace(/-/g, ''),
    expiresAt: iso(Date.now() + 12 * 3600_000),
  }
}

const ROLES: UserRole[] = ['viewer', 'operator', 'admin']
export const roleAtLeast = (role: UserRole, min: UserRole) => ROLES.indexOf(role) >= ROLES.indexOf(min)

export interface Caller {
  role: UserRole
  actor: Actor
  session?: MockSession
  user?: UserRec
}

/** The caller of a request: the session user, else the anonymous viewer (nginx's ui token). */
export function caller(st: AdminState): Caller | undefined {
  const s = readSession()
  if (s) {
    const u = st.users.get(s.username)
    if (u && !u.disabled && !u.deletedAt && Date.parse(s.expiresAt) > Date.now())
      return { role: u.role, actor: { type: 'user', name: u.username, role: u.role }, session: s, user: u }
    writeSession(null)
  }
  if (anonymousMode() === 'none') return undefined
  return { role: 'viewer', actor: { type: 'token', name: 'token:2', role: 'ui' } }
}

export const unauthorized = () => problem(401, 'unauthorized', 'Unauthorized', 'sign in to continue')

export interface Ctx {
  db: MockDb
  st: AdminState
  empty: boolean
  who: Caller
}

const WRITE = new Set(['POST', 'PUT', 'PATCH', 'DELETE'])

/**
 * Scenario latency and errors, background ticks, authentication, CSRF on session writes and the
 * role check (`min` = the operation's lowest sessionAuth role; `null` = no auth).
 */
export async function gate(request?: Request, min: UserRole | null = 'viewer'): Promise<Ctx | Response> {
  const sc = currentScenario()
  if (import.meta.env.MODE !== 'test') await delay(sc === 'slow' ? 2000 : 120 + Math.random() * 260)
  if (sc === 'error')
    return problem(500, 'internal', 'Internal Server Error', 'mock scenario "error": the hub is unavailable')
  const empty = sc === 'empty'
  const d = empty ? getEmptyDb() : getDb()
  const st = empty ? getEmptyAdmin() : getAdmin()
  tick(st, d)
  const who = caller(st)
  if (min === null)
    return { db: d, st, empty, who: who ?? { role: 'viewer', actor: { type: 'system', name: 'anonymous' } } }
  if (!who) return unauthorized()
  if (request && WRITE.has(request.method) && who.session) {
    if (request.headers.get('X-CSRF-Token') !== who.session.csrfToken)
      return forbidden('csrf_invalid', 'missing or wrong X-CSRF-Token')
  }
  if (!roleAtLeast(who.role, min)) return forbidden('forbidden', `requires role ${min} (you are ${who.role})`)
  return { db: d, st, empty, who }
}

/** If-Match handling: missing → 428 (unless optional), stale → 412. */
export function ifMatch(request: Request, version: number, optional = false): Response | undefined {
  const h = request.headers.get('If-Match')
  if (!h)
    return optional
      ? undefined
      : problem(428, 'precondition_required', 'Precondition Required', 'If-Match is required')
  const v = h.replace(/^W\//, '').replace(/"/g, '').trim()
  if (v !== String(version))
    return problem(
      412,
      'version_conflict',
      'Precondition Failed',
      `version ${v} is stale; the current version is ${version}`,
    )
  return undefined
}

export async function body<T>(request: Request): Promise<T | undefined> {
  try {
    const text = await request.text()
    return text ? (JSON.parse(text) as T) : undefined
  } catch {
    return undefined
  }
}

export function reasonError(reason: unknown): Response | undefined {
  if (typeof reason !== 'string' || reason.trim().length < 1 || reason.length > 1024)
    return badRequest('invalid_reason', 'reason: Required value: 1–1024 characters')
  return undefined
}

// ---------------------------------------------------------------------------------------------
// Background work, evaluated lazily on every request.
// ---------------------------------------------------------------------------------------------

export function tick(st: AdminState, d: MockDb) {
  const now = Date.now()
  for (const c of st.clusters.values()) {
    if (
      c.agentConnected &&
      c.appliedConfigVersion !== c.configVersion &&
      now - c.configChangedAt >= timing.syncMs
    )
      c.appliedConfigVersion = c.configVersion
    if (c.automation.paused && c.automation.resumeAt && Date.parse(c.automation.resumeAt) <= now) {
      const before = { ...c.automation }
      c.automation = {
        paused: false,
        reason: 'scheduled resume',
        by: { type: 'system', name: 'system' },
        since: iso(now),
        version: c.automation.version + 1,
      }
      audit(st, {
        time: iso(now),
        actor: { type: 'system', name: 'system' },
        action: 'AutomationResumed',
        target: { type: 'automation', id: c.name, cluster: c.name },
        reason: 'scheduled resume',
        version: c.automation.version,
        before,
        after: c.automation,
      })
      bump(st, [c.name])
    }
  }
  if (st.fleet.paused && st.fleet.resumeAt && Date.parse(st.fleet.resumeAt) <= now) {
    const before = { ...st.fleet }
    st.fleet = {
      paused: false,
      reason: 'scheduled resume',
      by: { type: 'system', name: 'system' },
      since: iso(now),
      version: st.fleet.version + 1,
    }
    audit(st, {
      time: iso(now),
      actor: { type: 'system', name: 'system' },
      action: 'AutomationResumed',
      target: { type: 'automation', id: '*' },
      reason: 'scheduled resume',
      version: st.fleet.version,
      before,
      after: st.fleet,
    })
    bump(st, [...st.clusters.keys()])
  }
  for (const [name, m] of st.readiness) {
    const meta = st.clusters.get(name)
    if (meta && meta.connectPendingSince != null && now - meta.connectPendingSince >= timing.connectMs) {
      meta.connectPendingSince = undefined
      connectAgent(st, d, name)
    }
    if (m.recheckRequestedAt && now - Date.parse(m.recheckRequestedAt) >= timing.recheckMs) {
      m.recheckRequestedAt = undefined
      m.checkedAt = iso(now)
    }
  }
  for (const a of st.approvals) {
    if (a.state === 'pending' && Date.parse(a.expiresAt) <= now) a.state = 'expired'
    if (a.state === 'pending' && a.autoConsume && Date.parse(a.approvedAt) + timing.approvalMs <= now) {
      const w = d.byId.get(a.workloadId)
      const r = mulberry32((now ^ a.id.length) >>> 0)
      const eventId = uuid(r)
      a.state = 'consumed'
      a.consumedAt = iso(now)
      a.consumedEventId = eventId
      if (w) {
        w.phase = 'Applied'
        w.reason = 'ApprovedOnce'
        w.message = `Applied once, approved by ${a.approvedBy.name}.`
        w.appliedAt = iso(now)
        d.events.push({
          id: eventId,
          type: 'applied',
          time: iso(now),
          workload: { namespace: w.namespace, kind: w.kind, name: w.name },
          recommendationID: a.recommendationID,
          strategy: w.strategy,
          resources: ['cpu', 'memory'],
          reason: 'ApprovedOnce',
          message: `Applied the approved recommendation once (approved by ${a.approvedBy.name}).`,
          details: { approvalId: a.id, approvedBy: a.approvedBy.name },
          cluster: w.cluster,
          receivedAt: iso(now + 1000),
          workloadId: w.id,
        })
      }
    }
  }
}

/** A config change for these clusters: new config version; the agent catches up after `syncMs`. */
export function bump(st: AdminState, clusters: string[]) {
  const now = Date.now()
  for (const name of new Set(clusters)) {
    const c = st.clusters.get(name)
    if (!c || c.deletedAt) continue
    c.configVersion++
    c.configChangedAt = now
  }
}

/** The simulated agent of a newly registered cluster connects and reports its checks. */
function connectAgent(st: AdminState, d: MockDb, name: string) {
  const m = st.readiness.get(name)
  const meta = st.clusters.get(name)
  if (!m || !meta) return
  const now = iso(Date.now())
  meta.agentConnected = true
  meta.agentVersion = 'v0.4.2'
  meta.appliedConfigVersion = meta.configVersion
  const info = d.clusters.find((c) => c.name === name)
  if (info) {
    info.agentStatus = 'connected'
    info.agentVersion = 'v0.4.2'
    info.lastHeartbeatAt = now
  }
  const labelOk = m.results['metrics.cluster-label']?.status === 'pass'
  for (const [id, r] of Object.entries(m.results)) {
    if (id === 'metrics.recommendations' || id === 'metrics.history' || id === 'pricing.coverage') continue
    if (!labelOk && id.startsWith('metrics.')) continue
    if (r.status !== 'pass') {
      m.history.unshift({
        time: now,
        kind: 'check',
        checkId: id,
        fromStatus: r.status,
        toStatus: 'pass',
        message: 'Agent reported.',
      })
      m.results[id] = { status: 'pass', message: id.startsWith('rbac.') ? 'All permissions granted.' : 'OK' }
    }
  }
  m.results['agent.connected'] = { status: 'pass', message: 'Last config pull 2 s ago.' }
  m.results['integration.argocd'] = {
    status: 'unknown',
    message: 'argocd-cm is not readable by the agent; apply the snippet if Argo CD manages this cluster.',
  }
  m.history.unshift({
    time: now,
    kind: 'state',
    fromState: 'Registered',
    toState: labelOk ? 'MetricsOK' : 'AgentConnected',
  })
  m.stateSince = now
  m.checkedAt = now
}
