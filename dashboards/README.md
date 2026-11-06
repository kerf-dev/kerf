# Grafana dashboards

Four dashboards in folder **kerf**, provisioned from this directory by the dev Grafana
(`dev/scripts/sync-dashboards.sh`). Every panel uses only PromQL (D-003) over the series in
[`docs/contracts/metrics.md`](../docs/contracts/metrics.md).

| UID | File | Purpose |
|---|---|---|
| `ro-overview` | `overview.json` | Net / gross monthly savings by cluster, namespace and resource; savings trend; CPU and memory requested vs used vs recommended; workloads per mode, risk flag and phase; recommender freshness and pricing source. |
| `ro-opportunities` | `opportunities.json` | One row per container, ranked by monthly savings: current vs recommended requests, confidence, risk flags, CPU/memory mode, phase. The workload cell opens the drill-down. `Show` filters savings / increases. |
| `ro-workload` | `workload.json` | Explainability for one workload: usage (max and avg of pods) with P50/P95/P99/max over the `Percentile window`, current request/limit (kube-state-metrics), recommended request/limit (hub), throttling, restarts and OOM kills, and automation events (table + annotations). |
| `ro-automation` | `automation.json` | `kerf_actions_total` by type over time, rollbacks by reason, blocked / skipped / fallback / failed reasons, workloads with events, phases and mode sources, and pipeline health (recommender runs, agent pulls, agent → hub requests, event buffer). |

Variables: `datasource` (default uid `victoriametrics`), `cluster`, `namespace`, `workload`, `container`
(plus `kind` and `window` on the drill-down). The dashboard links at the top carry the time range and
all variables; the drill-down takes single values and falls back to the first value for "All".

## Editing

The JSON is generated. Edit [`src/build.py`](src/build.py) (standard-library Python 3.9+), then:

```bash
make dashboards               # regenerate dashboards/*.json
make dashboards-check         # jq + up to date + every panel has a description + contract names/labels
make dashboards-preview       # import as *-preview copies into folder "kerf (preview)"
make dashboards-verify        # run every panel query through Grafana for every cluster (dev env)
make dashboards-verify-vm     # same, straight against VictoriaMetrics (no Grafana load)
make dashboards-preview-delete
```

`dashboards-check` is hooked into `verify-generate`. `verify.py --strict` also fails on queries without
data; `--cluster` / `--dashboard` narrow a run. The drill-down is verified for the top-savings workload
of each kind in each cluster (Deployment, StatefulSet, DaemonSet and CronJob joins).

## Query conventions

- Hub series carry scrape labels (`instance`, `kubernetes_pod`); during a hub rollout the old and new pod
  both export them. Every hub gauge is reduced with `max by (<identity labels>)` before summing or joining.
  Counters (`kerf_actions_total`, `kerf_recommender_runs_total`) are read only through `increase()`.
- Workload → pod uses the contract's owner joins (ReplicaSet → Deployment, Job → CronJob, direct for
  StatefulSet/DaemonSet); cAdvisor has no workload label.
- Modes and phases are encoded as numbers (`off=0, audit=1, auto-patch=2`; `Pending=0 … Released=5`) and
  turned back into text with value mappings, so all queries of a table return the same labels and the
  merge transformation joins them per container. Risk flags are folded into one `rf` label
  (`label_join`/`label_replace`) for the same reason.
- "Requested" in capacity panels = requests of running containers (KSM, joined with cAdvisor working set);
  "Recommended" = recommended request × `kerf_recommendation_average_replicas`.
- Savings are estimates (hub-side, current prices), not realized savings.
