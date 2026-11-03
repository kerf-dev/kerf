# WO-02 Synthetic metrics and backfill

- **Phase / wave:** 1 / 1
- **Depends on:** WO-00
- **Owns:** `tools/synth/`, `testdata/series/`, `make/synth.mk`
- **Decisions:** D-010

## Goal
Make 14–30 days of realistic history available in minutes, both for local clusters and for unit tests.

## Deliverables
- `tools/synth` CLI generating series for the input metrics in `docs/contracts/metrics.md` (CPU usage counter, memory working set, requests/limits, CFS throttling, OOMKill/restart counters, HPA spec series).
- Patterns: flat, diurnal, weekly, spiky/bursty, slow memory leak, step change, sparse (short-lived), throttled.
- Output modes: (a) push to VictoriaMetrics `/api/v1/import` for a given cluster/namespace/workload, (b) write JSON fixtures to `testdata/series/` for unit tests.
- Deterministic with a `--seed` flag.
- A scenario file format (YAML) so one command backfills a whole fake fleet (e.g. 2 clusters × 200 workloads).

## Definition of done
A scenario backfills 30 days for 400 workloads into local VictoriaMetrics in under 5 minutes, and PromQL over it returns the expected shapes.

## Verification
```bash
go test ./tools/synth/...
go run ./tools/synth backfill --scenario tools/synth/scenarios/fleet-small.yaml --vm-url http://localhost:8428 --days 30
curl -s 'localhost:8428/api/v1/query?query=quantile_over_time(0.95, rate(container_cpu_usage_seconds_total{workload="synth-flat-0"}[5m])[30d:5m])'
```
