# Roadmap

Work-orders live in `docs/work-orders/`. Live status is in `.claude/state/active-tasks.md`.
Each work-order ships on branch `wo-XX-<slug>`; the orchestrator verifies and merges it to `main` (D-013, D-015).

## Phase 1 – Visibility (audit only)
Recommendations, savings estimates and dashboards for every workload in every cluster. Nothing is changed.

| Wave | Work-order | Depends on |
|---|---|---|
| 0 | WO-00 Contracts and repo skeleton | – |
| 0 | WO-01 Local dev environment (k3d) | – |
| 1 | WO-02 Synthetic metrics and backfill | WO-00 |
| 1 | WO-03 Pricing service | WO-00 |
| 1 | WO-04 Hub and recommender | WO-00 |
| 1 | WO-05 Grafana dashboards | WO-00 |
| 1 | WO-14 Web UI (pulled forward, D-014) | WO-00 |

## Phase 2 – Safe automation
Agent, policies, `auto-patch` with `in-place` and `rollout`, guardrails, rollback, audit log, Argo CD integration.

| Wave | Work-order | Depends on |
|---|---|---|
| 1 | WO-06 Agent: policy resolution and apply | WO-00 |
| 1 | WO-07 Mutating admission webhook | WO-00 |
| 2 | WO-08 Health watch and rollback | WO-06 |
| 2 | WO-09 End-to-end suite incl. Argo CD | WO-01, WO-04, WO-06, WO-07 |

Exit criterion: one non-prod EKS namespace on `auto-patch` for 2 weeks with no unplanned rollback.

## Phase 3 – Smarter recommendations (work-orders not written yet)
- WO-10 `deferred` strategy, maintenance and freeze windows
- WO-11 HPA coordination (requests + HPA target in one change; lifts D-011)
- WO-12 JVM detection and heap-aware memory recommendations
- WO-13 Single-replica zero-downtime handling, StatefulSet `OnDelete`

## Phase 4 – Product (work-orders not written yet)
- WO-14b UI actions: one-click apply, mode changes from the UI
- WO-15 Notifications (Slack/Teams)
- WO-16 CUR-based price adjustment (Savings Plans, RIs, spot)
- WO-17 Realized savings (node cost trend vs. released requests)
- WO-18 EKS rollout: Helm values per cluster, Pod Identity for pricing, mTLS
