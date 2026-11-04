# WO-02 hand-over (synthetic metrics) — 2026-11-04

Branch `wo-02-synth` (commits dd45c30, eb7f178), pushed. Owns tools/synth/, testdata/series/, make/synth.mk.
Docs: tools/synth/README.md (series/labels, scenario format, live mode), testdata/series/README.md (fixtures).

## What is in the shared VictoriaMetrics (http://localhost:18428)
- Default fleet (tools/synth/scenarios/fleet-default.yaml): clusters `synth-a`, `synth-b`, 203 workloads each
  (Deployments/StatefulSets/CronJobs/DaemonSets) in payments, cards, ledger, onboarding, notifications, risk, platform.
  30 days at 60s (from 2026-10-05T01:41Z), ~157k series, ~1.03B samples.
- LIVE MODE is running (pid in ~/.cache/resource-optimizer/synth-live/pid, log live.log there): appends a
  sample every minute, seamless with the backfill — instant queries at "now" see current pods/requests/usage.
  `make synth-live-status`, `make synth-live`, `make synth-live-stop`. Refresh everything: `make synth-backfill` (~4.5 min).
- Nodes: synth-a m6i.xlarge on-demand + c6a.2xlarge spot; synth-b c6a.2xlarge on-demand + m6i.xlarge spot; eu-west-1a/b/c.
  Node names `ip-10-x-y-z.eu-west-1.compute.internal`.

## Series other tracks rely on (same names/labels as the real ro-* stack)
- cAdvisor (job=cadvisor, instance=node=node name): container_cpu_usage_seconds_total{cpu="total"},
  container_memory_working_set_bytes, container_cpu_cfs_periods_total / _throttled_periods_total (only with a CPU limit),
  container_oom_events_total; labels namespace, pod, container, image, name, id. NO workload label.
  Pod-cgroup series without `container` exist too -> always filter container!="".
  A restart = new container instance series (new id/name, counter from 0): aggregate by (cluster,namespace,pod,container).
- KSM (job=kube-state-metrics): kube_pod_owner, kube_replicaset_owner, kube_job_owner (job_name=<cronjob>-<unix minutes>),
  kube_pod_info{node}, kube_pod_container_info{image}, kube_pod_init_container_info,
  kube_pod_container_resource_requests/limits{resource=cpu|memory, unit=core|byte, node}, kube_pod_init_container_resource_*,
  kube_pod_container_status_restarts_total, kube_pod_container_status_last_terminated_reason{reason=OOMKilled|Error},
  kube_deployment_spec_replicas, kube_statefulset_replicas, kube_daemonset_status_desired_number_scheduled,
  kube_horizontalpodautoscaler_info / _spec_target_metric{metric_name, metric_target_type="utilization"} / _spec_min/max_replicas / _status_current/desired_replicas,
  kube_node_labels{label_node_kubernetes_io_instance_type, label_karpenter_sh_capacity_type, label_topology_kubernetes_io_region, _zone},
  kube_node_status_allocatable/capacity{resource=cpu|memory|pods}, kube_node_info.
- Ground truth (not contract): synth_workload_info{cluster,namespace,workload_kind,workload,profile,cpu_expect,memory_expect,risk_flags}=1.
  Profiles: over-provisioned (most), right-sized, throttled, oom-prone, hpa-coupled, jvm (image tag *-temurin21-jre), batch, daemon.
- Bookkeeping (not contract): synth_scenario_start_timestamp_seconds / synth_scenario_anchor_timestamp_seconds.

## For WO-03 (pricing): instance types m6i.xlarge, c6a.2xlarge, on-demand and spot, eu-west-1.
## For WO-04 (recommender): testdata/series/*.json = one fixture per pattern (14d, 5m buckets, expect.cpu/memory/riskFlags);
   loader synth.LoadFixture (import github.com/tomer1983/resource-optimizer/tools/synth/synth). WO-04's verification
   `backfill --scenario fleet-small.yaml` REPLACES nothing unless --replace; prefer `make synth-backfill-small` and
   `make synth-backfill` afterwards to restore the default fleet. fleet-small adds a convenience `workload` label on cAdvisor.
## VictoriaMetrics: limit was raised to 3Gi (it OOMed at 768Mi). Backfill has a memory guard. Avoid backfill loops:
   every --replace leaves deleted series in the index until retention.
