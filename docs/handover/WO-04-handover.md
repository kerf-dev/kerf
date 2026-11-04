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
- An agent request updates `clusters.last_seen_at` (heartbeat, ≤ every 15 s); `agentVersion` from the
  status batch. `agentStatus` is `stale` after 10 min.
- **UI (WO-14)**: all `ui` endpoints are implemented. Rows/detail of workloads that never had a
  recommendation have no recommendation fields. `riskFlags` is omitted (not `[]`) on rows whose
  recommendation has no flags. Savings trend = latest run total per cluster at each step (from
  `recommender_run_clusters`), so it starts with the first run; the dev values file backfills 7 daily
  runs on an empty database. `usage` returns max/avg across pods, request/limit step functions from
  KSM and the recommended request from history.
- **Dashboards (WO-05)**: hub series per docs/contracts/metrics.md, plus `ro_recommender_*` operational
  series: `runs_total{status}`, `run_duration_seconds`, `last_success_timestamp_seconds{cluster}`,
  `pricing_fallback{cluster}` (1 = default rate used), `recommendations{cluster}`;
  `ro_hub_http_requests_total{route,code}`. `ro_actions_total` carries `cluster` like every hub series.

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
- No API field for the pricing fallback (contract has none): proposed optional
  `WorkloadRecommendation.pricing {source, cpuCoreHourUSD, memoryGiBHourUSD}`; the data is stored.
- `dev/hub.yaml` added (the WO verification runs `go run ./cmd/hub --config dev/hub.yaml`); `dev/` is WO-01's.
- The heartbeat is stored with the database clock and compared with the hub clock (fine with NTP).
- VictoriaMetrics on ro-hub has a 768Mi limit (WO-02 note); a full run over ~450 workloads takes ~40 s
  and the hub peaks well under its 1Gi limit.
- WO verification uses ports 8080/8428; in the shared environment VictoriaMetrics is `localhost:18428`
  and the deployed hub `localhost:18080`.
