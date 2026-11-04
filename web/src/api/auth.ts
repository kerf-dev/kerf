import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { ApiError, api, setCsrfToken, unwrap } from './client'
import type { Me, UserRole } from './types'

/**
 * Session handling (D-023, docs/contracts/admin.md §3).
 *
 * `GET /v1/me` says who the browser is:
 * - `authMethod: session`: a logged-in user with a role and a CSRF token for writes;
 * - `authMethod: token`: no session, nginx added the read-only `ui` token (anonymous viewer);
 * - `401`: no session and no anonymous access, so every page needs a login.
 */
export const ROLE_RANK: Record<UserRole, number> = { viewer: 0, operator: 1, admin: 2 }
export const roleAtLeast = (role: UserRole | undefined, min: UserRole) =>
  ROLE_RANK[role ?? 'viewer'] >= ROLE_RANK[min]

export const ME_KEY = ['me'] as const

export function useMe() {
  const q = useQuery({
    queryKey: ME_KEY,
    queryFn: () => unwrap(api.GET('/v1/me')),
    retry: (n, e) => n < 2 && !(e instanceof ApiError && e.status >= 400 && e.status < 500),
    staleTime: 60_000,
    // Notices an expired or revoked session without waiting for a write to fail.
    refetchInterval: 5 * 60_000,
  })
  const csrf = q.data?.csrfToken
  useEffect(() => setCsrfToken(csrf), [csrf])
  return q
}

export interface Auth {
  me?: Me
  role: UserRole
  /** A logged-in user (not the anonymous `ui` token). */
  signedIn: boolean
  /** No session and no anonymous fallback: the hub answered 401. */
  loginRequired: boolean
  loading: boolean
  /** `/v1/me` failed for another reason (hub down, or a hub without the admin API). */
  error?: unknown
  can: (min: UserRole) => boolean
}

export function useAuth(): Auth {
  const q = useMe()
  const me = q.data
  const role = me?.role ?? 'viewer'
  const signedIn = me?.authMethod === 'session'
  const loginRequired = q.error instanceof ApiError && q.error.status === 401
  return {
    me,
    role,
    signedIn,
    loginRequired,
    loading: q.isPending,
    error: q.isError && !loginRequired ? q.error : undefined,
    // Writes need a session: the anonymous `ui` token is read-only whatever role it maps to.
    can: (min) => (min === 'viewer' ? true : signedIn && roleAtLeast(role, min)),
  }
}

export function useLogin() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: { username: string; password: string }) =>
      unwrap(api.POST('/v1/auth/login', { body: v })),
    onSuccess: (me) => {
      setCsrfToken(me.csrfToken)
      qc.setQueryData(ME_KEY, me)
      // Data may differ by role (e.g. admin-only lists): drop everything cached anonymously.
      void qc.invalidateQueries({ predicate: (q) => q.queryKey[0] !== ME_KEY[0] })
    },
  })
}

export function useLogout() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => unwrap(api.POST('/v1/auth/logout')),
    onSettled: () => {
      setCsrfToken(undefined)
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== ME_KEY[0] })
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
