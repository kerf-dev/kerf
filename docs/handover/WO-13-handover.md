# WO-13 hand-over (single-replica zero downtime, StatefulSet OnDelete)

Branch `wo-13-single-replica` (on main 33a939f). Deployed to **k3d-ro-spoke-1 only** with
`make deploy-agent AGENT_CLUSTERS=ro-spoke-1 AGENT_TAG=dev-wo13-<sha>` (release `agent`, namespace `resource-optimizer`);
ro-spoke-2 runs main (an accidental deploy there was rolled back to the orchestrator's revision).

## What it does

### Single-replica protection (guardrail, `internal/agent/guardrails/disruption.go`)
A workload is **single-replica** when it is a Deployment or StatefulSet with `replicas: 1`, or any workload
whose PodDisruptionBudget allows 0 disruptions for its expected pods (computed from the PDB spec like the
disruption controller: `minAvailable`/`maxUnavailable`, percentages rounded up; not from PDB status, which
drops to 0 whenever a pod is down). CronJobs and workloads scaled to 0 never are. Assessment:
`apply.AssessDisruption` (`internal/agent/apply/singlereplica.go`).

For a single-replica workload, without the opt-in annotation `resource-optimizer.oz.io/allow-disruption: "true"`:
- **in-place** is allowed only if no changed resource has `resizePolicy: RestartContainer` on its container;
- **rollout/deferred** is allowed only if the template change keeps a Ready pod at every moment: Deployment
  (or DaemonSet) `RollingUpdate` with maxSurge ≥ 1 and maxUnavailable 0 after rounding (Deployment
  defaults 25%/25% give 1/0 for one replica, so a default single-replica Deployment is safe). Recreate,
  StatefulSets (delete-first) and DaemonSet OnDelete never are;
- otherwise the decision is **blocked `SingleReplicaUnsafe`**; the RSR `Blocked` condition and the event say
  which rule (replicas / which PDB), why the strategy is unsafe, and name the opt-in annotation.
Order in `Evaluate`: after cooldown, before the WO-10 deferred wait (a structural block shows at once).

**In-place fallback** (Infeasible / Deferred too long): when the rollout it would fall back to is unsafe, the
fallback is refused: the pending resize is reverted on the pods, the RSR keeps `spec.strategy: rollout`
(the sticky fallback, so later evaluations explain the block with the rollout rule), phase `Blocked`,
event `blocked SingleReplicaUnsafe` with `details.fallbackReason`.

### StatefulSet `OnDelete` (`internal/agent/apply/ondelete.go`, `internal/agent/controller/disruption.go`)
Rollout patches the template (unchanged), phase `Applied` as for every rollout (the health watch starts).
Then, on every reconcile while pods are outdated (`controller-revision-hash` ≠ `status.updateRevision` and
cpu/memory requests/limits differ from the template), the agent:
1. waits until the StatefulSet controller observed the template, every pod exists, is Ready, and has been
   Ready for `--ondelete-settle` (default 30s, or `minReadySeconds` if longer) — the WO-08 health watch acts
   on OOM kills/restarts of the new pod in that time;
2. waits while the HealthWatch condition reports anything but `Watching`/`Healthy`/`WatchStopped`, while a
   freeze window is open (WO-10; a rollback's pod replacement goes on), and for `deferred` outside a
   maintenance window;
3. evicts the outdated pods with the highest ordinals, at most `--max-pod-deletes-per-run` (default 1) at a
   time, through the **eviction API** (UID precondition), so PDBs apply. A refusal (429, or an eviction the
   API server holds > 10s because a PDB is not observed yet) is retried and reported as
   **blocked `PodDisruptionBudget`** (RSR `Blocked` condition + one event, D-017 slot `ondelete`).
No new decision is taken while a roll is in flight (like an in-place change in `Applying`). The `Applied`
condition message carries the progress (`OnDelete StatefulSet: 1 of 2 pod(s) updated; waiting 30s after
Ready ...`). After a WO-08 rollback (phase `RolledBack`) the same roller replaces the pods again so the
restored template reaches them.

### Config
Flags `--max-pod-deletes-per-run`, `--ondelete-settle`; chart values `onDelete.maxPodDeletesPerRun`,
`onDelete.settle`. RBAC (chart): `pods/eviction create` (dropped with `readOnly`), `policy
poddisruptionbudgets get list watch`.

## Contract additions to make (WO-00 / orchestrator)
Defined in agent code until then (the hub does not validate reasons):
- `api/v1alpha1/wellknown.go`: `ReasonSingleReplicaUnsafe = "SingleReplicaUnsafe"` (blocked),
  `ReasonPodDisruptionBudget = "PodDisruptionBudget"` (blocked), and the user annotation
  `AnnotationAllowDisruption = Prefix + "allow-disruption"`. Today: `guardrails.ReasonSingleReplicaUnsafe`,
  `guardrails.ReasonPodDisruptionBudget`, `apply.AnnotationAllowDisruption`.
- `docs/contracts/events.md` rows: `SingleReplicaUnsafe` — "one replica (or a PDB allowing no disruption) and
  the change would leave no Ready pod (in-place needs a restart, or the rollout has no surge); opt in with
  allow-disruption"; `PodDisruptionBudget` — "an OnDelete StatefulSet pod eviction was refused by a PDB; retried".
- Web (WO-14): show both reasons; `details.fallbackReason` on SingleReplicaUnsafe, `details.pod` on PodDisruptionBudget.

## Verification
- `go test ./internal/agent/... -cover`: apply 58.6%, controller 83.7%, guardrails 86.7% (all ok except
  `TestHPACoordination`, which fails identically on a clean origin/main 33a939f — see findings).
  New tests: `apply/singlereplica_test.go` (assessment table incl. PDB rounding, Recreate, surge/unavailable
  resolution, DaemonSet; OnDelete plan: ordinal order, batch size, Ready/settle/missing-pod waits,
  minReadySeconds), `guardrails/disruption_test.go`, envtest `controller/disruption_test.go`:
  Recreate single replica blocked once (D-017) then applied after the opt-in; in-place Infeasible fallback
  refused (no fallback event, resize reverted, sticky Blocked); OnDelete StatefulSet with an emulated
  StatefulSet controller: PDB refusal -> blocked PodDisruptionBudget + Blocked condition + progress in the
  Applied message, then evictions `-1` then `-0`, never zero Ready pods, one applied event.
- `make lint`: 0 issues. `make test`: ok.
- **Live, ro-spoke-1** (namespace `wo13-live`, deleted afterwards): copies of `dev/workloads/single-replica.yaml`
  (+ its PDB minAvailable 1), `dev/workloads/statefulset-ondelete.yaml` (+ a PDB maxUnavailable 1) and a
  Recreate variant `single-recreate`; RightsizingPolicy `wo13-live` (cpu auto-patch, memory audit, rollout,
  learningPeriod 0s, minConfidence 0). A 1 s readiness sampler (`watch_ready.py`, pods' Ready condition)
  ran throughout: **0 samples with zero Ready pods**.
  - `single-replica`: hub 200m -> 100m; rollout with surge (default 25%/25%): new pod Ready at 06:00:04, old
    pod stopped at 06:00:07. Phase `Applied`, health watch `Watching`.
  - `statefulset-ondelete`: 100m -> 50m; template patched 05:59:59, `statefulset-ondelete-1` evicted at once
    (all pods long Ready), Ready 06:00:29; `-0` evicted 06:01:08 (after the 30 s settle), Ready 06:01:17.
    Applied message showed `OnDelete StatefulSet: 1 of 2 pod(s) updated; waiting for pod(s) to become Ready`.
  - `single-recreate`: `Blocked SingleReplicaUnsafe` — "single replica (replicas: 1; PodDisruptionBudget
    single-recreate allows 0 disruptions of 1 pod(s)): a rollout would leave no Ready pod: Deployment strategy
    Recreate stops every pod before starting new ones; annotate resource-optimizer.oz.io/allow-disruption=true
    to allow it"; template untouched; hub audit has `blocked SingleReplicaUnsafe` once.

## Findings for other tracks
- **TestHPACoordination (WO-11) fails on main** with envtest binaries present (3/3 runs on a clean
  origin/main 33a939f checkout): `hpa_test.go:142` expects a blocked/HPACoupled event "outside 40-90%" but gets
  "HPA coordination is off". Likely cause: the first event for `hpa-bounds` (before the test's policy exists)
  is `blocked HPACoupled` "coordination is off"; once coordination is on the reason, resources and values stay
  the same, so the D-017 dedup (WO-08b: type/reason/resources/values, not the message) suppresses the new
  "outside 40-90%" event. `make ci` / `make test` therefore fail on main and on this branch whenever envtest
  binaries are present (they skip without them); everything else in `make ci` passes.
- **make/agent.mk (WO-06):** `AGENT_TAG ?= dev-$(shell date ...)` was re-evaluated at every use, so a build
  crossing a second pushed one tag and deployed another (ImagePullBackOff, seen here). Fixed on main by the
  orchestrator (expanded once, like webhook.mk).
- **Eviction in envtest / before the disruption controller observes a PDB** the API server holds the eviction
  request (> 5 s, no answer); `apply.Evict` bounds it at 10 s and treats the timeout as a refusal.
- An OnDelete roll replaces pods from the current template, so unrelated template changes (image) that were
  waiting for an OnDelete restart are rolled out with ours. Pods already on `updateRevision` are never evicted.
- Applied for a rollout means "template patched" (as before); for OnDelete the progress is in the Applied
  condition message and the hub status message until every pod is replaced.
