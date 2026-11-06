import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { isTeamScoped, useMe } from './auth'
import { api, ifMatch, unwrap } from './client'
import { teamLabel } from '@/lib/teams'
import type {
  AutomationUpdate,
  ClusterCreate,
  ClusterUpdate,
  ConfigAuditQuery,
  DatasourceInput,
  HubPolicyType,
  InstallFormat,
  MetricsBinding,
  NotificationChannelInput,
  NotificationDeliveryStatus,
  NotificationDigestInput,
  NotificationRuleInput,
  PolicyChange,
  PolicyInput,
  PricingSettings,
  RecommenderSettings,
  TeamInput,
  TeamRole,
  TokenRole,
  UserRole,
  UserScope,
  WorkloadOverrideInput,
} from './types'

/**
 * Admin area queries and writes (docs/contracts/admin.md). Every versioned object carries `version`;
 * changes send `If-Match: "<version>"` so a stale page gets `412 version_conflict` instead of
 * silently overwriting someone else's change. CSRF is added by the client middleware.
 */

// ---------------------------------------------------------------- reads

/**
 * Fleet-wide endpoints (policies, readiness, capacity, node cost, ...) answer a team-scoped caller with
 * `403 scope_forbidden` (docs/teams.md): those callers never send the request.
 */
export function useFleetScope(enabled = true) {
  const me = useMe().data
  return enabled && !isTeamScoped(me)
}

const notFoundIsFinal = (n: number, e: unknown) =>
  n < 2 && !(e && typeof e === 'object' && 'status' in e && (e.status === 404 || e.status === 403))

export const useSettings = () =>
  useQuery({ queryKey: ['admin', 'settings'], queryFn: () => unwrap(api.GET('/v1/admin/settings')) })

export const useSettingsVersions = (section: 'recommender' | 'pricing') =>
  useQuery({
    queryKey: ['admin', 'settings-versions', section],
    queryFn: () =>
      unwrap(
        api.GET('/v1/admin/settings/{section}/versions', {
          params: { path: { section }, query: { limit: 20 } },
        }),
      ),
  })

export const usePolicies = (q: { cluster?: string; type?: HubPolicyType[]; includeDeleted?: boolean } = {}) =>
  useQuery({
    queryKey: ['admin', 'policies', q],
    queryFn: () => unwrap(api.GET('/v1/admin/policies', { params: { query: q } })),
    enabled: useFleetScope(),
  })

export const usePolicy = (policyId: string | undefined) =>
  useQuery({
    queryKey: ['admin', 'policy', policyId],
    queryFn: () =>
      unwrap(api.GET('/v1/admin/policies/{policyId}', { params: { path: { policyId: policyId! } } })),
    enabled: !!policyId,
    retry: notFoundIsFinal,
  })

export const usePolicyVersions = (policyId: string | undefined) =>
  useQuery({
    queryKey: ['admin', 'policy-versions', policyId],
    queryFn: () =>
      unwrap(
        api.GET('/v1/admin/policies/{policyId}/versions', {
          params: { path: { policyId: policyId! }, query: { limit: 100 } },
        }),
      ),
    enabled: !!policyId,
  })

export const usePolicyInventory = (cluster?: string[]) =>
  useQuery({
    queryKey: ['admin', 'policy-inventory', cluster ?? []],
    queryFn: () =>
      unwrap(
        api.GET('/v1/admin/policy-inventory', { params: { query: cluster?.length ? { cluster } : {} } }),
      ),
    enabled: useFleetScope(),
    refetchInterval: 30_000,
  })

export const useClusterAdmin = (cluster: string | undefined) =>
  useQuery({
    queryKey: ['admin', 'cluster', cluster],
    queryFn: () =>
      unwrap(api.GET('/v1/admin/clusters/{cluster}', { params: { path: { cluster: cluster! } } })),
    enabled: useFleetScope(!!cluster),
    retry: notFoundIsFinal,
    refetchInterval: 15_000,
  })

export const useFleetAutomation = () =>
  useQuery({
    queryKey: ['admin', 'automation'],
    queryFn: () => unwrap(api.GET('/v1/admin/automation')),
    refetchInterval: 15_000,
  })

export const useEffectivePolicy = (id: string, enabled = true) =>
  useQuery({
    queryKey: ['effective-policy', id],
    queryFn: () => unwrap(api.GET('/v1/workloads/{id}/effective-policy', { params: { path: { id } } })),
    enabled,
    retry: notFoundIsFinal,
  })

export const useTokens = (q: { cluster?: string; includeRevoked?: boolean } = {}, enabled = true) =>
  useQuery({
    queryKey: ['admin', 'tokens', q],
    queryFn: () => unwrap(api.GET('/v1/admin/tokens', { params: { query: q } })),
    enabled,
  })

/**
 * Teams with their workload counts and estimated savings. Viewers read them; a team-scoped caller gets
 * only its own teams and no `unassigned`.
 */
export const useTeams = (enabled = true) =>
  useQuery({
    queryKey: ['admin', 'teams'],
    queryFn: () => unwrap(api.GET('/v1/admin/teams')),
    enabled,
  })

/** Display names of the teams the caller can see (`unassigned` = "Unassigned"); the id while loading. */
export function useTeamLabel() {
  const teams = useTeams().data?.items
  return (value: string) => teamLabel(value, teams)
}

export const useUsers = (enabled = true) =>
  useQuery({
    queryKey: ['admin', 'users'],
    queryFn: () => unwrap(api.GET('/v1/admin/users')),
    enabled,
  })

export const useConfigAudit = (query: Omit<ConfigAuditQuery, 'continue'>, enabled = true) =>
  useInfiniteQuery({
    queryKey: ['config-audit', query],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/v1/audit/config', {
          params: { query: { ...query, ...(pageParam ? { continue: pageParam } : {}) } },
        }),
      ),
    initialPageParam: '' as string,
    getNextPageParam: (last) => last.continue || undefined,
    enabled,
  })

export const useDatasources = () =>
  useQuery({ queryKey: ['admin', 'datasources'], queryFn: () => unwrap(api.GET('/v1/admin/datasources')) })

export const useReadiness = (cluster: string | undefined, opts: { poll?: boolean } = {}) =>
  useQuery({
    queryKey: ['readiness', cluster],
    queryFn: () =>
      unwrap(api.GET('/v1/clusters/{cluster}/readiness', { params: { path: { cluster: cluster! } } })),
    enabled: useFleetScope(!!cluster),
    retry: notFoundIsFinal,
    // While a re-check is pending, poll quickly so results appear as they arrive.
    refetchInterval: (q) => (q.state.data?.recheckRequestedAt ? 2_000 : opts.poll ? 5_000 : 60_000),
  })

export const useReadinessHistory = (cluster: string | undefined) =>
  useQuery({
    queryKey: ['readiness-history', cluster],
    queryFn: () =>
      unwrap(
        api.GET('/v1/clusters/{cluster}/readiness/history', {
          params: { path: { cluster: cluster! }, query: { limit: 50 } },
        }),
      ),
    enabled: useFleetScope(!!cluster),
  })

export const useInstallSnippet = (
  cluster: string | undefined,
  format: InstallFormat,
  opts: { webhook?: boolean; certManager?: boolean } = {},
) =>
  useQuery({
    queryKey: ['admin', 'install', cluster, format, opts],
    queryFn: () =>
      unwrap(
        api.GET('/v1/admin/clusters/{cluster}/install', {
          params: { path: { cluster: cluster! }, query: { format, ...opts } },
        }),
      ),
    enabled: !!cluster,
  })

/** Component versions and the update check (Admin → Maintenance). */
export const useMaintenance = () =>
  useQuery({
    queryKey: ['admin', 'maintenance'],
    queryFn: () => unwrap(api.GET('/v1/admin/maintenance')),
    refetchInterval: 60_000,
  })

/** Admin → Notifications: channels, rules, the weekly digest and the delivery history. */
export const useNotificationStatus = () =>
  useQuery({
    queryKey: ['admin', 'notifications', 'status'],
    queryFn: () => unwrap(api.GET('/v1/admin/notifications/status')),
  })

export const useNotificationChannels = () =>
  useQuery({
    queryKey: ['admin', 'notifications', 'channels'],
    queryFn: () => unwrap(api.GET('/v1/admin/notifications/channels')),
    // The last delivery and the queue depth change on their own.
    refetchInterval: 30_000,
  })

export const useNotificationRules = () =>
  useQuery({
    queryKey: ['admin', 'notifications', 'rules'],
    queryFn: () => unwrap(api.GET('/v1/admin/notifications/rules')),
  })

export const useNotificationDigest = () =>
  useQuery({
    queryKey: ['admin', 'notifications', 'digest'],
    queryFn: () => unwrap(api.GET('/v1/admin/notifications/digest')),
    refetchInterval: 60_000,
  })

export const useNotificationDeliveries = (filter: {
  channelId?: string
  status?: NotificationDeliveryStatus
}) =>
  useInfiniteQuery({
    queryKey: ['admin', 'notifications', 'deliveries', filter],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/v1/admin/notifications/deliveries', {
          params: {
            query: {
              ...(filter.channelId ? { channelId: filter.channelId } : {}),
              ...(filter.status ? { status: filter.status } : {}),
              limit: 25,
              ...(pageParam ? { continue: pageParam } : {}),
            },
          },
        }),
      ),
    initialPageParam: '' as string,
    getNextPageParam: (last) => last.continue || undefined,
    refetchInterval: 30_000,
  })

/**
 * Clusters whose policies come only from Git (`policySource: git`, D-022): the hub sends them no
 * policies, so policy edits (defaults, policies, overrides) do not reach them and are shown read-only.
 */
export function useGitOnlyClusters() {
  const q = useQuery({
    queryKey: ['clusters'],
    queryFn: () => unwrap(api.GET('/v1/clusters')),
    refetchInterval: 30_000,
  })
  const names = (q.data?.items ?? []).filter((c) => c.policySource === 'git').map((c) => c.name)
  return { names, has: (c: string) => names.includes(c), loaded: !!q.data }
}

// ---------------------------------------------------------------- writes

/** All writes funnel through here so every page refreshes whatever it shows after a change. */
export function useWrite<TVars, TResult>(fn: (v: TVars) => Promise<TResult>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: () => qc.invalidateQueries(),
  })
}

const pathCluster = (cluster: string) => ({ path: { cluster } })

export const writes = {
  putRecommender: (v: { value: RecommenderSettings; reason: string; version: number }) =>
    unwrap(
      api.PUT('/v1/admin/settings/recommender', {
        params: { header: { 'If-Match': ifMatch(v.version) } },
        body: { value: v.value, reason: v.reason },
      }),
    ),
  putPricing: (v: { value: PricingSettings; reason: string; version: number }) =>
    unwrap(
      api.PUT('/v1/admin/settings/pricing', {
        params: { header: { 'If-Match': ifMatch(v.version) } },
        body: { value: v.value, reason: v.reason },
      }),
    ),
  recompute: (v: { cluster?: string }) =>
    unwrap(api.POST('/v1/admin/recompute', { params: { query: v.cluster ? { cluster: v.cluster } : {} } })),

  createPolicy: (body: PolicyInput) => unwrap(api.POST('/v1/admin/policies', { body })),
  updatePolicy: (v: { id: string; version: number; body: PolicyInput }) =>
    unwrap(
      api.PUT('/v1/admin/policies/{policyId}', {
        params: { path: { policyId: v.id }, header: { 'If-Match': ifMatch(v.version) } },
        body: v.body,
      }),
    ),
  deletePolicy: (v: { id: string; version: number; reason: string }) =>
    unwrap(
      api.DELETE('/v1/admin/policies/{policyId}', {
        params: { path: { policyId: v.id }, header: { 'If-Match': ifMatch(v.version) } },
        body: { reason: v.reason },
      }),
    ),
  revertPolicy: (v: { id: string; version: number; toVersion: number; reason: string }) =>
    unwrap(
      api.POST('/v1/admin/policies/{policyId}/revert', {
        params: { path: { policyId: v.id }, header: { 'If-Match': ifMatch(v.version) } },
        body: { version: v.toVersion, reason: v.reason },
      }),
    ),
  preview: (v: { changes: PolicyChange[]; limit?: number }) =>
    unwrap(api.POST('/v1/admin/preview', { body: { changes: v.changes, limit: v.limit ?? 50 } })),

  createCluster: (body: ClusterCreate) => unwrap(api.POST('/v1/admin/clusters', { body })),
  updateCluster: (v: { cluster: string; version: number; body: ClusterUpdate }) =>
    unwrap(
      api.PATCH('/v1/admin/clusters/{cluster}', {
        params: { path: { cluster: v.cluster }, header: { 'If-Match': ifMatch(v.version) } },
        body: v.body,
      }),
    ),
  deleteCluster: (v: { cluster: string; version: number; reason: string }) =>
    unwrap(
      api.DELETE('/v1/admin/clusters/{cluster}', {
        params: { path: { cluster: v.cluster }, header: { 'If-Match': ifMatch(v.version) } },
        body: { reason: v.reason },
      }),
    ),
  setClusterAutomation: (v: { cluster: string; body: AutomationUpdate }) =>
    unwrap(api.PUT('/v1/clusters/{cluster}/automation', { params: pathCluster(v.cluster), body: v.body })),
  setFleetAutomation: (body: AutomationUpdate) => unwrap(api.PUT('/v1/admin/automation', { body })),
  recheck: (cluster: string) =>
    unwrap(api.POST('/v1/clusters/{cluster}/readiness/recheck', { params: pathCluster(cluster) })),

  putOverride: (v: { id: string; version?: number; body: WorkloadOverrideInput }) =>
    unwrap(
      api.PUT('/v1/workloads/{id}/override', {
        params: { path: { id: v.id }, header: v.version ? { 'If-Match': ifMatch(v.version) } : {} },
        body: v.body,
      }),
    ),
  deleteOverride: (v: { id: string; version: number; reason: string }) =>
    unwrap(
      api.DELETE('/v1/workloads/{id}/override', {
        params: { path: { id: v.id }, header: { 'If-Match': ifMatch(v.version) } },
        body: { reason: v.reason },
      }),
    ),
  approve: (v: { id: string; recommendationID: string; reason: string; overrideReadiness?: boolean }) =>
    unwrap(
      api.POST('/v1/workloads/{id}/approve', {
        params: { path: { id: v.id } },
        body: {
          recommendationID: v.recommendationID,
          reason: v.reason,
          ...(v.overrideReadiness ? { overrideReadiness: true } : {}),
        },
      }),
    ),

  createToken: (body: {
    role: TokenRole
    cluster?: string
    description: string
    expiresAt?: string
    reason: string
  }) => unwrap(api.POST('/v1/admin/tokens', { body })),
  revokeToken: (v: { id: string; reason: string }) =>
    unwrap(
      api.DELETE('/v1/admin/tokens/{tokenId}', {
        params: { path: { tokenId: v.id } },
        body: { reason: v.reason },
      }),
    ),

  createTeam: (body: TeamInput) => unwrap(api.POST('/v1/admin/teams', { body })),
  updateTeam: (v: { id: string; version: number; body: TeamInput }) =>
    unwrap(
      api.PUT('/v1/admin/teams/{teamId}', {
        params: { path: { teamId: v.id }, header: { 'If-Match': ifMatch(v.version) } },
        body: v.body,
      }),
    ),
  deleteTeam: (v: { id: string; version: number; reason: string }) =>
    unwrap(
      api.DELETE('/v1/admin/teams/{teamId}', {
        params: { path: { teamId: v.id }, header: { 'If-Match': ifMatch(v.version) } },
        body: { reason: v.reason },
      }),
    ),

  createUser: (body: {
    username: string
    displayName?: string
    role: UserRole
    scope?: UserScope
    teamRoles?: TeamRole[]
    password: string
    mustChangePassword: boolean
    reason: string
  }) => unwrap(api.POST('/v1/admin/users', { body })),
  updateUser: (v: {
    username: string
    version: number
    body: {
      displayName?: string
      role?: UserRole
      disabled?: boolean
      scope?: UserScope
      teamRoles?: TeamRole[]
      reason: string
    }
  }) =>
    unwrap(
      api.PATCH('/v1/admin/users/{username}', {
        params: { path: { username: v.username }, header: { 'If-Match': ifMatch(v.version) } },
        body: v.body,
      }),
    ),
  deleteUser: (v: { username: string; version: number; reason: string }) =>
    unwrap(
      api.DELETE('/v1/admin/users/{username}', {
        params: { path: { username: v.username }, header: { 'If-Match': ifMatch(v.version) } },
        body: { reason: v.reason },
      }),
    ),
  resetPassword: (v: { username: string; reason: string; password?: string }) =>
    unwrap(
      api.POST('/v1/admin/users/{username}/reset-password', {
        params: { path: { username: v.username } },
        body: { reason: v.reason, ...(v.password ? { password: v.password } : {}) },
      }),
    ),

  createDatasource: (body: DatasourceInput) => unwrap(api.POST('/v1/admin/datasources', { body })),
  updateDatasource: (v: { id: string; version: number; body: DatasourceInput }) =>
    unwrap(
      api.PUT('/v1/admin/datasources/{datasourceId}', {
        params: { path: { datasourceId: v.id }, header: { 'If-Match': ifMatch(v.version) } },
        body: v.body,
      }),
    ),
  deleteDatasource: (v: { id: string; version: number; reason: string }) =>
    unwrap(
      api.DELETE('/v1/admin/datasources/{datasourceId}', {
        params: { path: { datasourceId: v.id }, header: { 'If-Match': ifMatch(v.version) } },
        body: { reason: v.reason },
      }),
    ),
  checkForUpdate: () => unwrap(api.POST('/v1/admin/maintenance/update-check')),

  createNotificationChannel: (body: NotificationChannelInput) =>
    unwrap(api.POST('/v1/admin/notifications/channels', { body })),
  updateNotificationChannel: (v: { id: string; version: number; body: NotificationChannelInput }) =>
    unwrap(
      api.PUT('/v1/admin/notifications/channels/{channelId}', {
        params: { path: { channelId: v.id }, header: { 'If-Match': ifMatch(v.version) } },
        body: v.body,
      }),
    ),
  deleteNotificationChannel: (v: { id: string; version: number; reason: string }) =>
    unwrap(
      api.DELETE('/v1/admin/notifications/channels/{channelId}', {
        params: { path: { channelId: v.id }, header: { 'If-Match': ifMatch(v.version) } },
        body: { reason: v.reason },
      }),
    ),
  testNotificationChannel: (v: { id: string }) =>
    unwrap(
      api.POST('/v1/admin/notifications/channels/{channelId}/test', {
        params: { path: { channelId: v.id } },
      }),
    ),
  createNotificationRule: (body: NotificationRuleInput) =>
    unwrap(api.POST('/v1/admin/notifications/rules', { body })),
  updateNotificationRule: (v: { id: string; version: number; body: NotificationRuleInput }) =>
    unwrap(
      api.PUT('/v1/admin/notifications/rules/{ruleId}', {
        params: { path: { ruleId: v.id }, header: { 'If-Match': ifMatch(v.version) } },
        body: v.body,
      }),
    ),
  deleteNotificationRule: (v: { id: string; version: number; reason: string }) =>
    unwrap(
      api.DELETE('/v1/admin/notifications/rules/{ruleId}', {
        params: { path: { ruleId: v.id }, header: { 'If-Match': ifMatch(v.version) } },
        body: { reason: v.reason },
      }),
    ),
  putNotificationDigest: (v: { version: number; body: NotificationDigestInput }) =>
    unwrap(
      api.PUT('/v1/admin/notifications/digest', {
        params: { header: { 'If-Match': ifMatch(v.version) } },
        body: v.body,
      }),
    ),
  sendNotificationDigest: () => unwrap(api.POST('/v1/admin/notifications/digest/send')),

  testDatasource: (v: { id: string; binding?: MetricsBinding }) =>
    unwrap(
      api.POST('/v1/admin/datasources/{datasourceId}/test', {
        params: { path: { datasourceId: v.id } },
        body: v.binding ? { binding: v.binding } : {},
      }),
    ),
}

/**
 * Every workload row (paged, up to `max`), for client-side selector matching in the policy editor.
 * The hub has no "match this selector" endpoint; labels are not in rows, so `matchLabels` is shown
 * as evaluated on the cluster.
 */
export const useWorkloadIndex = (max = 2000) =>
  useQuery({
    queryKey: ['workload-index', max],
    queryFn: async () => {
      const rows = []
      let cont: string | undefined
      do {
        const page = await unwrap(
          api.GET('/v1/workloads', {
            params: { query: { limit: 500, sort: 'savings', ...(cont ? { continue: cont } : {}) } },
          }),
        )
        rows.push(...page.items)
        cont = page.continue
      } while (cont && rows.length < max)
      return { rows, truncated: !!cont }
    },
    staleTime: 5 * 60_000,
  })

export const useClusterCapacity = (cluster: string | undefined) =>
  useQuery({
    queryKey: ['capacity', cluster],
    queryFn: () =>
      unwrap(api.GET('/v1/clusters/{cluster}/capacity', { params: { path: { cluster: cluster! } } })),
    enabled: useFleetScope(!!cluster),
    retry: notFoundIsFinal,
    refetchInterval: 60_000,
  })
