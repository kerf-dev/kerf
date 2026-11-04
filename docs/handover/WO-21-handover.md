# WO-21 hand-over (admin area: agent)

Branch `wo-21-admin-agent`, cut from origin/main 59539ee (WO-19 merged).
Contract: `docs/contracts/admin.md`, D-022 / D-024.

## What the agent does now

**Config pull** (`internal/agent/configsync`)
- `GET /v1/clusters/{c}/config` with `If-None-Match`, on every pull.
- The pull runs inside the recommendation puller, right after the recommendations and before the
  reconciles are triggered, so a pause or an approval applies to the reconciles of that same pull.
- The last config, its ETag and the consumed approvals are stored in the ConfigMap
  `<fullname>-config-state` (key `state.json`) and restored at start. A restart without the hub
  keeps the pause flag and the approvals.
- Above 768 KiB the policies and the ETag are left out of the stored state; the next pull then
  fetches everything again.
- A hub that answers 404/501 (no WO-20 yet), a 5xx or no answer keeps the last config, logged once.

**Materialisation**
- Hub-labelled CRs are made equal to `config.policies`: name, label, the three annotations and
  `spec`, written by server-side apply under field manager `resource-optimizer`. Fields someone else
  added (`kubectl edit`) are removed by a follow-up update.
- Labelled CRs that are not in the list are deleted. With `policySource: git` (`policies: []`) all
  of them go.
- CRs without the label are never written:
  - A hub policy whose name is taken by one is reported `valid: false`,
    `message: "name taken by an unmanaged policy"`.
  - A hub policy failing `validate.HubPolicy` is not applied. Its older valid CR stays, and the
    inventory reports that CR as invalid with "hub version N is invalid and not applied: …".
- Nothing is written before a config is received.
- `readOnly`: nothing is written; unmaterialised hub policies are reported with
  "agent is read-only: hub policies are not materialised".

**`selector.workloads`**: matched exactly, combined with AND (`policy.Matches`).

**Layering** (`policy.Resolve`, `admin.md` §2):
```
built-in < ro-global-defaults < chart clusterDefaults < ro-cluster-defaults
  < winning policy < ro-override-… < annotations
```
- A CR counts as a layer only with label `managed-by=hub` **and** annotation `hub-policy-type`
  `global-default`, `cluster-default` or `workload-override`. A Git CR carrying the annotation stays
  an ordinary competing policy.
- Mode sources: hub global default → `global-default`, hub cluster default → `cluster-default`,
  override → `policy`.
- `WorkloadStatus.override` names the override. The RSR `PolicyResolved` message also names it; the
  CRD status has no field for it.

**Inventory**: the status batch carries `configVersion` every time and `policyInventory` with every
full batch. The inventory lists every CR (hub and Git: name, source, priority, generation, valid,
message, hub id/version/type, spec) plus the hub policies without a CR (generation 0).
- `matchedWorkloads` counts the workloads where the policy is the winner or the override.
- Hub default layers count every workload.
- Validity of Git CRs is informational (`validate.Spec`).

**Pause**: `flags.paused` works like the cluster-wide freeze (WO-10):
- Nothing new is applied: `blocked/Paused` is emitted once per workload (D-017), with
  `details.pauseScope`.
- No in-place drift repair; an in-flight in-place change only observes.
- A release that would hand fields back to Argo CD waits (`blocked/Paused`).
- Health-watch rollbacks keep running.
- In the guardrails, Paused is checked right after "hub unreachable", so it is the reason the hub
  shows for every auto-patch workload while paused.
- Audit workloads stay `skipped/ModeAudit`.
- `resumeAt` is not evaluated. Resume takes effect on the next pull.

**Approve once**
- Trigger: an unexpired, unconsumed approval for the workload's current recommendation id.
- Its audit resources count as auto-patch for that one change. The strategy comes from the
  effective policy; every guardrail applies (paused → waits, low confidence → blocked, …).
- The applied event becomes `applied/ApprovedOnce` with `details.approvalId` and `approvedBy`
  (message "approved once by …"), and the approval is marked consumed locally (persisted).
- The RSR status and the hub keep showing the resolved modes (audit), so no `released` event follows.
- An in-place change in flight keeps the approval until it completes.
- A recommendation that is already live is not applied again.
- The next recommendation is skipped as `ModeAudit`.
- With rollout, the agent drops its field ownership on the next reconcile, as for any audit workload
  (values stay; see the open point below).

**Recheck plumbing (for WO-25)**: `Syncer.Recheck(requestedAt)` is called once per new
`flags.recheckRequestedAt`. `capabilities[]` and `checks[]` stay nil (omitted).

**RBAC / chart**
- `rightsizingpolicies`: create, update, patch, delete (only with `configSync.enabled` and not
  `readOnly`).
- New Role `<fullname>-config-state`: configmaps get/update on that name, plus create.
- Values: `configSync.enabled` (default true).
- Args: `--config-sync`, `--config-state-configmap`.

**Stub hub** (`agent stub-hub`)
- Serves `GET config`; `--config` takes a file (cluster → ClusterConfig, re-read every 10 s;
  configVersion derived when empty).
- Removes an approval when it stores its `applied/ApprovedOnce` event.
- Records the reported `configVersion` and `policyInventory`.
- `--upstream <hub URL>` forwards recommendations, statuses and events to a real hub, so an agent
  keeps its real data while taking config from the stub.
- Debug endpoints (stub only, no auth): `GET /debug/clusters/{c}` and
  `PUT /debug/clusters/{c}/config`.

**Other fixes**
- The full status flush after a pull was re-armed by every pull. With a pull interval under 2 s it
  never fired; it is now armed only when none is pending.
- `hubclient.ReportWorkloadStatus` now takes a `WorkloadStatusBatch`. It sends an items-less batch
  when it carries an inventory, capabilities or checks.

## For the orchestrator (outside my Owns)
1. `cmd/agent/main.go`: flags and wiring (agent component, as in WO-06/08/10).
2. **New metrics** (contract doc `docs/contracts/metrics.md` not edited):
   - `ro_agent_config_pulls_total{result=ok|not_modified|unsupported|error}`
   - `ro_agent_paused`
   - `ro_agent_policies{source,valid}`
3. **CRDs on the spokes**: ro-spoke-1 still had the pre-WO-19 `RightsizingPolicy` CRD (no
   `selector.workloads`). On such a CRD the apiserver prunes `workloads`, which widens an override
   to its namespace and kind.
   - Run `make agent-crds` on every spoke before or with the WO-21 deploy.
   - I applied it on ro-spoke-1 for the live check (see below).
4. **Open point (approve-once + rollout + Argo CD):** "the workload stays in audit" (D-024) means
   the agent does not keep ownership of the template fields after the change. On an Argo CD app
   with selfHeal, Argo CD then reverts the approved values at its next sync. In-place approvals are
   unaffected until the next rollout. If approved values should stick on GitOps workloads, the agent
   would have to keep ownership until the next recommendation. That needs a decision; I did not
   build it.

## Verification
(see below)
