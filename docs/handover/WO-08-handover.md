# WO-08 hand-over (health watch and rollback)

Branch `wo-08-health` (from main 38a82b4, main merged back in at 6c35968). Deployed to k3d-ro-spoke-1 and
k3d-ro-spoke-2 with `make deploy-agent` (release `agent`, namespace `resource-optimizer`).

## What it does
- `internal/health`: a controller in the agent's manager. An RSR that reaches phase `Applied` is watched for
  `guardrails.rollback.watchDuration` (default 30m) from `status.appliedAt`:
  - OOM kills and restarts from pod status. Pods are watched (cache strips managedFields), so a breach is acted
    on at once; every watched workload is also re-checked every 15s (`--health-check-interval`).
    Restarts count from zero for pods created after the change; for older pods (in-place) from the restart
    count at the first observation. OOM kills are remembered per termination, so repeated kills of one
    container count although the pod status keeps only the last one.
  - CPU throttling ratio, `sum(increase(throttled_periods)) / sum(increase(periods))` over the window since the
    change (needs ≥ 300 periods, first evaluated `--health-min-throttling-window` = 2m after the change), via PromQL
    against `--metrics-url` (default the hub's VictoriaMetrics `http://victoria-metrics.hub.svc:8428`, filtered
    by `cluster`, namespace, the workload's pods and the changed containers). Optional fallback
    `health.cadvisorFallback` reads the kubelets' cAdvisor through `nodes/proxy` (off by default: that RBAC reaches
    every kubelet API).
  - Thresholds from the effective policy (`guardrails.rollback`: oomKills 1, restarts 3, throttlingRatio 0.25).
    OOM kills only count when the change touched memory, throttling only when it touched cpu, restarts when it
    touched anything.
- Rollback restores the breached resources (OOMKills → memory, Throttling → cpu, Restarts → every changed
  resource) to `status.previous` with the strategy that applied them: in-place resizes the pods back through
  `pods/resize`; rollout re-applies the template with SSA under `resource-optimizer` (all owned fields are
  re-applied, so nothing owned is dropped). Then phase `RolledBack`, `rolledBackAt`, conditions
  `HealthWatch=False` and `Applied=False` with the rollback reason, and a `rolled_back` event (Warning on the RSR,
  buffered for the hub, never waits for delivery). Works with the hub unreachable (only throttling needs metrics).
- Afterwards the agent keeps phase `RolledBack` while further recommendations are skipped or blocked, and
  blocks decreases of the restored resource (from the HealthWatch reason) until `cooldown` after `rolledBackAt`.
  The same recommendation is never re-applied.
- A watch that ends without a breach sets `HealthWatch=False, Healthy`. Condition-only reasons:
  `Watching`, `Healthy`, `WatchStopped`.
- Metrics: `ro_health_watches_in_progress`, `ro_health_rollbacks_total{reason,strategy}`,
  `ro_health_watches_completed_total`, `ro_health_rollback_errors_total`,
  `ro_health_throttling_query_errors_total{source}`.
- Integration: `internal/agent/health_hook.go` (`agent.SetupHealth`) is the single wiring point; the watcher uses
  the agent's client, RSR index and `controller.Reconciler.Emit` for events. Flags: `--health-watch`,
  `--health-check-interval`, `--metrics-url`, `--metrics-token-file`, `--health-cadvisor-fallback`,
  `--health-min-throttling-window`; chart values under `health:`.

## D-017 (event de-duplication) — implemented here
`skipped`/`blocked` are emitted once per (workload, type, reason) and again only when the reason or the proposed
values change; a new recommendation id with the same values does not re-emit. An `applied`, `rolled_back` or
`released` event starts over. Per-resource blocks inside an applied change use one slot per resource. Survives
agent restarts via the RSR conditions and spec. `docs/contracts/events.md` wording updated. (The WO-06 hand-over's
note about per-id noise is obsolete.)

## Other changes in internal/agent
- `policy.Effective` resolves `guardrails.rollback.*`.
- Guardrails: the post-rollback cooldown blocks decreases only of the restored resources.
- Agent pods now `watch` pods (ClusterRole); `nodes/proxy get` only with `health.cadvisorFallback`.
- Dockerfile copies `internal/health`.

## Verification
- `go test ./internal/health/... -cover`: ok, 76% (unit: signals, restore logic, PromQL query/parse, cAdvisor
  parse, pod->workload mapping; envtest: rollout OOM rollback, in-place restarts rollback, throttling rollback,
  watch completing Healthy, retry of an unfinished restore). `make lint` 0 issues, `make test` all ok.
- Live, ro-spoke-1 (the WO's `memory-leak` cannot be used: the hub flags it `oom-killed` and never decreases it):
  namespace `wo08-health` (deleted afterwards), Deployment `leaky2` (python holding N MiB read from a ConfigMap,
  256Mi), RightsizingPolicy for that namespace (memory auto-patch, rollout, learningPeriod 0s, minConfidence 0,
  maxChangePercent 100, watchDuration 20m). The real hub recommended 32Mi; the agent applied it (rollout) and the
  watch started. Raising the allocation to 64 MiB OOM-killed the pod; 53 s after the ConfigMap change (most of it
  ConfigMap propagation) the RSR was `RolledBack` and the template back at 256Mi. Hub audit
  (`GET /v1/audit?workload=leaky2`): `applied` 256Mi->32Mi, then `rolled_back` OOMKills 32Mi->256Mi,
  details `{oomKills: 1, watchDuration: 20m0s}`; `.items[-1].type` = `"rolled_back"`.
- D-017 live: `overprovisioned` (audit) had 5 identical `skipped ModeAudit` events in the audit log between 01:00
  and 02:34 (one per hub run); after this build, none across the following runs.

## Found and fixed during the live run
- Stripping managedFields in the cache (meant for pods) hid the agent's own field ownership on workloads: it then
  re-applied templates on every reconcile and would never release. Only pods are stripped now.
- Race: the agent's drift repair, reading a cached RSR still `Applied`, re-applied the decreased values right after
  the watcher restored them. Now the watcher marks `RolledBack` first and then restores (a RolledBack RSR whose
  restore is not confirmed is restored again on the next reconcile), and drift repair re-reads the RSR uncached.

## For other tracks
- WO-09 (e2e): every applied change in any namespace is now health-watched (e.g. `e2e-fallback-*/web` showed
  `HealthWatch=True Watching`). Restarts ≥ 3 or an OOM kill after a memory change within 30m roll it back; set
  `guardrails.rollback` in the e2e policies if a test should not be rolled back.
- WO-07 (webhook): after a rollback the phase is `RolledBack`, so the webhook stops injecting; in-place pods are
  resized back, templates were never touched.
- WO-04 (hub): `rolled_back` events carry `details.oomKills|restarts|throttlingRatio` and `watchDuration`.
