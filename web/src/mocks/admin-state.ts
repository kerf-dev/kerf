/**
 * Admin-area state of the mock hub (D-022..D-025, docs/contracts/admin.md): users, tokens, settings,
 * hub policies and their versions, Git policies (inventory), cluster metadata and automation,
 * datasources, readiness, approvals and the config audit. Seeded consistently with `data.ts`: the
 * layering below (admin.md §2) reproduces the modes the fleet was generated with.
 */
import type { components } from '@/api/schema'
import { CLUSTERS, iso, POLICIES, type MockDb, type WorkloadModel } from './data'
import { hash, mulberry32, uuid, type Rng } from './random'
import { buildReadiness, type ReadinessModel } from './readiness'
import { seedNotifications, type NotificationModel } from './notifications-state'
import { overridePolicyName } from './sha256'

type S = components['schemas']
export type AdminPolicy = S['AdminPolicy']
export type PolicyVersion = S['PolicyVersion']
export type Spec = S['RightsizingPolicySpec']
export type Selector = S['PolicySelector']
export type Actor = S['Actor']
export type User = S['User']
export type UserRole = S['UserRole']
export type TokenInfo = S['TokenInfo']
export type Team = S['Team']
export type Automation = S['Automation']
export type ConfigChange = S['ConfigChange']
export type Datasource = S['Datasource']
export type Approval = S['Approval']
export type MetricsBinding = S['MetricsBinding']
export type Mode = S['Mode']
export type ModeSource = S['ModeSource']
export type EffectiveModes = S['EffectiveModes']
export type LayerSource = S['LayerSource']
export type PolicyLayer = S['PolicyLayer']
export type EffectiveField = S['EffectiveField']
export type HubPolicyType = S['HubPolicyType']
export type RecommenderSettingsDoc = S['RecommenderSettingsDoc']
export type PricingSettingsDoc = S['PricingSettingsDoc']
export type SettingsVersion = S['SettingsVersion']
export type UpdateStatus = S['UpdateStatus']

export interface UserRec extends User {
  password: string
  failedLogins: number
  lockedUntilMs?: number
}

/** A team as stored: the counts of `Team` are computed from the fleet when it is read. */
export type TeamRec = Omit<Team, 'workloads' | 'monthlySavingsUSD'>

export interface GitPolicy {
  name: string
  priority: number
  clusters: string[]
  spec: Spec
  generation: number
}

export interface ClusterMeta {
  name: string
  description: string
  environment: S['Environment']
  owner: string
  policySource: S['ClusterPolicySource']
  provider?: string
  region?: string
  metrics: MetricsBinding
  createdAt: string
  updatedAt?: string
  updatedBy?: Actor
  deletedAt?: string
  version: number
  automation: Automation
  /** config version counter (`c-<n>`) and when it last changed (ms) */
  configVersion: number
  appliedConfigVersion?: number
  configChangedAt: number
  agentConnected: boolean
  agentVersion?: string
  /** a newly registered cluster's simulated agent connects `timing.connectMs` after this (ms) */
  connectPendingSince?: number
}

export interface DatasourceRec extends Datasource {
  knownLabelValues: string[]
  deletedAt?: string
}

export interface ApprovalRec extends Approval {
  /** the simulated agent applies it `timing.approvalMs` after approval */
  autoConsume: boolean
}

/** Component versions and the update check (Admin → Maintenance). */
export interface MaintenanceModel {
  hubVersion: string
  chartVersion: string
  /** hub replicas; the first is the leader */
  hubReplicas: { instance: string; startedAt: string }[]
  pricing: { instance: string; version: string }[]
  /** webhook pods per cluster (agent self-check); no entry = no webhook found */
  webhooks: Map<string, { instance: string; version: string }[]>
  update: UpdateStatus
  /** what the public releases API returns on the next "Check now" */
  release: { version: string; name: string; url: string; publishedAt: string }
}

export interface AdminState {
  users: Map<string, UserRec>
  /** Teams (docs/teams.md), ordered by id; selectors match namespace names (the mock has no labels). */
  teams: TeamRec[]
  /** The label key that names a workload's team. */
  ownerLabel: string
  tokens: TokenInfo[]
  nextTokenId: number
  policies: AdminPolicy[]
  versions: Map<string, PolicyVersion[]>
  gitPolicies: GitPolicy[]
  recommender: RecommenderSettingsDoc
  pricing: PricingSettingsDoc
  /** Read-only rate sources returned with the pricing settings (the pricing service's series). */
  pricingRates: S['PricingRates']
  settingsVersions: SettingsVersion[]
  clusters: Map<string, ClusterMeta>
  fleet: Automation
  datasources: DatasourceRec[]
  readiness: Map<string, ReadinessModel>
  approvals: ApprovalRec[]
  audit: ConfigChange[]
  annotations: Map<string, { cpu?: Mode; memory?: Mode; profile?: S['Profile'] }>
  rng: Rng
  /** delay until the simulated agent applies a config change */
  syncDelayMs: number
  maintenance: MaintenanceModel
  notifications: NotificationModel
}

export const SYSTEM: Actor = { type: 'system', name: 'system' }
export const userActor = (u: { username: string; role: string }): Actor => ({
  type: 'user',
  name: u.username,
  role: u.role,
})

export const BUILT_IN: Spec = {
  profile: 'production',
  cpu: { mode: 'audit', limits: 'keep' },
  memory: { mode: 'audit', limitEqualsRequest: true },
  apply: { strategy: 'in-place', fallbackAfter: '10m' },
  guardrails: {
    learningPeriod: '168h',
    minConfidence: '0.7',
    maxChangePercent: 50,
    cooldown: '24h',
    rollback: { watchDuration: '1h', oomKills: 1, restarts: 3, throttlingRatio: '0.1' },
  },
  hpa: { coordinate: true },
  jvm: { detect: true },
}

const DEFAULT_JOBS = { cadvisor: ['kubelet', 'cadvisor'], kubeStateMetrics: ['kube-state-metrics'] }
export const defaultBinding = (name: string, datasourceId = 'local'): MetricsBinding => ({
  datasourceId,
  clusterLabel: 'cluster',
  clusterLabelValue: name,
  extraMatchers: [],
  jobNames: DEFAULT_JOBS,
})

// ---------------------------------------------------------------------------------------------
// Layering (admin.md §2)
// ---------------------------------------------------------------------------------------------

const live = (p: AdminPolicy) => !p.deletedAt

function glob(pattern: string, s: string) {
  const re = new RegExp(
    '^' +
      pattern
        .split('')
        .map((ch) => (ch === '*' ? '.*' : ch === '?' ? '.' : ch.replace(/[.+^${}()|[\]\\]/g, '\\$&')))
        .join('') +
      '$',
  )
  return re.test(s)
}

export interface MatchTarget {
  cluster: string
  namespace: string
  kind: S['WorkloadKind']
  name: string
}

/** Whether a selector matches; `labels` is set when a matchLabels selector was skipped. */
export function selectorMatches(sel: Selector | undefined, w: MatchTarget, flags?: { labels: boolean }) {
  if (!sel) return true
  if (sel.namespaces?.length && !sel.namespaces.some((p) => glob(p, w.namespace))) return false
  if (sel.excludeNamespaces?.some((p) => glob(p, w.namespace))) return false
  if (sel.kinds?.length && !sel.kinds.includes(w.kind)) return false
  if (
    sel.workloads?.length &&
    !sel.workloads.some((r) => r.kind === w.kind && r.namespace === w.namespace && r.name === w.name)
  )
    return false
  if (sel.matchLabels && Object.keys(sel.matchLabels).length) {
    if (flags) flags.labels = true
    return false
  }
  return true
}

const targets = (p: { clusters?: string[] }, cluster: string) =>
  !p.clusters?.length || p.clusters.includes(cluster)

function mergeSpec(a: Spec, b: Spec | undefined): Spec {
  if (!b) return a
  const out: Record<string, unknown> = { ...a }
  for (const [k, v] of Object.entries(b)) {
    if (k === 'selector' || k === 'priority' || v === undefined) continue
    const cur = out[k]
    if (
      v &&
      typeof v === 'object' &&
      !Array.isArray(v) &&
      cur &&
      typeof cur === 'object' &&
      !Array.isArray(cur)
    )
      out[k] = mergeSpec(cur as Spec, v as Spec)
    else out[k] = v
  }
  return out as Spec
}

/** Flattens a spec (without selector/priority) to `path → value` for the effective fields. */
export function flatten(spec: Spec | undefined, prefix = '', out: Record<string, string> = {}) {
  if (!spec) return out
  for (const [k, v] of Object.entries(spec)) {
    if (!prefix && (k === 'selector' || k === 'priority')) continue
    if (v === undefined || v === null) continue
    const path = prefix ? `${prefix}.${k}` : k
    if (typeof v === 'object' && !Array.isArray(v)) flatten(v as Spec, path, out)
    else out[path] = typeof v === 'string' ? v : JSON.stringify(v)
  }
  return out
}

export interface PolicySet {
  hub: AdminPolicy[]
  git: GitPolicy[]
}

export interface Evaluation {
  layers: PolicyLayer[]
  fields: EffectiveField[]
  modes: EffectiveModes
  policy?: string
  override?: string
  labelsSkipped: boolean
}

const SOURCE_TO_MODE: Record<LayerSource, ModeSource> = {
  'built-in': 'global-default',
  'global-default': 'global-default',
  'cluster-default-chart': 'cluster-default',
  'cluster-default': 'cluster-default',
  policy: 'policy',
  'workload-override': 'policy',
  annotation: 'annotation',
}

const ANNOTATION = {
  cpu: 'kerf.getkerf.dev/cpu-mode',
  memory: 'kerf.getkerf.dev/memory-mode',
  profile: 'kerf.getkerf.dev/profile',
}

export function evaluate(
  st: AdminState,
  w: MatchTarget & { id: string },
  set: PolicySet = currentSet(st),
): Evaluation {
  const meta = st.clusters.get(w.cluster)
  const gitMode = meta?.policySource === 'git'
  const hub = gitMode ? [] : set.hub.filter(live)
  const layers: PolicyLayer[] = [{ source: 'built-in', applied: true, spec: BUILT_IN }]
  const hubLayer = (source: LayerSource, p: AdminPolicy): PolicyLayer => ({
    source,
    name: p.name,
    origin: 'hub',
    policyId: p.id,
    priority: p.priority,
    applied: true,
    spec: stripSpec(p.spec),
  })
  const g = hub.find((p) => p.type === 'global-default')
  if (g) layers.push(hubLayer('global-default', g))
  const cd = hub.find((p) => p.type === 'cluster-default' && p.clusters.includes(w.cluster))
  if (cd) layers.push(hubLayer('cluster-default', cd))

  const flags = { labels: false }
  const candidates: { name: string; priority: number; layer: PolicyLayer }[] = []
  for (const p of hub)
    if (p.type === 'policy' && targets(p, w.cluster) && selectorMatches(p.spec.selector, w, flags))
      candidates.push({ name: p.name, priority: p.priority, layer: hubLayer('policy', p) })
  for (const p of set.git)
    if (p.clusters.includes(w.cluster) && selectorMatches(p.spec.selector, w, flags))
      candidates.push({
        name: p.name,
        priority: p.priority,
        layer: {
          source: 'policy',
          name: p.name,
          origin: 'git',
          priority: p.priority,
          applied: true,
          spec: stripSpec(p.spec),
        },
      })
  candidates.sort((a, b) => b.priority - a.priority || a.name.localeCompare(b.name))
  const winner = candidates[0]
  for (const c of candidates) {
    if (c !== winner) {
      c.layer.applied = false
      c.layer.note =
        c.priority === winner!.priority
          ? `same priority ${c.priority} as ${winner!.name}; ties go by name`
          : `priority ${c.priority} < ${winner!.priority} of ${winner!.name}`
    }
  }
  // Losing policies first (lower precedence), then the winner.
  layers.push(
    ...candidates
      .slice(1)
      .reverse()
      .map((c) => c.layer),
  )
  if (winner) layers.push(winner.layer)

  const ov = hub.find((p) => p.type === 'workload-override' && p.workloadId === w.id)
  if (ov) layers.push(hubLayer('workload-override', ov))
  const ann = st.annotations.get(w.id)
  if (ann?.profile)
    layers.push({
      source: 'annotation',
      name: ANNOTATION.profile,
      applied: true,
      spec: { profile: ann.profile },
    })
  if (ann?.cpu)
    layers.push({
      source: 'annotation',
      name: ANNOTATION.cpu,
      applied: true,
      spec: { cpu: { mode: ann.cpu } },
    })
  if (ann?.memory)
    layers.push({
      source: 'annotation',
      name: ANNOTATION.memory,
      applied: true,
      spec: { memory: { mode: ann.memory } },
    })

  const fieldMap = new Map<string, EffectiveField>()
  for (const l of layers) {
    if (!l.applied) continue
    for (const [path, value] of Object.entries(flatten(l.spec)))
      fieldMap.set(path, { path, value, source: l.source, ...(l.name ? { name: l.name } : {}) })
  }
  const fields = [...fieldMap.values()].sort((a, b) => a.path.localeCompare(b.path))
  const cpu = fieldMap.get('cpu.mode')!
  const mem = fieldMap.get('memory.mode')!
  return {
    layers,
    fields,
    modes: {
      cpu: cpu.value as Mode,
      memory: mem.value as Mode,
      cpuSource: SOURCE_TO_MODE[cpu.source],
      memorySource: SOURCE_TO_MODE[mem.source],
    },
    policy: winner?.name,
    override: ov?.name,
    labelsSkipped: flags.labels,
  }
}

function stripSpec(spec: Spec): Spec {
  const rest: Spec = { ...spec }
  delete rest.selector
  delete rest.priority
  return rest
}

export const currentSet = (st: AdminState): PolicySet => ({ hub: st.policies, git: st.gitPolicies })

/** Applies the current layering to every workload (modes, winning policy). */
export function recompute(st: AdminState, db: MockDb) {
  for (const w of db.workloads) {
    const e = evaluate(st, w)
    w.modes = e.modes
    w.policy = e.policy
  }
}

/** Merged effective spec (for the defaults "inherited" hints). */
export const mergeAll = (specs: (Spec | undefined)[]) => specs.reduce<Spec>((a, s) => mergeSpec(a, s), {})

// ---------------------------------------------------------------------------------------------
// Seed
// ---------------------------------------------------------------------------------------------

const PASSWORD = 'dev-password'

/** Schema `object` fields are generated as `Record<string, never>`. */
export const obj = (x: unknown) => x as Record<string, never>

export function seedAdmin(db: MockDb): AdminState {
  const now = db.now
  const r = mulberry32(hash('admin-seed'))
  const t = (days: number, hours = 0) => iso(now - days * 86400_000 - hours * 3600_000)
  const alice = { type: 'user', name: 'alice', role: 'admin' } as Actor
  const bob = { type: 'user', name: 'bob', role: 'operator' } as Actor
  const admin = { type: 'user', name: 'admin', role: 'admin' } as Actor

  const st: AdminState = {
    users: new Map(),
    teams: [],
    ownerLabel: 'team',
    tokens: [],
    nextTokenId: 1,
    policies: [],
    versions: new Map(),
    gitPolicies: [],
    recommender: { version: 0, value: { interval: '1h', retention: '2160h', profiles: [] } },
    pricing: { version: 0, value: { clusters: [] } },
    pricingRates: { clusters: [] },
    settingsVersions: [],
    clusters: new Map(),
    fleet: { paused: false, version: 0 },
    datasources: [],
    readiness: new Map(),
    approvals: [],
    audit: [],
    annotations: new Map(),
    rng: r,
    syncDelayMs: 5000,
    maintenance: seedMaintenance(db),
    notifications: seedNotifications(now),
  }
  const empty = db.clusters.length === 0

  // Users ------------------------------------------------------------------------------------
  const user = (u: Partial<UserRec> & Pick<User, 'username' | 'role'>, createdDaysAgo: number) => {
    const rec: UserRec = {
      displayName: u.username[0]!.toUpperCase() + u.username.slice(1),
      disabled: false,
      mustChangePassword: false,
      createdAt: t(createdDaysAgo),
      version: 1,
      password: PASSWORD,
      failedLogins: 0,
      ...u,
    }
    st.users.set(rec.username, rec)
  }
  user({ username: 'admin', role: 'admin', displayName: 'Bootstrap admin', lastLoginAt: t(0, 2) }, 210)
  user(
    {
      username: 'alice',
      role: 'admin',
      displayName: 'Alice Cohen',
      createdBy: 'admin',
      lastLoginAt: t(0, 5),
    },
    200,
  )
  user({ username: 'operator', role: 'operator', displayName: 'Dev operator', createdBy: 'admin' }, 210)
  user({ username: 'viewer', role: 'viewer', displayName: 'Dev viewer', createdBy: 'admin' }, 210)
  user(
    { username: 'bob', role: 'operator', displayName: 'Bob Levi', createdBy: 'admin', lastLoginAt: t(0, 7) },
    8,
  )
  user(
    {
      username: 'dana',
      role: 'operator',
      displayName: 'Dana Mizrahi',
      createdBy: 'admin',
      disabled: true,
      version: 2,
      updatedAt: t(3),
    },
    8,
  )
  user(
    {
      username: 'sam',
      role: 'viewer',
      displayName: 'Sam Peretz',
      createdBy: 'admin',
      mustChangePassword: true,
    },
    1,
  )

  // Team-scoped and mixed users (docs/teams.md). `oidc` users get their access from IdP groups.
  user(
    {
      username: 'paula',
      role: 'operator',
      displayName: 'Payments lead',
      createdBy: 'admin',
      scope: 'teams',
      teamRoles: [{ team: 'payments', role: 'operator' }],
    },
    30,
  )
  user(
    {
      username: 'victor',
      role: 'viewer',
      displayName: 'Fleet viewer, risk operator',
      createdBy: 'admin',
      scope: 'fleet',
      teamRoles: [{ team: 'risk', role: 'operator' }],
    },
    30,
  )
  user(
    {
      username: 'idp-cards',
      role: 'viewer',
      displayName: 'Cards (identity provider)',
      source: 'oidc',
      scope: 'teams',
      teamRoles: [{ team: 'cards', role: 'viewer' }],
    },
    14,
  )

  // Teams: a few namespaces each; accounts, lending, observability and platform stay unassigned.
  const team = (rec: Partial<TeamRec> & Pick<TeamRec, 'id'>, createdDaysAgo: number) =>
    st.teams.push({
      displayName: rec.id,
      namespaces: [],
      namespaceLabels: {},
      clusters: [],
      ownerValues: [],
      version: 1,
      createdAt: t(createdDaysAgo),
      createdBy: 'admin',
      ...rec,
    })
  team(
    {
      id: 'cards',
      displayName: 'Cards',
      description: 'Card issuing and transfers',
      namespaces: ['cards', 'transfers'],
    },
    60,
  )
  team(
    {
      id: 'payments',
      displayName: 'Payments',
      description: 'Checkout, processing and settlement',
      namespaces: ['payments'],
      ownerValues: ['payments-squad'],
    },
    60,
  )
  team(
    { id: 'risk', displayName: 'Risk and ledger', namespaces: ['risk', 'ledger'], clusters: ['prod-*'] },
    45,
  )

  // Datasources ------------------------------------------------------------------------------
  st.datasources.push({
    id: 'local',
    name: 'local',
    type: 'victoriametrics',
    url: 'http://victoria-metrics.kerf.svc:8428',
    builtIn: true,
    auth: { type: 'none', secretConfigured: false },
    headers: {},
    timeout: '30s',
    maxPoints: 11000,
    usedBy: [],
    version: 1,
    createdAt: t(210),
    createdBy: SYSTEM,
    updatedAt: t(210),
    updatedBy: SYSTEM,
    knownLabelValues: ['prod-eu-west-1', 'prod-us-east-1', 'staging-eu-west-1'],
  })
  const ampId = uuid(r)
  if (!empty)
    st.datasources.push({
      id: ampId,
      name: 'amp-eu1',
      type: 'amp',
      url: 'https://aps-workspaces.eu-central-1.amazonaws.com/workspaces/ws-4f1c2a9e-7d3b-4b8e-9a61-2c5d8e0f1a37/',
      builtIn: false,
      auth: {
        type: 'sigv4',
        sigv4: {
          region: 'eu-central-1',
          service: 'aps',
          roleARN: 'arn:aws:iam::123456789012:role/ro-hub-amp-reader',
        },
        secretConfigured: false,
      },
      headers: {},
      timeout: '30s',
      maxPoints: 11000,
      usedBy: [],
      version: 1,
      createdAt: t(14),
      createdBy: alice,
      updatedAt: t(14),
      updatedBy: alice,
      knownLabelValues: ['dr-eu-central-1', 'eks-prod-eu1'],
    })

  // Clusters ---------------------------------------------------------------------------------
  const meta: Record<string, Partial<ClusterMeta>> = {
    'prod-eu-west-1': { environment: 'prod', owner: 'platform', policySource: 'both', region: 'eu-west-1' },
    'prod-us-east-1': {
      environment: 'prod',
      owner: 'payments-sre',
      policySource: 'both',
      region: 'us-east-1',
      version: 2,
      updatedAt: t(0, 20),
      updatedBy: alice,
      automation: {
        paused: false,
        reason: 'INC-4711 resolved',
        by: alice,
        since: t(7, -5),
        version: 2,
      },
    },
    'dr-eu-central-1': {
      environment: 'non-prod',
      owner: 'sre',
      policySource: 'git',
      region: 'eu-central-1',
      version: 2,
      updatedAt: t(14),
      updatedBy: alice,
      metrics: defaultBinding('dr-eu-central-1', ampId),
    },
  }
  for (const c of db.clusters) {
    const spec = CLUSTERS.find((s) => s.name === c.name)
    const m = meta[c.name] ?? {}
    st.clusters.set(c.name, {
      name: c.name,
      description: c.description ?? '',
      environment: 'non-prod',
      owner: '',
      policySource: 'both',
      provider: 'aws',
      metrics: defaultBinding(c.name),
      createdAt: c.createdAt ?? t(210),
      version: 1,
      automation: { paused: false, version: 0 },
      configVersion: 40 + (hash(c.name) % 20),
      configChangedAt: now - 3600_000,
      agentConnected: spec?.status === 'connected',
      agentVersion: c.agentVersion,
      ...m,
    })
    const cm = st.clusters.get(c.name)!
    cm.appliedConfigVersion = cm.agentConnected ? cm.configVersion : undefined
    const workloads = c.counts.workloads
    st.readiness.set(
      c.name,
      buildReadiness(
        c.name,
        c.name === 'prod-eu-west-1'
          ? 'ready'
          : c.name === 'prod-us-east-1'
            ? 'audit'
            : cm.agentConnected
              ? 'ready'
              : 'registered',
        { now, createdAt: Date.parse(cm.createdAt), workloads },
      ),
    )
  }

  // Tokens -----------------------------------------------------------------------------------
  const tok = (x: Omit<TokenInfo, 'id'>) => {
    st.tokens.push({ id: String(st.nextTokenId++), ...x })
  }
  if (!empty)
    tok({
      role: 'agent',
      cluster: 'prod-eu-west-1',
      description: 'agent prod-eu-west-1',
      createdAt: t(210),
      lastUsedAt: t(0, 0.01),
    })
  tok({ role: 'ui', description: 'web nginx', createdAt: t(210), lastUsedAt: t(0, 0.02) })
  if (!empty) {
    tok({
      role: 'agent',
      cluster: 'prod-us-east-1',
      description: 'agent prod-us-east-1',
      createdAt: t(205),
      lastUsedAt: t(0, 0.01),
    })
    tok({ role: 'pricing', description: 'pricing service', createdAt: t(1), lastUsedAt: t(0, 0.05) })
    tok({
      role: 'admin',
      description: 'ci recompute',
      createdAt: t(60),
      createdBy: 'admin',
      expiresAt: t(-60),
      lastUsedAt: t(0, 2),
    })
    tok({
      role: 'ui',
      description: 'grafana link',
      createdAt: t(9),
      createdBy: 'admin',
      expiresAt: t(-5),
      lastUsedAt: t(0, 1),
    })
    tok({
      role: 'admin',
      description: 'old ci',
      createdAt: t(90),
      createdBy: 'admin',
      lastUsedAt: t(3, 1),
      revokedAt: t(3),
    })
    tok({
      role: 'agent',
      cluster: 'dr-eu-central-1',
      description: 'agent dr-eu-central-1',
      createdAt: t(0, 3),
      createdBy: 'admin',
    })
  }

  // Settings ---------------------------------------------------------------------------------
  const profiles = (prodWindow: string): S['RecommenderProfileSettings'][] => [
    {
      name: 'dev',
      window: '168h',
      cpuPercentile: 0.9,
      memoryPercentile: 0.95,
      cpuHeadroom: 0.1,
      memoryBuffer: 0.1,
    },
    {
      name: 'production',
      window: prodWindow,
      cpuPercentile: 0.95,
      memoryPercentile: 0.99,
      cpuHeadroom: 0.15,
      memoryBuffer: 0.15,
      memoryPeakWindow: '24h',
    },
    {
      name: 'high-availability',
      window: '672h',
      cpuPercentile: 0.99,
      memoryPercentile: 0.99,
      cpuHeadroom: 0.3,
      memoryBuffer: 0.25,
    },
  ]
  const rec1 = { interval: '1h', retention: '2160h', profiles: profiles('168h') }
  const rec2 = { interval: '1h', retention: '2160h', profiles: profiles('336h') }
  st.recommender = {
    version: 2,
    updatedAt: t(6),
    updatedBy: alice,
    reason: 'longer prod window',
    value: rec2,
  }
  const pricing1: S['PricingSettings'] = {
    defaultDiscountPercent: 0,
    clusters: empty
      ? []
      : [
          { cluster: 'prod-eu-west-1', discountPercent: 28 },
          { cluster: 'prod-us-east-1', cpuCoreHourUSD: 0.0198, memoryGiBHourUSD: 0.00265 },
        ],
  }
  st.pricing = { version: 1, updatedAt: t(10), updatedBy: admin, reason: 'EDP 2027', value: pricing1 }
  // The pricing service reads the Cost and Usage Report: most nodes at their effective rate.
  const curSources: Record<string, S['PricingRateSource'][]> = {
    'prod-eu-west-1': [
      { source: 'cur', nodes: 14 },
      { source: 'spot', nodes: 4 },
    ],
    'prod-us-east-1': [
      { source: 'cur', nodes: 9 },
      { source: 'list', nodes: 2 },
    ],
  }
  st.pricingRates = empty
    ? { clusters: [] }
    : {
        cur: { lastReadOK: true, lastSuccess: t(0, 7), rates: 11, period: { start: t(31), end: t(1) } },
        clusters: db.clusters.map((c) => ({
          cluster: c.name,
          sources: curSources[c.name] ?? [{ source: 'list', nodes: 3 }],
        })),
      }
  st.settingsVersions.push(
    {
      section: 'recommender',
      version: 2,
      value: obj(rec2),
      createdAt: t(6),
      actor: alice,
      reason: 'longer prod window',
    },
    {
      section: 'pricing',
      version: 1,
      value: obj(pricing1),
      createdAt: t(10),
      actor: admin,
      reason: 'EDP 2027',
    },
    {
      section: 'recommender',
      version: 1,
      value: obj(rec1),
      createdAt: t(13),
      actor: alice,
      reason: 'retention 90 days for FinOps reports',
    },
  )
  audit(st, {
    time: t(13),
    actor: alice,
    action: 'SettingsUpdated',
    target: { type: 'settings', id: 'recommender' },
    reason: 'retention 90 days for FinOps reports',
    version: 1,
    after: rec1,
  })
  audit(st, {
    time: t(10),
    actor: admin,
    action: 'SettingsUpdated',
    target: { type: 'settings', id: 'pricing' },
    reason: 'EDP 2027',
    version: 1,
    after: pricing1,
  })
  audit(st, {
    time: t(6),
    actor: alice,
    action: 'SettingsUpdated',
    target: { type: 'settings', id: 'recommender' },
    reason: 'longer prod window',
    version: 2,
    before: rec1,
    after: rec2,
  })

  if (empty) {
    sortAudit(st)
    return st
  }

  // Annotations (from the generated modes) -----------------------------------------------------
  for (const w of db.workloads) {
    const a: { cpu?: Mode; memory?: Mode; profile?: S['Profile'] } = {}
    if (w.modes.cpuSource === 'annotation') a.cpu = w.modes.cpu
    if (w.modes.memorySource === 'annotation') a.memory = w.modes.memory
    if (Object.keys(a).length) st.annotations.set(w.id, a)
  }

  // Policies ---------------------------------------------------------------------------------
  const mk = (
    type: HubPolicyType,
    name: string,
    clusters: string[],
    spec: Spec,
    at: string,
    by: Actor,
    reason: string,
    extra: Partial<AdminPolicy> = {},
  ): AdminPolicy => {
    const p: AdminPolicy = {
      id: uuid(r),
      type,
      name,
      priority: spec.priority ?? 0,
      clusters,
      spec,
      version: 1,
      createdAt: at,
      createdBy: by,
      updatedAt: at,
      updatedBy: by,
      ...extra,
    }
    st.policies.push(p)
    recordVersion(st, p, 'create', reason, by, at)
    audit(st, {
      time: at,
      actor: by,
      action: type === 'workload-override' ? 'OverrideSet' : 'PolicyCreated',
      target: policyTarget(p),
      reason,
      version: 1,
      after: p,
    })
    return p
  }
  const update = (p: AdminPolicy, patch: Partial<AdminPolicy>, at: string, by: Actor, reason: string) => {
    const before = structuredClone(p)
    Object.assign(p, patch, { version: p.version + 1, updatedAt: at, updatedBy: by })
    p.priority = p.spec.priority ?? p.priority
    recordVersion(st, p, 'update', reason, by, at)
    audit(st, {
      time: at,
      actor: by,
      action: 'PolicyUpdated',
      target: policyTarget(p),
      reason,
      version: p.version,
      before,
      after: structuredClone(p),
    })
  }

  const global = mk(
    'global-default',
    'ro-global-defaults',
    [],
    {
      priority: 0,
      cpu: { mode: 'audit' },
      memory: { mode: 'audit' },
      guardrails: { minConfidence: '0.7', maxChangePercent: 50, learningPeriod: '168h', cooldown: '24h' },
    },
    t(13),
    admin,
    'initial global defaults',
  )
  mk(
    'cluster-default',
    'ro-cluster-defaults',
    ['prod-eu-west-1'],
    {
      priority: 10,
      cpu: { mode: 'audit' },
      memory: { mode: 'audit' },
      apply: { strategy: 'in-place', fallbackAfter: '15m' },
    },
    t(13, -1),
    admin,
    'prod-eu-west-1: longer in-place fallback',
  )
  const nsPolicies = new Map<string, string[]>()
  for (const [ns, p] of Object.entries(POLICIES))
    nsPolicies.set(p.name, [...(nsPolicies.get(p.name) ?? []), ns])
  const pol = (name: string) => POLICIES[nsPolicies.get(name)![0]!]!
  const clusterNames = (idx: number[]) => idx.map((i) => CLUSTERS[i]!.name)

  // Git policies (reported by the agents, read-only in the UI).
  st.gitPolicies.push(
    {
      name: 'platform-auto',
      priority: 50,
      clusters: clusterNames(pol('platform-auto').clusters),
      generation: 3,
      spec: {
        priority: 50,
        selector: { namespaces: nsPolicies.get('platform-auto')! },
        cpu: { mode: 'auto-patch', limits: 'remove' },
        memory: { mode: 'auto-patch', limitEqualsRequest: true },
        apply: { strategy: 'in-place' },
      },
    },
    {
      name: 'ledger-hands-off',
      priority: 300,
      clusters: clusterNames(pol('ledger-hands-off').clusters),
      generation: 1,
      spec: {
        priority: 300,
        selector: { namespaces: nsPolicies.get('ledger-hands-off')! },
        cpu: { mode: 'off' },
        memory: { mode: 'off' },
      },
    },
  )

  // Legacy policy: created then deleted (soft delete keeps its versions).
  const legacy = mk(
    'policy',
    'legacy-cart-auto',
    ['prod-eu-west-1'],
    { priority: 120, selector: { namespaces: ['cart*'] }, cpu: { mode: 'auto-patch' } },
    t(13, -2),
    alice,
    'carry over from the old cart autoscaler',
    { description: 'Old cart namespace automation' },
  )
  {
    const before = structuredClone(legacy)
    Object.assign(legacy, { version: 2, deletedAt: t(4), updatedAt: t(4), updatedBy: alice })
    recordVersion(st, legacy, 'delete', 'replaced by stateless-auto', alice, t(4))
    audit(st, {
      time: t(4),
      actor: alice,
      action: 'PolicyDeleted',
      target: policyTarget(legacy),
      reason: 'replaced by stateless-auto',
      version: 2,
      before,
      after: structuredClone(legacy),
    })
  }

  const payments = mk(
    'policy',
    'payments-autopilot',
    clusterNames(pol('payments-autopilot').clusters),
    {
      priority: 200,
      selector: { namespaces: ['payments'] },
      cpu: { mode: 'audit' },
      memory: { mode: 'audit' },
      apply: { strategy: 'in-place' },
    },
    t(12),
    alice,
    'payments: start in audit',
    { description: 'Payments: CPU auto-patch, memory audit' },
  )
  mk(
    'policy',
    'stateless-auto',
    [],
    {
      priority: 100,
      selector: { namespaces: nsPolicies.get('stateless-auto')! },
      cpu: { mode: 'auto-patch', limits: 'ratio', limitRatio: '2' },
      memory: { mode: 'auto-patch' },
      apply: { strategy: 'in-place' },
    },
    t(12, -1),
    alice,
    'stateless services: full automation',
    { description: 'Stateless edge services (notifications, gateway)' },
  )
  mk(
    'policy',
    'kyc-cpu-only',
    clusterNames(pol('kyc-cpu-only').clusters),
    {
      priority: 150,
      selector: { namespaces: ['kyc'] },
      cpu: { mode: 'auto-patch' },
      memory: { mode: 'off' },
      profile: 'high-availability',
    },
    t(11),
    alice,
    'KYC: memory stays hands-off (JVM heaps)',
    { description: 'KYC: CPU only' },
  )
  update(
    payments,
    {
      spec: { ...payments.spec, cpu: { mode: 'auto-patch' }, guardrails: { maxChangePercent: 30 } },
    },
    t(10),
    alice,
    'FinOps Q4 rollout: CPU auto-patch',
  )
  mk(
    'policy',
    'batch-jobs-rollout',
    [],
    {
      priority: 50,
      selector: { kinds: ['CronJob'], excludeNamespaces: ['kube-*'] },
      apply: { strategy: 'rollout' },
    },
    t(6, -2),
    alice,
    'CronJobs cannot resize in place',
    { description: 'CronJobs: apply on the next run (rollout)' },
  )
  update(
    payments,
    {
      spec: {
        ...payments.spec,
        guardrails: { maxChangePercent: 30, minConfidence: '0.85', cooldown: '48h' },
      },
      description: 'Payments: CPU auto-patch, memory audit, stricter guardrails',
    },
    t(5),
    alice,
    'raise min confidence after the Nov 1 rollback',
  )
  update(
    global,
    { spec: { ...global.spec, guardrails: { ...global.spec.guardrails, minConfidence: '0.8' } } },
    t(1),
    alice,
    'min confidence 0.8 fleet-wide',
  )

  // Overrides (the hub's override names; annotations still win).
  const free = (w: WorkloadModel) => !st.annotations.has(w.id)
  const prod1 = db.workloads.filter((w) => w.cluster === 'prod-eu-west-1' && free(w))
  const prod2 = db.workloads.filter((w) => w.cluster === 'prod-us-east-1' && free(w))
  const ovSpec = (w: WorkloadModel, s: Spec): Spec => ({
    priority: 900000,
    selector: {
      namespaces: [w.namespace],
      kinds: [w.kind],
      workloads: [{ kind: w.kind, namespace: w.namespace, name: w.name }],
    },
    ...s,
  })
  const override = (w: WorkloadModel | undefined, s: Spec, at: string, by: Actor, reason: string) =>
    w &&
    mk(
      'workload-override',
      overridePolicyName(w.namespace, w.kind, w.name),
      [w.cluster],
      ovSpec(w, s),
      at,
      by,
      reason,
      {
        workloadId: w.id,
        priority: 900000,
      },
    )
  override(
    prod1.find((w) => w.kind === 'CronJob' && w.namespace !== 'payments'),
    { cpu: { mode: 'off' }, memory: { mode: 'off' } },
    t(7),
    bob,
    'Exclude: batch job, owner asked',
  )
  override(
    prod1.find((w) => w.namespace === 'payments' && w.name === 'checkout-api') ??
      prod1.find((w) => w.namespace === 'payments'),
    { memory: { mode: 'auto-patch' } },
    t(5, -1),
    alice,
    'checkout-api: memory stable for 60 days, automate it',
  )
  override(
    prod2.find((w) => w.namespace === 'cards' && w.kind === 'Deployment'),
    { profile: 'high-availability', apply: { strategy: 'rollout' } },
    t(3),
    bob,
    'card authorisation peaks at month end',
  )

  // Automation history, users, tokens, clusters, datasources, readiness --------------------------
  audit(st, {
    time: t(14),
    actor: alice,
    action: 'DatasourceCreated',
    target: { type: 'datasource', id: ampId, name: 'amp-eu1' },
    reason: 'DR metrics live in AMP',
    version: 1,
  })
  audit(st, {
    time: t(14, -0.5),
    actor: alice,
    action: 'ClusterUpdated',
    target: { type: 'cluster', id: 'dr-eu-central-1', cluster: 'dr-eu-central-1' },
    reason: 'bind DR to amp-eu1',
    version: 2,
    before: { metrics: defaultBinding('dr-eu-central-1') },
    after: { metrics: defaultBinding('dr-eu-central-1', ampId) },
  })
  audit(st, {
    time: t(9),
    actor: admin,
    action: 'TokenCreated',
    target: { type: 'token', id: '5', name: 'grafana link' },
    reason: 'read-only link for the FinOps Grafana',
  })
  audit(st, {
    time: t(8),
    actor: admin,
    action: 'UserCreated',
    target: { type: 'user', id: 'bob' },
    reason: 'payments SRE on-call',
    version: 1,
  })
  audit(st, {
    time: t(8, -0.2),
    actor: admin,
    action: 'UserCreated',
    target: { type: 'user', id: 'dana' },
    reason: 'payments SRE on-call',
    version: 1,
  })
  audit(st, {
    time: t(7, 0),
    actor: bob,
    action: 'AutomationPaused',
    target: { type: 'automation', id: 'prod-us-east-1', cluster: 'prod-us-east-1' },
    reason: 'incident INC-4711',
    version: 1,
    before: { paused: false },
    after: { paused: true },
  })
  audit(st, {
    time: t(7, -5),
    actor: alice,
    action: 'AutomationResumed',
    target: { type: 'automation', id: 'prod-us-east-1', cluster: 'prod-us-east-1' },
    reason: 'INC-4711 resolved',
    version: 2,
    before: { paused: true },
    after: { paused: false },
  })
  audit(st, {
    time: t(3, 1),
    actor: admin,
    action: 'TokenRevoked',
    target: { type: 'token', id: '6', name: 'old ci' },
    reason: 'leaked in CI logs',
  })
  audit(st, {
    time: t(3),
    actor: admin,
    action: 'UserUpdated',
    target: { type: 'user', id: 'dana' },
    reason: 'left the team',
    version: 2,
    before: { disabled: false },
    after: { disabled: true },
  })
  audit(st, {
    time: t(2, 3),
    actor: SYSTEM,
    action: 'ReadinessChanged',
    target: { type: 'readiness', id: 'prod-us-east-1', cluster: 'prod-us-east-1' },
    reason: 'rbac.resize, rbac.policies failing',
    before: { state: 'AutomationReady' },
    after: { state: 'ReadyAudit' },
  })
  audit(st, {
    time: t(1),
    actor: admin,
    action: 'UserCreated',
    target: { type: 'user', id: 'sam' },
    reason: 'FinOps analyst',
    version: 1,
  })
  audit(st, {
    time: t(0, 20),
    actor: alice,
    action: 'ClusterUpdated',
    target: { type: 'cluster', id: 'prod-us-east-1', cluster: 'prod-us-east-1' },
    reason: 'ownership moved to payments SRE',
    version: 2,
    before: { owner: 'payments' },
    after: { owner: 'payments-sre' },
  })
  audit(st, {
    time: t(0, 3),
    actor: admin,
    action: 'TokenCreated',
    target: { type: 'token', id: '7', name: 'agent dr-eu-central-1', cluster: 'dr-eu-central-1' },
    reason: 'rotate DR agent token',
  })

  // Approvals: one consumed (4 days ago), one pending.
  const approvable = prod1.filter(
    (w) => w.recommendation && w.modes.cpu === 'audit' && w.modes.memory === 'audit',
  )
  const approve = (w: WorkloadModel | undefined, daysAgo: number, consumed: boolean) => {
    if (!w?.recommendation) return
    const at = now - daysAgo * 86400_000
    const a: ApprovalRec = {
      id: uuid(r),
      workloadId: w.id,
      recommendationID: w.recommendation.id,
      approvedBy: bob,
      approvedAt: iso(at),
      expiresAt: iso(at + 24 * 3600_000),
      reason: consumed ? 'reviewed with the owning team' : 'owner confirmed the new requests',
      state: consumed ? 'consumed' : 'pending',
      ...(consumed ? { consumedAt: iso(at + 40 * 60_000), consumedEventId: uuid(r) } : {}),
      autoConsume: false,
    }
    st.approvals.push(a)
    audit(st, {
      time: iso(at),
      actor: bob,
      action: 'WorkloadApproved',
      target: { type: 'approval', id: a.id, cluster: w.cluster, workloadId: w.id, name: w.name },
      reason: a.reason,
      after: stripApproval(a),
    })
  }
  approve(approvable[approvable.length - 1], 4, true)
  approve(approvable[approvable.length - 2], 0.25, false)

  recompute(st, db)
  sortAudit(st)
  return st
}

/**
 * The mock fleet runs 0.4.2 (hub, pricing, prod-eu-west-1's agent and webhook); prod-us-east-1's agent
 * is one patch behind and it has no webhook; 0.5.0 is out. A fresh (empty) hub has not checked yet.
 */
export function seedMaintenance(db: MockDb): MaintenanceModel {
  const now = db.now
  const empty = db.clusters.length === 0
  const release = {
    version: '0.5.0',
    name: 'Kerf 0.5.0',
    url: 'https://github.com/kerf-dev/kerf/releases/tag/v0.5.0',
    publishedAt: iso(now - 3 * 86400_000),
  }
  const webhooks = new Map<string, { instance: string; version: string }[]>()
  if (db.clusters.some((c) => c.name === 'prod-eu-west-1'))
    webhooks.set('prod-eu-west-1', [
      { instance: 'kerf-webhook-6c9f7d8b54-2xkqp', version: '0.4.2' },
      { instance: 'kerf-webhook-6c9f7d8b54-tq7wn', version: '0.4.2' },
    ])
  const checkedAt = iso(now - 2 * 3600_000 - 17 * 60_000)
  return {
    hubVersion: '0.4.2',
    chartVersion: '0.4.2',
    hubReplicas: [
      { instance: 'hub-7d5c9b6f4d-8kz2m', startedAt: iso(now - 6 * 86400_000) },
      { instance: 'hub-7d5c9b6f4d-r4hvx', startedAt: iso(now - 6 * 86400_000 + 95_000) },
    ],
    pricing: [{ instance: 'pricing-5f8b7c9d6-lm2zq', version: '0.4.2' }],
    webhooks,
    release,
    update: empty
      ? {
          enabled: true,
          state: 'pending',
          currentVersion: '0.4.2',
          interval: '24h',
          source: 'https://api.github.com/repos/kerf-dev/kerf/releases',
        }
      : {
          enabled: true,
          state: 'update_available',
          currentVersion: '0.4.2',
          latestVersion: release.version,
          releaseName: release.name,
          releaseURL: release.url,
          publishedAt: release.publishedAt,
          checkedAt,
          lastSuccessAt: checkedAt,
          interval: '24h',
          source: 'https://api.github.com/repos/kerf-dev/kerf/releases',
        },
  }
}

export function stripApproval(a: ApprovalRec): Approval {
  const out: Approval & { autoConsume?: boolean } = { ...a }
  delete out.autoConsume
  return out
}

export function policyTarget(p: AdminPolicy): S['ConfigTarget'] {
  return {
    type: p.type === 'workload-override' ? 'workload-override' : 'policy',
    id: p.id,
    name: p.name,
    ...(p.type === 'cluster-default' || p.type === 'workload-override' ? { cluster: p.clusters[0] } : {}),
    ...(p.workloadId ? { workloadId: p.workloadId } : {}),
  }
}

export function recordVersion(
  st: AdminState,
  p: AdminPolicy,
  operation: PolicyVersion['operation'],
  reason: string,
  actor: Actor,
  at: string,
  revertedFrom?: number,
) {
  const v: PolicyVersion = {
    version: p.version,
    operation,
    ...(revertedFrom ? { revertedFrom } : {}),
    name: p.name,
    ...(p.description ? { description: p.description } : {}),
    priority: p.priority,
    clusters: [...p.clusters],
    spec: structuredClone(p.spec),
    deleted: !!p.deletedAt,
    createdAt: at,
    actor,
    reason,
  }
  st.versions.set(p.id, [v, ...(st.versions.get(p.id) ?? [])])
}

let auditSeq = 0
type AuditInput = Omit<ConfigChange, 'id' | 'type' | 'before' | 'after'> & {
  before?: unknown
  after?: unknown
}
/** Appends a config change; `before`/`after` are API representations (typed `object | null`). */
export function audit(st: AdminState, c: AuditInput) {
  const r = mulberry32(hash(`audit-${auditSeq++}-${c.time}-${c.action}`))
  st.audit.push({ id: uuid(r), type: 'config_changed', ...(c as Omit<ConfigChange, 'id' | 'type'>) })
}
export function sortAudit(st: AdminState) {
  st.audit.sort((a, b) => a.time.localeCompare(b.time))
}
