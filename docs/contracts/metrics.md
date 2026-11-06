# Metrics contract

Changing a name or label here is a contract change (see [CONTRIBUTING.md](../../CONTRIBUTING.md#before-you-start)).

All queries must be valid PromQL and run on both Prometheus and VictoriaMetrics.

## Rules for every series

- **`cluster` label is mandatory** on every series and equals the cluster name used in the hub API
  path (`/v1/clusters/{cluster}/...`), e.g. `ro-spoke-1`.
  - Input series from a spoke get it from vmagent `externalLabels: {cluster: <name>}`.
  - `kerf_*` series exported by hub-side components (pricing, hub) set `cluster` themselves, one value
    per spoke. The hub-side scrape config must use `honor_labels: true` and must not add or
    overwrite a `cluster` label for these jobs; otherwise `cluster` becomes `exported_cluster`.
- Our own series use the `kerf_` prefix. Base units only: seconds, bytes, cores, USD.
- Workload identity labels, wherever a series is about a workload:
  `namespace`, `workload_kind` (`Deployment|StatefulSet|DaemonSet|CronJob`), `workload` (name),
  and `container` when per container.
- Components may export extra operational series under their own prefix without a contract change:
  `kerf_hub_*`, `kerf_recommender_*`, `kerf_pricing_*`, `kerf_agent_*`, `kerf_webhook_*`, `kerf_health_*`.
  Everything else under `kerf_` is listed in this file.

## 1. Input series (spokes → hub VictoriaMetrics)

Scraped on each spoke and remote-written to the hub. Synthetic data must produce the
same names and labels, so the hub cannot tell the difference.

### cAdvisor (kubelet `/metrics/cadvisor`)

Filter real containers with `container!="", container!="POD"`.

| Series | Type | Labels used | Used for |
|---|---|---|---|
| `container_cpu_usage_seconds_total` | counter | cluster, namespace, pod, container | CPU usage (`rate(...[5m])`) |
| `container_memory_working_set_bytes` | gauge | cluster, namespace, pod, container | memory usage |
| `container_cpu_cfs_periods_total` | counter | cluster, namespace, pod, container | throttling ratio denominator |
| `container_cpu_cfs_throttled_periods_total` | counter | cluster, namespace, pod, container | throttling ratio numerator |
| `container_oom_events_total` | counter | cluster, namespace, pod, container | OOM kills (optional; KSM below is authoritative) |

cAdvisor series carry **no** workload label; the workload is joined from KSM (section 4).

### kube-state-metrics

KSM must run with
`--metric-labels-allowlist=nodes=[node.kubernetes.io/instance-type,karpenter.sh/capacity-type,topology.kubernetes.io/region,topology.kubernetes.io/zone]`.

| Series | Labels used | Used for |
|---|---|---|
| `kube_pod_container_resource_requests` | cluster, namespace, pod, container, node, resource (`cpu`\|`memory`), unit | current requests |
| `kube_pod_container_resource_limits` | same | current limits |
| `kube_pod_owner` | cluster, namespace, pod, owner_kind, owner_name | pod → ReplicaSet/StatefulSet/DaemonSet/Job |
| `kube_replicaset_owner` | cluster, namespace, replicaset, owner_kind, owner_name | ReplicaSet → Deployment |
| `kube_job_owner` | cluster, namespace, job_name, owner_kind, owner_name | Job → CronJob |
| `kube_pod_info` | cluster, namespace, pod, node | pod → node |
| `kube_pod_init_container_info` | cluster, namespace, pod, container | init/sidecar detection |
| `kube_pod_container_info` | cluster, namespace, pod, container, image | JVM/image detection |
| `kube_pod_container_status_restarts_total` | cluster, namespace, pod, container | `restarting` flag, rollback |
| `kube_pod_container_status_last_terminated_reason` | cluster, namespace, pod, container, reason (`OOMKilled`) | `oom-killed` flag, rollback |
| `kube_deployment_spec_replicas` | cluster, namespace, deployment | replicas |
| `kube_statefulset_replicas` | cluster, namespace, statefulset | replicas |
| `kube_daemonset_status_desired_number_scheduled` | cluster, namespace, daemonset | replicas |
| `kube_horizontalpodautoscaler_info` | cluster, namespace, horizontalpodautoscaler, scaletargetref_kind, scaletargetref_name | HPA target |
| `kube_horizontalpodautoscaler_spec_target_metric` | cluster, namespace, horizontalpodautoscaler, metric_name, metric_target_type | `hpa-coupled` (metric_name `cpu`/`memory`, target type `utilization`) |
| `kube_node_labels` | cluster, node, label_node_kubernetes_io_instance_type, label_karpenter_sh_capacity_type, label_topology_kubernetes_io_region, label_topology_kubernetes_io_zone | pricing |
| `kube_node_status_allocatable` | cluster, node, resource (`cpu`\|`memory`), unit | pricing cost split |

Capacity type values follow Karpenter: `on-demand`, `spot`.

## 2. Pricing series (exported by the pricing service)

One series per spoke node. All are gauges, in USD per hour, for the current price.

| Series | Meaning |
|---|---|
| `kerf_node_hourly_cost_usd` | price of the whole node |
| `kerf_cpu_core_hourly_cost_usd` | CPU share of the node price, per vCPU (allocatable) |
| `kerf_memory_gib_hourly_cost_usd` | memory share of the node price, per GiB (allocatable) |

Labels (all three): `cluster`, `node`, `instance_type`, `capacity_type` (`on-demand`\|`spot`),
`region`, `zone`, `provider` (`aws`\|`static`).

Invariant: `kerf_cpu_core_hourly_cost_usd * allocatable_cores + kerf_memory_gib_hourly_cost_usd * allocatable_gib ≈ kerf_node_hourly_cost_usd`.

## 3. Hub series (exported by the hub)

Workload labels below = `cluster, namespace, workload_kind, workload`.

### Recommendations (latest recommendation per workload; series disappear when a workload is gone)

| Series | Type | Extra labels | Meaning |
|---|---|---|---|
| `kerf_recommendation_cpu_cores` | gauge | container, `type` (`request`\|`limit`), `source` (`current`\|`recommended`) | CPU in cores. No series for an unset or removed limit. |
| `kerf_recommendation_memory_bytes` | gauge | container, `type`, `source` | memory in bytes |
| `kerf_recommendation_monthly_savings_usd` | gauge | container, `resource` (`cpu`\|`memory`) | estimated monthly savings; negative for increases |
| `kerf_recommendation_confidence` | gauge | – | 0–1 |
| `kerf_recommendation_data_coverage_ratio` | gauge | – | 0–1, fraction of the window with data |
| `kerf_recommendation_risk_flag` | gauge | `flag` (hub API `RiskFlag` values) | 1 while the flag is set; no series otherwise |
| `kerf_recommendation_generated_timestamp_seconds` | gauge | – | unix time the recommendation was generated |
| `kerf_recommendation_average_replicas` | gauge | – | replicas used for savings |

### Modes and actions (from agent reports)

| Series | Type | Extra labels | Meaning |
|---|---|---|---|
| `kerf_workload_mode` | gauge | `resource` (`cpu`\|`memory`), `mode` (`off`\|`audit`\|`auto-patch`), `source` (`global-default`\|`cluster-default`\|`policy`\|`annotation`) | 1 for the effective mode. Workloads the agent never reported have `mode="audit"`, `source="global-default"`. |
| `kerf_workload_phase` | gauge | `phase` (`Pending`\|`Applying`\|`Applied`\|`Blocked`\|`RolledBack`\|`Released`\|`Deferred`) | 1 for the current phase |
| `kerf_actions_total` | counter | `type` (event type), `strategy`, `resource` (`cpu`\|`memory`\|`""`), `reason` | incremented once per event stored by the hub (duplicates are not counted); one increment per resource listed in the event. Carries the workload labels like every hub series (the event's workload). |

`kerf_actions_total` lives in the hub process and resets on restart; always use `increase()`/`rate()`.

### Cluster readiness ([readiness.md](readiness.md))

Labels: `cluster` only (no workload labels).

| Series | Type | Extra labels | Meaning |
|---|---|---|---|
| `kerf_cluster_readiness` | gauge | `check` (check id, e.g. `metrics.ksm-owners`), `severity` (`blocking`\|`warning`\|`info`) | 1 when the latest result of the check is `pass`, 0 when it is `warn`, `fail` or `unknown`. One series per catalog check per registered cluster. |
| `kerf_cluster_readiness_state` | gauge | `state` (`Registered`\|`AgentConnected`\|`MetricsOK`\|`ReadyAudit`\|`AutomationReady`) | 1 for the cluster's current readiness state, 0 for the others. |

Alert examples: `kerf_cluster_readiness{severity="blocking"} == 0` (a blocking check is failing);
`kerf_cluster_readiness_state{state="AutomationReady"} == 0 and on(cluster) (sum by (cluster) (kerf_workload_mode{mode="auto-patch"}) > 0)`
(auto-patch workloads on a cluster that lost automation readiness).

### Hub replicas

The hub may run as several replicas on one database. One of them, the leader, runs the
recommender, the readiness checks and the sweeps. Labels: none (the scrape adds the pod); these
two series have no `cluster` label because they describe the hub process, not a spoke.

| Series | Type | Extra labels | Meaning |
|---|---|---|---|
| `kerf_hub_leader` | gauge | – | 1 while this replica is the leader, 0 otherwise. `sum(kerf_hub_leader)` is 1 in a healthy hub (0 during a failover, at most 15 s). |
| `kerf_hub_leader_transitions_total` | counter | – | changes of `kerf_hub_leader` on this replica (acquired or lost); resets on restart, use `increase()`. |

### Update check

The hub's check for a newer Kerf release ([Maintenance page](../maintenance.md)). Leader only, no `cluster` label.

| Series | Type | Extra labels | Meaning |
|---|---|---|---|
| `kerf_update_available` | gauge | `current`, `latest` | 1 when the latest stable release (`latest`) is newer than the hub's version (`current`), 0 when up to date. Absent when `hub.updateCheck.enabled` is false, before the first successful check, and for development builds. A failed check keeps the last result. |

Alert example: `max by (current, latest) (kerf_update_available) == 1` (the chart's `KerfUpdateAvailable`, severity `info`).

**Which replica exports what.** Series computed from the database are exported by the leader
only, so N replicas never export N copies: every `kerf_recommendation_*`, `kerf_workload_mode`,
`kerf_workload_phase` and `kerf_cluster_readiness*` series, and `kerf_recommender_last_success_timestamp_seconds`,
`kerf_recommender_pricing_fallback`, `kerf_recommender_recommendations`, `kerf_update_available`. After a failover they come
from the new leader (they keep their values; only the scrape's pod label changes). Per-process
series are exported by every replica and add up across replicas: `kerf_actions_total` (events stored
by that replica), `kerf_hub_http_*`, `kerf_recommender_runs_total`, `kerf_recommender_run_duration_seconds`.

## 4. Reference PromQL

Pod → workload. Each query returns 1 per pod with `workload_kind` and `workload` labels; `or` them
together and join usage on `(cluster, namespace, pod)`:

```promql
# pod -> Deployment (via ReplicaSet)
label_replace(
  max by (cluster, namespace, pod, workload) (
    label_replace(kube_pod_owner{owner_kind="ReplicaSet"}, "replicaset", "$1", "owner_name", "(.*)")
    * on (cluster, namespace, replicaset) group_left (workload)
      label_replace(kube_replicaset_owner{owner_kind="Deployment"}, "workload", "$1", "owner_name", "(.*)")
  ),
  "workload_kind", "Deployment", "workload", ".*"
)

# pod -> CronJob (via Job)
label_replace(
  max by (cluster, namespace, pod, workload) (
    label_replace(kube_pod_owner{owner_kind="Job"}, "job_name", "$1", "owner_name", "(.*)")
    * on (cluster, namespace, job_name) group_left (workload)
      label_replace(kube_job_owner{owner_kind="CronJob"}, "workload", "$1", "owner_name", "(.*)")
  ),
  "workload_kind", "CronJob", "workload", ".*"
)

# pod -> StatefulSet / DaemonSet (direct)
max by (cluster, namespace, pod, workload_kind, workload) (
  label_replace(
    label_replace(kube_pod_owner{owner_kind=~"StatefulSet|DaemonSet"}, "workload", "$1", "owner_name", "(.*)"),
    "workload_kind", "$1", "owner_kind", "(.*)"
  )
)
```

The hub owns the canonical version of these queries; dashboards may use the `kerf_*`
series instead of re-implementing the joins.

CPU throttling ratio per container:

```promql
sum by (cluster, namespace, pod, container) (rate(container_cpu_cfs_throttled_periods_total[5m]))
/ clamp_min(sum by (cluster, namespace, pod, container) (rate(container_cpu_cfs_periods_total[5m])), 1)
```

Note for synthetic data: real cAdvisor series have no `workload` label. Synthetic series may
add one for convenience, but the hub must not depend on it, so the generator must also emit
`kube_pod_owner` / `kube_replicaset_owner` / `kube_job_owner` for every synthetic pod.
