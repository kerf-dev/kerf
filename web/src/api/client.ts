import createClient, { type Middleware } from 'openapi-fetch'
import type { paths } from './schema'
import type { Problem } from './types'

/**
 * Typed hub client. The browser always talks to its own origin: in production nginx proxies `/v1/`
 * to the hub; in `live` dev mode Vite does the same; in `mock` mode MSW answers.
 *
 * Authentication (D-023): after `POST /v1/auth/login` the hub sets the HttpOnly `ro_session` cookie,
 * which the browser sends with every same-origin request; writes add the `X-CSRF-Token` from
 * `GET /v1/me` (see `setCsrfToken`). Without a session, nginx may add the read-only `ui` bearer token
 * (anonymous viewer). The browser never holds a bearer token.
 */
export class ApiError extends Error {
  readonly status: number
  readonly problem?: Problem
  /** Seconds from a `Retry-After` header (429 lockout / rate limit). */
  readonly retryAfter?: number
  constructor(status: number, problem?: Problem, fallback?: string, retryAfter?: number) {
    super(problem?.detail || problem?.title || fallback || `Request failed with status ${status}`)
    this.name = 'ApiError'
    this.status = status
    this.problem = problem
    this.retryAfter = retryAfter
  }
  get code() {
    return this.problem?.code
  }
}

/** True for an optimistic-locking conflict: the object changed since it was loaded. */
export const isConflict = (e: unknown) =>
  e instanceof ApiError && (e.status === 412 || e.code === 'version_conflict')

let csrfToken: string | undefined
/** Set from `Me.csrfToken` after login / on load; cleared on logout. */
export function setCsrfToken(t: string | undefined) {
  csrfToken = t
}

const csrf: Middleware = {
  onRequest({ request }) {
    if (
      csrfToken &&
      !['GET', 'HEAD', 'OPTIONS'].includes(request.method) &&
      !request.headers.has('X-CSRF-Token')
    )
      request.headers.set('X-CSRF-Token', csrfToken)
    return request
  },
}

/** `If-Match` header value for a versioned object (`ETag: "<version>"`). */
export const ifMatch = (version: number) => `"${version}"`

/**
 * Workload ids are sent as a single path segment with `/` encoded as `%2F` (hub.yaml). openapi-fetch
 * already encodes path params with encodeURIComponent; this middleware is a guard that fails loudly
 * if a future version stops doing so, because a decoded id would hit the wrong route.
 */
const guardWorkloadIdEncoding: Middleware = {
  onRequest({ request, schemaPath }) {
    if (schemaPath.includes('/v1/workloads/{id}')) {
      const path = new URL(request.url).pathname
      const seg = path.split('/')[3] ?? ''
      if (!seg.includes('%2F')) throw new Error(`workload id must be URL-encoded as one segment: ${path}`)
    }
    return undefined
  },
}

export const api = createClient<paths>({
  baseUrl: typeof window === 'undefined' ? 'http://localhost' : window.location.origin,
  querySerializer: { array: { style: 'form', explode: true } },
  // The session cookie is same-origin (nginx / the Vite proxy / MSW serve /v1 on this origin).
  credentials: 'same-origin',
  // Resolve fetch lazily so MSW (and tests) can patch it after the client is created.
  fetch: (input: Request) => globalThis.fetch(input),
})
api.use(guardWorkloadIdEncoding, csrf)

/** Unwraps an openapi-fetch result, throwing ApiError for non-2xx responses. */
export async function unwrap<T>(p: Promise<{ data?: T; error?: unknown; response: Response }>): Promise<T> {
  let res: { data?: T; error?: unknown; response: Response }
  try {
    res = await p
  } catch (e) {
    throw new ApiError(0, undefined, e instanceof Error ? `Network error: ${e.message}` : 'Network error')
  }
  // 204 No Content (logout, deletes): openapi-fetch returns `data: undefined` / `{}`.
  if (res.response.ok && res.response.status === 204) return undefined as T
  if (!res.response.ok || res.data === undefined) {
    const problem = (res.error && typeof res.error === 'object' ? res.error : undefined) as
      Problem | undefined
    const ra = Number(res.response.headers.get('Retry-After'))
    throw new ApiError(
      res.response.status,
      problem,
      res.response.statusText,
      Number.isFinite(ra) && ra > 0 ? ra : undefined,
    )
  }
  return res.data
}
