# tools/synth — synthetic Kubernetes usage history (WO-02)

`synth` generates realistic cAdvisor and kube-state-metrics history for fake clusters, so the hub,
recommender, dashboards and UI can be built and demoed against weeks of data that would otherwise
take weeks to collect (D-010). The series have the same names and labels as the real stack
(`docs/contracts/metrics.md`), so consumers can't tell the difference: workloads are reached through
the KSM owner chain, never through a label on cAdvisor series.

Synthetic clusters are always named `synth-*` (the shared environment uses `synth-a` and `synth-b`).
The generator refuses other names and never touches `ro-*` series.

## What the shared VictoriaMetrics holds

`http://localhost:18428` holds the **default fleet** (`scenarios/fleet-default.yaml`):

| | synth-a | synth-b |
|---|---|---|
| Workloads | 203 (182 Deployments, 10 StatefulSets, 8 CronJobs, 3 DaemonSets) | 203 (175 / 7 / 18 / 3) |
| Nodes | m6i.xlarge on-demand + c6a.2xlarge spot, eu-west-1a/b/c | c6a.2xlarge on-demand + m6i.xlarge spot |
| Namespaces | payments, cards, ledger, onboarding, notifications, risk, platform | same |

The history is 30 days at 60s resolution. **Live mode** (a background process started by
`make synth-backfill` / `make synth-live`) then appends a new sample every minute, continuing the
same simulation seamlessly (same pods, no counter resets), so `synth-a`/`synth-b` behave like live
clusters: instant queries at "now" see current pods, requests and usage. Without live mode the data
simply stops at the backfill time and goes stale within minutes.

```bash
make synth-backfill                      # wipe synth-a/synth-b, backfill 30 days up to now (~4.5 min), start live mode
make synth-live-status                   # is live mode running? last log lines
make synth-live / make synth-live-stop   # (re)start / stop live mode
make synth-verify                        # PromQL checks of what is in VictoriaMetrics
make synth-backfill SYNTH_DAYS=14        # shorter window
make synth-backfill-small                # the pattern scenario instead (see below)
make synth-delete                        # stop live mode, remove all synthetic series
```

Live mode keeps its binary, a copy of the scenario, its pid and its log in
`~/.cache/kerf/synth-live/` so it keeps running when this worktree goes away. It reads
where to continue from VictoriaMetrics (`synth_scenario_start_timestamp_seconds`,
`synth_scenario_anchor_timestamp_seconds`, the last `kube_node_labels` sample), so it can be stopped
and restarted at any time and fills the gap. Each tick re-simulates the fleet (~4 s of CPU per
minute). If it is not running (e.g. after a reboot), run `make synth-live`; after VictoriaMetrics
data loss, run `make synth-backfill`.

Profiles in the default mix (weights): over-provisioned 50, right-sized 14, throttled 8,
oom-prone 5, hpa-coupled 8, jvm 8, batch 7, plus modifiers: 4% created in the last 3 days
(low-confidence), 20% with an `envoy` sidecar, 6% with a usage step change at a release,
2% crash-restarting, 8% of services are StatefulSets, 3 DaemonSets per cluster, a release every ~14 days
(new ReplicaSet, image patch bump, new pods), spot pods rescheduled every ~7 days, on-demand ones every ~30.

Ground truth: every workload has `synth_workload_info{cluster, namespace, workload_kind, workload,
profile, cpu_expect, memory_expect, risk_flags}` = 1 (not part of the contract; real clusters don't have
it). Use it to check the recommender, e.g.

```promql
count by (profile, cpu_expect) (synth_workload_info{cluster="synth-a"})
```

`synth describe --manifest fleet.json` writes the same as a JSON list.

## Series emitted

Everything below carries `cluster` (`synth-a`/`synth-b`). Labels match what `ro-spoke-*` deliver
through vmagent today (checked against the live stack, k3s 1.35, KSM, kubelet cAdvisor).

### cAdvisor (`job="cadvisor"`, `instance`=`node`=node name)

| Series | Labels | Notes |
|---|---|---|
| `container_cpu_usage_seconds_total` | namespace, pod, container, image, name, id, cpu="total" | counter, per container instance |
| `container_memory_working_set_bytes` | same minus cpu | bytes, 4 KiB pages |
| `container_cpu_cfs_periods_total` / `container_cpu_cfs_throttled_periods_total` | same | only for containers with a CPU limit (as cAdvisor) |
| `container_oom_events_total` | same | 1 on the last sample of an OOM-killed instance |
| `container_cpu_usage_seconds_total`, `container_memory_working_set_bytes` **without `container`** | namespace, pod, id=`/kubepods[/burstable]/pod<uid>` | pod cgroup totals. Filter with `container!=""`. |

- No `workload` label (real cAdvisor has none). Exception: `fleet-small.yaml` sets
  `workloadLabel: true`, which adds `workload` and `workload_kind` for convenience. Never depend on it.
- A container restart (OOM, crash) starts a new instance: new `name`/`id`, counters from 0, exactly
  like kubelet. Aggregate with `max/sum by (cluster, namespace, pod, container)`.

### kube-state-metrics (`job="kube-state-metrics"`, `instance="kube-state-metrics.monitoring.svc:8080"`)

| Series | Labels |
|---|---|
| `kube_pod_owner` | namespace, pod, uid, owner_kind (`ReplicaSet`/`StatefulSet`/`DaemonSet`/`Job`), owner_name, owner_is_controller |
| `kube_replicaset_owner` | namespace, replicaset, owner_kind=`Deployment`, owner_name, owner_is_controller (kept for old ReplicaSets too) |
| `kube_job_owner` | namespace, job_name (`<cronjob>-<scheduled unix minutes>`), owner_kind=`CronJob`, owner_name |
| `kube_pod_info` | namespace, pod, uid, node, host_ip, pod_ip, created_by_kind, created_by_name, host_network |
| `kube_pod_container_info` | namespace, pod, uid, container, image, image_spec, image_id, container_id |
| `kube_pod_init_container_info` | same, for init containers (`db-migrate` on ~20% of ledger/payments services) |
| `kube_pod_container_resource_requests` / `_limits` | namespace, pod, uid, container, node, resource (`cpu`/`memory`), unit (`core`/`byte`) |
| `kube_pod_init_container_resource_requests` / `_limits` | same, init containers |
| `kube_pod_container_status_restarts_total` | namespace, pod, uid, container |
| `kube_pod_container_status_last_terminated_reason` | namespace, pod, uid, container, reason (`OOMKilled`/`Error`) — from the first restart on |
| `kube_deployment_spec_replicas` | namespace, deployment (follows the HPA for HPA workloads) |
| `kube_statefulset_replicas` | namespace, statefulset |
| `kube_daemonset_status_desired_number_scheduled` | namespace, daemonset |
| `kube_horizontalpodautoscaler_info` | namespace, horizontalpodautoscaler, scaletargetref_api_version, scaletargetref_kind, scaletargetref_name |
| `kube_horizontalpodautoscaler_spec_target_metric` | + metric_name (`cpu`/`memory`), metric_target_type=`utilization`; value = target % |
| `kube_horizontalpodautoscaler_spec_min_replicas` / `_max_replicas` / `_status_current_replicas` / `_status_desired_replicas` | namespace, horizontalpodautoscaler |
| `kube_node_labels` | node, label_node_kubernetes_io_instance_type (`m6i.xlarge`/`c6a.2xlarge`), label_karpenter_sh_capacity_type (`on-demand`/`spot`), label_topology_kubernetes_io_region (`eu-west-1`), label_topology_kubernetes_io_zone |
| `kube_node_status_allocatable` / `_capacity` | node, resource (`cpu`/`memory`/`pods`), unit (`core`/`byte`/`integer`) |
| `kube_node_info` | node, internal_ip, provider_id, kubelet_version, os_image, ... |

Node names look like EKS: `ip-10-33-144-31.eu-west-1.compute.internal`. Pod names follow Kubernetes
(`<deploy>-<rs hash>-<5>`, `<sts>-<ordinal>`, `<ds>-<5>`, `<job>-<5>`). JVM workloads have image tags
ending in `-temurin21-jre`.

Not emitted: `kube_pod_status_phase`, KSM series of completed Job pods (pods disappear when their run
ends), pause containers, network/disk series.

Joining usage to workloads: use the reference queries in `docs/contracts/metrics.md` section 4, e.g.
CPU per Deployment:

```promql
sum by (cluster, namespace, workload) (
  rate(container_cpu_usage_seconds_total{cluster=~"synth-.*", container!=""}[5m])
  * on (cluster, namespace, pod) group_left (workload)
  max by (cluster, namespace, pod, workload) (
    label_replace(kube_pod_owner{owner_kind="ReplicaSet"}, "replicaset", "$1", "owner_name", "(.*)")
    * on (cluster, namespace, replicaset) group_left (workload)
    label_replace(kube_replicaset_owner{owner_kind="Deployment"}, "workload", "$1", "owner_name", "(.*)")
  )
)
```

## Commands

```bash
go run ./tools/synth backfill --scenario tools/synth/scenarios/fleet-default.yaml --vm-url http://localhost:18428 --replace
go run ./tools/synth backfill --scenario tools/synth/scenarios/fleet-small.yaml --cluster synth-a --workload synth-flat-0
go run ./tools/synth live --scenario tools/synth/scenarios/fleet-default.yaml          # foreground live mode
go run ./tools/synth verify --scenario tools/synth/scenarios/fleet-default.yaml
go run ./tools/synth describe --scenario tools/synth/scenarios/fleet-default.yaml --manifest /tmp/fleet.json
go run ./tools/synth fixtures --out testdata/series
go run ./tools/synth delete --vm-url http://localhost:18428
```

Common flags: `--days`, `--seed` (deterministic: same seed + same end time = same data), `--end`
(RFC3339, default now), `--step`, `--cluster`, `--namespace`, `--workload`.

`backfill` flags: `--replace` (delete the scenario's clusters first), `--follow` (continue as
`live` afterwards), `--format remote-write|json`
(default remote-write to `/api/v1/write`; `json` uses `/api/v1/import`), `--workers`, `--parallel`,
`--batch-kib`, `--chunk` (import in time order, N of history at a time), `--max-vm-memory`
(pause while VictoriaMetrics' heap is above this fraction of its memory limit), `--dry-run`.

## Scenario format

```yaml
name: my-scenario
seed: 42
days: 30            # ≤ 35 (VM retention)
step: 60s
workloadLabel: false
groundTruth: true   # synth_workload_info
clusters:
  - name: synth-a   # must start with synth-
    region: eu-west-1
    requestPacking: 0.85          # auto-sized pools: requests fill 85% of allocatable
    nodePools:
      - {instanceType: m6i.xlarge, capacityType: on-demand, weight: 2}   # count: N for a fixed size
      - {instanceType: c6a.2xlarge, capacityType: spot, weight: 1, zones: [eu-west-1a]}
    fleet:                        # generated workloads
      workloads: 200
      namespaces: [payments, cards]
      profiles: {over-provisioned: 50, right-sized: 14, throttled: 8, oom-prone: 5, hpa-coupled: 8, jvm: 8, batch: 7}
      newWorkloads: 0.04
      sidecars: 0.2
      stepChanges: 0.06
      restarting: 0.02
      statefulSets: 0.08
      daemonSets: 3
      rolloutEveryDays: 14
    workloads:                    # explicit workloads
      - name: checkout
        namespace: payments
        kind: Deployment          # Deployment | StatefulSet | DaemonSet | CronJob
        replicas: 3
        age: 36h                  # created 36h before the end (default: before the window)
        rollouts: [10d, 2d]       # releases, as offsets before the end
        meanPodLifetime: 7d       # reschedules; default 30d on-demand / 7d spot; negative = never
        hpa: {min: 2, max: 8, metric: cpu, target: 70}
        schedule: {every: 1h, offset: 5m, duration: 10m}   # CronJob only
        containers:
          - name: app
            image: registry/payments/checkout:1.4.0
            sidecar: false
            requests: {cpu: 500m, memory: 512Mi}
            limits: {cpu: "1", memory: 512Mi}
            crashEvery: 0         # mean time between crashes (reason Error)
            cpu:    {pattern: diurnal, base: 150m, diurnal: 0.6, peakHour: 12, weekend: 0.5, noise: 0.08}
            memory: {pattern: leak, base: 250Mi, leakPerHour: 0.05}
        expect: {cpu: decrease, memory: increase, riskFlags: [oom-killed]}
```

Signal fields (CPU in cores, memory in bytes, Kubernetes quantities accepted): `pattern`, `base`,
`diurnal` (relative amplitude), `peakHour` (UTC), `weekend` (multiplier), `noise` (relative),
`spikesPerDay`/`spikeMult`/`spikeMinutes`, `stepAt`/`stepFactor`, `leakPerHour` (relative to base, per
hour of container uptime; resets on restart), `startupMult`/`startupMinutes` (JVM warm-up),
`sawtooth`/`sawtoothMinutes` (GC). `pattern` (`flat`, `diurnal`, `weekly`, `spiky`, `leak`, `step`,
`sawtooth`, `throttled`, `sparse`) only fills defaults for fields left at zero.

Simulation: CPU usage is capped at the CPU limit and the excess shows up as CFS throttling; memory at
the limit is an OOM kill (restart, new container instance, `OOMKilled` reason, restart counter +1).
HPA workloads compute replicas from load (scale up at once, scale down after a 5-minute
stabilization window) and split the load across pods. Memory-metric HPAs also scale on CPU load.

## Fixtures

`testdata/series/*.json` are pre-aggregated 5-minute fixtures of `fleet-small.yaml`'s pattern workloads
for unit tests. See `testdata/series/README.md`.

## Volume, speed and VictoriaMetrics memory

The default fleet is ~157k series / ~1.03B samples (both clusters, 30 days). Measured on the shared
k3d environment (VictoriaMetrics v1.155 single node, 3Gi limit): **4m35s** end to end with
`make synth-backfill` (3.8M samples/s); `fleet-small` takes ~40s.

- Prometheus remote write (`/api/v1/write`, protobuf + snappy) is the default: ~3× faster for
  VictoriaMetrics to ingest than JSON lines (`--format json`, `/api/v1/import`).
- Time-ordered chunks (`--chunk 48h`) were measured slower; the default sends each series' whole
  window at once.
- Memory guard: `backfill` reads `/metrics` every 300ms and pauses while the memory held by
  VictoriaMetrics' Go runtime (`go_memstats_sys_bytes - go_memstats_heap_released_bytes`) is above
  `--max-vm-memory` (default 80%) of `vm_available_memory_bytes`. RSS is not used: it includes
  mmapped data files that the kernel reclaims first. VictoriaMetrics keeps up to 60% for its own
  caches, so values below ~0.7 stall the import. During the default backfill it peaked at ~1.9GB of 3GB.
- The shared VictoriaMetrics was OOM-killed at its original 768Mi limit by backfills of a few
  million samples; it now runs with 3Gi.
- Every `--replace` leaves the deleted series in VictoriaMetrics' index until retention rotates it
  (35d), so avoid re-backfilling in a loop.

After importing, `backfill` calls `/internal/force_flush` and `/internal/resetRollupResultCache` so
cached query results don't hide the new history.
