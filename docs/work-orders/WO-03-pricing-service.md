# WO-03 Pricing service

- **Phase / wave:** 1 / 1
- **Depends on:** WO-00
- **Owns:** `cmd/pricing/`, `internal/pricing/`, `deploy/charts/pricing/`, `make/pricing.mk`, `dev/prices.yaml`
- **Decisions:** D-001, D-008

## Goal
Expose per-node cost and per-vCPU-hour / per-GiB-hour rates as metrics, without OpenCost.

## Deliverables
- `Provider` interface: price for (region, instance type, capacity type).
  - `aws` implementation: AWS Pricing API (on-demand, Linux, shared tenancy) and EC2 spot price history; cached, refreshed daily.
  - `static` implementation: prices from a YAML file (used locally and in tests).
- Node discovery: list nodes per cluster, read instance type, capacity type, region/zone, allocatable CPU and memory. Runs in the hub and reads spoke nodes via each cluster's metrics (`kube_node_labels`, `kube_node_status_allocatable`) — no kube credentials to spokes (D-004).
- Cost split: node price divided into CPU and memory rates using a configurable ratio (default: derived from the instance family's vCPU:GiB price ratio; fallback 1 vCPU = 9 GiB weight).
- Prometheus endpoint exporting the `ro_*_hourly_cost_usd` series from `docs/contracts/metrics.md`, plus a scrape config for vmagent on the hub.
- Helm chart.

## Definition of done
Locally, with the static provider, every spoke node has the three cost series in VictoriaMetrics. The AWS provider passes unit tests against recorded API responses.

## Verification
```bash
go test ./internal/pricing/... -cover          # >= 80% on the split and provider logic
go run ./cmd/pricing --provider static --prices dev/prices.yaml --vm-url http://localhost:8428 &
curl -s localhost:9090/metrics | grep -c ro_cpu_core_hourly_cost_usd
curl -s 'localhost:8428/api/v1/query?query=count by (cluster)(ro_node_hourly_cost_usd)'
```
