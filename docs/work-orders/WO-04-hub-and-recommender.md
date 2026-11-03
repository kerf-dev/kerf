# WO-04 Hub and recommender

- **Phase / wave:** 1 / 1
- **Depends on:** WO-00 (uses fixtures from WO-02 when available; start with hand-written ones)
- **Owns:** `cmd/hub/`, `internal/hub/`, `internal/recommender/`, `db/migrations/0002+`, `deploy/charts/hub/`, `make/hub.mk`
- **Decisions:** D-004, D-005, D-009, D-011

## Goal
The central service: compute a recommendation for every container of every workload in every cluster, store history, serve the hub API.

## Deliverables
- **Inventory** from metrics: workloads (Deployment, StatefulSet, DaemonSet, CronJob) via KSM owner series, containers, current requests/limits, replica count, HPA presence and metric type.
- **Strategy interface** + built-in `percentile` strategy: CPU request = P95 of 5m usage over the window × (1 + headroom); memory request = max(P99, peak) × (1 + buffer); memory limit = request; CPU limit per policy (keep/remove/ratio). Results rounded to sane steps (CPU 5m, memory 4Mi).
- **Profiles** `dev`, `production`, `high-availability`: window, percentiles, headroom, buffer.
- **Confidence** 0–1 from data coverage vs. window, number of samples, variance; recommendations below a threshold are marked `low-confidence`.
- **Risk flags:** `throttled` (CFS throttling ratio > threshold), `oom-killed`, `restarting`, `hpa-coupled` (D-011), `sidecar`, `short-lived`. Throttled/OOM workloads produce an *increase* recommendation, never a decrease.
- **Savings:** (current − recommended) × rate from pricing series × average replicas × 730h, per resource.
- **Persistence:** recommendation history and audit events in Postgres.
- **API:** implement `api/openapi/hub.yaml`; bearer token auth per cluster.
- **Metrics:** export `ro_recommendation_*` and `ro_workload_mode` for Grafana (WO-05).
- Scheduler: full run daily, configurable interval; every window configurable (D-010).
- PromQL only (D-003): queries must run on Prometheus and VictoriaMetrics.

## Out of scope
Applying anything; HPA target math (WO-11); JVM heap logic (WO-12).

## Definition of done
Against backfilled synthetic data, every scenario pattern yields the expected direction and magnitude; the API serves recommendations the agent contract expects.

## Verification
```bash
go test ./internal/recommender/... ./internal/hub/... -cover    # table tests per pattern in testdata/series
make dev-up && go run ./tools/synth backfill --scenario tools/synth/scenarios/fleet-small.yaml --vm-url http://localhost:8428 --days 14
go run ./cmd/hub --config dev/hub.yaml &
curl -s -H "Authorization: Bearer $SPOKE1_TOKEN" localhost:8080/v1/clusters/ro-spoke-1/recommendations | jq '.items | length'
curl -s 'localhost:8428/api/v1/query?query=sum by (cluster)(ro_recommendation_monthly_savings_usd)'
```
