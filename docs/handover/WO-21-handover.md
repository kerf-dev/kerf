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
- `make test-agent`: all agent packages ok (configsync 87.4%, controller 85.1%, policy 78.8%,
  guardrails 86.6% coverage).
- `make lint`: 0 issues. `helm lint deploy/charts/agent`: ok.
- **envtest, `configsync`**
  - `TestMaterialise`:
    - create, update, delete; nothing written before a config arrives or with readOnly
    - Git CR untouched; name collision reported invalid
    - invalid version not applied, older CR kept and reported invalid
    - a `kubectl edit` on a hub CR is reverted; SSA by `resource-optimizer`
    - `policySource: git` deletes every hub CR
  - `TestSyncPersists`: pause and consumed approvals survive a restart with the hub down; a 404
    or 5xx keeps the last config; the ETag is sent.
  - `TestStateApprovals`, `TestRecheck`.
- **envtest, controller**
  - `TestHubPolicyMaterialised`:
    - the CR appears about 300 ms after the config changes, i.e. on the next pull
    - the workload is applied by it
    - inventory with matched counts and `configVersion` is reported
    - a workload override layers on top (`WorkloadStatus.override`)
  - `TestPause`: `blocked/Paused` exactly once with `details.pauseScope`, nothing applied, hub
    status reason Paused, applied after resume.
  - `TestApproveOnce`:
    - `applied/ApprovedOnce` with `approvalId`/`approvedBy`, rollout
    - approval consumed; modes stay audit, no `released`
    - the next recommendation is not applied
  - `TestApproveOncePaused`: the approval waits for the resume.
- **Unit:** `TestHubLayers`, `TestSelectorWorkloads`, `TestPaused` (guardrails).

### Live, ro-spoke-1 (2026-11-04, 09:50–10:03 UTC)
Setup:
- Agent image `wo21-live1` (this branch), pointed at a stub hub in namespace `wo21-live`.
- The stub ran `--upstream` to the real hub, so recommendations, statuses and audit events stayed
  real; config came from the stub.
- `make agent-crds` was applied first (the CRD lacked `selector.workloads`).
- Workloads in `wo21-live`: `hub-gov` (label `hubpol=yes`), `approve-me` (audit, rollout).
- Git CR `wo21-git` (namespace `wo21-live`, audit, learningPeriod 0).

Results:
1. **Materialisation.** Config v1 was set at 09:51:54 with:
   - hub policy `wo21-hub-policy` (auto-patch, rollout, for `hubpol=yes`)
   - hub policy `wo21-git` (collides with the Git CR)
   - `wo21-bad` (priority 0)
   - pause on

   The CR `wo21-hub-policy` was created at 09:51:57, on the next pull. The Git CR stayed unchanged
   (no label, generation 1, cpu audit). Inventory reported to the stub:
   - `wo21-hub-policy` hub, generation 1, valid, matched 1
   - `wo21-git` git, valid, matched 2
   - `wo21-git` hub: invalid, "name taken by an unmanaged policy"
   - `wo21-bad` hub: invalid, "hub version 1 is invalid and not applied: spec.priority …"
   - `appliedConfigVersion` = the served version
2. **Pause.** With a real recommendation, hub-gov went Blocked/Paused. The real hub's audit had one
   `blocked/Paused {pauseScope: cluster}` at 09:55:27 and no more on later pulls. The template
   stayed at 300m.
3. **Approve once while paused.** An approval for approve-me's current recommendation produced
   `blocked/Paused` and was not applied.
4. **Resume (09:56:42).** At the next pull (09:56:57):
   - hub-gov: `applied/RecommendationApplied` (rollout, 300m→150m, clamped)
   - approve-me: `applied/ApprovedOnce {approvalId, approvedBy: wo21-operator}`, then
     `skipped/ModeAudit`
   - the stub removed the consumed approval
   - approve-me's RSR shows effective audit/audit; agent field ownership released; no `released`
     event
5. **Next recommendation.** After a recompute the hub's recommendation for approve-me changed
   (…5fc → …5ff). It was not applied: template still 150m, no new `applied`.
6. **Restart.** Pause set (fleet) and stored in `resource-optimizer-agent-config-state`; after
   `rollout restart` the agent logged `restored hub config … paused: true` before its first pull.
7. **Delete.** An empty config: the agent deleted `wo21-hub-policy` (Git CR untouched).

Cleanup:
- The agent was redeployed from this branch (`wo21-751d0c6`) with the normal hub URL. The real
  hub answers 404 for `/config`; the agent logs it once and changes nothing.
- Deleted: namespace `wo21-live` (stub hub and workloads), Git CR `wo21-git`, and the state
  ConfigMap.
- `kubectl get rsp` on ro-spoke-1: none.
- **ro-spoke-1 still runs this branch's agent** (main 59539ee + WO-21) until you deploy main.
