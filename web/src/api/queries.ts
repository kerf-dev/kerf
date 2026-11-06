import { keepPreviousData, useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { api, unwrap } from './client'
import type { FacetsQuery, ListWorkloadsQuery, QueryAuditQuery, ResourceName } from './types'

export type TimeRange = '24h' | '7d' | '14d' | '30d'
export const TIME_RANGES: { value: TimeRange; label: string; seconds: number }[] = [
  { value: '24h', label: '24h', seconds: 86400 },
  { value: '7d', label: '7d', seconds: 7 * 86400 },
  { value: '14d', label: '14d', seconds: 14 * 86400 },
  { value: '30d', label: '30d', seconds: 30 * 86400 },
]

/** Resolves a range to an RFC 3339 window ending now (rounded to the minute so caches hit). */
export function rangeWindow(r: TimeRange) {
  const secs = TIME_RANGES.find((x) => x.value === r)!.seconds
  const to = Math.floor(Date.now() / 60_000) * 60_000
  return { from: new Date(to - secs * 1000).toISOString(), to: new Date(to).toISOString() }
}

export const useSummary = () =>
  useQuery({ queryKey: ['summary'], queryFn: () => unwrap(api.GET('/v1/summary')), refetchInterval: 60_000 })

export const useClusters = () =>
  useQuery({
    queryKey: ['clusters'],
    queryFn: () => unwrap(api.GET('/v1/clusters')),
    refetchInterval: 30_000,
  })

export const useWorkloads = (query: ListWorkloadsQuery, opts: { enabled?: boolean } = {}) =>
  useQuery({
    queryKey: ['workloads', query],
    queryFn: () => unwrap(api.GET('/v1/workloads', { params: { query } })),
    placeholderData: keepPreviousData,
    enabled: opts.enabled ?? true,
  })

export const useWorkload = (id: string) =>
  useQuery({
    queryKey: ['workload', id],
    queryFn: () => unwrap(api.GET('/v1/workloads/{id}', { params: { path: { id } } })),
    retry: (n, e) => n < 2 && !('status' in e && (e.status === 404 || e.status === 400)),
  })

export const useUsage = (id: string, resource: ResourceName, range: TimeRange, enabled = true) =>
  useQuery({
    queryKey: ['usage', id, resource, range],
    queryFn: () =>
      unwrap(
        api.GET('/v1/workloads/{id}/usage', {
          params: { path: { id }, query: { resource, ...rangeWindow(range) } },
        }),
      ),
    placeholderData: keepPreviousData,
    enabled,
  })

export const useHistory = (id: string, enabled = true) =>
  useQuery({
    queryKey: ['history', id],
    queryFn: () =>
      unwrap(
        api.GET('/v1/workloads/{id}/history', {
          params: { path: { id }, query: { ...rangeWindow('30d'), limit: 200 } },
        }),
      ),
    enabled,
  })

export const useSavingsTrend = (range: TimeRange, groupBy: 'none' | 'cluster' = 'cluster') =>
  useQuery({
    queryKey: ['savings-trend', range, groupBy],
    queryFn: () =>
      unwrap(api.GET('/v1/savings/trend', { params: { query: { groupBy, ...rangeWindow(range) } } })),
    placeholderData: keepPreviousData,
  })

/** Realized window ending now: 12 months by month, or 30 days by day (UTC buckets). */
export function realizedWindow(step: 'day' | 'month') {
  const to = Math.floor(Date.now() / 60_000) * 60_000
  const d = new Date(to)
  const from =
    step === 'month'
      ? Date.UTC(d.getUTCFullYear(), d.getUTCMonth() - 11, 1)
      : Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate() - 29)
  return { from: new Date(from).toISOString(), to: new Date(to).toISOString() }
}

/** Realized savings of the fleet (or clusters): released requests x replicas x rates of the time. */
export const useRealizedSavings = (step: 'day' | 'month', cluster?: string[]) =>
  useQuery({
    queryKey: ['realized', step, cluster ?? []],
    queryFn: () =>
      unwrap(
        api.GET('/v1/savings/realized', {
          params: { query: { step, ...realizedWindow(step), ...(cluster?.length ? { cluster } : {}) } },
        }),
      ),
    placeholderData: keepPreviousData,
    refetchInterval: 5 * 60_000,
  })

export const useWorkloadRealized = (id: string, enabled = true) =>
  useQuery({
    queryKey: ['realized-workload', id],
    queryFn: () =>
      unwrap(
        api.GET('/v1/workloads/{id}/savings/realized', {
          params: { path: { id }, query: { step: 'day', ...realizedWindow('day') } },
        }),
      ),
    enabled,
  })

export const useNodeCostTrend = (cluster: string | undefined, range: TimeRange) =>
  useQuery({
    queryKey: ['node-cost', cluster, range],
    queryFn: () =>
      unwrap(
        api.GET('/v1/savings/realized/node-cost', {
          params: { query: { cluster: cluster!, ...rangeWindow(range) } },
        }),
      ),
    enabled: !!cluster,
    placeholderData: keepPreviousData,
  })

export const useAudit = (query: Omit<QueryAuditQuery, 'continue'>) =>
  useInfiniteQuery({
    queryKey: ['audit', query],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/v1/audit', {
          params: { query: { ...query, ...(pageParam ? { continue: pageParam } : {}) } },
        }),
      ),
    initialPageParam: '' as string,
    getNextPageParam: (last) => last.continue || undefined,
  })

/** Filter values with counts (D-016). Each facet ignores its own filter. */
export const useFacets = (query: FacetsQuery) =>
  useQuery({
    queryKey: ['facets', query],
    queryFn: () => unwrap(api.GET('/v1/workloads/facets', { params: { query } })),
    placeholderData: keepPreviousData,
    // Older hubs (before WO-04b) have no facets endpoint; callers fall back to static options.
    retry: false,
  })
