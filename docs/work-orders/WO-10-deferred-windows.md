# WO-10 `deferred` strategy, maintenance and freeze windows

- **Phase / wave:** 3 / 2
- **Depends on:** WO-06, WO-08 merged
- **Owns:** `internal/agent/windows/` (new), the window fields in `api/v1alpha1` (contract change, approved by the orchestrator at kickoff), `internal/agent/apply` call sites that check windows
- **Decisions:** D-005, D-007

## Goal
Changes that need a restart happen only when the owning team allows it, and nothing changes during a freeze.

## Deliverables
- `RightsizingPolicy.spec.windows`: `maintenance[]` (cron + duration + timezone) and `freeze[]` (absolute from/to or cron), plus a cluster-wide freeze via chart value.
- `deferred` strategy: queue the change, apply it as `rollout` inside the next maintenance window; RSR phase `Deferred` with `status.nextWindow`.
- Freeze blocks every strategy (including in-place and rollback-free releases) with `blocked/FreezeWindow`; rollbacks (WO-08) are still allowed.
- Hub status/UI: `nextWindow` reported in workload-status (additive field).

## Definition of done
On k3d, a `deferred` workload applies only inside a 2-minute window; a freeze blocks an in-place change and the event reaches the hub.

## Verification
```bash
go test ./internal/agent/... -cover
make lint test
# live: policy with a window starting in 2 min → RSR Deferred → Applied after window opens
```
