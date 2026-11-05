# Status — 2026-11-05

Everything below runs on the local k3d fleet (`ro-hub`, `ro-spoke-1`, `ro-spoke-2`) from `main`.
Live tracker: `.claude/state/active-tasks.md` · decisions: `DECISIONS.md` (D-001…D-028) · hand-overs: `docs/handover/`.

## Open it

| What | Where |
|---|---|
| Web UI | http://localhost:19090 — sign in as `viewer`, `operator` or `admin` (dev passwords in `deploy/charts/hub/values-dev.yaml`) |
| Hub API | http://localhost:18080 (tokens in `deploy/charts/hub/README.md`) |
| Grafana (4 dashboards, folder *resource-optimizer*) | http://localhost:13000 (admin/admin) |
| Argo CD | http://localhost:18090 (admin/admin123) |
| VictoriaMetrics | http://localhost:18428 |

Screenshots of every page, including the admin area (light, dark, tablet): `docs/screenshots/`.

## New: GitHub releases and going public (WO-26)

**Releases on GitHub.** Pushing a tag `vX.Y.Z` (or `vX.Y.Z-rc.N`) runs the release workflow: CI, then the five
multi-arch images and six charts go to `ghcr.io/tomer1983/resource-optimizer`, cosign signs each one, and a GitHub
Release is created with notes from `CHANGELOG.md`, install steps, every digest and the merged pull requests.
Released charts default to their own GHCR images, so `helm install` needs no registry setting. Details:
`docs/release.md` ("Releasing on GitHub"). Tested with a full local release (`0.1.0-wo26.1`): the published
chart renders its own images in production mode, and every binary logs its version.

**Not active yet.** The workflows are in `ci/github-workflows/`, because this machine's GitHub credential
lacks the `workflow` scope. One command with a credential that has it activates them
(`ci/github-workflows/README.md`).

**Going public.** LICENSE (Apache-2.0), NOTICE, SECURITY, CONTRIBUTING, code of conduct, issue templates,
Dependabot and a README for outside users are in. Before flipping visibility, decide the items in
`docs/open-source-checklist.md`: permission and license, the CRD group `resource-optimizer.oz.io`, history,
and the 43 `wo-*` branches.

## New overnight: ready for EKS (WO-18, D-028)

**One chart.** `deploy/charts/resource-optimizer` installs either side, chosen by `global.mode`:
- `hub`: hub, UI and pricing
- `spoke`: agent and webhook, on each workload cluster
- `all`: everything in one cluster

The five component charts are its subcharts; dev keeps using them directly.

**Production features** (each is a values switch):
- availability: 2 hub replicas, PDBs, zone spread, HPA
- secrets: from AWS Secrets Manager through External Secrets; the schema rejects inline passwords and tokens unless `dev: true`
- database: RDS with `verify-full` TLS
- ingress: ALB examples
- pricing: Pod Identity
- network: NetworkPolicies
- monitoring: ServiceMonitors, PrometheusRules (6 alerts), Grafana dashboards
- CRDs ship as templates that survive an uninstall

**Hub HA.** Hubs elect a leader with a Postgres lock. Only the leader runs the recommender and the background checks. Every replica serves the API. A killed leader is replaced in under a second.

**Release.** `make release REGISTRY=<ecr> VERSION=x.y.z` builds amd64 and arm64 images with an SBOM and pushes them, then pushes the charts to the same registry as OCI artifacts. See `docs/release.md`.

**Install guide.** `docs/install/eks.md` covers every step, from prerequisites through upgrade, uninstall and troubleshooting. Each step has a check command and its expected output. Examples are in `deploy/examples/eks/`: hub and spoke values, an Argo CD ApplicationSet, IAM policies, and the Secrets Manager layout.

**Staging proof (WO-18e).** I followed the guide on two new k3d clusters (`ro-stg-hub`, `ro-stg-spoke`), with local stand-ins for the AWS-only pieces. The results:
- it installed from the published chart
- the workload cluster reached *Automation ready*
- one workload was auto-patched in place
- leader failover took under 1 s
- an upgrade under load dropped none of 768 requests
- uninstall worked

The run found and fixed one real problem. An upgrade used to drop a few requests (2 × 502), because pods stopped accepting connections before the ingress stopped sending them. Fixed with a short `preStop` sleep. Details: `docs/handover/WO-18e-handover.md`. Repeat it with `make install-staging && make install-staging-verify`. Afterwards the full e2e suite on the dev fleet passed again on `main` (17 min, usual skips).

**For the real EKS install I need from you:**
- the AWS account and region, and the hub and workload clusters
- an ECR prefix with push access
- RDS PostgreSQL (or permission for me to create it)
- an ACM certificate and DNS names for the hub API and the UI; should the UI be internal or internet-facing?
- which add-ons already run: the AWS Load Balancer Controller, External Secrets, cert-manager, and Prometheus or VictoriaMetrics

## Earlier: admin area and cluster onboarding (phase 4)

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
- **Skipped:** JVM in-place memory: the k3d nodes report swap, so the agent correctly refuses.
- **kube-state-metrics outage (run with your OK):** `TestReadinessKSMDown` passed on its own (8m37s) with `RO_E2E_DISRUPTIVE=1`.
  - With KSM scaled to 0 on `ro-spoke-2`, `metrics.freshness` failed as a blocking check after about 6 minutes, and the cluster list showed the banner.
  - The test now restores KSM itself and waits until the spoke is *Automation ready* again (2m15s). Without that wait, the spoke stayed degraded for up to the hub's check interval (10m in dev, 1h by default), because the first re-check came before KSM had been scraped again.
  - It stays off in normal runs: it degrades `ro-spoke-2` for everyone for about 9 minutes.

## Needs you

1. **GitHub token with the `workflow` scope.** CI can't be enabled without it. Both workflows are parked in `ci/github-workflows/`; activate them with `git mv ci/github-workflows/*.yaml .github/workflows/`. The e2e workflow needs an 8-core runner.
2. **Remaining phase-4 work (not started):**
   - WO-15 notifications
   - WO-16 CUR-based prices
   - WO-17 realized savings
   - the real EKS install (WO-18 is ready for it; see above), then mTLS agent↔hub
   - the add-cluster wizard still shows component-chart snippets; the guide says to take only the token
   - OIDC login (D-023 says later)
3. **AWS pricing.** The tests replay AWS responses that were assembled offline. Re-capture them once there is real AWS access.
4. **Optional clean-up.** `../resource-optimizer-plan` (next to this checkout) duplicates the repo. The linked work sessions are idle and can be archived.

## Process notes

- **Deploys overlapping test runs.** Twice today a track deployed an agent while e2e was running. The suite now creates an `e2e-run-<id>` namespace on both spokes for its whole run, and drops its field ownership after restoring Helm-owned objects. Neither overlap made a test fail.
- **Pricing deploy.** The pricing image stopped building after WO-25: its Dockerfile didn't copy the hub packages pricing now imports. Its dev settings also pointed at the hub's hostname from the spokes. Both are fixed; the old pod served throughout.
- **One bad image tag.** During a GitHub outage, a failed push cut short my deploy command, and agents and web went out tagged `main-` for a few minutes. The image content was correct. My deploy commands now stop if the commit id is empty.
- **`ro-spoke-2` lost its container metrics overnight.** Docker restarted the k3d node containers around 04:30 UTC. Afterwards, kubelet's cAdvisor on `ro-spoke-2` exported series without the `container` label. The readiness check `metrics.cadvisor` caught it and dropped the cluster to *Agent connected*. Restarting that node container fixed it, and both spokes are back to *Automation ready*. On EKS the same check would flag a broken kubelet in the same way.
