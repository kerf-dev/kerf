/**
 * Mock `GET /v1/clusters/{cluster}/capacity`: a Karpenter cluster with blockers of several types,
 * a Cluster Autoscaler cluster with no blockers, and a cluster that never sent a report.
 * All names are synthetic.
 */
import { http, HttpResponse } from 'msw'
import type { CapacityAmounts, ClusterCapacity, ConsolidationBlocker } from '@/api/types'
import type { MockDb } from './data'
import { iso } from './data'
import { gate, problem } from './core'

const GiB = 1024 ** 3
const amt = (cpuCores: number, gib: number): CapacityAmounts => ({ cpuCores, memoryBytes: gib * GiB })

/** A real workload of the mock fleet, so the blocker links open a workload page. */
function workloadIn(db: MockDb, cluster: string, namespace: string) {
  return (
    db.workloads.find((w) => w.cluster === cluster && w.namespace === namespace && w.kind === 'Deployment') ??
    db.workloads.find((w) => w.cluster === cluster && w.kind === 'Deployment')
  )
}

function karpenter(db: MockDb, cluster: string): ClusterCapacity {
  const a = workloadIn(db, cluster, 'payments')
  const b = workloadIn(db, cluster, 'gateway')
  const ref = (w: typeof a) =>
    w
      ? {
          workloadId: w.id,
          object: { apiVersion: 'apps/v1', kind: w.kind, namespace: w.namespace, name: w.name },
        }
      : { object: { kind: 'Deployment', namespace: 'demo', name: 'sample-app' } }
  const blockers: ConsolidationBlocker[] = [
    {
      type: 'pdb-zero-disruptions',
      workloadId: ref(a).workloadId,
      object: {
        apiVersion: 'policy/v1',
        kind: 'PodDisruptionBudget',
        namespace: a?.namespace ?? 'demo',
        name: `${a?.name ?? 'sample-app'}-pdb`,
      },
      nodePool: 'general',
      message: 'Its disruption budget allows 0 disruptions, so its pods cannot be moved.',
      fix: 'Allow at least 1 disruption, or run more replicas than the budget requires.',
    },
    {
      type: 'do-not-disrupt-pod',
      object: { apiVersion: 'v1', kind: 'Pod', namespace: 'demo', name: 'sample-worker-0' },
      nodePool: 'general',
      message: 'The pod has the karpenter.sh/do-not-disrupt annotation.',
      fix: 'Remove the annotation when the pod can be restarted.',
    },
    {
      type: 'safe-to-evict-false',
      ...ref(b),
      object: {
        apiVersion: 'v1',
        kind: 'Pod',
        namespace: b?.namespace ?? 'demo',
        name: `${b?.name ?? 'sample-app'}-7c9d5-x2kqp`,
      },
      nodePool: '',
      message:
        'Has cluster-autoscaler.kubernetes.io/safe-to-evict=false: the Cluster Autoscaler does not remove node node-system-1.',
      fix: 'Remove the annotation if the pod can be rescheduled.',
    },
    {
      type: 'local-storage',
      object: { apiVersion: 'v1', kind: 'Pod', namespace: 'demo', name: 'sample-cache-1' },
      nodePool: '',
      message:
        'Uses local storage (emptyDir or hostPath): the Cluster Autoscaler does not remove node node-system-2.',
      fix: 'Annotate the pod with cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes listing the volumes, or safe-to-evict=true if the data can be lost.',
    },
    {
      type: 'do-not-disrupt-node',
      object: { apiVersion: 'v1', kind: 'Node', name: 'node-sample-0a1b' },
      nodePool: 'general',
      message: 'The node has the karpenter.sh/do-not-disrupt annotation.',
      fix: 'Remove the annotation from the node.',
    },
    {
      type: 'node-pool-minimum',
      object: { apiVersion: 'karpenter.sh/v1', kind: 'NodePool', name: 'general' },
      nodePool: 'general',
      message: 'The pool limits would be breached by removing more nodes.',
      fix: 'Lower the pool minimum.',
    },
    {
      type: 'disruption-budget-zero',
      object: { apiVersion: 'karpenter.sh/v1', kind: 'NodePool', name: 'spot-batch' },
      nodePool: 'spot-batch',
      message: 'The node pool budget allows 0 node disruptions right now.',
      fix: 'Raise the budget in spec.disruption.budgets, or wait for its schedule window to close.',
    },
    {
      type: 'consolidation-policy',
      object: { apiVersion: 'karpenter.sh/v1', kind: 'NodePool', name: 'general' },
      nodePool: 'general',
      message: 'The pool only consolidates empty nodes.',
      fix: 'Set consolidationPolicy to WhenEmptyOrUnderutilized.',
    },
  ]
  return {
    cluster,
    reportedAt: iso(db.now - 3 * 60_000),
    observedAt: iso(db.now - 3 * 60_000 - 20_000),
    autoscalers: [
      {
        kind: 'karpenter',
        object: { apiVersion: 'karpenter.sh/v1', kind: 'NodePool', name: 'general' },
      },
      // The nodes outside Karpenter belong to a Cluster Autoscaler node group.
      { kind: 'cluster-autoscaler', status: 'Healthy', scaleDown: 'NoCandidates' },
    ],
    totals: {
      nodes: 24,
      allocatable: amt(189.6, 706),
      requested: amt(132.4, 468),
      requestedAfter: amt(96.1, 402),
    },
    estimate: {
      method: 'first-fit-decreasing',
      methodURL: 'https://github.com/kerf-dev/kerf/blob/main/docs/node-savings.md',
      currentNodes: 24,
      estimatedNodes: 18,
      currentMonthlyUSD: 5240.4,
      estimatedMonthlyUSD: 3981.7,
      savingsMonthlyUSD: 1258.7,
      recommendations: 47,
      priceSource: 'mixed',
    },
    pools: [
      {
        name: 'general',
        autoscaler: 'karpenter',
        object: { apiVersion: 'karpenter.sh/v1', kind: 'NodePool', name: 'general' },
        nodes: 14,
        estimatedNodes: 10,
        minNodes: 3,
        allocatable: amt(110.6, 413),
        requested: amt(81.2, 292),
        requestedAfter: amt(58.9, 251),
        daemonSetOverhead: amt(0.4, 0.6),
        currentMonthlyUSD: 3358.2,
        estimatedMonthlyUSD: 2398.7,
        consolidationPolicy: 'WhenEmpty',
        instanceTypes: [
          {
            instanceType: 'm6i.2xlarge',
            capacityType: 'on-demand',
            nodes: 10,
            estimatedNodes: 7,
            allocatable: amt(7.9, 29.5),
            hourlyUSD: 0.384,
            priceSource: 'metrics',
          },
          {
            instanceType: 'm6i.xlarge',
            capacityType: 'on-demand',
            nodes: 4,
            estimatedNodes: 3,
            allocatable: amt(3.9, 14.5),
            hourlyUSD: 0.192,
            priceSource: 'rates',
          },
        ],
      },
      {
        name: 'spot-batch',
        autoscaler: 'karpenter',
        object: { apiVersion: 'karpenter.sh/v1', kind: 'NodePool', name: 'spot-batch' },
        nodes: 8,
        estimatedNodes: 6,
        allocatable: amt(63, 252),
        requested: amt(43.5, 160),
        requestedAfter: amt(31.2, 137),
        daemonSetOverhead: amt(0.4, 0.6),
        currentMonthlyUSD: 1346.2,
        estimatedMonthlyUSD: 1010.0,
        consolidationPolicy: 'WhenEmptyOrUnderutilized',
        unplaced: 2,
        instanceTypes: [
          {
            instanceType: 'c6a.2xlarge',
            capacityType: 'spot',
            nodes: 8,
            estimatedNodes: 6,
            allocatable: amt(7.9, 15.5),
            hourlyUSD: 0.0912,
            priceSource: 'metrics',
          },
        ],
      },
      {
        name: '',
        autoscaler: 'none',
        nodes: 2,
        estimatedNodes: 2,
        allocatable: amt(16, 41),
        requested: amt(7.7, 16),
        requestedAfter: amt(6, 14),
        currentMonthlyUSD: 536,
        estimatedMonthlyUSD: 573,
        instanceTypes: [
          {
            instanceType: 'm6i.xlarge',
            capacityType: 'on-demand',
            nodes: 2,
            estimatedNodes: 2,
            allocatable: amt(3.9, 14.5),
          },
        ],
      },
    ],
    blockers,
    blockersOmitted: 12,
  }
}

function clusterAutoscaler(db: MockDb, cluster: string): ClusterCapacity {
  return {
    cluster,
    reportedAt: iso(db.now - 26 * 3600_000),
    autoscalers: [
      {
        kind: 'cluster-autoscaler',
        status: 'Healthy',
        scaleDown: 'NoCandidates',
        lastProbeTime: iso(db.now - 60_000),
      },
    ],
    totals: { nodes: 18, allocatable: amt(142, 540), requested: amt(88, 330), requestedAfter: amt(90, 336) },
    estimate: {
      method: 'first-fit-decreasing',
      methodURL: 'https://github.com/kerf-dev/kerf/blob/main/docs/node-savings.md',
      currentNodes: 18,
      estimatedNodes: 19,
      currentMonthlyUSD: 2910,
      estimatedMonthlyUSD: 3012,
      savingsMonthlyUSD: -102,
      recommendations: 12,
      priceSource: 'rates',
    },
    pools: [
      {
        name: 'eks-workers',
        autoscaler: 'cluster-autoscaler',
        nodes: 18,
        estimatedNodes: 19,
        minNodes: 6,
        allocatable: amt(142, 540),
        requested: amt(88, 330),
        requestedAfter: amt(90, 336),
        currentMonthlyUSD: 2910,
        estimatedMonthlyUSD: 3012,
        instanceTypes: [
          {
            instanceType: 'c6a.2xlarge',
            capacityType: 'on-demand',
            nodes: 18,
            estimatedNodes: 19,
            allocatable: amt(7.9, 15.5),
            hourlyUSD: 0.2214,
            priceSource: 'rates',
          },
        ],
      },
    ],
    blockers: [],
    truncated: true,
  }
}

export function capacityFor(db: MockDb, cluster: string): ClusterCapacity | undefined {
  const spec = db.clusterSpecs.find((c) => c.name === cluster)
  if (!spec) return undefined
  if (cluster === 'prod-eu-west-1') return karpenter(db, cluster)
  if (cluster === 'prod-us-east-1') return clusterAutoscaler(db, cluster)
  return {
    cluster,
    autoscalers: [],
    totals: { nodes: 0, allocatable: amt(0, 0), requested: amt(0, 0), requestedAfter: amt(0, 0) },
    pools: [],
    blockers: [],
  }
}

export const capacityHandlers = [
  http.get('*/v1/clusters/:cluster/capacity', async ({ request, params }) => {
    const g = await gate(request)
    if (g instanceof Response) return g
    const body = capacityFor(g.db, String(params.cluster))
    if (!body) return problem(404, 'not_found', 'Not Found', `cluster ${String(params.cluster)} not found`)
    return HttpResponse.json(body)
  }),
]
