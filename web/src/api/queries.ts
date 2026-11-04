import { keepPreviousData, useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { api, unwrap } from './client'
import type { ListWorkloadsQuery, QueryAuditQuery, ResourceName } from './types'

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
