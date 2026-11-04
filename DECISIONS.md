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

## D-014 Web UI in phase 1 — Accepted (2026-11-04)
A custom web UI (WO-14) replaces Grafana as the primary interface and moves from phase 4 to phase 1; Grafana dashboards
(WO-05) stay for operators. Stack: React 19 + TypeScript, Vite, Tailwind v4, shadcn/ui, TanStack Query/Table, ECharts,
client generated from `api/openapi/hub.yaml`. Served as static files by nginx, `/api` proxied to the hub.
The hub API gains read endpoints for the UI (summary, workloads, usage series, history, clusters, savings trend) under a
separate `ui` token scope.

## D-015 Autonomous delivery — Accepted (2026-11-04)
The orchestrator owns delivery end to end: it starts tracks, verifies them, and merges verified branches into `main`
itself (no PRs while `gh` is unavailable). Each merge is recorded in the integration log of `.claude/state/active-tasks.md`.

## D-016 Hub API additions for the UI — Accepted (2026-11-04)
Additive, optional changes to `api/openapi/hub.yaml` (WO-04b): `GET /v1/workloads/facets` (role `ui`, counts per
cluster/namespace/kind/mode/risk flag under the same filters as `/v1/workloads`), `WorkloadRecommendation.pricing`
(`source`, `cpuCoreHourUSD`, `memoryGiBHourUSD`; `source=default` when no pricing series exist) and
`confidenceFactors` (coverage, samples, variance with weights) in the detail explanation. Agents ignore them.

## D-017 Event de-duplication — Accepted (2026-11-04)
The hub mints a new recommendation id on every run, so per-id de-duplication floods the audit log with identical
`skipped`/`blocked` events. The agent emits a `skipped`/`blocked` event once per (workload, type, reason) and again
only when the reason or the proposed values change. `applied`, `fallback`, `rolled-back`, `released`, `failed` are
unchanged (one per action).

## D-018 Dev tokens — Accepted (2026-11-04)
Local k3d only: the hub's dev tokens live in `deploy/charts/hub/values-dev.yaml`; each spoke's agent gets the
matching one from `deploy/charts/agent/values-<cluster>.yaml` with `tokenSecret.create: true`, so `make deploy-agent`
and Tilt connect agents without manual Secret handling. Real clusters use an externally managed Secret (create: false).

## D-019 Admin recompute trigger — Accepted (2026-11-04)
`POST /v1/admin/recompute` (role `admin`, optional `?cluster=`) starts a recommender run immediately and returns
202 with the run id. Used by the e2e suite and operators; the scheduled interval is unchanged.

## D-020 HPA coupling lifted — Accepted (2026-11-04)
WO-11 replaces D-011's audit-only rule: workloads with a CPU/memory utilization HPA get a request change together
with a new HPA `averageUtilization` target (kept within 40–90 %), applied in one `rollout`. HPAs on external/custom
metrics or pinned at `maxReplicas` stay audit-only. D-011 remains in force for workloads where that guard applies.

## D-021 Hub deploys and migrations are centralized — Accepted (2026-11-04)
After two hub outages caused by parallel deploys with colliding migration numbers, only the orchestrator deploys the
hub (from `main`, after merging), and the orchestrator assigns `db/migrations` numbers. Spoke deploys check that no
`make e2e` is running first.

## D-022 Policy management: UI + Git — Accepted (2026-11-04, user)
The hub stores policies created in the UI. Each agent pulls them and materialises them as `RightsizingPolicy` CRs
labelled `resource-optimizer.oz.io/managed-by=hub`, and it only ever changes CRs with that label. CRs from Git
keep working, are reported back to the hub, and appear read-only in the UI. Global and cluster defaults become the
hub policies `ro-global-defaults` (priority 0) and `ro-cluster-defaults` (priority 10). UI workload overrides use
priority ≥ 900000 and a new `selector.workloads` field. Workload annotations still win (D-005). Per-cluster pause
is a flag in the agent config pull and behaves like a cluster-wide freeze. Plan: `docs/plans/admin-area.md`.

## D-023 UI login: local users first — Accepted (2026-11-04, user)
The hub manages users (bcrypt) with the roles `viewer`, `operator` and `admin`, and issues HttpOnly session
cookies with CSRF protection. The initial admin comes from a Secret. OIDC comes later on the same session and role
model. The machine tokens (`agent`, `ui`, `admin`) are unchanged.

## D-024 Approve once — Accepted (2026-11-04, user)
Operators can approve the current recommendation of a workload in `audit` mode. The hub records the approval for
that recommendation id. The agent applies it once, with all guardrails, and the workload stays in audit.

## D-025 Metrics sources and cluster readiness — Accepted (2026-11-04)
Each cluster is bound to a datasource (Prometheus-compatible: Prometheus, VictoriaMetrics, Thanos, Mimir, AMP) with
a configurable cluster label key/value and extra matchers. Query results are re-labelled to the registered cluster
name. Readiness is computed from hub-side metrics checks and agent self-checks (RBAC per capability, resize support,
webhook, Argo CD config). `auto-patch` requires the state *Automation ready*; an admin can override it with a
reason, and the override is audited. Plan: `docs/plans/cluster-onboarding.md`.
