# WO-04 hand-over (hub and recommender)

Branch `wo-04-hub`, pushed. Deployed to ro-hub, namespace `resource-optimizer`, Helm release `hub`
(`make deploy-hub`). Chart docs and the dev tokens: `deploy/charts/hub/README.md`.

## Endpoints
- Host: `http://localhost:18080` (NodePort 30080). Spokes: `http://hub-api.hub.svc:8080`. In ro-hub:
  `http://hub.resource-optimizer.svc:8080`. `/metrics`, `/healthz`, `/readyz` need no token.
- CORS allows `http://localhost:5173` (UI dev server); `ETag` is exposed to the browser.
- Tokens: Secret `hub-dev-tokens` (keys `ro-spoke-1`, `ro-spoke-2`, `synth-a`, `synth-b`, `ui`, `admin`).
  `ui` token: `ro-dev-ui-97446cd3a095035d6290abc5`.
- On the host instead of the cluster: `make run-hub` (port 8080, `dev/hub.yaml`). Never both at once.

## Behaviour other tracks rely on
- **Agent (WO-06)**: every run (dev: every 5 min, production: 24 h) creates a new recommendation `id`
  (UUIDv7) per workload, so the ETag changes each run. `settings` echoes the agent-reported settings
  of that workload (defaults: production / keep / memoryLimitEqualsRequest true). The hub uses reported
  `settings` from `POST .../workload-status` in the next run. Only workloads with usage data have a
  recommendation. Native sidecars come with `init: true` and flag `sidecar`; classic init containers
  are not rightsized. `removeLimits: [cpu]` only with `cpuLimits: remove` and an existing limit.
  `throttled`/`oom-killed` containers are never decreased (raised by at least +20 % over current).
  `hpa-coupled` = HPA on cpu/memory **utilization** (D-011).
- An agent request updates `clusters.last_seen_at` (heartbeat, ≤ every 15 s, hub clock); `agentVersion`
  from the status batch. `agentStatus` is `stale` after 10 min. A workload an agent reports before any
  metrics exist for it is kept until a run whose data covers its creation (no flapping deletes).
- **UI (WO-14)**: all `ui` endpoints are implemented. Rows/detail of workloads that never had a
  recommendation have no recommendation fields. `riskFlags` is omitted (not `[]`) on rows whose
  recommendation has no flags. `usage` returns max/avg across pods, request/limit step functions from
  KSM and the recommended request from history; containers without usage and without a
  recommendation (classic init containers) are left out.
- **Savings trend (WO-04b)**: points are aligned to multiples of `step` (UTC) and each holds the value
  at the end of its bucket, `min(t+step, to)`. Every regular run stores per cluster exactly the total
  `GET /v1/summary` shows (same summation and rounding) and it takes effect when the run finishes, so
  with the default `to` the last point equals the summary per cluster and for the fleet (fleet savings
  in the summary are the sum of the rounded cluster savings). The trend starts with the first run.
- **Backfill (WO-04c)**: dev computes history from the stored metrics, one backfill run as of each UTC
  midnight of the 29 days before the first regular run (within `history.retention`). The regular run
  goes first; the backfill runs fill in between regular runs (one at a time, newest first), and
  missing days are filled at every start. Backfill runs off that grid or failed (e.g. the 7 runs
  computed by older versions as of the start time, while the synthetic data was being regenerated)
  are deleted with their recommendations once the new ones are stored.
- **Recompute (D-019, WO-04c)**: `POST /v1/admin/recompute[?cluster=]` (admin) queues a run and
  returns `202 {runId, cluster?, requestedAt, coalesced}`. It runs as soon as no other run is in
  progress, as of that moment (data up to the last full step, 5 min). Requests for the same scope
  arriving while one is still queued get its id (`coalesced: true`). Done when the cluster's
  `lastRecommenderRunAt` (`GET /v1/clusters`) passes `requestedAt`.
- **Usage recommendation (WO-04c)**: `recommendation` is the step function of the recommendation
  history, sampled on the same timestamps as `usage`/`request`/`limit` (VictoriaMetrics aligns range
  queries to multiples of `step`, so the first point can precede `from`); no points before the
  workload's first recommendation.
- **Identical consecutive runs** are stored as separate recommendations (each run has its own id and
  `generatedAt`, which agents and the freshness metric rely on); history older than
  `history.fullResolution` is thinned to one per day. The UI collapses unchanged runs.
- **D-016 additions (WO-04b)**: `GET /v1/workloads/facets` (same filters as `/v1/workloads`; each facet
  is counted without its own filter, `total` with all of them), `WorkloadRecommendation.pricing
  {source metrics|default, cpuCoreHourUSD, memoryGiBHourUSD}` on every recommendation shape (absent on
  rows from before), and `ContainerExplanation.confidenceFactors {coverage, samples, variance:
  {score, weight}, confidence}` in the detail (weights 0.6/0.2/0.2; `variance` = 1/(1+cv)).
- **Dashboards (WO-05)**: hub series per docs/contracts/metrics.md, plus `ro_recommender_*` operational
  series: `runs_total{status}`, `run_duration_seconds`, `last_success_timestamp_seconds{cluster}`,
  `pricing_fallback{cluster}` (1 = default rate used), `recommendations{cluster}`;
  `ro_hub_http_requests_total{route,code}`. `ro_actions_total` carries the workload labels (`cluster`,
  `namespace`, `workload_kind`, `workload`) like every hub series (WO-04c).

## Recommender
- PromQL only (`internal/recommender/queries.go`, canonical pod→workload joins); usage = per-step max
  across pods; memory per step uses `max_over_time(working_set[step])` (catches peaks and pods that
  start on the step grid, e.g. CronJobs). Range queries are chunked (2016 points) and halve the chunk
  when VictoriaMetrics answers "not enough memory".
- CPU = P(profile) × (1 + headroom); memory = max(P(profile), peak over `memoryPeakWindow`, default the
  whole window) × (1 + buffer); rounded up to 5m / 4Mi; minimums 10m / 32Mi.
- Profiles: dev 7d P90/P95 +10 %/+10 %; production 14d P95/P99 +15 %/+15 %; high-availability 28d
  P99/P99 +30 %/+25 %. Only the windows of profiles in use are queried.
- Confidence = 0.6 × coverage + 0.2 × samples (saturates at 7 days) + 0.2 × 1/(1+cv); < 0.5 →
  `low-confidence`.
- Savings = (current − recommended request) × cluster-average rate × average running pods × 730 h.
  Without `ro_cpu_core_hourly_cost_usd`/`ro_memory_gib_hourly_cost_usd` for a cluster: default rates,
  a note in the explanation, `ro_recommender_pricing_fallback{cluster}=1`, `recommendations.pricing.source = "default"`.

## DB
`db/migrations/0002_recommender_runs.sql`: `recommender_runs`, `recommender_run_clusters`,
`recommendations.pricing`, index on `audit_events (workload_id, time)`. History thinning: every run kept
for `history.fullResolution` (dev 6h), then one per workload and day, deleted after `history.retention`.

## Tests
`make test-hub` (unit tests); with Postgres: `RO_TEST_DATABASE_URL=postgres://ro:ro@localhost:15432/postgres?sslmode=disable
go test -race ./internal/hub/... ./internal/recommender/... -cover` (creates and drops a throwaway database per test).
Coverage: recommender 92 %, hub 89 %, store 86 %. `RO_TEST_PROM_URL=http://localhost:18428 RO_TEST_CLUSTER=synth-a
go test ./internal/recommender -run TestLive -v` runs the engine against live VictoriaMetrics.

## Deviations / open points
- `dev/hub.yaml` added (the WO verification runs `go run ./cmd/hub --config dev/hub.yaml`); `dev/` is WO-01's.
- VictoriaMetrics on ro-hub has a 768Mi limit (WO-02 note); a full run over ~450 workloads takes ~40 s
  and the hub peaks well under its 1Gi limit.
- WO verification uses ports 8080/8428; in the shared environment VictoriaMetrics is `localhost:18428`
  and the deployed hub `localhost:18080`.

## WO-04d: incomplete metrics and pricing gaps
- **Incomplete metrics guard**: a regular run whose inventory, or number of workloads with request
  series, falls below half of the last accepted run (clusters with >= 10 workloads) is refused for
  that cluster (run cluster `failed`, "metrics look incomplete ..."): nothing is stored, the summary
  and the trend keep the previous values. A drop lasting longer than
  `recommender.incompleteMetricsGrace` (default 30m, negative disables) is accepted as real.
  After a restart the baseline is the stored live inventory. This is what produced the trend dip and
  the zero-savings history entries on 2026-11-04 00:40–01:45 UTC (WO-02 regenerating synthetic data:
  6–31 of 203 workloads, request series missing, no pricing series).
- **Pricing gaps**: a cluster without pricing series at a run's time uses, for a backfill (as-of more
  than 1 h ago), today's series; otherwise the rates of the cluster's last run with series (within
  24 h). Both keep `pricing.source = "metrics"`; only a cluster with no rates anywhere gets `default`.
- **Backfill repricing**: backfill runs that used the default rates while the cluster's latest regular
  run has pricing series are rebuilt by the backfill plan at the next start (the dev history then
  carries today's rates).
- **One-off cleanup of the existing dev history** (not run; for the orchestrator):
  ```sql
  BEGIN;
  CREATE TEMP TABLE bad AS SELECT rc.run_id, rc.cluster FROM recommender_run_clusters rc
    JOIN recommender_runs r ON r.id = rc.run_id
    WHERE NOT r.backfill AND rc.cluster IN ('synth-a', 'synth-b')
      AND rc.as_of >= '2026-11-04 00:40' AND rc.as_of < '2026-11-04 01:45'
      AND (rc.pricing->>'source' = 'default' OR rc.workloads < 200
           OR rc.monthly_savings_cpu_usd + rc.monthly_savings_memory_usd = 0);
  DELETE FROM recommendations x USING workloads w, bad b
    WHERE w.id = x.workload_id AND x.run_id = b.run_id AND w.cluster = b.cluster;
  DELETE FROM recommender_run_clusters rc USING bad b WHERE rc.run_id = b.run_id AND rc.cluster = b.cluster;
  COMMIT;
  ```
  (18 run-cluster rows of 26 in that window.)

## WO-04e: pruning workloads deleted on agent-backed clusters
The inventory spans the whole profile window (days), so a workload deleted on a real cluster
stayed in `/v1/workloads`, the facets and the summary until it left the window. Now:

- **Pulls are recorded.** An agent's `GET /v1/clusters/{c}/recommendations` (first page only;
  requests within 10 s of the last recorded pull don't count) stores the pull time. Migration
  `0006_workload_prune.sql` keeps the last two pulls in `clusters.agent_pull_at` and
  `clusters.agent_prev_pull_at`.
- **The full batch is not flagged.** The agent sends a full status batch 2 s after every successful
  pull, but `WorkloadStatusBatch` doesn't mark it, so the hub infers it from the pull times.
  `workload_status.updated_at` is now the hub clock of the last report.
- **When a workload is deleted.** At each pull, a live workload of that cluster is soft-deleted
  (`deleted_at`, `deleted_by = 'agent'`) when all of these hold:
  - it wasn't reported since the pull before the previous one, so it is missing from two full batches;
  - some workload of the cluster was reported since the previous pull, so that batch arrived;
  - it existed before those pulls;
  - it has no pods in the latest regular run, where `attributes.live` is true when the workload had
    request series at the window end. Attributes written before WO-04e count as live.
- **Expected timing.** Ghosts go at the third pull after the hub deploy, about 1 minute with the
  dev 30 s pull and about 2–3 minutes in prod.
- **What stays.** History, status, `/v1/workloads/{id}` and `/v1/audit` are unchanged.
- **Revival.** A status report revives a workload with `deleted_by = 'agent'`. A regular run revives
  it only when it has pods again: it is still in the inventory window but no longer counts there.
- **Other deletions.** Runs set `deleted_by = 'metrics'` for workloads that leave the inventory, as
  before; existing deleted rows are back-filled as `metrics`.
- **Clusters without an agent** never pull, so the synthetic clusters keep the metrics-only behaviour.
- **Summary vs trend.** The summary drops a ghost at once. The trend drops it from the next regular
  run, because the stored run total comes from the live workloads.
- **Safeguards.** A workload with pods is never pruned, even when the agent doesn't report it, and
  pulls with no status batch since the previous pull prune nothing.
- **Tests.** `internal/hub/prune_test.go` (TestPruneAbsent) and `store_test.go` (TestRecordPull).
- **Open point.** A `full: true` flag on `WorkloadStatusBatch`, set by the agent on full flushes
  and on every chunk, would make the inference exact. That is an additive contract change plus
  ~5 agent lines (WO-06).
