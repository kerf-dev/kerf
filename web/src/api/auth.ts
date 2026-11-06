import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { ApiError, api, setCsrfToken, unwrap } from './client'
import type { Me, UserRole } from './types'

/**
 * Session handling (docs/auth.md, docs/contracts/admin.md §3). Signing in is always required.
 *
 * `GET /v1/me` says who the browser is:
 * - `authMethod: session`: a signed-in user with a role and a CSRF token for writes;
 * - `401`: no session (never signed in, signed out, expired or revoked): the app shows only the
 *   sign-in page.
 * The web proxy holds no token, so a browser never reads anything without a session.
 */
export const ROLE_RANK: Record<UserRole, number> = { viewer: 0, operator: 1, admin: 2 }
export const roleAtLeast = (role: UserRole | undefined, min: UserRole) =>
  ROLE_RANK[role ?? 'viewer'] >= ROLE_RANK[min]

export const ME_KEY = ['me'] as const

/** Team-scoped callers (docs/teams.md) see only the workloads of their teams and no fleet-wide pages. */
export const isTeamScoped = (me: Me | undefined) => me?.scope === 'teams'

/** The role that applies on every workload: a team-scoped caller has none. */
function fleetRoleOf(me: Me | undefined): UserRole | undefined {
  if (!me || isTeamScoped(me)) return undefined
  // `me.role` is the highest role anywhere (team roles included); the user's own role is the fleet one.
  return me.user?.role ?? me.role
}

/**
 * The caller's role on a team: the fleet role (fleet scope) raised by the binding on that team.
 * `undefined` = no access to the team's workloads at all or only through the fleet (`unassigned`).
 */
export function roleOnTeam(me: Me | undefined, team: string | undefined): UserRole | undefined {
  if (!me) return undefined
  const fleet = fleetRoleOf(me)
  const bound = team ? me.teamRoles?.find((r) => r.team === team)?.role : undefined
  if (!bound) return fleet
  return !fleet || ROLE_RANK[bound] > ROLE_RANK[fleet] ? bound : fleet
}

/** Operator (or better) on the workload's team: what workload writes need. */
export function canOperateOnTeam(me: Me | undefined, team: string | undefined) {
  const r = roleOnTeam(me, team)
  return !!r && ROLE_RANK[r] >= ROLE_RANK.operator
}

/**
 * Where to go after sign-in: a path of this app only. Anything that would leave the origin
 * (`https://…`, `//host`, `/\host`, control characters that browsers strip) or that leads back to
 * the sign-in page gives `/`.
 */
export function safeNext(next: string | null | undefined, origin = window.location.origin): string {
  if (!next || !next.startsWith('/') || next[1] === '\\') return '/'
  for (const c of next) if (c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f) return '/'
  let u: URL
  try {
    u = new URL(next, origin)
  } catch {
    return '/'
  }
  if (u.origin !== origin || u.pathname === '/login' || u.pathname.startsWith('/login/')) return '/'
  return u.pathname + u.search + u.hash
}

/** The sign-in page for the current location, which it returns to afterwards. */
export const loginPath = (loc: { pathname: string; search: string; hash?: string }) =>
  loc.pathname === '/' && !loc.search
    ? '/login'
    : `/login?next=${encodeURIComponent(loc.pathname + loc.search + (loc.hash ?? ''))}`

const ME_POLL_MS = 5 * 60_000

/** Re-asks `/v1/me` right after the session's end (at most every 5 min) so an idle tab notices it. */
export function meRefetchInterval(me: Me | undefined, now = Date.now()): number {
  const end = me?.sessionExpiresAt ? Date.parse(me.sessionExpiresAt) : NaN
  if (!Number.isFinite(end)) return ME_POLL_MS
  return Math.min(ME_POLL_MS, Math.max(5_000, end - now + 1_000))
}

export function useMe() {
  const q = useQuery({
    queryKey: ME_KEY,
    queryFn: () => unwrap(api.GET('/v1/me')),
    retry: (n, e) => n < 2 && !(e instanceof ApiError && e.status >= 400 && e.status < 500),
    staleTime: 60_000,
    // Notices an expired or revoked session without waiting for a request to fail.
    refetchInterval: (query) => meRefetchInterval(query.state.data),
  })
  const csrf = q.error ? undefined : q.data?.csrfToken
  useEffect(() => setCsrfToken(csrf), [csrf])
  return q
}

export const AUTH_PROVIDERS_KEY = ['auth-providers'] as const

/** How the sign-in page offers to sign in (public: answered without a session). */
export function useAuthProviders() {
  return useQuery({
    queryKey: AUTH_PROVIDERS_KEY,
    queryFn: () => unwrap(api.GET('/v1/auth/providers')),
    // An older hub without the endpoint: password sign-in only.
    retry: false,
    staleTime: 5 * 60_000,
  })
}

export interface Auth {
  /** Who the browser is; undefined without a session. */
  me?: Me
  role: UserRole
  /** A signed-in user (a session). */
  signedIn: boolean
  /** No session: the hub answered 401, so only the sign-in page shows. */
  loginRequired: boolean
  /** The session this tab had ended (expired, revoked or signed out elsewhere). */
  expired: boolean
  loading: boolean
  /** `/v1/me` failed for another reason (hub down, or a hub without the admin API). */
  error?: unknown
  can: (min: UserRole) => boolean
  /** The caller sees only its teams' workloads (`scope: teams`). */
  teamScoped: boolean
  /** A session user with operator on the team (fleet role raised by its team roles). */
  canOperateTeam: (team: string | undefined) => boolean
}

export function useAuth(): Auth {
  const q = useMe()
  const loginRequired = q.error instanceof ApiError && q.error.status === 401
  // After a 401 the query keeps the last answer: never treat that as signed in.
  const me = loginRequired ? undefined : q.data
  const role = me?.role ?? 'viewer'
  const signedIn = me?.authMethod === 'session'
  // Pages and nav that need a fleet-wide role use the fleet one: a fleet viewer who is operator on one
  // team has `role: operator` but cannot open the operator pages.
  const gateRole = isTeamScoped(me) ? role : (fleetRoleOf(me) ?? role)
  return {
    me,
    role,
    signedIn,
    loginRequired,
    expired: loginRequired && q.data?.authMethod === 'session',
    loading: q.isPending,
    error: q.isError && !loginRequired ? q.error : undefined,
    // Writes need a session (a bearer token in a dev proxy may read, never write).
    can: (min) => (min === 'viewer' ? !!me : signedIn && roleAtLeast(gateRole, min)),
    teamScoped: isTeamScoped(me),
    canOperateTeam: (team) => signedIn && canOperateOnTeam(me, team),
  }
}

/** Every cached answer but `/v1/me`: dropped when the user changes, so nobody sees another's data. */
const notMe = (q: { queryKey: readonly unknown[] }) => q.queryKey[0] !== ME_KEY[0]

export function useLogin() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: { username: string; password: string }) =>
      unwrap(api.POST('/v1/auth/login', { body: v })),
    onSuccess: (me) => {
      setCsrfToken(me.csrfToken)
      qc.removeQueries({ predicate: notMe })
      qc.setQueryData(ME_KEY, me)
    },
  })
}

export function useLogout() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => unwrap(api.POST('/v1/auth/logout')),
    onSettled: () => {
      setCsrfToken(undefined)
      qc.removeQueries({ predicate: notMe })
      // Reset (not just invalidate) so nothing reads the old session while /v1/me refetches.
      void qc.resetQueries({ queryKey: ME_KEY })
    },
  })
}

export function useChangePassword() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: { currentPassword: string; newPassword: string }) =>
      unwrap(api.POST('/v1/me/password', { body: v })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ME_KEY }),
  })
}

export function actorLabel(a: { type: string; name: string } | undefined) {
  if (!a) return '—'
  if (a.type === 'system') return 'system'
  return a.name
}
