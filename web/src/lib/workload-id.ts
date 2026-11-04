import type { WorkloadKind } from '@/api/types'

/**
 * Workload ids are `<cluster>/<namespace>/<kind>/<name>` (hub.yaml). In API paths they travel as ONE
 * URL-encoded segment (`/` → `%2F`). In UI routes we use the readable form
 * `/workloads/<cluster>/<namespace>/<kind>/<name>` instead, and convert at the API boundary.
 */
export interface WorkloadIdParts {
  cluster: string
  namespace: string
  kind: WorkloadKind
  name: string
}

const KINDS = new Set<string>(['Deployment', 'StatefulSet', 'DaemonSet', 'CronJob'])

export function parseWorkloadId(id: string): WorkloadIdParts | undefined {
  const parts = id.split('/')
  if (parts.length !== 4 || parts.some((p) => p === '')) return undefined
  const [cluster, namespace, kind, name] = parts as [string, string, string, string]
  if (!KINDS.has(kind)) return undefined
  return { cluster, namespace, kind: kind as WorkloadKind, name }
}

export function workloadId(p: WorkloadIdParts): string {
  return `${p.cluster}/${p.namespace}/${p.kind}/${p.name}`
}

/** The path segment for the hub API: the whole id, encoded so `/` becomes `%2F`. */
export function encodeWorkloadId(id: string): string {
  return encodeURIComponent(id)
}

/** UI route for a workload. Each part is encoded on its own so the route keeps readable slashes. */
export function workloadPath(id: string): string {
  return `/workloads/${id.split('/').map(encodeURIComponent).join('/')}`
}
