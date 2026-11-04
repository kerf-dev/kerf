# WO-13 Single-replica zero-downtime handling, StatefulSet `OnDelete`

- **Phase / wave:** 3 / 1
- **Depends on:** WO-06, WO-08 merged
- **Owns:** `internal/agent/apply/singlereplica.go`, `internal/agent/apply/ondelete.go` (new), guardrail additions in `internal/agent/guardrails`
- **Decisions:** D-007

## Goal
Rightsizing never causes downtime for single-replica workloads, and StatefulSets with `updateStrategy: OnDelete` are handled explicitly.

## Deliverables
- Single replica (Deployment `replicas: 1`, or PDB that allows 0 disruptions): in-place only when the resize needs no restart; a `rollout` is allowed only with `maxSurge ≥ 1` and `maxUnavailable: 0`, otherwise `blocked/SingleReplicaUnsafe` (opt-in annotation `resource-optimizer.oz.io/allow-disruption: "true"`).
- StatefulSet `OnDelete`: `rollout` patches the template, then the agent deletes pods one at a time (highest ordinal first), waiting for Ready and health watch (WO-08) between pods; configurable `maxPodDeletesPerRun`.
- PDB respected for every pod deletion (use the eviction API).
- RSR conditions and events explain every block.

## Definition of done
On k3d, `dev/workloads/single-replica` and `dev/workloads/statefulset-ondelete` in auto-patch reach `Applied` with no period of zero Ready pods (checked by a watcher in the test).

## Verification
```bash
go test ./internal/agent/... -cover
make lint test
# live: watch `kubectl get deploy single-replica -w` during apply → readyReplicas never 0
```
