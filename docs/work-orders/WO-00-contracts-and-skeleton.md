# WO-00 Contracts and repo skeleton

- **Phase / wave:** 1 / 0 (blocks every other work-order)
- **Depends on:** –
- **Owns:** `go.mod`, `Makefile`, `make/common.mk`, `.gitignore`, `.golangci.yml`, `.github/workflows/ci.yaml`, `api/`, `docs/contracts/`, `db/migrations/`, empty `cmd/*/main.go` stubs
- **Decisions:** D-004, D-005, D-006, D-007, D-012

## Goal
Define every boundary between components so wave-1 tracks can work in parallel without touching each other's files.

## Deliverables
1. **Skeleton:** Go module `github.com/tomer1983/resource-optimizer` (D-012), root `Makefile` that only includes `make/*.mk`, `make/common.mk` with `build`, `test`, `lint`, `generate`. Stub `main.go` for `hub`, `pricing`, `agent`, `webhook`. GitHub Actions `ci.yaml` running build, vet, lint, test and the generate-diff check on every PR (D-013).
2. **CRDs** in `api/v1alpha1` (kubebuilder markers, generated YAML in `api/crds/`):
   - `RightsizingPolicy` (cluster-scoped): `selector` (namespace globs, matchLabels, kinds), `priority`, `profile`, `cpu` {mode, limits: keep|remove|ratio}, `memory` {mode, limitEqualsRequest}, `apply` {strategy, fallbackAfter, maintenanceWindows, freezeWindows}, `guardrails` {learningPeriod, maxChangePercent, minCPU, minMemory, cooldown, rollback {watchDuration, oomKills, restarts, throttlingRatio}}, `hpa` {coordinate, targetBounds}, `jvm` {detect}.
   - `RightsizingRecommendation` (namespaced, one per workload, written only by the agent): `spec.targetRef`, `spec.containers[]` {name, requests, limits}, `spec.strategy`, `spec.source` {recommendationID, generatedAt}; `status.phase` (Pending | Applying | Applied | Blocked | RolledBack | Released), `status.previous`, `status.appliedAt`, conditions.
   - Annotation keys (mode overrides, managed marker, previous values) as Go constants.
3. **Hub API** `api/openapi/hub.yaml` (OpenAPI 3.1):
   - `GET /v1/clusters/{cluster}/recommendations` — per workload: id, containers, confidence, risk flags, monthly savings, data coverage.
   - `POST /v1/clusters/{cluster}/workload-status` — batch: resolved modes, applied values, phase.
   - `POST /v1/clusters/{cluster}/events` — audit events (see events contract).
   - `GET /v1/audit` — query audit log (cluster, namespace, workload, time range).
   - Bearer token per cluster (D-012).
4. **Contracts docs:**
   - `docs/contracts/metrics.md` — required input series (cAdvisor, kube-state-metrics) with the `cluster` label; pricing series `ro_cpu_core_hourly_cost_usd`, `ro_memory_gib_hourly_cost_usd`, `ro_node_hourly_cost_usd`; hub series `ro_recommendation_*`, `ro_workload_mode`, `ro_actions_total`. Labels for each.
   - `docs/contracts/events.md` — event types (`applied`, `skipped`, `blocked`, `fallback`, `rolled_back`, `released`, `failed`) and payload.
5. **DB schema** `db/migrations/0001_init.sql`: clusters, workloads, recommendations (history), workload_status, audit_events.

## Out of scope
Any business logic.

## Definition of done
All deliverables exist, generated code is committed, and every later work-order can name the types/endpoints/metrics it uses.

## Verification
```bash
go build ./... && go vet ./...
make generate && git diff --exit-code          # generated CRDs/deepcopy up to date
npx --yes @redocly/cli@1 lint api/openapi/hub.yaml
k3d cluster create wo00 --image rancher/k3s:v1.35.0-k3s1 && kubectl apply --server-side -f api/crds/ && k3d cluster delete wo00
```
