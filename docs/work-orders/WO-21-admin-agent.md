# WO-21 Admin area: agent

- **Phase / wave:** 4 / 1
- **Depends on:** WO-19 merged
- **Owns:** `internal/agent/` (new `configsync` package + hooks), `deploy/charts/agent/` (RBAC for policy CR writes), `make/agent.mk`
- **Decisions:** D-004, D-005, D-022, D-024

## Deliverables
- **Config pull** `GET /v1/clusters/{c}/config` with ETag, on the pull interval and after any 304. The last config is persisted (in-cluster ConfigMap or CR status) so that a restart without the hub keeps the pause flag and approvals.
- **Materialisation:** create, update and delete `RightsizingPolicy` CRs labelled `managed-by=hub`, with server-side apply using field manager `resource-optimizer`. The agent never touches unlabelled CRs. If a CR name collides with a Git-managed CR, report it as invalid and don't touch it. Invalid specs are not applied and are reported in the inventory.
- **`selector.workloads`** support in policy matching (the CRD field comes from WO-19).
- **Inventory:** every policy CR (hub and Git) with priority, generation, valid and message, plus `configVersion`, in the status batch.
- **Pause:** treated like the cluster-wide freeze (WO-10). No new applies, and `blocked/Paused` is emitted once per workload (D-017 dedup); rollbacks continue. Resume takes effect on the next pull.
- **Approve once:** for an audit workload with an approval matching its current recommendation id, apply it with the configured strategy and all guardrails, emit `applied/ApprovedOnce`, and mark the approval consumed (reported to the hub). The next recommendation is not applied.
- **RBAC:** create/update/delete `rightsizingpolicies` only. A `readOnly: true` install skips materialisation but still reports the inventory.

## Definition of done
Unit + envtest coverage for materialisation (create/update/delete, Git CR untouched, collision, invalid), pause and approve-once. Live on ro-spoke-1 against the WO-20 hub, or against the stub hub if WO-20 hasn't landed yet: a policy from the hub appears as a CR within one pull.

## Verification
```bash
make test-agent && make lint
kubectl --context k3d-ro-spoke-1 get rsp -l resource-optimizer.oz.io/managed-by=hub
```
