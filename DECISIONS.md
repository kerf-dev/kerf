# Architecture decisions

Newest at the bottom. Status: **Accepted**, **Proposed** (default chosen, awaiting confirmation), or **Superseded**.

## D-001 Build in-house, no commercial tool, no OpenCost — Accepted (2026-11-04)
We build the whole system. OpenCost is not used; pricing is our own service (D-008).
Feature set taken from KRR, VPA/Goldilocks, Kubecost, PerfectScale, CAST AI and StormForge.

## D-002 Out of scope — Accepted (2026-11-04)
- Cost allocation / showback per team.
- Argo Rollouts / canary awareness.
- Generating PRs against CD repos (each org structures them differently).

## D-003 Platform — Accepted (2026-11-04)
- Target: up to 10 EKS clusters on Kubernetes 1.35.
- Metrics: Prometheus or VictoriaMetrics is a hard requirement; all queries must be valid PromQL.
  Dynatrace exists in the org but is not a dependency.
- Audience: platform teams and FinOps.

## D-004 Hub-and-spoke, agent pulls — Accepted (2026-11-04)
A central hub (recommender, API, Postgres, pricing) produces recommendations for every workload.
A per-cluster agent pulls them, resolves the local policy, enforces guardrails and applies changes.
The hub never holds write credentials to workload clusters.

## D-005 Modes per workload, per resource — Accepted (2026-11-04)
Modes: `off`, `audit`, `auto-patch`, set separately for CPU and memory.
Precedence: global default (`audit`) < cluster default < `RightsizingPolicy` (by priority) < workload annotation.

## D-006 Apply directly; Argo CD ignores only our fields — Accepted (2026-11-04)
The agent patches live objects with server-side apply under field manager `resource-optimizer`.
Argo CD is configured with `ignoreDifferences.managedFieldsManagers: [resource-optimizer]` and
`RespectIgnoreDifferences=true`, so workloads not managed by us keep Git as source of truth.
Switching a workload back from `auto-patch` must release field ownership so Argo restores Git values.

## D-007 Apply strategies — Accepted (2026-11-04)
- `in-place` (default): resize running pods via the `resize` subresource, plus a mutating webhook that
  sets values on new pods. Pod templates are never touched, so no rollout and no Argo drift.
  Fallback to `rollout` when a resize is `Infeasible` or stays `Deferred` too long.
- `rollout`: patch the pod template (rolling restart).
- `deferred`: patch only inside maintenance windows (phase 3).
- Memory changes on JVM/Python containers require `resizePolicy: RestartContainer` or `rollout`.
- In-place is not used on nodes with swap or static CPU/Memory Manager.

## D-008 Pricing — Accepted (2026-11-04)
Pricing service reads node instance type and capacity type from labels, prices them from the AWS
Pricing API (on-demand) and EC2 spot price history, splits node cost into per-vCPU-hour and
per-GiB-hour rates, and exports them as metrics. A static file-based implementation is used locally.
CUR-based discount adjustment is phase 4.

## D-009 Language and frameworks — Accepted (2026-11-04)
Go for all components; controller-runtime for agent and webhook; Postgres for hub state and audit log.

## D-010 Local development — Accepted (2026-11-04)
k3d with k3s v1.35.x: one `hub` cluster and two spoke clusters. Tilt for the inner loop.
Long learning windows are simulated by backfilling synthetic history into VictoriaMetrics, and every
time window in the system must be configurable.

## D-011 HPA-coupled workloads stay audit-only until HPA coordination ships — Accepted (2026-11-04)
Workloads with a CPU/memory-utilization HPA are flagged `hpa-coupled` and never auto-patched before WO-11.

## D-012 Identifiers — Accepted (2026-11-04)
- Go module path: `github.com/tomer1983/resource-optimizer` (matches the repo).
- CRD API group: `resource-optimizer.oz.io`.
- Annotation prefix: same as the API group.
- Hub <-> agent auth: per-cluster bearer token for MVP; mTLS before production.

## D-013 Source hosting and CI — Accepted (2026-11-04)
Code lives in `github.com/tomer1983/resource-optimizer`, default branch `main`. CI runs on GitHub Actions.
Each work-order is delivered on its own branch `wo-XX-<slug>` and merged to `main` by PR after its verification passes.
