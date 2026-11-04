# WO-09 End-to-end suite incl. Argo CD

- **Phase / wave:** 2 / 2
- **Depends on:** WO-01, WO-04, WO-06, WO-07 merged (WO-08 scenarios added when it lands)
- **Owns:** `test/e2e/`, `make/e2e.mk`, `ci/github-workflows/e2e.yaml` (moved to `.github/workflows/` once a token with `workflow` scope exists)
- **Decisions:** D-006, D-007, D-010

## Goal
One command proves the whole system works across hub and spokes, including the Argo CD contract.

## Deliverables
Go test suite against the k3d environment, each scenario independent:
1. Audit mode: recommendation appears in the hub, nothing changes in the cluster.
2. `in-place` CPU decrease: values change, no restarts, Deployment spec unchanged.
3. `in-place` memory decrease on JVM workload: container restarts (RestartContainer), not the pod.
4. `Infeasible` resize → fallback to `rollout`, `fallback` event recorded.
5. `rollout`: new ReplicaSet, field manager `resource-optimizer` owns the resource fields.
6. **Argo CD:** after a `rollout` patch, a hard sync does not revert values and the app is Synced.
7. **Argo CD release:** switch workload to `audit` → next sync restores Git values.
8. Webhook: scale-out gets recommended values; webhook down does not block pod creation.
9. Guardrails: throttled workload never decreased; HPA-coupled workload untouched; max change % respected.
10. Rollback scenarios from WO-08.
11. Hub unreachable: agent applies nothing new and reverts nothing.

CI job creates the clusters, runs the suite, and uploads logs on failure.

## Definition of done
The suite passes 3 consecutive runs locally and in CI in under 30 minutes.

## Verification
```bash
make dev-reset && make e2e
make e2e && make e2e      # flakiness check
```
