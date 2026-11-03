# WO-06 Agent: policy resolution and apply

- **Phase / wave:** 2 / 1
- **Depends on:** WO-00 (runs against a stub hub until WO-04 lands)
- **Owns:** `cmd/agent/`, `internal/agent/`, `deploy/charts/agent/`, `make/agent.mk`
- **Decisions:** D-004, D-005, D-006, D-007, D-011

## Goal
The per-cluster controller that decides, per workload and resource, whether to act, and applies changes safely.

## Deliverables
- **Policy resolution:** effective policy per workload from global default → cluster default → `RightsizingPolicy` (highest priority match) → workload annotations. Separate modes for CPU and memory. Exposed in `RightsizingRecommendation.status` and reported to the hub.
- **Pull loop:** fetch recommendations from the hub, report workload status and events (`docs/contracts/events.md`). Works offline: no hub → no new actions, nothing reverted.
- **Guardrails:** learning period (data coverage), confidence threshold, max change % per step, min CPU/memory floors, cooldown between changes, never decrease when `throttled`/`oom-killed`, skip `hpa-coupled` (D-011), freeze windows.
- **Strategy `in-place`:** write the `RightsizingRecommendation` (phase Applying), resize each running pod via the `resize` subresource honoring each container's `resizePolicy`; watch pod resize status; on `Infeasible` or `Deferred` longer than `fallbackAfter` → switch to `rollout` and emit `fallback`. Memory on JVM/Python containers requires `RestartContainer` (D-007). Skip nodes with swap or static CPU/Memory Manager.
- **Strategy `rollout`:** server-side apply of container resources on the pod template with field manager `resource-optimizer`, for Deployment, StatefulSet, DaemonSet and CronJob. Previous values stored in an annotation and in the CR.
- **Release:** when a workload leaves `auto-patch`, give up field ownership without deleting the fields, so Argo CD restores Git values; phase → Released.
- **RBAC:** minimal ClusterRole (get/list/watch workloads and pods, patch workloads, `pods/resize`, CRDs). Read-only when every policy is `audit`.
- Helm chart.

## Out of scope
Health watch and rollback (WO-08), `deferred` strategy (WO-10), HPA target changes (WO-11).

## Definition of done
envtest covers policy precedence, guardrails and field-manager behavior; on k3d, an overprovisioned Deployment in `auto-patch` is resized in place with zero restarts, and a `rollout` workload gets a new ReplicaSet with the patched values.

## Verification
```bash
go test ./internal/agent/... -cover            # envtest: precedence matrix, every guardrail, release keeps fields
make dev-up && tilt up agent
kubectl --context k3d-ro-spoke-1 annotate deploy/overprovisioned resource-optimizer.oz.io/cpu-mode=auto-patch
kubectl --context k3d-ro-spoke-1 get rsr overprovisioned -o jsonpath='{.status.phase}'            # Applied
kubectl --context k3d-ro-spoke-1 get pods -l app=overprovisioned -o jsonpath='{..restartCount}'   # unchanged
kubectl --context k3d-ro-spoke-1 get deploy overprovisioned --show-managed-fields -o yaml | grep -A3 resource-optimizer
```
