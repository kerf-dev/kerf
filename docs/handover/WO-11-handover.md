# WO-11 HPA coordination — hand-over

Lifts D-011 for workloads whose HPA scales on one CPU or memory **utilization** metric: their
requests change together with the HPA's `averageUtilization` target so the scaling behaviour stays
the same. Everything else about D-011 (audit-only) still holds when coordination is off or impossible.

## The rule

An HPA on a resource's utilization scales on `usage / (sum of the pod's app-container requests × target)`
(`desired = ceil(replicas × utilization / target)`). Keeping `sum(requests) × target` constant keeps
that ratio, so: `target' = target × sum(requests before) / sum(requests after)`, rounded to whole percent.

- **Hub (recommender, `internal/recommender/hpa.go`)**: for `hpa-coupled` workloads the strategy's
  requests are kept unless the target would leave **40–90 %** (widened to include the current
  target); then the target is clamped and the requests of that resource are scaled so the product
  holds again (limits follow the limit rule, savings are recomputed, explanation notes say so).
  The recommendation carries `hpa {name, metric, currentTarget, recommendedTarget, minReplicas,
  maxReplicas, skipReason}`. `skipReason` (no `recommendedTarget`): `max-replicas-reached` (peak
  `kube_horizontalpodautoscaler_status_current_replicas` over the loaded window ≥ `spec.max_replicas`),
  `unsupported-metrics` (more than one utilization metric, or any other metric, or no target),
  `missing-request`, `target-out-of-bounds` (a throttled / oom-killed container would have to go lower).
  Example `dev/workloads/hpa-cpu`: 100m request, ~40m usage, target 60 % → strategy ~50m → target
  120 % → clamped to 90 %: request 70m, target 86 %.
- **Agent** (`internal/agent/apply/hpa.go`, reconciler side in `internal/agent/controller/hpa.go`):
  for a workload whose HPA's utilization resource is in auto-patch:
  - `RightsizingPolicy spec.hpa.coordinate` (global default **false**) off → blocked `HPACoupled` (as before);
  - HPA not coordinatable (several or other metrics, `ContainerResource`, live `currentReplicas ≥ maxReplicas`,
    or the hub's `hpa.skipReason`) → blocked `HPACoupled` with the reason in the message;
  - otherwise the strategy is **rollout** (in-place is never used for HPA workloads), the agent
    recomputes the target from the live HPA and the guardrailed targets (after `maxChangePercent`
    clamping), blocks if it leaves `spec.hpa.targetBounds` (default 40–90 %, widened to the current
    target), and in the same reconcile patches the pod template and then the HPA (`spec.metrics`,
    server-side apply, field manager `resource-optimizer`, forced).
  - Resources the HPA does not scale on change as for any workload (e.g. memory with a CPU HPA)
    without touching the HPA — previously the whole workload was blocked.
  - RSR: `spec.hpa {name, resource, averageUtilization}` and `status.previousHPATarget` (additive CRD fields).
  - `applied` event: message ends with `; HPA <name> cpu target 60% -> 86%`, details `hpa`, `hpaResource`,
    `hpaTargetFrom`, `hpaTargetTo` (documented in docs/contracts/events.md).
  - Drift repair re-asserts the target like the template; release (leaving auto-patch) drops our
    managedFields entry on the HPA too (values stay; Argo CD restores Git on its next sync);
    a health-watch rollback (WO-08) of the HPA's resource restores `previousHPATarget`
    (`apply.RestoreHPA`, also in `finishRollback`).
  - If the HPA patch fails after the template was patched, the change is recorded as applied, a
    `failed`/`ApplyError` event is sent, and drift repair retries the target.
- **RBAC**: agent chart gets `patch` on `horizontalpodautoscalers` (unless `readOnly`).
- **Argo CD**: the global `resource.customizations.ignoreDifferences.all` (managedFieldsManagers
  resource-optimizer, WO-01) covers HPAs. The spoke-1 Application's own `ignoreDifferences` list
  names only workload kinds; add `autoscaling/HorizontalPodAutoscaler` there if a sync with
  `RespectIgnoreDifferences` turns out not to honour the global setting (not changed here: WO-01 file).

## Contract changes (approved at kickoff)
- `api/openapi/hub.yaml`: `WorkloadRecommendation.hpa` → `HPACoordination` (additive, optional).
- `api/v1alpha1`: `RightsizingRecommendation.spec.hpa`, `.status.previousHPATarget` (CRDs regenerated, chart copy synced).
- `db/migrations/0004_recommendation_hpa.sql`: `recommendations.hpa jsonb` (number pending the
  orchestrator; WO-12 has 0003). `ADD COLUMN IF NOT EXISTS`.
- `docs/contracts/events.md`: `HPACoupled` description, `applied` HPA detail keys.
- D-011 text: to be updated by the orchestrator (see report).

## Tests
- `internal/recommender/hpa_test.go`: table of coordination cases (bounds, sidecar sum, init ignored,
  memory, skip reasons, limits/savings), invariant check, `Recommend` hook.
- `internal/hub/hpa_test.go`: `hpa` on agent/UI/history endpoints, skip at max replicas.
- `internal/agent/apply/hpa_test.go`: `Coupling`, `RequestSum`, `NewHPATarget`.
- `internal/agent/controller/hpa_test.go` (envtest): blocked without coordination → policy on →
  requests + target in one rollout (in-place configured), RSR fields, event details, out-of-bounds
  and at-max blocked, drift repair, release of the HPA, rollback restore.

## e2e (for WO-09)
Scenario "HPA coordination" (not added here): copy `dev/workloads/hpa-cpu` into a test namespace with
`resource-optimizer.oz.io/mode: auto-patch`, a RightsizingPolicy selecting it with
`spec.hpa.coordinate: true`, `guardrails.learningPeriod: 0s`, `minConfidence: "0"`; after
`POST /v1/admin/recompute?cluster=ro-spoke-1` expect one `applied` event with `hpaTargetFrom=60`,
the template request and the HPA target changed (HPA `managedFields` has `resource-optimizer`
owning `f:metrics`), and `status.currentReplicas` within ±1 of the value before over the next 10 min.
The existing e2e `hpa` scenario (blocked `HPACoupled`) still holds as long as its namespace has no
coordinating policy.

## Known
- `internal/health` `TestThrottlingRollback` is flaky on origin/main too (2 of 3 runs failed there:
  `cpu after rollback = 100m`, then `already exists` on rerun) — WO-08.

## Live definition of done (2026-11-04, main-a18528f / main-33a939f)
Copy of `dev/workloads/hpa-cpu` in namespace `wo11-hpa` with `mode: auto-patch` and a policy with
`hpa.coordinate: true`, `learningPeriod: 0s`, `minConfidence: "0"`, on both spokes:
- ro-spoke-1, 05:52:59 UTC: one `applied` event, "cpu request 100m->70m, memory limit 64Mi->32Mi;
  HPA hpa-cpu cpu target 60% -> 86%", one new ReplicaSet; the HPA's `spec.metrics` owned by
  `resource-optimizer` (Apply). ro-spoke-2, 05:55:19 UTC: same change, then `skipped/NoSignificantChange`.
- The next hub run is stable (currentTarget 86, recommendedTarget 86, request 70m: no ratcheting).
- Replicas: 2 in every 30 s sample before and for 12 min (spoke-1) / 10 min (spoke-2) after the
  change; utilization ~40 % of 60 before, ~50-66 % of 86 after (same ratio). Test resources deleted.
