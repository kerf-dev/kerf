# Metrics contract

Owner: WO-00. Changing a name or label here is a contract change (see `CLAUDE.md`).

All queries must be valid PromQL and run on both Prometheus and VictoriaMetrics (D-003).

## Rules for every series

- **`cluster` label is mandatory** on every series and equals the cluster name used in the hub API
  path (`/v1/clusters/{cluster}/...`), e.g. `ro-spoke-1`.
  - Input series from a spoke get it from vmagent `externalLabels: {cluster: <name>}` (WO-01).
  - `ro_*` series exported by hub-side components (pricing, hub) set `cluster` themselves, one value
    per spoke. The hub-side scrape config must use `honor_labels: true` and must not add or
    overwrite a `cluster` label for these jobs; otherwise `cluster` becomes `exported_cluster`.
- Our own series use the `ro_` prefix. Base units only: seconds, bytes, cores, USD.
- Workload identity labels, wherever a series is about a workload:
  `namespace`, `workload_kind` (`Deployment|StatefulSet|DaemonSet|CronJob`), `workload` (name),
  and `container` when per container.
- Components may export extra operational series under their own prefix without a contract change:
  `ro_hub_*`, `ro_recommender_*`, `ro_pricing_*`, `ro_agent_*`, `ro_webhook_*`, `ro_health_*`.
  Everything else under `ro_` is listed in this file.

## 1. Input series (spokes → hub VictoriaMetrics)

Scraped on each spoke and remote-written to the hub (WO-01). Synthetic data (WO-02) must produce the
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
| `kube_pod_container_info` | cluster, namespace, pod, container, image | JVM/image detection (WO-12) |
| `kube_pod_container_status_restarts_total` | cluster, namespace, pod, container | `restarting` flag, rollback |
| `kube_pod_container_status_last_terminated_reason` | cluster, namespace, pod, container, reason (`OOMKilled`) | `oom-killed` flag, rollback |
| `kube_deployment_spec_replicas` | cluster, namespace, deployment | replicas |
| `kube_statefulset_replicas` | cluster, namespace, statefulset | replicas |
| `kube_daemonset_status_desired_number_scheduled` | cluster, namespace, daemonset | replicas |
| `kube_horizontalpodautoscaler_info` | cluster, namespace, horizontalpodautoscaler, scaletargetref_kind, scaletargetref_name | HPA target |
| `kube_horizontalpodautoscaler_spec_target_metric` | cluster, namespace, horizontalpodautoscaler, metric_name, metric_target_type | `hpa-coupled` (metric_name `cpu`/`memory`, target type `utilization`) |
| `kube_node_labels` | cluster, node, label_node_kubernetes_io_instance_type, label_karpenter_sh_capacity_type, label_topology_kubernetes_io_region, label_topology_kubernetes_io_zone | pricing (WO-03) |
| `kube_node_status_allocatable` | cluster, node, resource (`cpu`\|`memory`), unit | pricing cost split |

Capacity type values follow Karpenter: `on-demand`, `spot`.

## 2. Pricing series (exported by the pricing service, WO-03)

One series per spoke node. All are gauges, in USD per hour, for the current price.

| Series | Meaning |
|---|---|
| `ro_node_hourly_cost_usd` | price of the whole node |
| `ro_cpu_core_hourly_cost_usd` | CPU share of the node price, per vCPU (allocatable) |
| `ro_memory_gib_hourly_cost_usd` | memory share of the node price, per GiB (allocatable) |

Labels (all three): `cluster`, `node`, `instance_type`, `capacity_type` (`on-demand`\|`spot`),
`region`, `zone`, `provider` (`aws`\|`static`).

Invariant: `ro_cpu_core_hourly_cost_usd * allocatable_cores + ro_memory_gib_hourly_cost_usd * allocatable_gib ≈ ro_node_hourly_cost_usd`.

## 3. Hub series (exported by the hub, WO-04)

Workload labels below = `cluster, namespace, workload_kind, workload`.

### Recommendations (latest recommendation per workload; series disappear when a workload is gone)

| Series | Type | Extra labels | Meaning |
|---|---|---|---|
| `ro_recommendation_cpu_cores` | gauge | container, `type` (`request`\|`limit`), `source` (`current`\|`recommended`) | CPU in cores. No series for an unset or removed limit. |
| `ro_recommendation_memory_bytes` | gauge | container, `type`, `source` | memory in bytes |
| `ro_recommendation_monthly_savings_usd` | gauge | container, `resource` (`cpu`\|`memory`) | estimated monthly savings; negative for increases |
| `ro_recommendation_confidence` | gauge | – | 0–1 |
| `ro_recommendation_data_coverage_ratio` | gauge | – | 0–1, fraction of the window with data |
| `ro_recommendation_risk_flag` | gauge | `flag` (hub API `RiskFlag` values) | 1 while the flag is set; no series otherwise |
| `ro_recommendation_generated_timestamp_seconds` | gauge | – | unix time the recommendation was generated |
| `ro_recommendation_average_replicas` | gauge | – | replicas used for savings |

### Modes and actions (from agent reports)

| Series | Type | Extra labels | Meaning |
|---|---|---|---|
| `ro_workload_mode` | gauge | `resource` (`cpu`\|`memory`), `mode` (`off`\|`audit`\|`auto-patch`), `source` (`global-default`\|`cluster-default`\|`policy`\|`annotation`) | 1 for the effective mode. Workloads the agent never reported have `mode="audit"`, `source="global-default"`. |
| `ro_workload_phase` | gauge | `phase` (`Pending`\|`Applying`\|`Applied`\|`Blocked`\|`RolledBack`\|`Released`) | 1 for the current phase |
| `ro_actions_total` | counter | `type` (event type), `strategy`, `resource` (`cpu`\|`memory`\|`""`), `reason` | incremented once per event stored by the hub (duplicates are not counted); one increment per resource listed in the event. Carries the workload labels like every hub series (the event's workload). |

`ro_actions_total` lives in the hub process and resets on restart; always use `increase()`/`rate()`.

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

The hub (WO-04) owns the canonical version of these queries; dashboards (WO-05) may use the `ro_*`
series instead of re-implementing the joins.

CPU throttling ratio per container:

```promql
sum by (cluster, namespace, pod, container) (rate(container_cpu_cfs_throttled_periods_total[5m]))
/ clamp_min(sum by (cluster, namespace, pod, container) (rate(container_cpu_cfs_periods_total[5m])), 1)
```

Note for synthetic data (WO-02): real cAdvisor series have no `workload` label. Synthetic series may
add one for convenience, but the hub must not depend on it, so the generator must also emit
`kube_pod_owner` / `kube_replicaset_owner` / `kube_job_owner` for every synthetic pod.
