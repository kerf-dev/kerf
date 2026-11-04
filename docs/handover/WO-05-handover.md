# WO-05 hand-over (Grafana dashboards)

Branch `wo-05-dashboards` (commit 2fed4ce on main f84e6ea; worktree `.claude/worktrees/wo-05-dashboards`), pushed.
Owns `dashboards/` + `make/dashboards.mk`. Docs: `dashboards/README.md`.

Verification (2026-11-04): `jq empty` on all 4 files OK; `make dashboards-verify` (every query through Grafana
/api/ds/query, clusters ro-spoke-1/2 + synth-a/b, drill-down for the top workload of each kind) → 0 errors.
Empty queries are all legitimately empty: no CPU limits (limit lines, throttling), no OOM on sampled workloads,
no per-workload events (hub label gap below), no agents on synth-*, no rollbacks/failures yet.

## Dashboards (folder resource-optimizer, tag resource-optimizer)
| UID | File |
|---|---|
| `ro-overview` | dashboards/overview.json — savings by cluster/namespace/resource + trend, CPU/memory requested vs used vs recommended, modes/flags/phases, freshness/pricing source |
| `ro-opportunities` | dashboards/opportunities.json — per-container table ranked by $/month (current vs rec requests, confidence, risk flags, modes, phase); workload cell → drill-down |
| `ro-workload` | dashboards/workload.json — usage max/avg of pods, P50/P95/P99/max over $window (`@ end()` subquery), KSM request/limit, hub recommended request/limit, throttling, restarts/OOM kills, events table + annotations |
| `ro-automation` | dashboards/automation.json — ro_actions_total by type/reason, rollbacks, blocked/skipped/fallback/failed, workloads with events, phases, mode sources, pipeline health (recommender runs, agent pulls, agent→hub requests, event buffer) |

Variables: datasource (uid victoriametrics), cluster, namespace, workload, container (+ kind, window on the drill-down).

## How it is built
- JSON is GENERATED from `dashboards/src/build.py` (stdlib Python 3.9). Don't hand-edit JSON; `make dashboards`.
- `make dashboards-check` (jq, up to date, descriptions, contract names/labels) is hooked into `verify-generate`.
- `make dashboards-verify` runs every panel query through Grafana /api/ds/query for each cluster (drill-down: top-savings
  workload of each kind per cluster); `make dashboards-verify-vm` does the same straight against VictoriaMetrics.
- `make dashboards-preview` imports `*-preview` copies (folder "resource-optimizer (preview)", uid suffix -preview) via the
  API — they are still there for review; `make dashboards-preview-delete` removes them. Provisioned dashboards untouched.

## Findings for other tracks
- **WO-04 vs contract:** hub's `ro_actions_total` has only cluster,type,strategy,resource,reason — no namespace,
  workload_kind, workload (contract section 3 says all hub series carry the workload labels). Dashboards follow the
  contract: fleet views work (`=~".*"` matches a missing label); per-namespace/workload event views stay empty until
  the hub adds the labels.
- `ro_recommender_runs_total` / `_run_duration_seconds` have no `cluster` label (hub-wide); panels say so.
- **WO-01:** Grafana idled at ~368 MB RSS against its 384Mi limit and was OOMKilled under ~30 small queries
  (02:08Z). Fixed on main: `dev/helm/grafana.yaml` now sets 1Gi (deployed).
- Grafana 13 (dashboardNewLayouts): a dashboard opened in kiosk mode in a small viewport can show only row headers;
  normal mode renders fine.
