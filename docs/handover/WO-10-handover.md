# WO-10 hand-over (deferred strategy, maintenance and freeze windows)

Branch `wo-10-windows`. Not deployed yet: the orchestrator merges WO-12 → WO-11 → WO-10, deploys the hub from
main (migration 0005), then the agent is deployed and the live check below is run.

## Contract changes (additive, approved at kickoff)
- CRD `RightsizingRecommendation`: phase `Deferred`; `status.nextWindow` (when the next maintenance window
  opens, set only while Deferred); print column "Next Window" (wide output).
- `RightsizingPolicy`: no new fields. The WO's `spec.windows` already exists as `spec.apply.maintenanceWindows`
  and `spec.apply.freezeWindows` (WO-00): recurring `schedule` (5-field cron) + `duration` (+ `timeZone`), or
  one-off `start` + `end`. Adding `spec.windows` would have been a second way to say the same; the field docs now
  describe the freeze semantics.
- Hub API: `Phase` enum gains `Deferred`; `WorkloadStatus.nextWindow` (date-time).
- DB: `db/migrations/0005_deferred_windows.sql` (number assigned by the orchestrator; idempotent): phase CHECK
  gains `Deferred`, new column `workload_status.next_window`. Hub accepts, stores and returns both.
- Web: `Deferred` in `PHASES` and the phase badge map, `schema.d.ts` regenerated (minimal; WO-14d owns the UI).
- Docs: events.md (`FreezeWindow`, `OutsideMaintenanceWindow` meanings), metrics.md (`ro_workload_phase` values).
- CRDs regenerated (`make generate`, `make agent-sync-crds`); apply them on the spokes with `make agent-crds`
  (server-side) — Helm's `crds/` does not upgrade existing CRDs.

## Agent behaviour
- New package `internal/agent/windows`: `Open`, `Next` (next opening), `End` (when open windows close, following
  chained ones), cluster `Freeze` (`LoadFreeze`, `Merge`). `policy.InWindow` delegates to it.
- `deferred`: once a change passes every guardrail (learning period, confidence, cooldown, …) and no maintenance
  window is open, the RSR gets the guardrailed values, `spec.strategy: deferred`, phase `Deferred`,
  `status.nextWindow`, and one `blocked/OutsideMaintenanceWindow` event (D-017 de-duplication); the reconcile is
  requeued for the window start. Inside a window it is re-evaluated and applied as a rollout (SSA on the
  template); the `applied` event carries `strategy: deferred`. No change → not Deferred. No window configured →
  Deferred without `nextWindow`.
- Freeze = policy `freezeWindows` ∪ cluster-wide freeze (chart value `freeze: {now, windows}`, file
  `--freeze-file`; a policy cannot override it). While frozen: no apply of any strategy (`blocked/FreezeWindow`),
  no in-place drift repair, an in-flight in-place change only observes (no new resize, no fallback), and a
  release that would hand fields back to Argo CD waits (`blocked/FreezeWindow`, ownership kept, the stored
  effective modes held) and happens — with exactly one `released` — when the freeze ends. Health-watch rollbacks
  (WO-08) are not affected. An invalid freeze window counts as frozen.
- Hub status carries `nextWindow` while Deferred.
- Also fixed: the puller blocked its loop for 2s after every pull (waiting to send the full status batch), which
  delayed events and statuses; the full flush is now a timer.

## Verification (so far)
- `make ci` green (build, vet, lint 0 issues, tests incl. envtest, verify-generate, lint-openapi), with
  `RO_TEST_DATABASE_URL` set so the hub's Postgres tests ran against migrations 0001, 0002, 0005.
- `internal/agent/windows` 96.6% coverage (open/next/end, time zones, chained windows, freeze file).
- envtest: `TestDeferredWindow` (Deferred with nextWindow and hub status, applied only after the window opens,
  `applied` strategy deferred, nextWindow cleared), `TestFreezeBlocksInPlace` (pod not resized during the freeze,
  resized after), `TestFreezePostponesRelease` (ownership kept during the freeze, released exactly once after).
- Hub: `TestDeferredStatus` (Deferred + nextWindow round trip, cleared on the next status).
- Web: lint, vitest 39/39, `gen:api:check` up to date.

## Live check (to run after the hub is deployed from main)
On ro-spoke-1, own namespace: a workload with a RightsizingPolicy `strategy: deferred` and a one-off maintenance
window starting ~2 min ahead → RSR `Deferred` with `nextWindow`, `blocked/OutsideMaintenanceWindow` in the hub
audit log → `Applied` (`applied`, strategy deferred) right after the window opens. Then a policy freeze window on
an in-place workload → `blocked/FreezeWindow` in the hub audit log, pod not resized.
