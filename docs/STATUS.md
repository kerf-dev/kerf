# Status — 2026-11-04 afternoon

Everything below runs on the local k3d fleet (`ro-hub`, `ro-spoke-1`, `ro-spoke-2`) from `main`.
Live tracker: `.claude/state/active-tasks.md` · decisions: `DECISIONS.md` (D-001…D-027) · hand-overs: `docs/handover/`.

## Open it

| What | Where |
|---|---|
| Web UI | http://localhost:19090 — sign in as `viewer`, `operator` or `admin` (dev passwords in `deploy/charts/hub/values-dev.yaml`) |
| Hub API | http://localhost:18080 (tokens in `deploy/charts/hub/README.md`) |
| Grafana (4 dashboards, folder *resource-optimizer*) | http://localhost:13000 (admin/admin) |
| Argo CD | http://localhost:18090 (admin/admin123) |
| VictoriaMetrics | http://localhost:18428 |

Screenshots of every page, including the admin area (light, dark, tablet): `docs/screenshots/`.

## New since this morning: admin area and cluster onboarding (phase 4)

The UI is no longer read-only. Sign-in uses local users with three roles (D-023):

| Role | Can |
|---|---|
| viewer | everything the UI showed before |
| operator | pause/resume clusters, per-workload mode, exclude, approve once |
| admin | everything: defaults, policies, recommender, pricing, clusters, tokens, users, metrics sources |

**Admin pages:**
- **Automation:** pause or resume per cluster or for the whole fleet.
- **Defaults:** shows the impact before saving.
- **Policies:**
  - selector builder with the live list of matching workloads
  - effective-policy simulator ("which setting won and why")
  - version history, revert and YAML export
- **Recommender:** profiles and interval, "Recompute now".
- **Pricing:** discounts and fixed rates.
- **Metrics sources:** Prometheus, VictoriaMetrics, Thanos, Mimir, AMP.
- **Access:** users and tokens.
- **Audit:** who changed what, with the reason and a before/after diff.

**Write actions on existing pages:**
- The workload Automation card: override, exclude, approve once.
- Bulk "Set mode…" and "Exclude" on Opportunities.
- Writes to `prod` clusters ask you to type the cluster name.

**How changes reach a cluster (D-022):**
- The hub stores the intent. Each agent pulls it and creates RightsizingPolicies labelled `managed-by=hub`, usually within 30 s.
- Policies from Git are never touched. They appear read-only, with a Git badge.
- The hub still has no write access to any cluster (D-004).

**Onboarding a cluster (D-025):**
- **Add-cluster wizard:**
  1. Identify the cluster.
  2. Pick its metrics source and cluster label, and test them before any agent exists.
  3. Get the agent token (shown once) and install snippets: Helm, values file, Argo CD Application, and the `ignoreDifferences` snippet.
  4. Watch a live checklist.
- **Readiness states:** Registered → Agent connected → Metrics OK → Ready (audit) → Automation ready.
  - Checks cover missing metrics (cAdvisor, kube-state-metrics, node labels, freshness, retention…) and the agent's own access: RBAC per capability, in-place resize, webhook, Argo CD config, clock skew.
  - Each failing check comes with the exact fix.
- **Auto-patch gate:** auto-patch is refused on a cluster that isn't *Automation ready*, including through a revert. An admin can override with a reason, and the override is audited.
- **Right now:** both real spokes are *Automation ready*. `synth-a`/`synth-b` stay *Registered*, because they have no agent.

**Two decisions taken on the way:**
- **D-026:** metrics-source credentials never leave the hub; pricing queries other clusters through a hub proxy.
- **D-027:** after "approve once", the agent keeps ownership of the changed fields, so Argo CD self-heal can't undo the approved values.

## What it shows

- 451 workloads across 4 clusters: 2 real k3d spokes, and `synth-a`/`synth-b` with 203 synthetic EKS-like workloads each.
- Estimated savings about **$9,800 / month**.
- All workloads are in `audit` (the global default); auto-patch is opt-in.

## Verification

- `make ci` passes on `main`: build, vet, golangci-lint (0 issues), unit/envtest/Postgres tests, generate check, OpenAPI lint.
- `make web-verify` passes: 31 Playwright tests, plus lint, typecheck and the unit/contract tests. `npm audit` reports 0 vulnerabilities.
- **End-to-end:** `make e2e && make e2e` passed back to back on `main` (14m30s + 14m08s, 31 tests each). The suite now also covers:
  - a UI-made policy becoming a CR and being applied, with the audit actor
  - pause and resume
  - approve once, including on Argo CD with self-heal
  - revert, Git policies left untouched, and roles (viewer 403, operator vs admin)
  - registering a cluster and the auto-patch gate
  - readiness, and losing an RBAC rule → *Ready (audit)*
- **Bug found and fixed:** the suite caught an agent bug. Drift repair stopped after the next recommender run, because the hub issues a new recommendation id on every run. Fixed in WO-21c, and covered by the test now.
- **Skipped:**
  - JVM in-place memory: the k3d nodes report swap, so the agent correctly refuses.
  - kube-state-metrics scaled to 0: waits for your OK, see below.

## Needs you

1. **May the e2e suite scale kube-state-metrics to 0 on `ro-spoke-2`?** The test checks that a broken metrics setup shows up as a blocking failure, then restores KSM. It is skipped by default (`RO_E2E_DISRUPTIVE=1` enables it), because a session's permission check refused the scale-down.
2. **GitHub token with the `workflow` scope.** CI can't be enabled without it. Both workflows are parked in `ci/github-workflows/`; activate them with `git mv ci/github-workflows/*.yaml .github/workflows/`. The e2e workflow needs an 8-core runner.
3. **Remaining phase-4 work (not started):**
   - WO-15 notifications
   - WO-16 CUR-based prices
   - WO-17 realized savings
   - WO-18 EKS rollout (Pod Identity for pricing, mTLS agent↔hub)
   - OIDC login (D-023 says later)
4. **AWS pricing.** The tests replay AWS responses that were assembled offline. Re-capture them once there is real AWS access.
5. **Optional clean-up.** `/Users/tomer/Projects/resource-optimizer-plan` duplicates the repo. The linked work sessions are idle and can be archived.

## Process notes

- **Deploys overlapping test runs.** Twice today a track deployed an agent while e2e was running. The suite now creates an `e2e-run-<id>` namespace on both spokes for its whole run, and drops its field ownership after restoring Helm-owned objects. Neither overlap made a test fail.
- **Pricing deploy.** The pricing image stopped building after WO-25: its Dockerfile didn't copy the hub packages pricing now imports. Its dev settings also pointed at the hub's hostname from the spokes. Both are fixed; the old pod served throughout.
- **One bad image tag.** During a GitHub outage, a failed push cut short my deploy command, and agents and web went out tagged `main-` for a few minutes. The image content was correct. Deploy commands now refuse an empty tag.
