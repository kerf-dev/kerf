/**
 * Cluster readiness for the mock API (D-025, docs/contracts/readiness.md). The catalog mirrors
 * `api/v1alpha1/readiness` (ids, category, severity, source, gates, title) in catalog order.
 */
import type { components } from '@/api/schema'

type S = components['schemas']
export type ReadinessCheck = S['ReadinessCheck']
export type ReadinessState = S['ReadinessState']
export type ReadinessStatus = S['ReadinessStatus']
export type AgentCapability = S['AgentCapability']
export type ClusterReadiness = S['ClusterReadiness']
export type ReadinessHistoryEntry = S['ReadinessHistoryEntry']
export type InstallSnippet = S['InstallSnippet']
export type InstallFormat = S['InstallFormat']
export type MetricsBinding = S['MetricsBinding']

export const STATES: ReadinessState[] = [
  'Registered',
  'AgentConnected',
  'MetricsOK',
  'ReadyAudit',
  'AutomationReady',
]
export const stateRank = (s: ReadinessState) => STATES.indexOf(s)

interface CatalogEntry {
  id: string
  category: S['ReadinessCategory']
  severity: S['ReadinessSeverity']
  source: 'hub' | 'agent'
  gates?: ReadinessState
  capability?: AgentCapability['name']
  title: string
}

export const CATALOG: CatalogEntry[] = [
  {
    id: 'agent.connected',
    category: 'agent',
    severity: 'blocking',
    source: 'hub',
    gates: 'AgentConnected',
    title: 'Agent has pulled from the hub',
  },
  {
    id: 'agent.version',
    category: 'agent',
    severity: 'blocking',
    source: 'agent',
    gates: 'AgentConnected',
    title: 'Agent and hub versions are compatible',
  },
  {
    id: 'agent.crds',
    category: 'agent',
    severity: 'blocking',
    source: 'agent',
    gates: 'AgentConnected',
    title: 'CRDs installed and current',
  },
  {
    id: 'agent.clock-skew',
    category: 'agent',
    severity: 'warning',
    source: 'agent',
    title: 'Clock skew to the hub under 30 s',
  },
  {
    id: 'datasource.reachable',
    category: 'datasource',
    severity: 'blocking',
    source: 'hub',
    gates: 'MetricsOK',
    title: 'Datasource reachable and authenticated',
  },
  {
    id: 'metrics.cluster-label',
    category: 'metrics',
    severity: 'blocking',
    source: 'hub',
    gates: 'MetricsOK',
    title: 'Cluster label matches series',
  },
  {
    id: 'metrics.cadvisor',
    category: 'metrics',
    severity: 'blocking',
    source: 'hub',
    gates: 'MetricsOK',
    title: 'cAdvisor CPU and memory working set',
  },
  {
    id: 'metrics.ksm-owners',
    category: 'metrics',
    severity: 'blocking',
    source: 'hub',
    gates: 'MetricsOK',
    title: 'kube-state-metrics owner series',
  },
  {
    id: 'metrics.ksm-resources',
    category: 'metrics',
    severity: 'blocking',
    source: 'hub',
    gates: 'MetricsOK',
    title: 'kube-state-metrics requests and limits',
  },
  {
    id: 'metrics.freshness',
    category: 'metrics',
    severity: 'blocking',
    source: 'hub',
    gates: 'MetricsOK',
    title: 'Data fresh (last sample under 5 min)',
  },
  {
    id: 'metrics.owner-join',
    category: 'metrics',
    severity: 'warning',
    source: 'hub',
    title: 'Pod to owner join coverage at least 95 %',
  },
  {
    id: 'metrics.scrape-interval',
    category: 'metrics',
    severity: 'warning',
    source: 'hub',
    title: 'Scrape resolution 60 s or finer',
  },
  {
    id: 'metrics.history',
    category: 'metrics',
    severity: 'warning',
    source: 'hub',
    title: 'History covers the profile window',
  },
  {
    id: 'metrics.throttling',
    category: 'metrics',
    severity: 'warning',
    source: 'hub',
    title: 'CPU throttling series',
  },
  {
    id: 'metrics.restarts-oom',
    category: 'metrics',
    severity: 'warning',
    source: 'hub',
    title: 'Restart and OOM series',
  },
  { id: 'metrics.hpa', category: 'metrics', severity: 'warning', source: 'hub', title: 'HPA series' },
  {
    id: 'metrics.node-labels',
    category: 'metrics',
    severity: 'warning',
    source: 'hub',
    title: 'Node labels allowlisted in kube-state-metrics',
  },
  {
    id: 'metrics.node-allocatable',
    category: 'metrics',
    severity: 'warning',
    source: 'hub',
    title: 'Node allocatable series',
  },
  { id: 'pricing.coverage', category: 'pricing', severity: 'warning', source: 'hub', title: 'Nodes priced' },
  {
    id: 'metrics.recommendations',
    category: 'metrics',
    severity: 'blocking',
    source: 'hub',
    gates: 'ReadyAudit',
    title: 'Recommendations produced',
  },
  {
    id: 'rbac.read',
    category: 'rbac',
    severity: 'blocking',
    source: 'agent',
    gates: 'AgentConnected',
    capability: 'read',
    title: 'RBAC: read workloads, pods, HPAs, nodes',
  },
  {
    id: 'rbac.resize',
    category: 'rbac',
    severity: 'blocking',
    source: 'agent',
    gates: 'AutomationReady',
    capability: 'resize',
    title: 'RBAC: patch pods/resize',
  },
  {
    id: 'rbac.rollout',
    category: 'rbac',
    severity: 'blocking',
    source: 'agent',
    gates: 'AutomationReady',
    capability: 'rollout',
    title: 'RBAC: patch deployments, statefulsets, daemonsets',
  },
  {
    id: 'rbac.hpa',
    category: 'rbac',
    severity: 'blocking',
    source: 'agent',
    gates: 'AutomationReady',
    capability: 'hpa',
    title: 'RBAC: patch HPAs',
  },
  {
    id: 'rbac.evict',
    category: 'rbac',
    severity: 'blocking',
    source: 'agent',
    gates: 'AutomationReady',
    capability: 'evict',
    title: 'RBAC: create pods/eviction',
  },
  {
    id: 'rbac.policies',
    category: 'rbac',
    severity: 'blocking',
    source: 'agent',
    gates: 'AutomationReady',
    capability: 'policies',
    title: 'RBAC: write rightsizingpolicies',
  },
  {
    id: 'rbac.events',
    category: 'rbac',
    severity: 'blocking',
    source: 'agent',
    gates: 'AutomationReady',
    capability: 'events',
    title: 'RBAC: create events',
  },
  {
    id: 'apply.in-place-resize',
    category: 'apply',
    severity: 'warning',
    source: 'agent',
    title: 'In-place resize available',
  },
  {
    id: 'webhook.healthy',
    category: 'webhook',
    severity: 'warning',
    source: 'agent',
    title: 'Webhook installed and healthy',
  },
  {
    id: 'webhook.cert-manager',
    category: 'webhook',
    severity: 'info',
    source: 'agent',
    title: 'cert-manager present',
  },
  {
    id: 'nodes.in-place-memory',
    category: 'nodes',
    severity: 'info',
    source: 'agent',
    title: 'Nodes without swap or static managers',
  },
  {
    id: 'integration.argocd',
    category: 'integration',
    severity: 'warning',
    source: 'agent',
    title: 'Argo CD ignores resource-optimizer fields',
  },
  {
    id: 'policy.namespace-coverage',
    category: 'policy',
    severity: 'info',
    source: 'agent',
    title: 'Namespace coverage',
  },
]
export const catalogEntry = (id: string) => CATALOG.find((c) => c.id === id)

const DOCS = 'https://docs.resource-optimizer.dev/readiness'
export const HUB_URL = 'https://ro-hub.platform.internal'
export const AGENT_SECRET = 'resource-optimizer-agent-token'

const RBAC_RULES: Record<AgentCapability['name'], string[]> = {
  read: [
    '{"apiGroups":[""],"resources":["pods","nodes"],"verbs":["get","list","watch"]}',
    '{"apiGroups":["apps","batch"],"resources":["deployments","statefulsets","daemonsets","replicasets","cronjobs","jobs"],"verbs":["get","list","watch"]}',
    '{"apiGroups":["autoscaling"],"resources":["horizontalpodautoscalers"],"verbs":["get","list","watch"]}',
  ],
  resize: ['{"apiGroups":[""],"resources":["pods/resize"],"verbs":["patch"]}'],
  rollout: [
    '{"apiGroups":["apps"],"resources":["deployments","statefulsets","daemonsets"],"verbs":["patch"]}',
  ],
  hpa: ['{"apiGroups":["autoscaling"],"resources":["horizontalpodautoscalers"],"verbs":["patch"]}'],
  evict: ['{"apiGroups":[""],"resources":["pods/eviction"],"verbs":["create"]}'],
  policies: [
    '{"apiGroups":["resource-optimizer.oz.io"],"resources":["rightsizingpolicies"],"verbs":["create","update","delete"]}',
  ],
  events: ['{"apiGroups":["","events.k8s.io"],"resources":["events"],"verbs":["create"]}'],
}

function rbacRemediation(cap: AgentCapability['name']) {
  const rules = RBAC_RULES[cap].map(
    (r) => JSON.parse(r) as { apiGroups: string[]; resources: string[]; verbs: string[] },
  )
  return (
    "kubectl patch clusterrole resource-optimizer-agent --type=json -p '[" +
    rules.map((r) => `{"op":"add","path":"/rules/-","value":${JSON.stringify(r)}}`).join(',') +
    "]'\n# or upgrade the agent chart without readOnly: helm upgrade resource-optimizer-agent … --set readOnly=false"
  )
}

/** Remediation per check id (shown for checks that do not pass). */
function remediationFor(
  id: string,
  cluster: string,
): { remediation: string; remediationType: 'command' | 'yaml' | 'text' } | undefined {
  const cap = catalogEntry(id)?.capability
  if (cap) return { remediation: rbacRemediation(cap), remediationType: 'command' }
  switch (id) {
    case 'agent.connected':
      return { remediation: helmSnippet(cluster, false, false), remediationType: 'command' }
    case 'agent.version':
      return {
        remediation: `helm upgrade resource-optimizer-agent oci://ghcr.io/oz/resource-optimizer/charts/agent --version 0.4.2 -n resource-optimizer --reuse-values`,
        remediationType: 'command',
      }
    case 'agent.crds':
      return {
        remediation:
          'kubectl apply --server-side -f https://github.com/oz/resource-optimizer/releases/download/v0.4.2/crds.yaml',
        remediationType: 'command',
      }
    case 'agent.clock-skew':
      return { remediation: 'Check NTP / chrony on the nodes running the agent.', remediationType: 'text' }
    case 'datasource.reachable':
      return {
        remediation: 'Check the datasource URL, TLS settings and credentials in Admin → Metrics sources.',
        remediationType: 'text',
      }
    case 'metrics.cluster-label':
      return {
        remediation:
          'Set the cluster label key/value of the metrics binding to a value that exists (see the suggestions), or add an external label in vmagent / Prometheus:\nglobal:\n  external_labels:\n    cluster: ' +
          cluster,
        remediationType: 'yaml',
      }
    case 'metrics.cadvisor':
      return {
        remediation:
          'Scrape the kubelet cAdvisor endpoint (job "kubelet", path /metrics/cadvisor) and keep the `container` label.',
        remediationType: 'text',
      }
    case 'metrics.ksm-owners':
    case 'metrics.ksm-resources':
      return {
        remediation:
          'helm upgrade --install kube-state-metrics prometheus-community/kube-state-metrics -n kube-system',
        remediationType: 'command',
      }
    case 'metrics.freshness':
      return {
        remediation: 'kubectl -n monitoring rollout restart deploy/vmagent  # then check remote-write errors',
        remediationType: 'command',
      }
    case 'metrics.node-labels':
      return {
        remediation:
          'kube-state-metrics:\n  metricLabelsAllowlist:\n    - nodes=[node.kubernetes.io/instance-type,karpenter.sh/capacity-type,topology.kubernetes.io/zone,topology.kubernetes.io/region]',
        remediationType: 'yaml',
      }
    case 'metrics.history':
      return {
        remediation: 'Nothing to do: the check passes once the metrics cover the profile window.',
        remediationType: 'text',
      }
    case 'webhook.healthy':
      return {
        remediation:
          'helm upgrade --install resource-optimizer-webhook oci://ghcr.io/oz/resource-optimizer/charts/webhook -n resource-optimizer --set certManager.enabled=true',
        remediationType: 'command',
      }
    case 'integration.argocd':
      return { remediation: ARGOCD_IGNORE, remediationType: 'yaml' }
    case 'apply.in-place-resize':
      return {
        remediation:
          'Upgrade the cluster to Kubernetes 1.33+; until then `in-place` falls back to `rollout`.',
        remediationType: 'text',
      }
    default:
      return undefined
  }
}

export interface CheckResult {
  status: ReadinessStatus
  message: string
  details?: Record<string, string>
}

export interface ReadinessModel {
  cluster: string
  results: Record<string, CheckResult>
  checkedAt: string
  stateSince: string
  recheckRequestedAt?: string
  history: ReadinessHistoryEntry[]
}

const PASS = (message: string, details?: Record<string, string>): CheckResult => ({
  status: 'pass',
  message,
  ...(details ? { details } : {}),
})

/** Pass messages per check (the "all good" readiness of a healthy cluster). */
function healthy(id: string, workloads: number): CheckResult {
  const pods = Math.max(12, Math.round(workloads * 3.1))
  switch (id) {
    case 'agent.connected':
      return PASS('Last config pull 14 s ago.')
    case 'agent.version':
      return PASS('Agent v0.4.2, hub v0.4.2 (minimum agent v0.4.0).')
    case 'agent.crds':
      return PASS('rightsizingpolicies.resource-optimizer.oz.io v1alpha1 installed (stored version current).')
    case 'agent.clock-skew':
      return PASS('Skew 0.4 s.')
    case 'datasource.reachable':
      return PASS('HTTP 200 in 41 ms.', { httpStatus: '200' })
    case 'metrics.cluster-label':
      return PASS(`${(pods * 9).toLocaleString('en-US')} series match the binding.`)
    case 'metrics.cadvisor':
      return PASS(
        `${(pods * 2).toLocaleString('en-US')} container CPU and memory series with the container label.`,
      )
    case 'metrics.ksm-owners':
      return PASS(`kube_pod_owner covers ${pods} pods; ReplicaSet and Job owners present.`)
    case 'metrics.ksm-resources':
      return PASS('Requests and limits series present for every container.')
    case 'metrics.freshness':
      return PASS('Last sample 19 s ago.')
    case 'metrics.owner-join':
      return PASS('99.6 % of cAdvisor pods join to an owner.')
    case 'metrics.scrape-interval':
      return PASS('Median resolution 30 s.')
    case 'metrics.history':
      return PASS('History 90 d ≥ 28 d (high-availability window).')
    case 'metrics.throttling':
      return PASS('container_cpu_cfs_throttled_periods_total present.')
    case 'metrics.restarts-oom':
      return PASS('Restart, last-terminated-reason and OOM event series present.')
    case 'metrics.hpa':
      return PASS('kube_horizontalpodautoscaler_spec_* present.')
    case 'metrics.node-labels':
      return PASS('Instance type, capacity type, zone and region labels present.')
    case 'metrics.node-allocatable':
      return PASS('kube_node_status_allocatable present for every node.')
    case 'pricing.coverage':
      return PASS('All nodes priced from the pricing service.')
    case 'metrics.recommendations':
      return PASS(`Recommendations for ${workloads} of ${workloads} live workloads.`)
    case 'apply.in-place-resize':
      return PASS('Kubernetes v1.33.4; pods/resize available.')
    case 'webhook.healthy':
      return PASS('MutatingWebhookConfiguration ready; dry-run pod was mutated.')
    case 'webhook.cert-manager':
      return PASS('cert-manager v1.18.2 issues the webhook certificate.')
    case 'nodes.in-place-memory':
      return PASS('No node uses swap or a static CPU/memory manager.', { nodes: '0' })
    case 'integration.argocd':
      return PASS('argocd-cm ignores fields managed by resource-optimizer.')
    case 'policy.namespace-coverage':
      return PASS('kube-system and kube-node-lease excluded by the global defaults.', {
        excluded: 'kube-system,kube-node-lease',
      })
    default: {
      const cap = catalogEntry(id)?.capability
      return PASS(cap ? `All ${cap} permissions granted.` : 'OK')
    }
  }
}

export type ReadinessProfile = 'ready' | 'audit' | 'registered' | 'new'

/** Seeds the readiness of a cluster. */
export function buildReadiness(
  cluster: string,
  profile: ReadinessProfile,
  opts: { now: number; createdAt: number; workloads: number; labelMatches?: boolean },
): ReadinessModel {
  const { now, workloads } = opts
  const iso = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')
  const results: Record<string, CheckResult> = {}
  for (const c of CATALOG) results[c.id] = healthy(c.id, workloads)
  const history: ReadinessHistoryEntry[] = []
  const created = opts.createdAt
  const st = (t: number, fromState: ReadinessState | undefined, toState: ReadinessState) =>
    history.push({ time: iso(t), kind: 'state', ...(fromState ? { fromState } : {}), toState })
  const ck = (
    t: number,
    checkId: string,
    fromStatus: ReadinessStatus,
    toStatus: ReadinessStatus,
    message: string,
  ) => history.push({ time: iso(t), kind: 'check', checkId, fromStatus, toStatus, message })
  let stateSince = created

  if (profile === 'ready' || profile === 'audit') {
    st(created, undefined, 'Registered')
    st(created + 4 * 60_000, 'Registered', 'AgentConnected')
    st(created + 6 * 60_000, 'AgentConnected', 'MetricsOK')
    st(created + 62 * 60_000, 'MetricsOK', 'ReadyAudit')
    st(created + 3 * 86400_000, 'ReadyAudit', 'AutomationReady')
    stateSince = created + 3 * 86400_000
  }
  if (profile === 'ready') {
    results['integration.argocd'] = {
      status: 'warn',
      message:
        'Argo CD is installed and argocd-cm has no managedFieldsManagers entry for resource-optimizer.',
    }
    ck(
      now - 3 * 86400_000,
      'integration.argocd',
      'pass',
      'warn',
      'argocd-cm was replaced by the platform GitOps repo.',
    )
  }
  if (profile === 'audit') {
    const lost = now - 2 * 86400_000 - 3 * 3600_000
    results['rbac.resize'] = {
      status: 'fail',
      message: 'SelfSubjectAccessReview denied patch pods/resize.',
    }
    results['rbac.policies'] = {
      status: 'fail',
      message:
        'SelfSubjectAccessReview denied create, update, delete rightsizingpolicies.resource-optimizer.oz.io.',
    }
    results['metrics.node-labels'] = {
      status: 'warn',
      message:
        'kube_node_labels has no label_node_kubernetes_io_instance_type or label_karpenter_sh_capacity_type: pricing falls back to default rates.',
      details: { missing: 'instance-type,capacity-type' },
    }
    results['pricing.coverage'] = {
      status: 'warn',
      message: '6 of 18 nodes priced with default rates (unknown instance type).',
      details: { priced: '12', nodes: '18' },
    }
    results['webhook.healthy'] = {
      status: 'fail',
      message: 'MutatingWebhookConfiguration resource-optimizer has no caBundle; new pods are not mutated.',
    }
    results['webhook.cert-manager'] = {
      status: 'warn',
      message: 'cert-manager not found; the webhook uses a self-signed certificate.',
    }
    results['apply.in-place-resize'] = PASS('Kubernetes v1.33.2; pods/resize available.')
    ck(
      lost,
      'rbac.resize',
      'pass',
      'fail',
      'Agent chart upgraded with readOnly: true (ClusterRole lost pods/resize).',
    )
    ck(lost, 'rbac.policies', 'pass', 'fail', 'ClusterRole lost write verbs on rightsizingpolicies.')
    st(lost, 'AutomationReady', 'ReadyAudit')
    ck(
      now - 9 * 86400_000,
      'metrics.node-labels',
      'pass',
      'warn',
      'kube-state-metrics redeployed without metricLabelsAllowlist.',
    )
    ck(
      now - 9 * 86400_000,
      'webhook.healthy',
      'pass',
      'fail',
      'Webhook certificate rotated without caBundle injection.',
    )
    stateSince = lost
  }
  if (profile === 'registered' || profile === 'new') {
    st(created, undefined, 'Registered')
    for (const c of CATALOG) {
      if (c.source === 'agent')
        results[c.id] = { status: 'unknown', message: 'No result yet: the agent has not reported.' }
    }
    results['agent.connected'] = {
      status: 'fail',
      message: 'The agent has never pulled its config. Install it with the snippet below.',
    }
    results['metrics.recommendations'] = { status: 'fail', message: 'No recommendation produced yet.' }
    results['metrics.history'] = {
      status: 'warn',
      message:
        profile === 'new'
          ? 'History 0 d of 14 d (production window).'
          : 'History 6 d of 14 d (production window).',
      details: { enoughDataAt: iso(now + (profile === 'new' ? 14 : 8) * 86400_000) },
    }
    results['pricing.coverage'] = {
      status: 'warn',
      message: 'No node priced yet: the pricing service has not seen this cluster.',
    }
    if (profile === 'new' && opts.labelMatches === false) {
      for (const id of [
        'metrics.cadvisor',
        'metrics.ksm-owners',
        'metrics.ksm-resources',
        'metrics.freshness',
      ])
        results[id] = { status: 'unknown', message: 'Not checked: the cluster label matches no series.' }
      for (const id of [
        'metrics.owner-join',
        'metrics.scrape-interval',
        'metrics.throttling',
        'metrics.restarts-oom',
        'metrics.hpa',
        'metrics.node-labels',
        'metrics.node-allocatable',
      ])
        results[id] = { status: 'unknown', message: 'Not checked: the cluster label matches no series.' }
      results['metrics.cluster-label'] = {
        status: 'fail',
        message: `No series with cluster="${cluster}" in the datasource.`,
        details: { suggestions: 'prod-eu-west-1,prod-us-east-1,staging-eu-west-1' },
      }
    }
  }
  history.sort((a, b) => b.time.localeCompare(a.time))
  return {
    cluster,
    results,
    checkedAt: iso(now - 4 * 60_000),
    stateSince: iso(stateSince),
    history,
  }
}

export function toChecks(m: ReadinessModel): ReadinessCheck[] {
  return CATALOG.map((c) => {
    const r = m.results[c.id] ?? { status: 'unknown' as const, message: 'No result yet.' }
    const rem = r.status === 'pass' ? undefined : remediationFor(c.id, m.cluster)
    const check: ReadinessCheck = {
      id: c.id,
      category: c.category,
      severity: c.severity,
      source: c.source,
      ...(c.gates ? { gates: c.gates } : {}),
      status: r.status,
      message: r.message,
      ...(rem ?? {}),
      docsURL: `${DOCS}#${c.id}`,
      checkedAt: m.checkedAt,
      ...(r.details ? { details: r.details } : {}),
    }
    return check
  })
}

/** readiness.ComputeState: the highest state whose gating blocking checks (and earlier ones) pass. */
export function computeState(checks: ReadinessCheck[]): ReadinessState {
  let state: ReadinessState = 'Registered'
  for (const s of STATES.slice(1)) {
    const gating = checks.filter((c) => c.severity === 'blocking' && c.gates === s)
    if (gating.every((c) => c.status === 'pass')) state = s
    else break
  }
  return state
}

export function toClusterReadiness(m: ReadinessModel): ClusterReadiness {
  const checks = toChecks(m)
  const state = computeState(checks)
  const next = STATES[stateRank(state) + 1]
  const caps: AgentCapability[] = CATALOG.filter((c) => c.capability).map((c) => {
    const r = m.results[c.id]!
    const granted = r.status === 'pass'
    return {
      name: c.capability!,
      granted,
      ...(granted || r.status === 'unknown' ? {} : { missing: RBAC_RULES[c.capability!] }),
      ...(granted ? {} : { message: r.message }),
    }
  })
  const agentReported = m.results['agent.connected']?.status === 'pass'
  return {
    cluster: m.cluster,
    state,
    stateSince: m.stateSince,
    checkedAt: m.checkedAt,
    summary: summaryCounts(checks),
    ...(next
      ? {
          nextState: next,
          blockedBy: checks
            .filter((c) => c.severity === 'blocking' && c.gates === next && c.status !== 'pass')
            .map((c) => c.id),
        }
      : {}),
    ...(agentReported ? { capabilities: caps } : {}),
    ...(m.recheckRequestedAt ? { recheckRequestedAt: m.recheckRequestedAt } : {}),
    checks,
  }
}

function summaryCounts(checks: ReadinessCheck[]) {
  return {
    blocking: checks.filter((c) => c.severity === 'blocking' && c.status !== 'pass').length,
    warnings: checks.filter((c) => c.severity === 'warning' && (c.status === 'warn' || c.status === 'fail'))
      .length,
    unknown: checks.filter((c) => c.status === 'unknown').length,
  }
}

export function readinessSummary(m: ReadinessModel): S['ReadinessSummary'] {
  const checks = toChecks(m)
  const s = summaryCounts(checks)
  return { state: computeState(checks), blocking: s.blocking, warnings: s.warnings, checkedAt: m.checkedAt }
}

export const ARGOCD_IGNORE = `resource.customizations.ignoreDifferences.all: |
  managedFieldsManagers:
  - resource-optimizer
`

function helmSnippet(cluster: string, webhook: boolean, certManager: boolean) {
  const lines = [
    '# 1. Store the agent token (shown once when the token was created) in a Secret:',
    'kubectl create namespace resource-optimizer --dry-run=client -o yaml | kubectl apply -f -',
    `kubectl -n resource-optimizer create secret generic ${AGENT_SECRET} \\`,
    '  --from-literal=token="$RO_AGENT_TOKEN"',
    '',
    '# 2. Install the agent:',
    'helm upgrade --install resource-optimizer-agent oci://ghcr.io/oz/resource-optimizer/charts/agent \\',
    '  --version 0.4.2 -n resource-optimizer \\',
    `  --set clusterName=${cluster} \\`,
    `  --set hub.url=${HUB_URL} \\`,
    `  --set hub.tokenSecret.name=${AGENT_SECRET}`,
  ]
  if (webhook) {
    lines.push(
      '',
      '# 3. Optional: the mutating webhook (keeps requests on new pods):',
      'helm upgrade --install resource-optimizer-webhook oci://ghcr.io/oz/resource-optimizer/charts/webhook \\',
      `  --version 0.4.2 -n resource-optimizer --set certManager.enabled=${certManager ? 'true' : 'false'}`,
    )
  }
  return lines.join('\n')
}

export function installSnippet(
  cluster: string,
  format: InstallFormat,
  webhook: boolean,
  certManager: boolean,
): InstallSnippet {
  const notes = [
    `The token is not part of the snippet: create the Secret ${AGENT_SECRET} (key token) with the value shown once when the agent token was created.`,
    'Add the Argo CD snippet to argocd-cm if Argo CD manages the workloads (D-006).',
  ]
  if (format === 'helm')
    return {
      format,
      content: helmSnippet(cluster, webhook, certManager),
      filename: 'install.sh',
      argocdIgnoreDifferences: ARGOCD_IGNORE,
      notes,
    }
  if (format === 'values')
    return {
      format,
      filename: 'values.yaml',
      argocdIgnoreDifferences: ARGOCD_IGNORE,
      notes,
      content: [
        `clusterName: ${cluster}`,
        'hub:',
        `  url: ${HUB_URL}`,
        '  tokenSecret:',
        `    name: ${AGENT_SECRET}`,
        '    key: token',
        'readOnly: false',
        'pullInterval: 30s',
        ...(webhook ? ['webhook:', '  enabled: true', `  certManager: ${certManager}`] : []),
        '',
      ].join('\n'),
    }
  return {
    format,
    filename: `${cluster}-resource-optimizer.yaml`,
    argocdIgnoreDifferences: ARGOCD_IGNORE,
    notes,
    content: [
      'apiVersion: argoproj.io/v1alpha1',
      'kind: Application',
      'metadata:',
      `  name: resource-optimizer-agent-${cluster}`,
      '  namespace: argocd',
      'spec:',
      '  project: platform',
      '  destination:',
      `    name: ${cluster}`,
      '    namespace: resource-optimizer',
      '  source:',
      '    repoURL: ghcr.io/oz/resource-optimizer/charts',
      '    chart: agent',
      '    targetRevision: 0.4.2',
      '    helm:',
      '      valuesObject:',
      `        clusterName: ${cluster}`,
      '        hub:',
      `          url: ${HUB_URL}`,
      '          tokenSecret:',
      `            name: ${AGENT_SECRET}`,
      '  syncPolicy:',
      '    automated: { prune: true, selfHeal: true }',
      '    syncOptions: [CreateNamespace=true, ServerSideApply=true]',
      '',
    ].join('\n'),
  }
}

/** Metrics checks the hub runs from `POST /v1/admin/datasources/{id}/test`. */
export function metricsCheckResults(
  cluster: string,
  binding: MetricsBinding | undefined,
  knownValues: string[],
  now: number,
): {
  ok: boolean
  found?: { containerSeries: number; pods: number; nodes: number }
  checks: ReadinessCheck[]
} {
  const m: ReadinessModel = {
    cluster,
    results: {},
    checkedAt: new Date(now).toISOString().replace(/\.\d{3}Z$/, 'Z'),
    stateSince: '',
    history: [],
  }
  const ids = binding
    ? CATALOG.filter(
        (c) => c.source === 'hub' && c.id !== 'agent.connected' && c.id !== 'metrics.recommendations',
      ).map((c) => c.id)
    : ['datasource.reachable']
  const value = binding ? (binding.clusterLabel === '' ? '' : binding.clusterLabelValue || cluster) : ''
  const matches = !binding || binding.clusterLabel === '' || knownValues.includes(value)
  let h = 0
  for (const ch of value) h = (h * 31 + ch.charCodeAt(0)) >>> 0
  const pods = matches ? 80 + (h % 240) : 0
  for (const id of ids) {
    if (!matches && id !== 'datasource.reachable')
      m.results[id] =
        id === 'metrics.cluster-label'
          ? {
              status: 'fail',
              message: `No series with ${binding!.clusterLabel}="${value}". Existing values: ${knownValues.join(', ')}.`,
              details: { suggestions: knownValues.join(',') },
            }
          : { status: 'unknown', message: 'Not checked: the cluster label matches no series.' }
    else m.results[id] = healthy(id, Math.round(pods / 3))
  }
  if (matches && binding && h % 2 === 0)
    m.results['metrics.node-labels'] = {
      status: 'warn',
      message: 'kube_node_labels has no label_node_kubernetes_io_instance_type',
    }
  if (matches && binding)
    m.results['metrics.history'] = {
      status: 'warn',
      message: 'History 6 d of 14 d (production window).',
      details: { enoughDataAt: new Date(now + 8 * 86400_000).toISOString().replace(/\.\d{3}Z$/, 'Z') },
    }
  const checks = toChecks(m).filter((c) => ids.includes(c.id))
  return {
    ok: checks.every((c) => c.severity !== 'blocking' || c.status === 'pass'),
    ...(binding ? { found: { containerSeries: pods * 6, pods, nodes: matches ? 3 + (h % 21) : 0 } } : {}),
    checks,
  }
}
