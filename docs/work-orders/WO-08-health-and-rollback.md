# WO-08 Health watch and rollback

- **Phase / wave:** 2 / 2
- **Depends on:** WO-06 merged
- **Owns:** `internal/health/`, plus the single integration point in `internal/agent/` agreed at kickoff (one file, `internal/agent/health_hook.go`)
- **Decisions:** D-004, D-007

## Goal
Every automatic change is watched, and reverted automatically if the workload gets worse.

## Deliverables
- After each `applied` event, watch the workload for `guardrails.rollback.watchDuration`:
  - OOMKills and restarts from pod status (watch, immediate),
  - CPU throttling ratio from metrics (PromQL against the hub's metrics endpoint, or local cAdvisor if no hub).
- On threshold breach: restore previous values with the same strategy, phase → RolledBack, emit `rolled_back` with the reason, block further decreases for that resource until the cooldown ends.
- Rollback works when the hub is unreachable.
- Metrics for watches in progress and rollbacks by reason.

## Definition of done
On k3d, forcing an OOM after a memory decrease rolls back within one watch interval and the event reaches the hub's audit log.

## Verification
```bash
go test ./internal/health/... -cover
make dev-up && tilt up hub agent webhook
# Apply an aggressive memory recommendation to dev/workloads/memory-leak, then:
kubectl --context k3d-ro-spoke-1 get rsr memory-leak -o jsonpath='{.status.phase}'    # RolledBack
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" 'localhost:8080/v1/audit?workload=memory-leak' | jq '.items[-1].type'   # "rolled_back"
```
