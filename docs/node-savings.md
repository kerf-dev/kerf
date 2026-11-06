# Node savings: capacity, the node estimate and consolidation blockers

Lower requests reduce the bill only when nodes go away. Whether they do depends on the node autoscaler
(Karpenter or the Cluster Autoscaler) and on what stops it from removing a node. The **Capacity** tab of a
cluster page shows:

- **Capacity:** CPU and memory requested vs allocatable, now and after the open recommendations.
- **Node estimate:** how many nodes each node pool would need after the recommendations, and what they cost.
- **Consolidation blockers:** what is likely to keep nodes from being removed, with the object that causes it.

Kerf never creates, deletes, cordons or drains nodes. Karpenter or the Cluster Autoscaler do that; Kerf only
reads their state and runs alongside them.

## What the agent reads

Every 5 minutes (`capacity.interval`) the agent builds a report and sends it to the hub when it changed (and at
least every 30 minutes). Everything is read only:

| Object | Used for | Permission |
|---|---|---|
| Nodes | allocatable CPU and memory, instance type (`node.kubernetes.io/instance-type`), capacity type, node pool, the `karpenter.sh/do-not-disrupt` and `cluster-autoscaler.kubernetes.io/scale-down-disabled` annotations | already granted |
| Pods | requests per container (regular containers, sidecars, init containers, pod overhead), owner, node; blocking annotations and local storage | already granted |
| PodDisruptionBudgets | budgets that allow no disruption now | already granted |
| Karpenter `NodePool` (`karpenter.sh/v1`) | consolidation policy, disruption budgets, allowed instance and capacity types, `replicas` of static pools | `get`, `list` |
| Karpenter `NodeClaim` | the node pool of nodes not labelled yet | `get`, `list` |
| ConfigMap `kube-system/cluster-autoscaler-status` | node groups with their minimum and maximum size, health, scale-down status | `get` on that ConfigMap only |

A node's pool is its `karpenter.sh/nodepool` label; otherwise the first of these labels it has:
`eks.amazonaws.com/nodegroup`, `cloud.google.com/gke-nodepool`, `kubernetes.azure.com/agentpool`, `agentpool`,
`kops.k8s.io/instancegroup`, `kwok-nodegroup` (set your own list with `capacity.nodePoolLabels`). Such a pool
belongs to the Cluster Autoscaler when a node group of the same name (or, on EKS, `eks-<nodegroup>-<id>`) is in
its status ConfigMap. The Cluster Autoscaler writes that ConfigMap with `--write-status-configmap=true` (the
default); if yours lives elsewhere, set `capacity.clusterAutoscaler.statusConfigMap`.

Turn the report off with `capacity.enabled: false` (agent flag `--capacity=false`); the Capacity tab then says
that no report has arrived. The agent sends pod requests grouped by owner, node pool and shape, never pod
specs, environment variables or images.

## How the estimate works

The node estimate is an **estimate**, not a forecast of what the autoscaler will do. For each node pool:

1. **Pods after the recommendations.** Each pod's request is the sum of its containers' requests (with the
   latest recommendation of its workload where there is one), at least its largest init container (plus the
   sidecars started before it), plus the pod overhead. Every latest recommendation counts, whatever the
   workload's mode: the estimate shows what applying them would allow.
2. **Daemonset overhead.** Daemonset and static pods move with the node, not with the packing: their largest
   pod per daemonset (after recommendations) is reserved on every node of the pool.
3. **Instance types.** The candidates are the instance types (and capacity types) running in the pool, each
   with the smallest allocatable seen, restricted to the types the Karpenter `NodePool` allows when it lists
   them. Types the pool no longer allows still count in today's cost but are never chosen.
4. **First-fit decreasing.** Pods are sorted by size (CPU and memory relative to the largest type) and each is
   placed on the first node where it fits; a new node of the largest type is opened when none does. Each
   resulting node then gets the **cheapest type its pods fit**. A pod larger than every type is counted as one
   node of the largest type ("unplaced").
5. **Minimum.** A pool never goes below its minimum: the Cluster Autoscaler node group `minSize`, or
   `replicas` of a static Karpenter `NodePool`. The difference is filled with the cheapest type.
6. **Price.** An instance type's hourly price comes from the pricing service series
   `kerf_node_hourly_cost_usd` (by instance type and capacity type, over the last hour). Without a series the
   hub uses the cluster's per-core and per-GiB rates times the instance's allocatable (`rates`). Monthly =
   hourly × 730.

The estimated monthly savings are today's node cost minus the estimated node cost; negative means the
recommendations need more nodes (for example when they raise requests).

What the estimate does not model: pod affinity and anti-affinity, topology spread, taints and tolerations, host
ports, volume zones, the per-node pod limit, instance types not running in the pool today (Karpenter may pick a
cheaper one), spot interruptions, and the timing of consolidation. Treat it as an upper bound of what
consolidation can remove, and check the blockers.

## Consolidation blockers

Each blocker names its object (kind, namespace and name, with a link to the Kerf workload when there is one) and
a short fix:

| Blocker | Applies to | Why it blocks | Fix |
|---|---|---|---|
| PodDisruptionBudget at zero | both | `disruptionsAllowed` is 0, so pods covered by it cannot be evicted and their nodes cannot be drained | lower `minAvailable` or raise `maxUnavailable`, or add a replica |
| `karpenter.sh/do-not-disrupt` pod | Karpenter | Karpenter does not voluntarily disrupt a node running the pod | remove the annotation, or keep it only on pods that must finish |
| `karpenter.sh/do-not-disrupt` node | Karpenter | the node is never consolidated | remove the annotation from the node |
| `safe-to-evict=false` pod | Cluster Autoscaler | the Cluster Autoscaler does not remove the pod's node | remove the annotation if the pod can move |
| Local storage | Cluster Autoscaler | pods with an `emptyDir` on disk or a `hostPath` keep their node, unless marked safe to evict | annotate `cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes` with the volumes |
| Node pool minimum | both | the node group `minSize` (or static `replicas`) is above the nodes the estimate needs | lower the minimum if the nodes are not needed |
| Consolidation policy | Karpenter | `consolidationPolicy: WhenEmpty` removes only empty nodes; underused nodes stay | `WhenEmptyOrUnderutilized` |
| Disruption budget at 0 | Karpenter | a budget with `nodes: "0"` (always, or during its schedule) stops consolidation | raise `nodes` or narrow its schedule or reasons |
| `scale-down-disabled` node | Cluster Autoscaler | the node is never scaled down | remove the annotation |

Daemonset and static pods never block. Budgets whose `reasons` list only `Drifted` do not block consolidation
and are not listed. The list shows at most 200 blockers; the rest are counted.

## API

`GET /v1/clusters/{cluster}/capacity` (viewer) returns `ClusterCapacity`: `reportedAt` (absent when no report
arrived), `totals`, `estimate` (with `method` and `methodURL` pointing here), `pools` with their instance types,
and `blockers`. The agent sends the report as `capacity` in its workload status batch. Both are in the
[hub API](../api/openapi/hub.yaml).

## Not done yet

- **Pacing `auto-patch` by disruption budgets.** Kerf does not yet limit how many changes `auto-patch` starts in
  a cluster to what the Karpenter disruption budgets allow.
