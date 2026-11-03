# WO-05 Grafana dashboards

- **Phase / wave:** 1 / 1
- **Depends on:** WO-00 (metric names); real data once WO-03/WO-04 land — build against WO-02 synthetic series first
- **Owns:** `dashboards/`
- **Decisions:** D-003, D-005

## Goal
Platform and FinOps can see waste, opportunities and why each recommendation was made, without a custom UI.

## Deliverables
- **Overview:** estimated monthly savings by cluster and namespace, CPU and memory requested vs. used vs. recommended, count of workloads per mode and per risk flag.
- **Opportunities:** table ranked by monthly savings — cluster, namespace, workload, container, current vs. recommended, confidence, risk flags, mode.
- **Workload drill-down (explainability):** usage over the window with P50/P95/P99/max lines, current request and limit, recommended values, throttling and OOM events.
- **Automation:** actions over time by type (`ro_actions_total`), rollbacks, blocked reasons.
- Variables: cluster, namespace, workload, container; every panel uses only PromQL.
- Dashboards as JSON files, provisioned by WO-01's Grafana.

## Definition of done
All four dashboards render with data in the local environment and have no panel errors.

## Verification
```bash
for f in dashboards/*.json; do jq empty "$f" || exit 1; done
make dev-up   # Grafana on the hub provisions dashboards/ automatically
# Open each dashboard, cycle the cluster variable through both spokes, confirm no "No data"/error panels
```
