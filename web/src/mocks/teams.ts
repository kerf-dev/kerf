/**
 * Teams of the mock hub (docs/teams.md): the team endpoints, which team a workload belongs to, and
 * what a team-scoped caller may see and change. The mock fleet has no Kubernetes labels, so a team
 * claims workloads through its namespace globs and cluster globs only.
 */
import { http } from 'msw'
import type { components } from '@/api/schema'
import { iso, summarize, type WorkloadModel } from './data'
import { audit, type AdminState, type TeamRec, type UserRole } from './admin-state'
import {
  badRequest,
  body,
  conflict,
  forbidden,
  gate,
  ifMatch,
  json,
  notFound,
  reasonError,
  roleAtLeast,
  type Ctx,
} from './core'
import { HttpResponse } from 'msw'

type S = components['schemas']

export const UNASSIGNED = 'unassigned'
const DNS1123 = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/
const RANK: UserRole[] = ['viewer', 'operator', 'admin']
const higher = (a: UserRole | undefined, b: UserRole | undefined): UserRole | undefined =>
  !a ? b : !b ? a : RANK.indexOf(a) >= RANK.indexOf(b) ? a : b

export function glob(pattern: string, s: string): boolean {
  const re = pattern
    .replace(/[.+^${}()|[\]\\]/g, '\\$&')
    .replace(/\*/g, '.*')
    .replace(/\?/g, '.')
  return new RegExp(`^${re}$`).test(s)
}

/** The team of a workload: the first team (by id) whose selectors match, else `unassigned`. */
export function teamOf(st: AdminState, w: Pick<WorkloadModel, 'cluster' | 'namespace'>): string {
  for (const t of st.teams) {
    if (!t.namespaces.length) continue
    if (t.namespaceLabels && Object.keys(t.namespaceLabels).length) continue // the mock has no namespace labels
    if (!t.namespaces.some((g) => glob(g, w.namespace))) continue
    if (t.clusters.length && !t.clusters.some((g) => glob(g, w.cluster))) continue
    return t.id
  }
  return UNASSIGNED
}

/** What the caller may see and do, per team. */
export interface Access {
  /** `scope: teams`: sees only the teams it has a role on. */
  scoped: boolean
  /** Has team roles or a team scope (so team-aware errors apply). */
  involved: boolean
  sees: (team: string) => boolean
  roleOn: (team: string) => UserRole | undefined
}

export function accessOf(who: Ctx['who']): Access {
  const u = who.user
  const bindings = u?.teamRoles ?? []
  const scoped = u?.scope === 'teams'
  const bound = (team: string) => bindings.find((b) => b.team === team)?.role
  return {
    scoped,
    involved: scoped || bindings.length > 0,
    sees: (team) => !scoped || !!bound(team),
    roleOn: (team) => (scoped ? bound(team) : higher(who.role, bound(team))),
  }
}

export function visibleWorkloads(g: Ctx, pool: WorkloadModel[] = g.db.workloads): WorkloadModel[] {
  const a = accessOf(g.who)
  return a.scoped ? pool.filter((w) => a.sees(teamOf(g.st, w))) : pool
}

export const seesWorkload = (g: Ctx, w: WorkloadModel) => accessOf(g.who).sees(teamOf(g.st, w))

/** Workload writes need operator on the workload's team (fleet role raised by a team role). */
export function needOperatorOn(g: Ctx, w: WorkloadModel): Response | undefined {
  const a = accessOf(g.who)
  if (!a.involved)
    return roleAtLeast(g.who.role, 'operator')
      ? undefined
      : forbidden('forbidden', `requires role operator (you are ${g.who.role})`)
  const team = teamOf(g.st, w)
  const role = a.roleOn(team)
  return role && roleAtLeast(role, 'operator')
    ? undefined
    : forbidden('scope_forbidden', `changing ${w.id} needs operator on team ${team}`)
}

/** `team` of rows and details: present once any team exists. */
export const withTeam = <T extends object>(
  st: AdminState,
  w: WorkloadModel,
  row: T,
): T & { team?: string } => (st.teams.length ? { ...row, team: teamOf(st, w) } : row)

export function teamCounts(g: Ctx, id: string) {
  const ws = g.db.workloads.filter((w) => teamOf(g.st, w) === id)
  return { workloads: ws.length, monthlySavingsUSD: summarize(ws, g.db.lastRunAt).monthlySavingsUSD }
}

/** `FleetSummary.teams`: absent while no team exists; a scoped caller gets only its teams. */
export function teamSummaries(g: Ctx, pool: WorkloadModel[]): S['TeamSummary'][] | undefined {
  if (!g.st.teams.length) return undefined
  const a = accessOf(g.who)
  const ids = [...g.st.teams.map((t) => t.id), ...(a.scoped ? [] : [UNASSIGNED])].filter((id) => a.sees(id))
  return ids.map((id) => ({
    team: id,
    displayName: g.st.teams.find((t) => t.id === id)?.displayName ?? 'Unassigned',
    ...summarize(
      pool.filter((w) => teamOf(g.st, w) === id),
      g.db.lastRunAt,
    ),
  }))
}

export function teamOut(g: Ctx, t: TeamRec): S['Team'] {
  return { ...t, ...teamCounts(g, t.id) }
}

// ---------------------------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------------------------

const list = (v: unknown, name: string, errs: string[]): string[] => {
  if (v == null) return []
  if (!Array.isArray(v) || v.some((x) => typeof x !== 'string' || !x.trim() || x.length > 253)) {
    errs.push(`${name}: Invalid value: a list of non-empty strings`)
    return []
  }
  return [...new Set((v as string[]).map((x) => x.trim()))]
}

function validate(b: S['TeamInput'] | undefined, id: string | undefined, st: AdminState) {
  const errs: string[] = []
  if (!b) return { errs: ['body: Required value'] }
  if (b.displayName && b.displayName.length > 128) errs.push('displayName: Too long: at most 128 characters')
  if (b.description && b.description.length > 1024)
    errs.push('description: Too long: at most 1024 characters')
  const labels = b.namespaceLabels ?? {}
  if (typeof labels !== 'object' || Object.values(labels).some((v) => typeof v !== 'string'))
    errs.push('namespaceLabels: Invalid value: a map of strings')
  const rec = {
    namespaces: list(b.namespaces, 'namespaces', errs),
    namespaceLabels: labels as Record<string, string>,
    clusters: list(b.clusters, 'clusters', errs),
    ownerValues: list(b.ownerValues, 'ownerValues', errs),
  }
  void st
  void id
  return { errs, rec }
}

export const teamHandlers = [
  http.get('*/v1/admin/teams', async ({ request }) => {
    const g = await gate(request, 'viewer')
    if (g instanceof Response) return g
    const a = accessOf(g.who)
    const body: S['TeamList'] = {
      items: g.st.teams.filter((t) => a.sees(t.id)).map((t) => teamOut(g, t)),
      ownerLabel: g.st.ownerLabel,
      ...(a.scoped ? {} : { unassigned: teamCounts(g, UNASSIGNED) }),
    }
    return json(body)
  }),

  http.post('*/v1/admin/teams', async ({ request }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const b = await body<S['TeamInput']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    const { errs, rec } = validate(b, undefined, g.st)
    if (!b!.id || !DNS1123.test(b!.id))
      errs.push(`id: Invalid value: ${JSON.stringify(b!.id ?? '')}: must be a DNS label`)
    else if (b!.id === UNASSIGNED) errs.push('id: Reserved value: "unassigned"')
    if (errs.length || !rec) return badRequest('invalid_team', errs.join('; '))
    if (g.st.teams.some((t) => t.id === b!.id)) return conflict('already_exists', `team ${b!.id} exists`)
    const t: TeamRec = {
      id: b!.id!,
      displayName: b!.displayName?.trim() || b!.id!,
      ...(b!.description ? { description: b!.description } : {}),
      ...rec,
      version: 1,
      createdAt: iso(Date.now()),
      createdBy: g.who.actor.name,
    }
    g.st.teams.push(t)
    g.st.teams.sort((x, y) => x.id.localeCompare(y.id))
    audit(g.st, {
      time: t.createdAt,
      actor: g.who.actor,
      action: 'TeamCreated',
      target: { type: 'team', id: t.id },
      reason: b!.reason,
      version: 1,
      after: teamOut(g, t),
    })
    return json(teamOut(g, t), { status: 201, version: 1 })
  }),

  http.get('*/v1/admin/teams/:teamId', async ({ request, params }) => {
    const g = await gate(request, 'viewer')
    if (g instanceof Response) return g
    const t = g.st.teams.find((x) => x.id === params.teamId)
    if (!t || !accessOf(g.who).sees(t.id))
      return notFound('team_not_found', `team ${String(params.teamId)} not found`)
    return json(teamOut(g, t), { version: t.version })
  }),

  http.put('*/v1/admin/teams/:teamId', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const t = g.st.teams.find((x) => x.id === params.teamId)
    if (!t) return notFound('team_not_found', `team ${String(params.teamId)} not found`)
    const m = ifMatch(request, t.version)
    if (m) return m
    const b = await body<S['TeamInput']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    const { errs, rec } = validate(b, t.id, g.st)
    if (b!.id && b!.id !== t.id) errs.push('id: Invalid value: it cannot change')
    if (errs.length || !rec) return badRequest('invalid_team', errs.join('; '))
    const before = teamOut(g, t)
    Object.assign(t, rec, {
      displayName: b!.displayName?.trim() || t.id,
      description: b!.description || undefined,
      version: t.version + 1,
      updatedAt: iso(Date.now()),
    })
    audit(g.st, {
      time: t.updatedAt!,
      actor: g.who.actor,
      action: 'TeamUpdated',
      target: { type: 'team', id: t.id },
      reason: b!.reason,
      version: t.version,
      before,
      after: teamOut(g, t),
    })
    return json(teamOut(g, t), { version: t.version })
  }),

  http.delete('*/v1/admin/teams/:teamId', async ({ request, params }) => {
    const g = await gate(request, 'admin')
    if (g instanceof Response) return g
    const t = g.st.teams.find((x) => x.id === params.teamId)
    if (!t) return notFound('team_not_found', `team ${String(params.teamId)} not found`)
    const m = ifMatch(request, t.version)
    if (m) return m
    const b = await body<S['ChangeReason']>(request)
    const r = reasonError(b?.reason)
    if (r) return r
    const before = teamOut(g, t)
    g.st.teams = g.st.teams.filter((x) => x !== t)
    // The users' bindings to the team go with it.
    for (const u of g.st.users.values())
      if (u.teamRoles?.some((x) => x.team === t.id)) {
        u.teamRoles = u.teamRoles.filter((x) => x.team !== t.id)
        u.version++
      }
    audit(g.st, {
      time: iso(Date.now()),
      actor: g.who.actor,
      action: 'TeamDeleted',
      target: { type: 'team', id: t.id },
      reason: b!.reason,
      version: t.version + 1,
      before,
      after: null,
    })
    return new HttpResponse(null, { status: 204 })
  }),
]

/** Checks the `scope` and `teamRoles` of a user create or update; `undefined` = fine. */
export function validateAccess(
  st: AdminState,
  v: { role: UserRole; scope: S['UserScope']; teamRoles: S['TeamRole'][] },
): string[] {
  const errs: string[] = []
  if (!['fleet', 'teams'].includes(v.scope)) errs.push(`scope: Unsupported value: ${JSON.stringify(v.scope)}`)
  const seen = new Set<string>()
  for (const r of v.teamRoles) {
    if (!st.teams.some((t) => t.id === r.team))
      errs.push(`teamRoles: Not found: team ${JSON.stringify(r.team)}`)
    if (!['viewer', 'operator'].includes(r.role))
      errs.push(`teamRoles: Unsupported value: ${JSON.stringify(r.role)}`)
    if (seen.has(r.team)) errs.push(`teamRoles: Duplicate value: ${JSON.stringify(r.team)}`)
    seen.add(r.team)
  }
  if (v.role === 'admin' && (v.scope === 'teams' || v.teamRoles.length))
    errs.push('role: Invalid value: admins are always fleet-wide and have no team roles')
  if (v.scope === 'teams' && !v.teamRoles.length)
    errs.push('teamRoles: Required value: a team-scoped user needs at least one team role')
  return errs
}

/** The highest role a user has anywhere: its role, or its team roles for a team-scoped user. */
export function highestRole(u: {
  role: UserRole
  scope?: S['UserScope']
  teamRoles?: S['TeamRole'][]
}): UserRole {
  return (
    u.teamRoles?.reduce<UserRole>((hi, r) => higher(hi, r.role)!, u.scope === 'teams' ? 'viewer' : u.role) ??
    u.role
  )
}

/** The role stored for a user: its own, or for a team-scoped user the highest of its team roles. */
export function storedRole(u: {
  role: UserRole
  scope?: S['UserScope']
  teamRoles?: S['TeamRole'][]
}): UserRole {
  return u.scope === 'teams'
    ? (u.teamRoles ?? []).reduce<UserRole>((hi, r) => higher(hi, r.role)!, 'viewer')
    : u.role
}
