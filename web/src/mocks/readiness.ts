/**
 * Cluster readiness for the mock API (docs/contracts/readiness.md). The catalog mirrors
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
  {
    id: 'metrics.dcgm',
    category: 'metrics',
    severity: 'warning',
    source: 'hub',
    title: 'NVIDIA DCGM GPU series',
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
    title: 'Argo CD ignores Kerf fields',
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

const DOCS = 'https://docs.kerf.dev/readiness'
export const HUB_URL = 'https://ro-hub.platform.internal'
export const AGENT_SECRET = 'kerf-agent-token'

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
    '{"apiGroups":["kerf.getkerf.dev"],"resources":["rightsizingpolicies"],"verbs":["create","update","delete"]}',
  ],
  events: ['{"apiGroups":["","events.k8s.io"],"resources":["events"],"verbs":["create"]}'],
}

function rbacRemediation(cap: AgentCapability['name']) {
  const rules = RBAC_RULES[cap].map(
    (r) => JSON.parse(r) as { apiGroups: string[]; resources: string[]; verbs: string[] },
  )
  return (
    "kubectl patch clusterrole kerf-agent --type=json -p '[" +
    rules.map((r) => `{"op":"add","path":"/rules/-","value":${JSON.stringify(r)}}`).join(',') +
    "]'\n# or upgrade the agent chart without readOnly: helm upgrade kerf-agent … --set readOnly=false"
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
        remediation: `helm upgrade kerf-agent oci://ghcr.io/kerf-dev/kerf/charts/agent --version 0.4.2 -n kerf --reuse-values`,
        remediationType: 'command',
      }
    case 'agent.crds':
      return {
        remediation:
          'kubectl apply --server-side -f https://github.com/kerf-dev/kerf/releases/download/v0.4.2/crds.yaml',
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
    case 'metrics.dcgm':
      return {
        remediation:
          'Run the NVIDIA DCGM exporter on the GPU nodes and scrape it (job "dcgm-exporter", port 9400):\nhelm upgrade --install dcgm-exporter gpu-helm-charts/dcgm-exporter -n gpu-operator\n# GPU workloads show "No data" until the series reach the datasource.',
        remediationType: 'command',
      }
    case 'metrics.history':
      return {
        remediation: 'Nothing to do: the check passes once the metrics cover the profile window.',
        remediationType: 'text',
      }
    case 'webhook.healthy':
      return {
        remediation:
          'helm upgrade --install kerf-webhook oci://ghcr.io/kerf-dev/kerf/charts/webhook -n kerf --set certManager.enabled=true',
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
  /** replaces the catalog remediation of this check (datasource-specific wording) */
  remediation?: { remediation: string; remediationType: 'command' | 'yaml' | 'text' }
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
      return PASS('rightsizingpolicies.kerf.getkerf.dev v1alpha1 installed (stored version current).')
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
    case 'metrics.dcgm':
      return PASS(
        'DCGM_FI_DEV_GPU_UTIL, DCGM_FI_DEV_FB_USED and DCGM_FI_DEV_FB_FREE present for every GPU pod.',
      )
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
      return PASS('argocd-cm ignores fields managed by kerf.')
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
      message: 'Argo CD is installed and argocd-cm has no managedFieldsManagers entry for kerf.',
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
      message: 'SelfSubjectAccessReview denied create, update, delete rightsizingpolicies.kerf.getkerf.dev.',
    }
    results['metrics.node-labels'] = {
      status: 'warn',
      message:
        'kube_node_labels has no label_node_kubernetes_io_instance_type or label_karpenter_sh_capacity_type: pricing falls back to default rates.',
      details: { missing: 'instance-type,capacity-type' },
    }
    results['metrics.dcgm'] = {
      status: 'warn',
      message: 'DCGM exporter series missing: DCGM_FI_DEV_GPU_UTIL, DCGM_FI_DEV_FB_USED, DCGM_FI_DEV_FB_FREE',
      details: { missing: 'DCGM_FI_DEV_GPU_UTIL,DCGM_FI_DEV_FB_USED,DCGM_FI_DEV_FB_FREE' },
    }
    results['pricing.coverage'] = {
      status: 'warn',
      message: '6 of 18 nodes priced with default rates (unknown instance type).',
      details: { priced: '12', nodes: '18' },
    }
    results['webhook.healthy'] = {
      status: 'fail',
      message: 'MutatingWebhookConfiguration kerf has no caBundle; new pods are not mutated.',
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
        'metrics.dcgm',
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
    const rem = r.status === 'pass' ? undefined : (r.remediation ?? remediationFor(c.id, m.cluster))
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
  // Like the hub: agent checks that never reported have no row in `checks`; blocking ones
  // still appear in `blockedBy` and in `summary.unknown`.
  const reported = checks.filter((c) => !(c.source === 'agent' && c.status === 'unknown'))
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
    checks: reported,
  }
}

function summaryCounts(checks: ReadinessCheck[]) {
  return {
    // Failing blocking checks only; blocking checks without a result are `unknown` (hub semantics).
    blocking: checks.filter((c) => c.severity === 'blocking' && (c.status === 'fail' || c.status === 'warn'))
      .length,
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
  - kerf
`

const MOCK_CHART = 'oci://ghcr.io/kerf-dev/kerf/charts/kerf'

/** The values of the umbrella chart in mode spoke, as the hub renders them. */
function spokeValues(cluster: string, webhook: boolean, indent = ''): string[] {
  return [
    'global:',
    '  mode: spoke',
    `  clusterName: ${cluster}`,
    '  imageRegistry: ghcr.io',
    'agent:',
    '  hub:',
    `    url: ${HUB_URL}`,
    '    tokenSecret:',
    `      name: ${AGENT_SECRET}`,
    '      key: token',
    '      create: false',
    '  replicaCount: 2',
    '  leaderElection: true',
    '  readOnly: false',
    ...(webhook ? [] : ['webhook:', '  enabled: false']),
  ].map((l) => indent + l)
}

function helmSnippet(cluster: string, webhook: boolean, certManager: boolean) {
  const lines = [
    '#!/bin/sh',
    'set -eu',
    `# Kerf (chart kerf, global.mode spoke) for cluster ${cluster}.`,
    '# KERF_AGENT_TOKEN: the agent token shown once when the cluster was created (or POST /v1/admin/tokens).',
    ': "${KERF_AGENT_TOKEN:?set KERF_AGENT_TOKEN to the agent token}"',
    '',
    'kubectl create namespace kerf --dry-run=client -o yaml | kubectl apply -f -',
    `kubectl -n kerf create secret generic ${AGENT_SECRET} --from-literal=token="$KERF_AGENT_TOKEN" --dry-run=client -o yaml | kubectl apply -f -`,
    '',
  ]
  if (webhook && certManager)
    lines.push(
      'helm upgrade --install cert-manager oci://quay.io/jetstack/charts/cert-manager --version v1.19.1 -n cert-manager --create-namespace --set crds.enabled=true --wait',
      '',
    )
  lines.push(
    `helm upgrade --install kerf ${MOCK_CHART} --version 0.4.2 \\`,
    "  -n kerf --create-namespace --wait --timeout 10m -f - <<'KERF_VALUES'",
    ...spokeValues(cluster, webhook),
    'KERF_VALUES',
    '',
  )
  return lines.join('\n')
}

export function installSnippet(
  cluster: string,
  format: InstallFormat,
  webhook: boolean,
  certManager: boolean,
): InstallSnippet {
  const notes = [
    `The agent token is not in this snippet: create the Secret ${AGENT_SECRET} (key token) in namespace kerf with the token shown once when the cluster was created.`,
    'This installs the umbrella chart kerf in mode spoke with the settings of this cluster. docs/install/eks.md step 5.3 adds the production extras.',
    "If Argo CD manages the workloads, add the argocdIgnoreDifferences entry to argocd-cm so it does not revert the agent's changes.",
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
      filename: `values-${cluster}.yaml`,
      argocdIgnoreDifferences: ARGOCD_IGNORE,
      notes: [
        ...notes,
        `helm upgrade --install kerf ${MOCK_CHART} --version 0.4.2 -n kerf --create-namespace -f values-${cluster}.yaml --wait --timeout 10m`,
      ],
      content: [
        `# Kerf (chart kerf, global.mode spoke) values for cluster ${cluster}.`,
        `# The token is read from the Secret ${AGENT_SECRET} (key token); it is not in this file.`,
        ...spokeValues(cluster, webhook),
        '',
      ].join('\n'),
    }
  return {
    format,
    filename: `kerf-${cluster}.yaml`,
    argocdIgnoreDifferences: ARGOCD_IGNORE,
    notes,
    content: [
      `# Argo CD Application for cluster ${cluster} (chart kerf, global.mode spoke).`,
      `# The token is read from the Secret ${AGENT_SECRET} (key token); it is not in this file.`,
      'apiVersion: argoproj.io/v1alpha1',
      'kind: Application',
      'metadata:',
      `  name: kerf-${cluster}`,
      '  namespace: argocd',
      '  labels:',
      '    app.kubernetes.io/part-of: kerf',
      'spec:',
      '  project: default',
      '  source:',
      '    repoURL: ghcr.io/kerf-dev/kerf/charts',
      '    chart: kerf',
      '    targetRevision: 0.4.2',
      '    helm:',
      '      releaseName: kerf',
      '      valuesObject:',
      ...spokeValues(cluster, webhook, '        '),
      '  destination:',
      '    server: https://kubernetes.default.svc',
      '    namespace: kerf',
      '  ignoreDifferences:',
      "    - group: '*'",
      "      kind: '*'",
      '      managedFieldsManagers: [kerf]',
      ...(webhook
        ? [
            '    - group: admissionregistration.k8s.io',
            '      kind: MutatingWebhookConfiguration',
            "      jqPathExpressions: ['.webhooks[]?.clientConfig.caBundle']",
          ]
        : []),
      '  syncPolicy:',
      '    automated: { prune: true, selfHeal: true }',
      '    syncOptions: [CreateNamespace=true, ServerSideApply=true, RespectIgnoreDifferences=true]',
      '',
    ].join('\n'),
  }
}

/** The environment a mock Dynatrace datasource reads. */
export const DYNATRACE_ENV_URL = 'https://abc12345.apps.dynatrace.com'

const TEXT = (remediation: string) => ({ remediation, remediationType: 'text' as const })

/**
 * Hub-side metrics checks of a cluster bound to a Dynatrace datasource: Dynatrace wording and
 * remediation, no Prometheus, kube-state-metrics or scrape-config steps.
 */
export function dynatraceResults(cluster: string): Record<string, CheckResult> {
  return {
    'datasource.reachable': PASS(`HTTP 200 in 63 ms from ${DYNATRACE_ENV_URL}.`, { httpStatus: '200' }),
    'metrics.cluster-label': PASS(`k8s.cluster.name="${cluster}" matches 1,987 container series.`),
    'metrics.cadvisor': PASS(
      '1,912 dt.kubernetes.container.cpu_usage and 1,987 dt.kubernetes.container.memory_working_set series.',
    ),
    'metrics.ksm-owners': PASS('k8s.workload.kind and k8s.workload.name dimensions cover 412 containers.'),
    'metrics.ksm-resources': PASS(
      'dt.kubernetes.container.requests_cpu, requests_memory, limits_cpu and limits_memory present.',
    ),
    'metrics.freshness': PASS('Last sample 42 s ago.'),
    'metrics.owner-join': PASS('99.1 % of containers carry a workload owner.'),
    'metrics.scrape-interval': PASS('Median resolution 60 s.'),
    'metrics.history': PASS('History 35 d ≥ 28 d (high-availability window).'),
    'metrics.throttling': PASS(
      'derived throttling ratio (dt.containers.cpu.throttling_ratio) for 412 containers: Dynatrace has no CFS period counters, so recommendations mark throttling as derived',
    ),
    'metrics.restarts-oom': {
      status: 'warn',
      message: 'dt.kubernetes.container.oom_kills missing: the OOM guardrail is blind',
      details: { missing: 'dt.kubernetes.container.oom_kills' },
      remediation: TEXT(
        'Enable Kubernetes monitoring for the cluster in Dynatrace (Dynatrace Operator with a DynaKube that monitors the Kubernetes platform) so dt.kubernetes.container.restarts and dt.kubernetes.container.oom_kills are recorded.',
      ),
    },
    'metrics.hpa': {
      status: 'unknown',
      message:
        'HPA coupling not available: Dynatrace records no HorizontalPodAutoscaler metrics, so Kerf cannot tell which workloads an HPA scales',
      remediation: TEXT(
        'Review the recommendations of HPA-scaled workloads before you automate them, or exclude those workloads with a policy. Kerf does not guess HPA coupling for a Dynatrace datasource.',
      ),
    },
    'metrics.node-labels': PASS(
      'Smartscape K8S_NODE labels carry instance type, capacity type, zone and region.',
    ),
    'metrics.node-allocatable': PASS(
      'dt.kubernetes.node.cpu_allocatable and memory_allocatable present for every node.',
    ),
    'metrics.dcgm': {
      status: 'unknown',
      message: 'GPU metrics are not read from a Dynatrace datasource',
      remediation: TEXT(
        'Nothing to do: GPU workloads of a cluster read from Dynatrace show "No data" for GPU metrics.',
      ),
    },
  }
}

type MetricStatus = S['DatasourceMetricStatus']

/** The Kerf inputs of a Dynatrace datasource, the source they are read from and what was found. */
export function dynatraceMetrics(found: boolean): MetricStatus[] {
  const st = (n: number): Pick<MetricStatus, 'status' | 'series'> =>
    found ? { status: 'found', series: n } : { status: 'missing', series: 0 }
  return [
    {
      input: 'container_cpu_usage_seconds_total',
      sourceMetric: 'dt.kubernetes.container.cpu_usage',
      usedFor: 'CPU usage percentiles',
      ...st(1912),
    },
    {
      input: 'container_memory_working_set_bytes',
      sourceMetric: 'dt.kubernetes.container.memory_working_set',
      usedFor: 'Memory usage percentiles',
      ...st(1987),
    },
    {
      input: 'kube_pod_container_resource_requests',
      sourceMetric: 'dt.kubernetes.container.requests_cpu, requests_memory',
      usedFor: 'Current requests',
      ...st(1987),
    },
    {
      input: 'kube_pod_container_resource_limits',
      sourceMetric: 'dt.kubernetes.container.limits_cpu, limits_memory',
      usedFor: 'Current limits',
      ...st(1840),
    },
    {
      input: 'kube_pod_owner',
      sourceMetric: 'k8s.workload.kind, k8s.workload.name',
      usedFor: 'Pod to workload join',
      ...st(412),
    },
    {
      input: 'kube_node_labels',
      sourceMetric: 'Smartscape K8S_NODE labels',
      usedFor: 'Node pricing',
      ...st(24),
    },
    {
      input: 'kube_node_status_allocatable',
      sourceMetric: 'dt.kubernetes.node.cpu_allocatable, memory_allocatable',
      usedFor: 'Node capacity',
      ...st(24),
    },
    {
      input: 'container_cpu_cfs_throttled_periods_total',
      sourceMetric: 'dt.containers.cpu.throttling_ratio',
      usedFor: 'Throttling guardrail',
      ...st(412),
      note: 'Derived ratio: Dynatrace has no CFS period counters.',
    },
    {
      input: 'kube_pod_container_status_restarts_total',
      sourceMetric: 'dt.kubernetes.container.restarts',
      usedFor: 'Restart and OOM guardrail',
      status: 'missing',
      series: 0,
    },
    {
      input: 'kube_pod_container_status_last_terminated_reason',
      sourceMetric: 'dt.kubernetes.container.oom_kills',
      usedFor: 'OOM guardrail',
      status: 'missing',
      series: 0,
    },
    {
      input: 'kube_horizontalpodautoscaler_*',
      usedFor: 'HPA coupling',
      status: 'notAvailable',
      note: 'Dynatrace records no HorizontalPodAutoscaler metrics.',
    },
    {
      input: 'DCGM_FI_DEV_*',
      usedFor: 'GPU recommendations',
      status: 'notAvailable',
      note: 'GPU series are not read from a Dynatrace datasource.',
    },
    {
      input: 'kube_pod_init_container_*',
      usedFor: 'Init container sizing',
      status: 'notAvailable',
      note: 'Init containers are not distinguished from app containers.',
    },
  ]
}

/** Metrics checks the hub runs from `POST /v1/admin/datasources/{id}/test`. */
export function metricsCheckResults(
  cluster: string,
  binding: MetricsBinding | undefined,
  knownValues: string[],
  now: number,
  dynatrace = false,
): {
  ok: boolean
  found?: { containerSeries: number; pods: number; nodes: number }
  metrics?: MetricStatus[]
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
  if (dynatrace && matches) {
    const dt = dynatraceResults(value || cluster)
    for (const id of ids) if (dt[id]) m.results[id] = dt[id]
  }
  if (!dynatrace && matches && binding && h % 2 === 0)
    m.results['metrics.node-labels'] = {
      status: 'warn',
      message: 'kube_node_labels has no label_node_kubernetes_io_instance_type',
    }
  if (!dynatrace && matches && binding)
    m.results['metrics.history'] = {
      status: 'warn',
      message: 'History 6 d of 14 d (production window).',
      details: { enoughDataAt: new Date(now + 8 * 86400_000).toISOString().replace(/\.\d{3}Z$/, 'Z') },
    }
  const checks = toChecks(m).filter((c) => ids.includes(c.id))
  return {
    ok: checks.every((c) => c.severity !== 'blocking' || c.status === 'pass'),
    ...(dynatrace ? { metrics: dynatraceMetrics(matches) } : {}),
    ...(binding && !dynatrace
      ? { found: { containerSeries: pods * 6, pods, nodes: matches ? 3 + (h % 21) : 0 } }
      : {}),
    checks,
  }
}
