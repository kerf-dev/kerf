import createClient, { type Middleware } from 'openapi-fetch'
import type { paths } from './schema'
import type { Problem } from './types'

/**
 * Typed hub client. The browser always talks to its own origin: in production nginx proxies `/v1/`
 * to the hub and adds the `ui` bearer token; in `live` dev mode Vite does the same; in `mock` mode
 * MSW answers. The browser never holds a token.
 */
export class ApiError extends Error {
  readonly status: number
  readonly problem?: Problem
  constructor(status: number, problem?: Problem, fallback?: string) {
    super(problem?.detail || problem?.title || fallback || `Request failed with status ${status}`)
    this.name = 'ApiError'
    this.status = status
    this.problem = problem
  }
}

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
  // Resolve fetch lazily so MSW (and tests) can patch it after the client is created.
  fetch: (input: Request) => globalThis.fetch(input),
})
api.use(guardWorkloadIdEncoding)

/** Unwraps an openapi-fetch result, throwing ApiError for non-2xx responses. */
export async function unwrap<T>(p: Promise<{ data?: T; error?: unknown; response: Response }>): Promise<T> {
  let res: { data?: T; error?: unknown; response: Response }
  try {
    res = await p
  } catch (e) {
    throw new ApiError(0, undefined, e instanceof Error ? `Network error: ${e.message}` : 'Network error')
  }
  if (!res.response.ok || res.data === undefined) {
    const problem = (res.error && typeof res.error === 'object' ? res.error : undefined) as
      Problem | undefined
    throw new ApiError(res.response.status, problem, res.response.statusText)
  }
  return res.data
}
