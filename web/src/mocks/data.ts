/**
 * Seeded mock fleet for the MSW API (`make web-dev`). Shapes come from the generated OpenAPI types, so
 * TypeScript enforces the contract, and `handlers.test.ts` validates every response against
 * `api/openapi/hub.yaml` with a JSON Schema validator.
 *
 * The fleet: two production EKS clusters with ~200 workloads each across fintech namespaces, plus a
 * DR cluster whose agent never connected (to exercise empty states).
 */
import type {
  AuditEvent,
  ClusterInfo,
  ContainerRecommendation,
  EffectiveModes,
  EventType,
  Mode,
  ModeSource,
  Phase,
  Profile,
  Resources,
  RiskFlag,
  Strategy,
  Summary,
  ValueSettings,
  WorkloadDetail,
  WorkloadKind,
  WorkloadRecommendation,
  WorkloadRow,
  WorkloadStatus,
} from '@/api/types'
import { between, hash, mulberry32, pick, uuid, weighted, type Rng } from './random'

const Mi = 1024 ** 2
const Gi = 1024 ** 3
const HOURS_PER_MONTH = 730

export const cpuQ = (cores: number) => {
  const m = Math.max(1, Math.round(cores * 1000))
  return m % 1000 === 0 ? `${m / 1000}` : `${m}m`
}
export const memQ = (bytes: number) => {
  const mi = Math.max(1, Math.round(bytes / Mi))
  return mi % 1024 === 0 ? `${mi / 1024}Gi` : `${mi}Mi`
}
const roundUp = (v: number, step: number) => Math.ceil(v / step) * step

interface ClusterSpec {
  name: string
  description: string
  cpuPrice: number // USD per core-hour
  memPrice: number // USD per GiB-hour
  workloads: number
  status: ClusterInfo['agentStatus']
  agentVersion?: string
  heartbeatAgoSec?: number
  defaultModeSource: ModeSource
  nodes: number
}

const CLUSTERS: ClusterSpec[] = [
  {
    name: 'prod-eu-west-1',
    description: 'Primary production · Ireland · m6i on-demand',
    cpuPrice: 0.0316,
    memPrice: 0.00424,
    workloads: 204,
    status: 'connected',
    agentVersion: 'v0.4.2',
    heartbeatAgoSec: 22,
    defaultModeSource: 'cluster-default',
    nodes: 24,
  },
  {
    name: 'prod-us-east-1',
    description: 'US production · N. Virginia · c6a spot + on-demand',
    cpuPrice: 0.0221,
    memPrice: 0.00297,
    workloads: 196,
    status: 'connected',
    agentVersion: 'v0.4.1',
    heartbeatAgoSec: 41,
    defaultModeSource: 'global-default',
    nodes: 18,
  },
  {
    name: 'dr-eu-central-1',
    description: 'Disaster recovery · Frankfurt · warm standby',
    cpuPrice: 0.0316,
    memPrice: 0.00424,
    workloads: 0,
    status: 'never',
    defaultModeSource: 'global-default',
    nodes: 3,
  },
]

type Tpl = readonly [name: string, kind?: WorkloadKind]
const NAMESPACES: Record<string, { jvm?: boolean; apps: readonly Tpl[] }> = {
  payments: {
    apps: [
      ['checkout-api'],
      ['payment-processor'],
      ['payment-router'],
      ['refund-worker'],
      ['settlement-batch', 'CronJob'],
      ['card-tokenizer'],
      ['threeds-service'],
      ['payment-webhooks'],
      ['reconciliation-job', 'CronJob'],
      ['payout-scheduler'],
    ],
  },
  cards: {
    jvm: true,
    apps: [
      ['card-issuing-api'],
      ['card-auth-engine'],
      ['card-lifecycle'],
      ['pin-service'],
      ['virtual-cards'],
      ['card-controls'],
      ['statement-generator', 'CronJob'],
    ],
  },
  ledger: {
    jvm: true,
    apps: [
      ['ledger-api'],
      ['ledger-writer', 'StatefulSet'],
      ['balance-projector'],
      ['journal-compactor', 'CronJob'],
      ['double-entry-validator'],
      ['ledger-archive', 'StatefulSet'],
    ],
  },
  risk: {
    apps: [
      ['risk-scoring'],
      ['rules-engine'],
      ['aml-screening'],
      ['sanctions-check'],
      ['transaction-monitor'],
      ['risk-model-server'],
    ],
  },
  'fraud-detection': {
    apps: [
      ['fraud-detector'],
      ['feature-store', 'StatefulSet'],
      ['device-fingerprint'],
      ['velocity-checker'],
    ],
  },
  kyc: { apps: [['kyc-api'], ['document-ocr'], ['identity-verifier'], ['liveness-check']] },
  accounts: {
    jvm: true,
    apps: [['accounts-api'], ['customer-profile'], ['account-opening'], ['statements-api']],
  },
  transfers: {
    apps: [['transfers-api'], ['sepa-gateway'], ['swift-adapter'], ['instant-payments'], ['fx-quotes']],
  },
  lending: {
    jvm: true,
    apps: [
      ['loan-origination'],
      ['credit-decision'],
      ['repayment-scheduler', 'CronJob'],
      ['collections-worker'],
    ],
  },
  notifications: { apps: [['notification-hub'], ['push-sender'], ['email-sender'], ['sms-gateway']] },
  auth: { apps: [['auth-server'], ['token-service'], ['mfa-service'], ['session-store', 'StatefulSet']] },
  gateway: { apps: [['api-gateway'], ['rate-limiter'], ['bff-mobile'], ['bff-web']] },
  'data-platform': {
    jvm: true,
    apps: [
      ['kafka-connect'],
      ['cdc-debezium'],
      ['stream-processor'],
      ['airflow-scheduler'],
      ['airflow-worker'],
      ['clickhouse', 'StatefulSet'],
    ],
  },
  observability: {
    apps: [
      ['node-exporter', 'DaemonSet'],
      ['fluent-bit', 'DaemonSet'],
      ['otel-collector'],
      ['vector-agent', 'DaemonSet'],
    ],
  },
  platform: {
    apps: [['external-dns'], ['cert-manager'], ['karpenter'], ['ingress-nginx'], ['kyverno']],
  },
}
const VARIANTS = [
  'worker',
  'consumer',
  'canary',
  'replay',
  'admin',
  'grpc',
  'sync',
  'exporter',
  'migrator',
  'cache',
  'reader',
  'scheduler',
]

/** Namespace → policy that sets the modes (per cluster index). */
const POLICIES: Record<string, { name: string; cpu: Mode; memory: Mode; clusters: number[] }> = {
  payments: { name: 'payments-autopilot', cpu: 'auto-patch', memory: 'audit', clusters: [0] },
  notifications: { name: 'stateless-auto', cpu: 'auto-patch', memory: 'auto-patch', clusters: [0, 1] },
  gateway: { name: 'stateless-auto', cpu: 'auto-patch', memory: 'auto-patch', clusters: [0] },
  observability: { name: 'platform-auto', cpu: 'auto-patch', memory: 'auto-patch', clusters: [0, 1] },
  platform: { name: 'platform-auto', cpu: 'auto-patch', memory: 'auto-patch', clusters: [0, 1] },
  ledger: { name: 'ledger-hands-off', cpu: 'off', memory: 'off', clusters: [0, 1] },
  kyc: { name: 'kyc-cpu-only', cpu: 'auto-patch', memory: 'off', clusters: [1] },
}

const PROFILE_MARGIN: Record<Profile, { cpu: number; mem: number }> = {
  dev: { cpu: 0.1, mem: 0.1 },
  production: { cpu: 0.15, mem: 0.2 },
  'high-availability': { cpu: 0.3, mem: 0.3 },
}

export interface ContainerModel {
  name: string
  init?: boolean
  sidecar?: boolean
  /** values before our last apply (equal to `current` when nothing was applied) */
  original: Resources
  current: Resources
  recommended: Resources
  removeLimits?: ('cpu' | 'memory')[]
  cpu: { p50: number; p95: number; p99: number; max: number }
  mem: { p50: number; p95: number; p99: number; max: number }
  riskFlags: RiskFlag[]
  savings: { cpu: number; memory: number; total: number }
  explanation: NonNullable<ContainerRecommendation['explanation']>
}

export interface WorkloadModel {
  id: string
  seed: number
  cluster: string
  clusterIndex: number
  namespace: string
  kind: WorkloadKind
  name: string
  replicas: number
  firstSeenAt: string
  modes: EffectiveModes
  policy?: string
  phase: Phase
  strategy: Strategy
  settings: ValueSettings
  profile: Profile
  reason?: string
  message?: string
  appliedAt?: string
  hasRecommendation: boolean
  containers: ContainerModel[]
  recommendation?: WorkloadRecommendation
  history: WorkloadRecommendation[]
  riskFlags: RiskFlag[]
  confidence: number
  savings: { cpu: number; memory: number; total: number }
  shortLived: boolean
}

export interface MockDb {
  now: number
  lastRunAt: string
  clusters: ClusterInfo[]
  clusterSpecs: ClusterSpec[]
  workloads: WorkloadModel[]
  byId: Map<string, WorkloadModel>
  events: AuditEvent[]
}

const iso = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')

function sumSavings(xs: { cpu: number; memory: number; total: number }[]) {
  const s = { cpu: 0, memory: 0, total: 0 }
  for (const x of xs) {
    s.cpu += x.cpu
    s.memory += x.memory
    s.total += x.total
  }
  return { cpu: round2(s.cpu), memory: round2(s.memory), total: round2(s.total) }
}
const round2 = (n: number) => Math.round(n * 100) / 100

function buildContainer(
  r: Rng,
  opts: {
    name: string
    kind: WorkloadKind
    jvm: boolean
    sidecar?: 'istio-proxy' | 'otel-agent'
    init?: boolean
    profile: Profile
    settings: ValueSettings
    throttled: boolean
    oom: boolean
    applied: boolean
    replicas: number
    spec: ClusterSpec
  },
): ContainerModel {
  const { profile, settings } = opts
  let reqCpu: number
  let reqMem: number
  if (opts.sidecar === 'istio-proxy') {
    reqCpu = pick(r, [0.1, 0.1, 0.2, 0.5])
    reqMem = pick(r, [128, 128, 256]) * Mi
  } else if (opts.sidecar === 'otel-agent') {
    reqCpu = pick(r, [0.05, 0.1])
    reqMem = pick(r, [64, 128]) * Mi
  } else if (opts.init) {
    reqCpu = pick(r, [0.1, 0.25])
    reqMem = pick(r, [64, 128, 256]) * Mi
  } else if (opts.kind === 'DaemonSet') {
    reqCpu = pick(r, [0.05, 0.1, 0.2])
    reqMem = pick(r, [64, 128, 256]) * Mi
  } else {
    reqCpu = weighted(r, [
      [0.1, 1],
      [0.25, 3],
      [0.5, 4],
      [1, 4],
      [2, 3],
      [4, 1],
    ] as const)
    reqMem = (opts.jvm ? pick(r, [1, 2, 2, 4, 4, 8]) : pick(r, [0.25, 0.5, 0.5, 1, 1, 2, 4])) * Gi
  }
  const hasCpuLimit = r() < 0.55
  const memLimitEq = r() < 0.6
  const original: Resources = {
    requests: { cpu: cpuQ(reqCpu), memory: memQ(reqMem) },
    limits: {
      ...(hasCpuLimit ? { cpu: cpuQ(reqCpu * pick(r, [1, 2, 2, 4])) } : {}),
      memory: memQ(memLimitEq ? reqMem : reqMem * 1.5),
    },
  }

  // Utilization relative to the request.
  const sidecarish = !!opts.sidecar || !!opts.init
  const cpuU =
    opts.throttled && !sidecarish
      ? between(r, 0.95, 1.4)
      : sidecarish
        ? between(r, 0.05, 0.3)
        : Math.pow(r(), 1.6) * 0.75 + 0.06
  let memU =
    opts.oom && !sidecarish
      ? between(r, 0.92, 1.0)
      : sidecarish
        ? between(r, 0.2, 0.5)
        : between(r, 0.28, 0.85)
  if (opts.jvm && !sidecarish) memU = Math.max(memU, between(r, 0.55, 0.9))
  const cpuP95 = reqCpu * cpuU
  const cpu = {
    p50: cpuP95 * between(r, 0.45, 0.7),
    p95: cpuP95,
    p99: cpuP95 * between(r, 1.08, 1.3),
    max: 0,
  }
  cpu.max = cpu.p99 * between(r, 1.1, 1.6)
  const memP95 = reqMem * memU
  const mem = {
    p50: memP95 * between(r, 0.82, 0.95),
    p95: memP95,
    p99: memP95 * between(r, 1.01, 1.06),
    max: 0,
  }
  mem.max = opts.oom ? reqMem * (memLimitEq ? 1 : 1.5) : mem.p99 * between(r, 1.0, 1.08)

  const margin = PROFILE_MARGIN[profile]
  const cpuStep = cpu.p95 < 0.1 ? 0.005 : cpu.p95 < 1 ? 0.01 : 0.05
  const memStep = mem.max < 256 * Mi ? 4 * Mi : mem.max < 2 * Gi ? 16 * Mi : 64 * Mi
  const cpuNotes: string[] = []
  const memNotes: string[] = []
  let recCpu = roundUp(cpu.p95 * (1 + margin.cpu), cpuStep)
  const memBase = Math.max(mem.p99, mem.max)
  let recMem = roundUp(memBase * (1 + margin.mem), memStep)
  if (opts.throttled && !sidecarish) {
    recCpu = Math.max(recCpu, roundUp(cpu.p99 * 1.25, cpuStep), reqCpu)
    cpuNotes.push('raised: throttled — never decreased while CFS throttling is above 5 %')
  }
  if (opts.oom && !sidecarish) {
    recMem = Math.max(recMem, roundUp(reqMem * 1.25, memStep))
    memNotes.push('raised: oom-killed — at least +25 % over the last limit')
  }
  const minCpu = 0.01
  const minMem = 32 * Mi
  if (recCpu < minCpu) {
    recCpu = minCpu
    cpuNotes.push('clamped to the 10m floor')
  }
  if (recMem < minMem) {
    recMem = minMem
    memNotes.push('clamped to the 32Mi floor')
  }
  if (opts.jvm && !sidecarish) memNotes.push('JVM heap detected: memory buffer applied to max(p99, peak)')

  const recommended: Resources = { requests: { cpu: cpuQ(recCpu), memory: memQ(recMem) }, limits: {} }
  let limitRule = 'keep'
  const removeLimits: ('cpu' | 'memory')[] = []
  if (settings.cpuLimits === 'remove') {
    limitRule = 'remove'
    if (hasCpuLimit) removeLimits.push('cpu')
  } else if (settings.cpuLimits === 'ratio') {
    limitRule = `ratio ${settings.cpuLimitRatio ?? '2'}`
    recommended.limits!.cpu = cpuQ(recCpu * Number(settings.cpuLimitRatio ?? '2'))
  } else if (hasCpuLimit) {
    recommended.limits!.cpu = original.limits!.cpu
  }
  const memLimitRule = settings.memoryLimitEqualsRequest ? 'equals request' : 'keep ratio'
  recommended.limits!.memory = memQ(settings.memoryLimitEqualsRequest ? recMem : recMem * 1.5)

  // After an apply, the live values are the previous recommendation, so the next one is a small tweak.
  const current: Resources = opts.applied
    ? {
        requests: {
          cpu: cpuQ(roundUp(recCpu * between(r, 1.0, 1.12), cpuStep)),
          memory: memQ(roundUp(recMem * between(r, 1.0, 1.08), memStep)),
        },
        limits: {
          ...(recommended.limits!.cpu ? { cpu: recommended.limits!.cpu } : {}),
          memory: memQ(
            roundUp(
              (settings.memoryLimitEqualsRequest ? recMem : recMem * 1.5) * between(r, 1.0, 1.08),
              memStep,
            ),
          ),
        },
      }
    : original

  const curCpu = parseCpu(current.requests!.cpu!)
  const curMem = parseMem(current.requests!.memory!)
  const sCpu = round2((curCpu - recCpu) * opts.spec.cpuPrice * opts.replicas * HOURS_PER_MONTH)
  const sMem = round2(((curMem - recMem) / Gi) * opts.spec.memPrice * opts.replicas * HOURS_PER_MONTH)
  const riskFlags: RiskFlag[] = []
  if (opts.sidecar) riskFlags.push('sidecar')
  if (opts.throttled && !sidecarish) riskFlags.push('throttled')
  if (opts.oom && !sidecarish) riskFlags.push('oom-killed')

  return {
    name: opts.name,
    ...(opts.init ? { init: true } : {}),
    sidecar: !!opts.sidecar,
    original,
    current,
    recommended,
    ...(removeLimits.length ? { removeLimits } : {}),
    cpu,
    mem,
    riskFlags,
    savings: { cpu: sCpu, memory: sMem, total: round2(sCpu + sMem) },
    explanation: {
      profile,
      strategyName: 'percentile',
      window: '336h0m0s',
      cpu: {
        statistic: 'p95',
        observed: { p50: cpuQ(cpu.p50), p95: cpuQ(cpu.p95), p99: cpuQ(cpu.p99), max: cpuQ(cpu.max) },
        base: cpuQ(cpu.p95),
        margin: margin.cpu,
        roundedTo: cpuQ(cpuStep),
        limitRule,
        notes: cpuNotes,
      },
      memory: {
        statistic: 'max(p99,peak)',
        observed: { p50: memQ(mem.p50), p95: memQ(mem.p95), p99: memQ(mem.p99), max: memQ(mem.max) },
        base: memQ(memBase),
        margin: margin.mem,
        roundedTo: memQ(memStep),
        limitRule: memLimitRule,
        notes: memNotes,
      },
    },
  }
}

function parseCpu(q: string) {
  return q.endsWith('m') ? Number(q.slice(0, -1)) / 1000 : Number(q)
}
function parseMem(q: string) {
  return q.endsWith('Gi') ? Number(q.slice(0, -2)) * Gi : Number(q.slice(0, -2)) * Mi
}

function toApiContainer(c: ContainerModel, withExplanation: boolean): ContainerRecommendation {
  return {
    name: c.name,
    ...(c.init ? { init: true } : {}),
    current: c.current,
    recommended: c.recommended,
    ...(c.removeLimits ? { removeLimits: c.removeLimits } : {}),
    riskFlags: c.riskFlags,
    monthlySavingsUSD: c.savings,
    ...(withExplanation ? { explanation: c.explanation } : {}),
  }
}

function scaleResources(res: Resources, f: number): Resources {
  const out: Resources = { requests: {}, limits: {} }
  for (const k of ['requests', 'limits'] as const) {
    const src = res[k] ?? {}
    if (src.cpu) out[k]!.cpu = cpuQ(parseCpu(src.cpu) * f)
    if (src.memory) out[k]!.memory = memQ(parseMem(src.memory) * f)
  }
  return out
}

function buildWorkload(
  spec: ClusterSpec,
  ci: number,
  ns: string,
  name: string,
  kind: WorkloadKind,
  now: number,
  lastRun: number,
): WorkloadModel {
  const id = `${spec.name}/${ns}/${kind}/${name}`
  const seed = hash(id)
  const r = mulberry32(seed)
  const nsInfo = NAMESPACES[ns]!
  const jvm = !!nsInfo.jvm && kind !== 'DaemonSet' && r() < 0.8

  const replicas =
    kind === 'DaemonSet'
      ? spec.nodes
      : kind === 'CronJob'
        ? round2(between(r, 0.1, 0.9))
        : kind === 'StatefulSet'
          ? pick(r, [3, 3, 5])
          : Math.round(
              weighted(r, [
                [2, 3],
                [3, 4],
                [4, 3],
                [6, 2],
                [8, 1],
                [12, 1],
              ] as const) * (ci === 1 ? 0.75 : 1),
            ) || 1

  // Modes (D-005 precedence: global < cluster < policy < annotation).
  const pol = POLICIES[ns]
  let modes: EffectiveModes = {
    cpu: 'audit',
    memory: 'audit',
    cpuSource: spec.defaultModeSource,
    memorySource: spec.defaultModeSource,
  }
  let policy: string | undefined
  if (pol && pol.clusters.includes(ci)) {
    modes = { cpu: pol.cpu, memory: pol.memory, cpuSource: 'policy', memorySource: 'policy' }
    policy = pol.name
  }
  if (r() < 0.06) {
    const m = pick(r, ['off', 'auto-patch', 'audit'] as const)
    modes = { ...modes, cpu: m, cpuSource: 'annotation' }
  }
  if (r() < 0.04) {
    const m = pick(r, ['off', 'auto-patch'] as const)
    modes = { ...modes, memory: m, memorySource: 'annotation' }
  }
  const auto = modes.cpu === 'auto-patch' || modes.memory === 'auto-patch'

  const hpa = kind === 'Deployment' && r() < 0.12
  const throttled = kind !== 'CronJob' && r() < 0.06
  const oom = r() < (jvm ? 0.07 : 0.03)
  const restarting = oom ? r() < 0.6 : r() < 0.02
  const shortLived = kind === 'CronJob'
  const ageDays = r() < 0.05 ? between(r, 0.2, 1.5) : between(r, 20, 400)
  const firstSeen = now - ageDays * 86400_000
  const hasRecommendation = ageDays > 0.5
  const profile: Profile = weighted(r, [
    ['production', 8],
    ['high-availability', ns === 'payments' || ns === 'auth' || ns === 'ledger' ? 4 : 1],
    ['dev', 0.5],
  ] as const)
  const settings: ValueSettings = {
    profile,
    cpuLimits: weighted(r, [
      ['keep', 5],
      ['remove', 3],
      ['ratio', 2],
    ] as const),
    memoryLimitEqualsRequest: r() < 0.7,
  }
  if (settings.cpuLimits === 'ratio') settings.cpuLimitRatio = pick(r, ['2', '1.5', '3'])

  let phase: Phase = 'Pending'
  let reason: string | undefined
  let message: string | undefined
  if (auto && hasRecommendation) {
    if (hpa) {
      phase = 'Blocked'
      reason = 'HPACoupled'
      message = 'CPU-utilization HPA targets this workload; recommendations stay audit-only (D-011).'
    } else {
      phase = weighted(r, [
        ['Applied', 70],
        ['Blocked', 14],
        ['RolledBack', 4],
        ['Applying', 4],
        ['Pending', 8],
      ] as const)
      if (phase === 'Blocked') {
        reason = throttled
          ? 'Throttled'
          : oom
            ? 'OOMKilled'
            : pick(r, [
                'LowConfidence',
                'Cooldown',
                'LearningPeriod',
                'FreezeWindow',
                'OutsideMaintenanceWindow',
              ])
        message = {
          Throttled: 'CPU decrease refused: throttling ratio 0.12 over the last hour.',
          OOMKilled: 'Memory decrease refused: 2 OOM kills in the last 24h.',
          LowConfidence: 'Confidence 0.58 is below guardrails.minConfidence 0.7.',
          Cooldown: 'Last change 3h ago; cooldown is 24h.',
          LearningPeriod: 'Data covers 5d of the required 7d learning period.',
          FreezeWindow: 'Inside freeze window "black-friday" (Nov 27 – Dec 2).',
          OutsideMaintenanceWindow: 'Strategy deferred; next maintenance window Sun 02:00 UTC.',
        }[reason]
      } else if (phase === 'RolledBack') {
        // The rollback reason has to match a resource the agent actually changed.
        reason =
          modes.memory === 'auto-patch'
            ? pick(r, ['OOMKills', 'Restarts'])
            : pick(r, ['Throttling', 'Restarts'])
        message =
          reason === 'OOMKills'
            ? '2 OOM kills within 30m after change (threshold 1); previous values restored.'
            : reason === 'Restarts'
              ? '4 container restarts within 30m after change (threshold 3).'
              : 'Throttling ratio 0.21 after change (threshold 0.1).'
      } else if (phase === 'Applied') {
        reason = 'RecommendationApplied'
      } else if (phase === 'Applying') {
        reason = 'RecommendationApplied'
        message = 'Resize in progress on 2 of 4 pods.'
      }
    }
  } else if (!auto && hasRecommendation && r() < 0.05) {
    phase = 'Released'
    reason = 'ModeChanged'
    message = 'Mode changed from auto-patch; field ownership released so Argo CD restores Git values.'
  }
  const applied = phase === 'Applied' || phase === 'Applying'
  const strategy: Strategy =
    kind === 'StatefulSet'
      ? 'rollout'
      : weighted(r, [
          ['in-place', 8],
          ['rollout', 2],
          ['deferred', 1],
        ] as const)

  const spec2 = { profile, settings, throttled, oom, applied, replicas, spec, kind, jvm }
  const containers: ContainerModel[] = []
  if (r() < 0.1 && kind !== 'DaemonSet') {
    containers.push(
      buildContainer(r, {
        ...spec2,
        name: pick(r, ['migrate', 'vault-agent-init', 'wait-for-db']),
        init: true,
        throttled: false,
        oom: false,
      }),
    )
  }
  containers.push(
    buildContainer(r, { ...spec2, name: r() < 0.5 ? 'app' : name.split('-').slice(0, 2).join('-') }),
  )
  const istio = !['observability', 'platform'].includes(ns) && kind !== 'CronJob' && r() < 0.4
  if (istio)
    containers.push(
      buildContainer(r, {
        ...spec2,
        name: 'istio-proxy',
        sidecar: 'istio-proxy',
        throttled: false,
        oom: false,
      }),
    )
  else if (r() < 0.12)
    containers.push(
      buildContainer(r, {
        ...spec2,
        name: 'otel-agent',
        sidecar: 'otel-agent',
        throttled: false,
        oom: false,
      }),
    )

  const coverage = ageDays >= 14 ? between(r, 0.88, 1) : Math.min(1, ageDays / 14) * between(r, 0.85, 1)
  let confidence = round2(
    Math.min(
      0.99,
      coverage * 0.75 + between(r, 0.12, 0.24) - (restarting ? 0.12 : 0) - (shortLived ? 0.1 : 0),
    ),
  )
  if (confidence < 0.1) confidence = 0.1
  const flags = new Set<RiskFlag>(containers.flatMap((c) => c.riskFlags))
  if (hpa) flags.add('hpa-coupled')
  if (restarting) flags.add('restarting')
  if (shortLived) flags.add('short-lived')
  if (confidence < 0.6) flags.add('low-confidence')
  const riskFlags = [...flags]
  const savings = sumSavings(containers.map((c) => c.savings))

  const windowSec = 336 * 3600
  const coveredSec = Math.round(Math.min(windowSec, ageDays * 86400) * coverage)
  const generatedAt = iso(lastRun - Math.floor(r() * 600) * 1000)
  const recId = uuid(r)
  const mkRec = (
    id: string,
    at: number,
    cs: ContainerRecommendation[],
    conf: number,
    s: typeof savings,
  ): WorkloadRecommendation => ({
    id,
    workload: { namespace: ns, kind, name },
    generatedAt: iso(at),
    strategyName: 'percentile',
    settings,
    averageReplicas: replicas,
    containers: cs,
    confidence: conf,
    riskFlags,
    monthlySavingsUSD: s,
    dataCoverage: {
      ratio: round2(coverage),
      window: '336h0m0s',
      covered: `${Math.floor(coveredSec / 3600)}h${Math.floor((coveredSec % 3600) / 60)}m0s`,
      firstSample: iso(Math.max(firstSeen, lastRun - windowSec * 1000)),
      lastSample: iso(lastRun - 60_000),
      samples: Math.round(coveredSec / 30) * containers.length,
    },
  })
  const recommendation = hasRecommendation
    ? mkRec(
        recId,
        Date.parse(generatedAt),
        containers.map((c) => toApiContainer(c, true)),
        confidence,
        savings,
      )
    : undefined

  // Daily history of recommendations, newest first; the latest one equals `recommendation`.
  const history: WorkloadRecommendation[] = []
  if (recommendation) {
    history.push({ ...recommendation, containers: containers.map((c) => toApiContainer(c, false)) })
    const days = Math.min(14, Math.floor(ageDays))
    for (let d = 1; d <= days; d++) {
      const f = 1 + (r() - 0.5) * 0.08 + (applied ? d * 0.02 : 0)
      const cs = containers.map((c) => ({
        ...toApiContainer(c, false),
        recommended: scaleResources(c.recommended, f),
      }))
      const conf = round2(Math.max(0.1, confidence - d * 0.012 + (r() - 0.5) * 0.02))
      const s = {
        cpu: round2(savings.cpu / f),
        memory: round2(savings.memory / f),
        total: round2(savings.total / f),
      }
      history.push(mkRec(uuid(r), lastRun - d * 86400_000 - Math.floor(r() * 600) * 1000, cs, conf, s))
    }
  }
  const appliedAt =
    applied || phase === 'RolledBack'
      ? iso(lastRun - between(r, 1, Math.min(10, Math.max(1, ageDays - 1))) * 86400_000)
      : undefined

  return {
    id,
    seed,
    cluster: spec.name,
    clusterIndex: ci,
    namespace: ns,
    kind,
    name,
    replicas,
    firstSeenAt: iso(firstSeen),
    modes,
    policy,
    phase,
    strategy,
    settings,
    profile,
    reason,
    message,
    appliedAt,
    hasRecommendation,
    containers,
    recommendation,
    history,
    riskFlags: hasRecommendation ? riskFlags : [],
    confidence,
    savings: hasRecommendation ? savings : { cpu: 0, memory: 0, total: 0 },
    shortLived,
  }
}

function buildEvents(w: WorkloadModel, now: number): AuditEvent[] {
  const r = mulberry32(w.seed ^ 0x9e3779b9)
  const out: AuditEvent[] = []
  const workload = { namespace: w.namespace, kind: w.kind, name: w.name }
  const main = w.containers.find((c) => !c.init && !c.sidecar) ?? w.containers[0]!
  const ev = (type: EventType, t: number, reason: string, extra: Partial<AuditEvent> = {}) => {
    const time = iso(t)
    out.push({
      id: uuid(r),
      type,
      time,
      workload,
      reason,
      cluster: w.cluster,
      receivedAt: iso(t + Math.floor(between(r, 1, 20)) * 1000),
      workloadId: w.id,
      ...extra,
    })
  }
  const recAt = (i: number) => w.history[Math.min(i, w.history.length - 1)]
  if (!w.hasRecommendation) return out

  // audit / off: D-017 — one "skipped" per (workload, type, reason), when the agent first saw it.
  const notAuto = (['cpu', 'memory'] as const).filter((res) => w.modes[res] !== 'auto-patch')
  if (notAuto.length) {
    const first = recAt(w.history.length - 1)!
    const allOff = notAuto.every((res) => w.modes[res] === 'off')
    ev('skipped', Date.parse(first.generatedAt) + 90_000, allOff ? 'ModeOff' : 'ModeAudit', {
      recommendationID: first.id,
      resources: notAuto,
      message:
        notAuto.length === 2
          ? 'no resource is in auto-patch mode'
          : `${notAuto[0]} is in ${w.modes[notAuto[0]!]} mode (${(notAuto[0] === 'cpu' ? w.modes.cpuSource : w.modes.memorySource) ?? 'global-default'}${w.policy ? `: ${w.policy}` : ''})`,
    })
  }

  const autoRes = (['cpu', 'memory'] as const).filter((x) => w.modes[x] === 'auto-patch')
  const change = (from: Resources, to: Resources) =>
    autoRes.length
      ? [
          {
            name: main.name,
            from: { requests: pick2(from.requests, autoRes), limits: pick2(from.limits, autoRes) },
            to: { requests: pick2(to.requests, autoRes), limits: pick2(to.limits, autoRes) },
          },
        ]
      : []

  if (w.appliedAt && autoRes.length) {
    const t = Date.parse(w.appliedAt)
    const rec = w.history.find((h) => Date.parse(h.generatedAt) <= t) ?? recAt(0)!
    if (w.strategy === 'in-place' && r() < 0.12) {
      ev('fallback', t - 120_000, pick(r, ['ResizeInfeasible', 'ResizeDeferredTimeout']), {
        recommendationID: rec.id,
        strategy: 'rollout',
        resources: autoRes,
        message: 'Pod resize could not be satisfied on the current node; switched to a rolling update.',
        details: { resizeStatus: 'Infeasible' },
      })
    }
    // A rolled-back change went from the original values to the recommendation and back again.
    const appliedTo = w.phase === 'RolledBack' ? main.recommended : main.current
    ev('applied', t, 'RecommendationApplied', {
      recommendationID: rec.id,
      strategy: w.strategy,
      resources: autoRes,
      containers: change(main.original, appliedTo),
      message: `Applied recommendation to ${w.containers.length} container${w.containers.length > 1 ? 's' : ''} via ${w.strategy}.`,
    })
    if (w.phase === 'RolledBack') {
      ev('rolled_back', t + between(r, 8, 28) * 60_000, w.reason ?? 'OOMKills', {
        recommendationID: rec.id,
        strategy: w.strategy,
        resources: autoRes,
        containers: change(appliedTo, main.original),
        message: w.message,
        details:
          w.reason === 'OOMKills'
            ? { oomKills: '2', watchDuration: '30m' }
            : w.reason === 'Restarts'
              ? { restarts: '4', watchDuration: '30m' }
              : { throttlingRatio: '0.21', watchDuration: '30m' },
      })
    }
    // later runs too close to the live values
    // (D-017: once, not per run)
    const later = [...w.history].reverse().find((h) => Date.parse(h.generatedAt) > t)
    if (later && r() < 0.6) {
      ev('skipped', Date.parse(later.generatedAt) + 60_000, 'NoSignificantChange', {
        recommendationID: later.id,
        resources: autoRes,
        message: 'Recommended values within 10 % of live values.',
      })
    }
    // an older apply for long-lived workloads
    if (r() < 0.3 && w.history.length > 8) {
      const old = recAt(w.history.length - 1)!
      ev('applied', Date.parse(old.generatedAt) + 300_000, 'RecommendationApplied', {
        recommendationID: old.id,
        strategy: w.strategy,
        resources: autoRes,
        containers: change(scaleResources(main.original, 1.2), main.original),
      })
    }
  }
  if (w.phase === 'Blocked' && w.reason) {
    ev('blocked', Date.parse(recAt(0)!.generatedAt) + 120_000, w.reason, {
      recommendationID: recAt(0)!.id,
      resources: autoRes.length ? autoRes : undefined,
      message: w.message,
      details:
        w.reason === 'Throttled'
          ? { throttlingRatio: '0.12' }
          : w.reason === 'OOMKilled'
            ? { oomKills: '2' }
            : undefined,
    })
    if (w.history.length > 3 && w.reason !== 'HPACoupled') {
      ev('blocked', Date.parse(recAt(3)!.generatedAt) + 120_000, 'Cooldown', {
        recommendationID: recAt(3)!.id,
        resources: autoRes,
        message: 'Last change 6h ago; cooldown is 24h.',
      })
    }
  }
  if (w.phase === 'Released') {
    const t = Date.parse(recAt(Math.min(2, w.history.length - 1))!.generatedAt) + 3600_000
    ev('released', t, 'ModeChanged', {
      strategy: w.strategy,
      resources: ['cpu', 'memory'],
      message: w.message,
      details: { previousMode: 'auto-patch', newMode: w.modes.cpu },
    })
  }
  if (autoRes.length && r() < 0.025) {
    ev('failed', now - between(r, 1, 6) * 86400_000, pick(r, ['ApplyError', 'ResizeError']), {
      recommendationID: recAt(1)?.id,
      strategy: w.strategy,
      resources: autoRes,
      message:
        'admission webhook "validate.kyverno.svc" denied the request: resources.limits.cpu is required',
      details: { error: 'Forbidden' },
    })
  }
  return out.map((e) => clean(e))
}

function pick2(v: Resources['requests'], keys: readonly ('cpu' | 'memory')[]) {
  const out: Record<string, string> = {}
  for (const k of keys) if (v?.[k]) out[k] = v[k]!
  return out
}

/** Drops undefined values so responses only carry fields that are set. */
function clean<T extends object>(o: T): T {
  return JSON.parse(JSON.stringify(o)) as T
}

export function buildDb(nowMs: number = Date.now()): MockDb {
  const now = Math.floor(nowMs / 60_000) * 60_000
  // The recommender runs hourly at :05.
  const hourStart = Math.floor(now / 3600_000) * 3600_000
  const lastRun = hourStart + 5 * 60_000 <= now ? hourStart + 5 * 60_000 : hourStart - 55 * 60_000
  const workloads: WorkloadModel[] = []

  CLUSTERS.forEach((spec, ci) => {
    if (spec.workloads === 0) return
    const r = mulberry32(hash(spec.name))
    const names: [string, string, WorkloadKind][] = []
    const seen = new Set<string>()
    const nsList = Object.keys(NAMESPACES)
    for (const ns of nsList) {
      for (const [n, k] of NAMESPACES[ns]!.apps) {
        if (ci === 1 && r() < 0.1) continue
        names.push([ns, n, k ?? 'Deployment'])
        seen.add(`${ns}/${n}`)
      }
    }
    while (names.length < spec.workloads) {
      const ns = pick(
        r,
        nsList.filter((n) => n !== 'observability' && n !== 'platform'),
      )
      const base = pick(r, NAMESPACES[ns]!.apps)
      const v = pick(r, VARIANTS)
      const n = `${base[0]}-${v}`
      if (seen.has(`${ns}/${n}`)) continue
      seen.add(`${ns}/${n}`)
      names.push([ns, n, v === 'scheduler' || v === 'migrator' ? 'CronJob' : 'Deployment'])
    }
    for (const [ns, n, k] of names) workloads.push(buildWorkload(spec, ci, ns, n, k, now, lastRun))
  })

  const byId = new Map(workloads.map((w) => [w.id, w]))
  const events = workloads
    .flatMap((w) => buildEvents(w, now))
    .filter((e) => Date.parse(e.time) <= now)
    .sort((a, b) => a.time.localeCompare(b.time))

  const clusters: ClusterInfo[] = CLUSTERS.map((spec) => {
    const ws = workloads.filter((w) => w.cluster === spec.name)
    return clean({
      name: spec.name,
      description: spec.description,
      createdAt: iso(now - 210 * 86400_000 + (hash(spec.name) % 1000) * 60_000),
      lastHeartbeatAt: spec.heartbeatAgoSec != null ? iso(now - spec.heartbeatAgoSec * 1000) : undefined,
      agentVersion: spec.agentVersion,
      agentStatus: spec.status,
      counts: {
        workloads: ws.length,
        withRecommendation: ws.filter((w) => w.hasRecommendation).length,
        autoPatch: ws.filter((w) => w.modes.cpu === 'auto-patch' || w.modes.memory === 'auto-patch').length,
        blocked: ws.filter((w) => w.phase === 'Blocked').length,
      },
    })
  })

  return { now, lastRunAt: iso(lastRun), clusters, clusterSpecs: CLUSTERS, workloads, byId, events }
}

// ---------------------------------------------------------------------------------------------
// Projections used by the handlers
// ---------------------------------------------------------------------------------------------

export function toRow(w: WorkloadModel): WorkloadRow {
  const base: WorkloadRow = {
    id: w.id,
    cluster: w.cluster,
    namespace: w.namespace,
    kind: w.kind,
    name: w.name,
    modes: w.modes,
    phase: w.phase,
  }
  if (!w.recommendation) return base
  return {
    ...base,
    recommendationID: w.recommendation.id,
    generatedAt: w.recommendation.generatedAt,
    containers: w.containers.map((c) => toApiContainer(c, false)),
    confidence: w.confidence,
    riskFlags: w.riskFlags,
    monthlySavingsUSD: w.savings,
  }
}

export function toStatus(w: WorkloadModel, db: MockDb): WorkloadStatus {
  return clean({
    workload: { namespace: w.namespace, kind: w.kind, name: w.name },
    policy: w.policy ?? '',
    modes: w.modes,
    strategy: w.strategy,
    settings: w.settings,
    phase: w.phase,
    reason: w.reason,
    message: w.message,
    recommendationID: w.recommendation?.id,
    live: w.containers.map((c) => ({ name: c.name, requests: c.current.requests, limits: c.current.limits })),
    appliedAt: w.appliedAt,
    observedAt: iso(db.now - (w.seed % 50) * 1000),
  })
}

export function toDetail(w: WorkloadModel, db: MockDb): WorkloadDetail {
  return clean({
    id: w.id,
    cluster: w.cluster,
    namespace: w.namespace,
    kind: w.kind,
    name: w.name,
    modes: w.modes,
    phase: w.phase,
    firstSeenAt: w.firstSeenAt,
    lastSeenAt: iso(db.now - (w.seed % 120) * 1000),
    recommendation: w.recommendation,
    status: toStatus(w, db),
  })
}

export function summarize(ws: WorkloadModel[], lastRunAt: string): Summary {
  const s: Summary = {
    workloads: ws.length,
    workloadsByMode: {
      cpu: { off: 0, audit: 0, 'auto-patch': 0 },
      memory: { off: 0, audit: 0, 'auto-patch': 0 },
    },
    workloadsByRiskFlag: {},
    workloadsByPhase: {},
    cpu: { requested: 0, used: 0, recommended: 0 },
    memory: { requested: 0, used: 0, recommended: 0 },
    monthlySavingsUSD: { cpu: 0, memory: 0, total: 0 },
  }
  for (const w of ws) {
    s.workloadsByMode.cpu[w.modes.cpu]++
    s.workloadsByMode.memory[w.modes.memory]++
    for (const f of w.riskFlags) s.workloadsByRiskFlag[f] = (s.workloadsByRiskFlag[f] ?? 0) + 1
    s.workloadsByPhase![w.phase] = (s.workloadsByPhase![w.phase] ?? 0) + 1
    for (const c of w.containers) {
      if (c.init) continue
      const reqC = parseCpu(c.current.requests!.cpu!)
      const reqM = parseMem(c.current.requests!.memory!)
      s.cpu.requested += reqC * w.replicas
      s.memory.requested += reqM * w.replicas
      s.cpu.used += c.cpu.p50 * 0.92 * w.replicas
      s.memory.used += c.mem.p50 * w.replicas
      s.cpu.recommended += (w.hasRecommendation ? parseCpu(c.recommended.requests!.cpu!) : reqC) * w.replicas
      s.memory.recommended +=
        (w.hasRecommendation ? parseMem(c.recommended.requests!.memory!) : reqM) * w.replicas
    }
  }
  s.monthlySavingsUSD = sumSavings(ws.map((w) => w.savings))
  for (const k of ['requested', 'used', 'recommended'] as const) {
    s.cpu[k] = Math.round(s.cpu[k] * 1000) / 1000
    s.memory[k] = Math.round(s.memory[k])
  }
  if (ws.length) s.lastRecommenderRunAt = lastRunAt
  return s
}

// ---------------------------------------------------------------------------------------------
// Time series
// ---------------------------------------------------------------------------------------------

/** Picks a "nice" step so (to - from) / step <= maxPoints. */
export function chooseStep(rangeSec: number, maxPoints = 1000): number {
  const nice = [60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400]
  for (const s of nice) if (rangeSec / s <= maxPoints) return s
  return Math.ceil(rangeSec / maxPoints / 86400) * 86400
}

export function goDuration(sec: number): string {
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  return `${h}h${m}m${s}s`
}

export function parseStep(step: string | null): number | undefined {
  if (!step) return undefined
  let total = 0
  let ok = false
  for (const m of step.matchAll(/(\d+(?:\.\d+)?)(h|m|s)/g)) {
    ok = true
    total += Number(m[1]) * (m[2] === 'h' ? 3600 : m[2] === 'm' ? 60 : 1)
  }
  return ok && total > 0 ? total : undefined
}

/** Smooth deterministic noise in [-1, 1]. */
function noise(seed: number, t: number) {
  return (
    Math.sin(t * 0.00131 + seed) * 0.5 +
    Math.sin(t * 0.000377 + seed * 1.7) * 0.3 +
    Math.sin(t * 0.0071 + seed * 0.3) * 0.2
  )
}

function stepAt(changes: { at: number; v: number }[], t: number): number {
  let v = changes[0]!.v
  for (const c of changes) if (c.at <= t) v = c.v
  return v
}

export function usageSeries(
  w: WorkloadModel,
  c: ContainerModel,
  resource: 'cpu' | 'memory',
  fromSec: number,
  toSec: number,
  stepSec: number,
) {
  const stats = resource === 'cpu' ? c.cpu : c.mem
  const parse = resource === 'cpu' ? parseCpu : parseMem
  const seed = (w.seed % 10_000) + c.name.length * 13
  const usage: [number, number][] = []
  const usageAvg: [number, number][] = []
  const start = Math.ceil(fromSec / stepSec) * stepSec
  const r = mulberry32(w.seed ^ hash(c.name + resource))
  const spikes = Array.from({ length: 6 }, () => fromSec + r() * (toSec - fromSec))
  const runEvery = 6 * 3600
  for (let t = start; t < toSec; t += stepSec) {
    if (w.shortLived && t % runEvery > 40 * 60) continue // CronJob pods only exist while running
    const hour = (((t / 3600) % 24) + 24) % 24
    const dow = new Date(t * 1000).getUTCDay()
    const weekend = dow === 0 || dow === 6 ? 0.72 : 1
    let v: number
    if (resource === 'cpu') {
      const daily =
        0.55 +
        0.45 * Math.max(0, Math.sin(((hour - 6) / 24) * Math.PI * 2)) +
        0.12 * Math.sin(((hour - 13) / 12) * Math.PI * 2)
      v = stats.p50 * 0.9 * daily * weekend * (1 + 0.18 * noise(seed, t))
      for (const s of spikes)
        if (Math.abs(t - s) < stepSec * 1.5) v = Math.max(v, stats.max * between(r, 0.8, 1))
      v = Math.min(v, stats.max)
    } else {
      const period = w.riskFlags.includes('oom-killed') ? 9 * 3600 : 30 * 3600
      const saw = ((((t + seed * 97) % period) + period) % period) / period
      v = stats.p50 * (0.9 + 0.12 * saw) * (1 + 0.02 * noise(seed, t))
      if (w.riskFlags.includes('oom-killed') && saw > 0.93) v = stats.max
      for (const s of spikes) if (Math.abs(t - s) < stepSec * 2) v = Math.max(v, stats.p99 * 0.99)
    }
    v = Math.max(0, v)
    const avgFactor = w.replicas > 1 ? 0.78 + 0.08 * noise(seed + 3, t) : 1
    usage.push([t, resource === 'cpu' ? round4(v) : Math.round(v)])
    usageAvg.push([t, resource === 'cpu' ? round4(v * avgFactor) : Math.round(v * avgFactor)])
  }

  const appliedAt = w.appliedAt ? Date.parse(w.appliedAt) / 1000 : undefined
  const reqChanges = [{ at: -Infinity, v: parse(c.original.requests![resource]!) }]
  if (appliedAt && w.phase !== 'RolledBack')
    reqChanges.push({ at: appliedAt, v: parse(c.current.requests![resource]!) })
  const origLimit = c.original.limits?.[resource]
  const curLimit = c.current.limits?.[resource]
  const limChanges = [{ at: -Infinity, v: origLimit ? parse(origLimit) : NaN }]
  if (appliedAt && w.phase !== 'RolledBack')
    limChanges.push({ at: appliedAt, v: curLimit ? parse(curLimit) : NaN })
  const recChanges = [...w.history].reverse().map((h) => {
    const hc = h.containers.find((x) => x.name === c.name)
    const q = hc?.recommended.requests?.[resource]
    return { at: Date.parse(h.generatedAt) / 1000, v: q ? parse(q) : NaN }
  })
  const stepFn = (changes: { at: number; v: number }[]) => {
    const out: [number, number][] = []
    for (let t = start; t < toSec; t += stepSec) {
      const v = stepAt(changes, t)
      if (!Number.isNaN(v)) out.push([t, resource === 'cpu' ? round4(v) : Math.round(v)])
    }
    return out
  }
  return {
    name: c.name,
    usage,
    usageAvg,
    request: stepFn(reqChanges),
    limit: stepFn(limChanges),
    recommendation: recChanges.length ? stepFn([{ at: -Infinity, v: NaN }, ...recChanges]) : [],
  }
}
const round4 = (n: number) => Math.round(n * 10000) / 10000

export function savingsTrend(
  db: MockDb,
  clusters: string[],
  fromSec: number,
  toSec: number,
  stepSec: number,
) {
  const out: { cluster: string; points: { time: string; cpu: number; memory: number; total: number }[] }[] =
    []
  for (const name of clusters) {
    const ws = db.workloads.filter((w) => w.cluster === name)
    const now = sumSavings(ws.map((w) => w.savings))
    const r = mulberry32(hash(name + 'trend'))
    const points: { time: string; cpu: number; memory: number; total: number }[] = []
    if (!ws.length) {
      out.push({ cluster: name, points })
      continue
    }
    const start = Math.ceil(fromSec / stepSec) * stepSec
    // Applied workloads: their savings were larger before the apply (that is what got realized).
    const priceFactor = db.clusterSpecs.find((s) => s.name === name)!.cpuPrice / 0.0316
    const appliedBoost = ws
      .filter((w) => w.appliedAt)
      .map((w) => ({ at: Date.parse(w.appliedAt!) / 1000, v: w.replicas * 9 * priceFactor }))
    const firstSeen = ws.map((w) => ({ at: Date.parse(w.firstSeenAt) / 1000, v: w.savings }))
    for (let t = start; t < toSec; t += stepSec) {
      const ageDays = (db.now / 1000 - t) / 86400
      let cpu = 0
      let memory = 0
      for (const f of firstSeen) {
        if (f.at <= t) {
          cpu += f.v.cpu
          memory += f.v.memory
        }
      }
      const boost = appliedBoost.filter((a) => a.at > t).reduce((s, a) => s + a.v, 0)
      const wobble = 1 + 0.025 * Math.sin((t / 86400) * 2.1 + r() * 0.2) + (r() - 0.5) * 0.01
      // Usage has been creeping down as teams tune JVM flags, so older estimates were a bit lower.
      const drift = 1 - Math.min(0.14, ageDays * 0.0045)
      cpu = (cpu + boost * 0.7) * wobble * drift
      memory = (memory + boost * 0.3) * wobble * drift
      points.push({
        time: iso(t * 1000),
        cpu: round2(cpu),
        memory: round2(memory),
        total: round2(cpu + memory),
      })
    }
    if (points.length && toSec * 1000 >= db.now - stepSec * 1000) {
      const last = points[points.length - 1]!
      last.cpu = now.cpu
      last.memory = now.memory
      last.total = now.total
    }
    out.push({ cluster: name, points })
  }
  return out
}
