# Status — 2026-11-04 morning

Everything below runs on the local k3d fleet (`ro-hub`, `ro-spoke-1`, `ro-spoke-2`) from `main`.
Live tracker: `.claude/state/active-tasks.md` · decisions: `DECISIONS.md` (D-001…D-021) · hand-overs: `docs/handover/`.

## Open it

| What | Where |
|---|---|
| Web UI | http://localhost:19090 |
| Hub API | http://localhost:18080 (tokens in `deploy/charts/hub/README.md`) |
| Grafana (4 dashboards, folder *resource-optimizer*) | http://localhost:13000 (admin/admin) |
| Argo CD | http://localhost:18090 (admin/admin123) |
| VictoriaMetrics | http://localhost:18428 |

Screenshots of every page (light, dark, tablet): `docs/screenshots/`.

## What it shows right now

- 451 workloads across 4 clusters: 2 real k3d spokes, and `synth-a`/`synth-b` with 203 synthetic EKS-like workloads each and 30 days of history.
- Estimated savings **$9,826 / month** (CPU $8,896, memory $930). Requested 707 cores, used 182, recommended 290.
- Risk flags: throttled 35, OOM-killed 24, restarting 31, HPA-coupled 23, JVM fixed heap 2, low confidence 87.
- All workloads are in `audit` (the global default). Auto-patch is opt-in per policy or annotation.
- The real-spoke dollar figures are tiny: the k3d nodes report the whole Docker VM as allocatable, which makes per-core rates unrealistically low.

## Delivered (work-orders on `main`)

| Phase | Work-orders |
|---|---|
| 1 Visibility | WO-00 contracts · WO-01 k3d env · WO-02 synthetic data · WO-03 pricing (static + AWS) · WO-04 hub + recommender (+04b–04e) · WO-05 Grafana · WO-14 web UI (+14c, 14d) |
| 2 Safe automation | WO-06 agent (policies, in-place/rollout, Argo CD field manager) · WO-07 admission webhook · WO-08 health watch + rollback · WO-09 e2e suite incl. Argo CD |
| 3 Smarter | WO-10 deferred strategy + maintenance/freeze windows · WO-11 HPA coordination · WO-12 JVM-aware memory · WO-13 single-replica + StatefulSet OnDelete |

## Verification

- `make ci` passes on `main` (build, vet, golangci-lint 0 issues, all unit/envtest/Postgres tests, generate check, OpenAPI lint).
- The web UI's `npm audit` reports 0 vulnerabilities.
- The e2e suite against the shared clusters, `make e2e && make e2e`, passed back to back on `main` (14.6 and 14.4 minutes, 16 tests). It covers:
  - audit mode
  - in-place resize, rollout, and the Infeasible→rollout fallback
  - Argo CD: a hard sync keeps our values, and a release restores the Git values
  - the webhook, including pod creation while it is down
  - rollback after an OOM kill or repeated restarts
  - guardrails, the single-replica guard, and the JVM fixed-heap guard
  - an outage of the hub
- One scenario skips by design: JVM in-place memory. The k3d nodes report swap, so the agent correctly refuses in-place memory resizes.
- Each phase-3 work-order was also live-checked on the spokes:
  - deferred applied the moment its window opened, and a freeze blocked a change
  - the HPA target moved 60→86 % together with CPU 100m→70m, and replicas held at 2
  - single-replica rollouts never left zero Ready pods

## Needs you

1. **GitHub token with the `workflow` scope.** Without it, CI can't be enabled. Both workflows are parked in `ci/github-workflows/`; activate them with `git mv ci/github-workflows/*.yaml .github/workflows/`. Neither workflow has run on GitHub yet, and e2e needs an 8-core runner.
2. **Phase-4 decisions** (no work-orders yet):
   - UI write actions: apply, or change a mode from the UI. The `ui` token is read-only by design.
   - notifications
   - CUR-based prices
   - realized savings
   - the EKS rollout (Pod Identity for pricing, mTLS agent↔hub)
3. **AWS pricing.** The tests replay AWS responses that were assembled offline. Re-capture them once there is real AWS access.
4. **Optional clean-up.** `/Users/tomer/Projects/resource-optimizer-plan` duplicates the repo and can be deleted. The linked work sessions are idle and can be archived.

## Process notes from the night

- **Two short hub outages.** Parallel tracks deployed the hub with colliding migration numbers. Now only the orchestrator deploys the hub and assigns migration numbers (D-021, `CLAUDE.md`).
- **A merge-time slip.** At one point I reported a deploy that had not run: zsh does not word-split `make $t`, so make only printed its help. I caught it, redeployed, and corrected the tracker.
- **Image tags.** Image tags in `make/*.mk` now expand once, so the pushed tag and the deployed tag always match.
