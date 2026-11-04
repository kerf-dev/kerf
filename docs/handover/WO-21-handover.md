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
4. **Open point (approve-once + rollout + Argo CD):** decided as D-027, implemented in WO-21b (below).
   Original note: "the workload stays in audit" (D-024) means
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

## WO-21b: approve-once keeps field ownership (D-027)

Branch `wo-21b-approve-ownership` from origin/main 51d7612. Changed: `internal/agent/controller`
only (`reconciler.go`, `admin_test.go`); `rbac.yaml` untouched (WO-25 p2 owns it).

**Behaviour**
- An approve-once apply sets the RSR `Applied` condition reason to `ApprovedOnce` ("… is live
  (approved once)"). That condition reason is the persisted marker, so it survives agent restarts.
- While it holds, `release()` keeps the `resource-optimizer` field manager on the template resources
  the agent owns that are still in `audit`. Argo CD's `ignoreDifferences.managedFieldsManagers`
  (D-006) then keeps self-heal from reverting them. HPA targets of the change are kept the same way.
- The hold ends:
  - per resource set to `off` (annotation, policy or override); the other resource stays held;
  - for all resources when the hub's recommendation has a new id **and** different values (beyond
    the tolerance) for the held resources, compared with the applied targets; requests always,
    limits where the hub recommends one;
  - when the change was rolled back (phase no longer Applied).

  A release emits no event (logged: "releasing the field ownership of an approved change", why).
  The values stay until Argo CD (or anyone) changes them. Release still waits for a freeze or pause.
- An auto-patch apply, or a new approved apply, replaces the marker as before.

**Interpretation to confirm:** D-027 says "a newer recommendation arrives". The hub issues a new
recommendation id on every recommender run, even when the values do not change. Releasing on any new
id would hand approved values back to Argo CD at the next run (daily in production). So only a new
id with **different values** counts as newer. If you want the literal reading (any new id), it is a
one-line change in `approvedHold`.

**Not implemented: "approval revoked".** The contract has no revocation: approval states are only
`pending | consumed | expired | superseded`, and consumed approvals are no longer sent to the
agent. The agent has no signal after consumption. Today an operator ends the hold by setting the
workload to `off` (Exclude) or by turning it to `auto-patch`. If revocation is wanted, the
contract needs it first (e.g. `ConfigFlags`/`ClusterConfig.revokedApprovals[]` with the
recommendation ids); the agent hook is `approvedHold`.

**Verification**
- `make test-agent` all ok (controller 85.2%); `make lint` 0 issues.
- envtest `TestApprovedOwnership`:
  - after the approved rollout the agent still owns cpu and memory
  - an Argo-style non-forced apply of the Git values conflicts
  - a new id with the same values keeps the hold
  - different values release it with no value change and no `released` event; the Git apply then
    restores the Git values
- envtest `TestApprovedOwnershipModeOff`: cpu off releases cpu only; mode off releases all.
- `TestSameAdvice` (table); existing approve-once tests unchanged and green.
- **Live, ro-spoke-1 with Argo CD (2026-11-04 10:56–11:05 UTC).** Agent `wo21b-cc82649`, real
  WO-20 hub.

  Setup:
  - Deployment `argo-approve` (Git: 300m/128Mi, audit, rollout) in `e2e/wo21b-live` of the
    in-cluster git server.
  - Application `wo21b-live`: automated, prune, selfHeal, `RespectIgnoreDifferences`,
    `managedFieldsManagers: [resource-optimizer]`.
  - A Git-free policy relaxing the guardrails for the namespace.

  Results:
  1. Approved the current recommendation through `POST /v1/workloads/{id}/approve` (admin token).
     At 11:01:48: `applied/ApprovedOnce {approvalId, approvedBy: token:3}`, template 150m/64Mi,
     hub approval state `consumed`.
  2. Field managers: `resource-optimizer`, `argocd-controller`. Applied condition `ApprovedOnce`.
  3. After a hard refresh and about 2 minutes: Application `Synced`/`Healthy`, no new sync
     operation (last one was the initial sync), template still 150m/64Mi.
  4. Git commit adding `cpu-mode: "off"`. At 11:03:46 the agent logged "cpu set to off" and
     released cpu only. Argo CD self-heal restored cpu to 300m within about a second; memory stayed
     owned and held at 64Mi.
  5. Not exercised live: the newer-recommendation release (it needs the hub to change its values);
     covered by envtest.

  Cleanup: Application deleted (cascade), `e2e/wo21b-live` removed from Git (1825e99), namespace
  and policy deleted, agent rolled back to helm revision 20 (`main-9325962`, as deployed by the
  orchestrator).

## WO-21c: drift repair across recommendation ids

Branch `wo-21c-drift-repair` from origin/main 266ebb3. Changed: `internal/agent/controller`
only (`reconciler.go`, new `drift_test.go`).

**Bug.** Drift repair (`keepLive`) ran only while `status.appliedRecommendationID` equalled the
current recommendation id. The hub issues a new id on every run, also with the same values. That
new id evaluated to `skipped/NoSignificantChange` and never reached `keepLive`, so drift repair
stopped at the next hub run. E2E symptom: `TestWebhook/webhook_down_does_not_block_pod_creation`
failed when a hub run landed between the apply and the drift check.

**Behaviour now** (`decide()`, workload Applied with at least one auto-patch resource):
- **Same id:** as before, `keepLive`.
- **New id, guardrailed targets within the tolerance (5%) of the applied values:** the id is
  *adopted*.
  - "Guardrailed targets": the values the guardrails would set.
  - The comparison uses every auto-patch resource; a resource that became auto-patch after the
    change has no applied value and goes through the normal path.
  - What adoption does:
    - `spec.source` (id, generatedAt, confidence, riskFlags) and `status.appliedRecommendationID`
      become the new recommendation's.
    - `appliedAt` and the cooldown are unchanged.
    - No `applied` event.
    - `skipped/NoSignificantChange` is still reported once (D-017), as before. Its reason stays in
      the Blocked condition, so a restarted agent does not repeat it.
  - Then `keepLive`.
  - The spec update bumps the generation. The status (observedGeneration) is written in the same
    reconcile, so the webhook ignores the RSR only between those two requests.
- **New id outside the tolerance:** normal path (guardrails, cooldown, apply).
  - New: while that change is **blocked or skipped** (cooldown, low confidence, hub unreachable,
    HPA, stale …), the applied values stay live, and `keepLive` keeps repairing drift.
  - Before, the same-id branch already repaired regardless of the guardrail outcome.
  - Pause and freeze still stop repair (`keepLive` returns early, as before).
  - A deferred change replaces the spec (phase Deferred), as before.
- **After a rollback (WO-08):** the agent waits ("rolled back; waiting for a new recommendation")
  when the id is the rolled-back one, **or** when the new id's guardrailed targets equal the
  rolled-back values (spec) for every resource it would change.
  - Different values go through the guardrails as before (rollback cooldown on decreases).
  - Related fix: a skipped or blocked decision in phase RolledBack no longer overwrites the RSR
    spec. It keeps the rolled-back values and id. Before, a cooldown-blocked new recommendation
    was written into the spec, and the "same id" check then refused that recommendation for good
    after the cooldown.
- **Approve once (D-027):**
  - Audit-only approved workloads: nothing is adopted (no auto-patch resource). The hold logic
    from WO-21b already compares values, so a new id with the same values keeps the hold.
  - Mixed (cpu auto-patch, memory approved): the adoption keeps the `ApprovedOnce` marker and the
    hold.
- **HPA:** `keepLive` → `keepHPA` as before; the adoption leaves `spec.hpa` and the containers
  unchanged.

**Why guardrailed targets, not raw values.** The applied spec holds guardrailed values (clamped
by minimums and max change). Comparing the raw recommendation would:
- miss a clamped change that stays the same;
- adopt a recommendation that a changed policy would now apply differently (e.g. a new limits
  profile), which then would never be applied.

**Verification**
- `make test-agent` all ok (controller 85.4%); `make lint` 0 issues; controller suite green 3×.
- New envtests (`drift_test.go`), each table case on its own workload:
  - `TestDriftRepairAcrossIDs` (in-place, cpu auto-patch; next run = same id / new id same
    values / new id within tolerance / outside / outside during the cooldown). After the run, a
    new pod with the template values (webhook down) is resized to the live values.
    - Adoption: id in spec and status, observedGeneration current.
    - Event counts: 1 applied (2 for outside), 1 skipped for an adopted new id.
    - A third run with the same values emits nothing.
  - `TestRollbackAcrossIDs` (rollout, cooldown 0 so only the new check protects; same id / new id
    same / within tolerance / outside):
    - the rolled-back values are not applied again, and the spec keeps them;
    - outside: applied.
  - `TestApprovedAcrossIDs` (audit + approve once; same / new id / tolerance / outside): hold
    kept with the `ApprovedOnce` condition, released for different values.
  - `TestApprovedMixedAdopt`: the adoption keeps `ApprovedOnce` and the hold; a forced Argo
    revert of cpu is repaired; 1 applied event.
  - Against the old reconciler, these cases fail: new id, tolerance, outside-cooldown, rollback
    new id and tolerance, mixed.
- Existing `TestSkippedWhileAppliedSurvivesRestart` (skipped once, also across a restart)
  unchanged and green.
- **Live, ro-spoke-1 (2026-11-04 13:22–13:29 UTC).**
  - Setup:
    - Agent `wo21c-71b8b69` (helm rev 29), real WO-20 hub.
    - Namespace `wo21c-live`; policy `wo21c-live` (cpu auto-patch, memory audit, in-place,
      learningPeriod 0, minConfidence 0, maxChangePercent 100).
    - Deployment `drift` (stress-ng, 500m).
  1. Recompute: `applied/RecommendationApplied` in place, 500m→15m, id A `…61cc44b60da0`.
  2. Recompute: new id B `…bcaf24b7c016` with the same 15m. It was adopted:
     - status and spec carry B; generation 2/2; `appliedAt` unchanged;
     - Blocked condition `False/NoSignificantChange`;
     - hub audit: one `skipped/NoSignificantChange`, no new `applied`.
  3. A further hub run: id C `…28696aa7cb3a`, adopted (log "new recommendation advises the
     live values: adopted"). The hub still has exactly 2 events for the workload (1 applied,
     1 skipped) for 3 ids.
  4. Scale 1→2: the webhook injected 15m into the new pod, stamped with id C. So the webhook
     trusts the RSR after the adoption's generation bump.
  5. Drift: the old pod was resized by hand to 500m (`pods/resize`). The agent resized it back to
     15m within about 2 s, under id C ("resized pod to the applied values"), with 0 restarts.
     Before this fix the applied id (A) no longer matched, so nothing would have repaired it.
  - Cleanup: namespace and policy deleted.
  - WO-24's second e2e run (`e2e-run-ac3e1d`) started seconds after this deploy, so it ran on
    this branch's agent. Its log shows drift repair resizing a pod in `e2e-webhook-…`. The agent
    was rolled back to helm rev 28 (`main-1e72c9e`) after that run.
