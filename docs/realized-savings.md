# Realized savings

Every other dollar figure in Kerf is an **estimate**: the difference between the current and the recommended
requests, priced per node type, *if* the recommendation were applied. Realized savings answer a different
question: **how much have the changes Kerf actually applied saved, since they were applied?**

The two are kept apart everywhere:

| | Estimated | Realized |
|---|---|---|
| Label in the UI | "Estimated (if applied)" | "Realized since \<date\>" |
| Hub API | `Savings` (`GET /v1/summary`, `GET /v1/savings/trend`, recommendations) | `RealizedAmount` (`GET /v1/savings/realized`, `GET /v1/workloads/{id}/savings/realized`) |
| Metrics | `kerf_recommendation_monthly_savings_usd` | `kerf_hub_realized_savings_usd` |
| Unit | USD per month, from now on | USD, accumulated since the first change |

They are never added together: an estimate shrinks as recommendations are applied (the opportunity becomes
realized), so the sum would count the same saving twice.

## Where it shows

- **Overview**: the *Realized savings* card ("Realized since \<date\>", the total, the requests released now
  and a chart by month or by day for the last 30 days), and the *Node cost trend* card per cluster.
- **Workload page**: the workload's own realized saving next to its estimate.
- **Hub API**: `GET /v1/savings/realized` (fleet, `cluster`, `namespace` filters; `step=month|day`),
  `GET /v1/workloads/{id}/savings/realized` (also for a workload deleted since), and
  `GET /v1/savings/realized/node-cost?cluster=…` ([OpenAPI](../api/openapi/hub.yaml)).
- **Grafana**: the *Realized savings* row of the Kerf Overview dashboard.

## Method

### 1. Released requests (attributable)

The source of truth is the audit log the hub already stores: every `applied`, `rolled_back` and `released`
event of a workload (see [events](contracts/events.md)). `applied` and `rolled_back` events list, per changed
container, the requests before (`from`) and after (`to`) the change.

For each workload, container and resource Kerf keeps a running sum, in event time order:

- `applied` or `rolled_back`: add `from − to` of the request. A decrease adds, a later increase subtracts, and
  a rollback (`from` = the applied value, `to` = the restored value) takes the release back **from the moment
  it happened**. The sum is always "requests before Kerf's first change minus requests now".
- `released` (the workload left `auto-patch`, Kerf handed the fields back so GitOps restores the Git values):
  the listed resources (all when none are listed) go back to zero. Kerf no longer claims them, whatever values
  stay live.
- A container change without both a `from` and a `to` request is skipped for that resource.

Only **requests** count, never limits: requests are what reserves node capacity.

Per UTC hour, for each workload:

```
released_cores  = time-weighted average over the hour of the running CPU sum (cores per replica)
released_bytes  = the same for memory (bytes per replica)
replicas        = average number of scheduled pods of the workload over the hour
cpu_usd         = released_cores × replicas × USD per core-hour
memory_usd      = released_bytes / 2^30 × replicas × USD per GiB-hour
```

A rollback 30 minutes into an hour counts that hour half. The realized saving of a workload, namespace or
cluster is the sum of its hours.

**Replicas** come from kube-state-metrics: the average, over the hour, of the number of the workload's pods
that have requests and a node (one sample every 5 minutes, a missing sample counts as zero pods, so a workload
scaled to zero saves nothing). If the cluster's metrics have no data at all for an hour (an outage, or history
older than the metrics retention), Kerf uses the average replicas of the workload's latest stored
recommendation at that time; if there is none either, the hour counts zero. The API reports both cases in
`coverage`. Completed pods that kube-state-metrics still lists (finished Jobs of a CronJob) count while they
are listed.

**Rates** are the cluster's rates **of that hour**: the CPU-core and GiB rates that the recommender run of the
time stored (`recommender_run_clusters.pricing`, the average over the cluster's priced nodes from the pricing
service, after the discounts and fixed rates of the hub's pricing settings). The latest run at or before the
end of the hour applies; before the first stored run, the first one. A cluster that never had a stored run
uses the recommender's default rates (`coverage.defaultPricing`). A later change of the pricing settings does
not reprice past hours.

**When it is computed.** The hub leader materialises the hours in Postgres (`realized_savings_hourly`) every
10 minutes, up to the last complete hour. On the first run after an upgrade it backfills from the earliest
stored change, so **changes applied before the upgrade are counted**, from the audit events and the stored
rates and recommendations, and from the metrics wherever their retention still covers it. Events the agent
buffered while the hub was unreachable arrive late; the next run recomputes from the hour they happened in.
The API reads the stored hours and never recomputes on a request; `computedThrough` says how far they go.

### 2. Node cost trend (correlated)

Per cluster and hour, the hub also stores:

- the cost of the cluster's priced nodes (sum of `kerf_node_hourly_cost_usd`, USD per hour) and their number;
- the ratio of the scheduled pods' requests to the nodes' allocatable capacity, for CPU and memory.

The UI draws them next to the requests Kerf released. If the node autoscaler turned released requests into
fewer nodes, the lines show it. Kerf does **not** claim that one caused the other: deploys, traffic and other
teams change the node count too. The API marks the series `relation: correlated`.

Out of scope: allocating the node bill to teams (showback) and reconciling with the cloud bill.

## Reproduce the numbers

### SQL (hub database)

The ledger of one workload, oldest first:

```sql
SELECT time, type, resources, c->>'name' AS container,
       c->'from'->'requests'->>'cpu'    AS cpu_from,    c->'to'->'requests'->>'cpu'    AS cpu_to,
       c->'from'->'requests'->>'memory' AS memory_from, c->'to'->'requests'->>'memory' AS memory_to
FROM audit_events LEFT JOIN LATERAL jsonb_array_elements(containers) c ON true
WHERE cluster = 'prod-eu-west-1' AND namespace = 'payments'
  AND workload_kind = 'Deployment' AND workload_name = 'checkout'
  AND type IN ('applied', 'rolled_back', 'released')
ORDER BY time, id;
```

The rates in effect for a cluster over time:

```sql
SELECT as_of, pricing->>'cpuCoreHourUSD' AS cpu_core_hour_usd,
       pricing->>'memoryGiBHourUSD' AS memory_gib_hour_usd, pricing->>'source' AS source
FROM recommender_run_clusters
WHERE cluster = 'prod-eu-west-1' AND status = 'succeeded'
ORDER BY as_of;
```

The materialised hours, with every input of the formula (check any row by hand:
`cpu_usd = cpu_cores × replicas × cpu_core_hour_usd`):

```sql
SELECT hour, cpu_cores, memory_bytes, replicas, replicas_source,
       cpu_core_hour_usd, memory_gib_hour_usd, pricing_source, cpu_usd, memory_usd
FROM realized_savings_hourly
WHERE cluster = 'prod-eu-west-1' AND namespace = 'payments'
  AND workload_kind = 'Deployment' AND workload_name = 'checkout'
ORDER BY hour;
```

Realized savings by month, per cluster (what the Overview chart shows):

```sql
SELECT cluster, date_trunc('month', hour AT TIME ZONE 'UTC') AS month,
       round(sum(cpu_usd)::numeric, 2) AS cpu_usd, round(sum(memory_usd)::numeric, 2) AS memory_usd,
       round(sum(cpu_usd + memory_usd)::numeric, 2) AS total_usd
FROM realized_savings_hourly
GROUP BY 1, 2 ORDER BY 1, 2;
```

How far the computation goes, per cluster:

```sql
SELECT cluster, computed_through, node_cost_through, events_through FROM realized_savings_state;
```

### PromQL

Replicas of every workload of a cluster, averaged over the hour ending at each evaluation time (run it as a
range query with a 1 h step; the hub uses the [canonical pod → workload join](contracts/metrics.md#4-reference-promql),
shortened here to Deployments):

```promql
sum_over_time((
  count by (cluster, namespace, workload_kind, workload) (
    max by (cluster, namespace, pod) (kube_pod_container_resource_requests{cluster="prod-eu-west-1", resource=~"cpu|memory", node!=""})
    * on (cluster, namespace, pod) group_left (workload_kind, workload)
    label_replace(
      max by (cluster, namespace, pod, workload) (
        label_replace(kube_pod_owner{cluster="prod-eu-west-1", owner_kind="ReplicaSet"}, "replicaset", "$1", "owner_name", "(.*)")
        * on (cluster, namespace, replicaset) group_left (workload)
        max by (cluster, namespace, replicaset, workload) (
          label_replace(kube_replicaset_owner{cluster="prod-eu-west-1", owner_kind="Deployment"}, "workload", "$1", "owner_name", "(.*)")
        )
      ),
      "workload_kind", "Deployment", "workload", ".*"
    )
  )
)[1h:5m]) / scalar(count_over_time(vector(1)[1h:5m]))
```

The node cost trend (hourly averages; the cost series are in the hub's own metrics store, the capacity series
in the cluster's):

```promql
avg_over_time((sum by (cluster) (max by (cluster, node) (kerf_node_hourly_cost_usd{cluster="prod-eu-west-1"})))[1h:5m])

avg_over_time((
  sum by (cluster) (kube_pod_container_resource_requests{cluster="prod-eu-west-1", resource="cpu", node!=""})
  / sum by (cluster) (kube_node_status_allocatable{cluster="prod-eu-west-1", resource="cpu"})
)[1h:5m])
```

The totals the hub exports (leader only, refreshed after every computation), as in the Grafana row:

```promql
# Realized USD since the first change, per cluster
sum by (cluster) (max by (cluster, namespace, workload_kind, workload, resource) (kerf_hub_realized_savings_usd))

# Requests released in the last computed hour (x replicas): cores and bytes
sum by (cluster) (kerf_hub_realized_released_requests{resource="cpu"})
sum by (cluster) (kerf_hub_realized_released_requests{resource="memory"})

# How far the computation goes
kerf_hub_realized_computed_through_timestamp_seconds
```

| Series | Labels | Meaning |
|---|---|---|
| `kerf_hub_realized_savings_usd` | `cluster`, `namespace`, `workload_kind`, `workload`, `resource` | realized USD since the workload's first applied change |
| `kerf_hub_realized_released_requests` | same | requests released in the last computed hour × replicas (cores for `cpu`, bytes for `memory`) |
| `kerf_hub_realized_computed_through_timestamp_seconds` | `cluster` | unix time up to which the hours are computed |

### A worked example

A Deployment with 3 replicas has its CPU request lowered from 2 cores to 1 core on 1 October at 00:00 UTC; the
cluster's rate is $0.04 per core-hour. On 31 October at 00:00 the realized saving is
1 core × 3 replicas × 720 h × $0.04 = **$86.40**. Had the health watch rolled the change back on 11 October,
it would be 1 × 3 × 240 h × $0.04 = **$28.80**, and nothing after. Had a later change raised the request to
1.5 cores on 21 October, it would be (480 h × 1 core + 240 h × 0.5 core) × 3 × $0.04 = **$72.00**.
