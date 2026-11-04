# Active tasks

Updated: 2026-11-04 by orchestrator. Status: `todo` · `in-progress` · `review` · `done` · `blocked`.

| WO | Title | Wave | Status | Track / session | Branch | Blocked by | Notes |
|---|---|---|---|---|---|---|---|
| WO-00 | Contracts and repo skeleton | 0 | done | local_b7f83517 | wo-00-contracts | – | Merged 62156e3; CI workflow parked |
| WO-01 | Local dev environment (k3d) | 0 | done | local_991f585a | wo-01-dev-env | – | Merged c5e7e20; clusters running |
| WO-02 | Synthetic metrics and backfill | 1 | done | local_038e4612 | wo-02-synth | – | Merged; live mode running |
| WO-03 | Pricing service | 1 | done | local_14a53b46 | wo-03-pricing | – | Merged; ClusterIP only, 1m refresh in dev |
| WO-04 | Hub and recommender | 1 | done | local_9125207c | wo-04-hub | – | Merged; NodePort 30080 |
| WO-04b | Hub fixes + UI API additions (D-016) | 1 | done | local_9125207c | wo-04b-hub-fixes | – | Merged |
| WO-04c | Hub: action labels, recompute (D-019), usage step series, trend backfill | 2 | done | local_9125207c | wo-04c-hub | – | |
| WO-05 | Grafana dashboards | 1 | done | local_ed8cb3f5 | wo-05-dashboards | – | Merged; 4 dashboards provisioned |
| WO-06 | Agent: policy resolution and apply | 1 | done | local_11de0a00 | wo-06-agent | – | Merged; agents connected (D-018) |
| WO-07 | Mutating admission webhook | 1 | done | local_14a53b46 | wo-07-webhook | – | Merged; deployed on both spokes |
| WO-08 | Health watch and rollback | 2 | done | local_11de0a00 | wo-08-health | – | Merged incl. D-017 |
| WO-08b | Agent fixes: dedup check, double release, non-UUID ids | 2 | done | local_11de0a00 | wo-08b-agent-fixes | – | |
| WO-09 | End-to-end suite incl. Argo CD | 2 | done | local_038e4612 | wo-09-e2e | – | Merged; 2 consecutive passes (12.7/14.4 min) |
| WO-09b | E2E: webhook (8) and rollback (10) scenarios | 2 | done | local_038e4612 | wo-09b-e2e | – | |
| WO-14 | Web UI | 1 | done | local_9fadb724 | wo-14-web | – | Merged; http://localhost:19090 |
| WO-14c | Web polish: deps, D-016 consumption | 1 | done | local_9fadb724 | wo-14c-web-polish | – | Merged; npm audit clean |
| WO-12 | JVM-aware memory | 3 | done | local_14a53b46 | wo-12-jvm | – | |
| WO-13 | Single replica / OnDelete | 3 | done | local_ed8cb3f5 | wo-13-single-replica | – | Merged; live check: never 0 Ready |
| WO-04d | Hub: trend partial-day bucket, intermittent default pricing | 2 | done | local_9125207c | wo-04d-hub | WO-11 live check | |
| WO-09c | E2E: JVM scenario 2 replicas, single-replica guard test | 2 | done | local_038e4612 | wo-09c-e2e | – | Merged; g1/g2 PASS 14.6/14.4 min on main |
| WO-13b | WO-13 contract constants | 3 | done | local_ed8cb3f5 | wo-13b-contract | – | Merged |
| WO-04e | Hub: prune deleted workloads (migration 0006) | 2 | done | local_9125207c | wo-04e-hub-prune | – | Ghost e2e/test workloads in fleet KPIs |
| WO-10 | Deferred strategy and windows | 3 | done | local_11de0a00 | wo-10-windows | – | |
| WO-11 | HPA coordination | 3 | done | local_9125207c | wo-11-hpa | – | |
| WO-14d | UI: recommendation history series, recompute, D-019 | 2 | done | local_9fadb724 | wo-14d-web | – | |
| WO-19 | Admin: contract (+ onboarding/readiness contract) | 4 | done | local_7697cfde | wo-19-admin-contract | – | Migrations 0007–0010 assigned |
| WO-20 | Admin: hub | 4 | done | local_9125207c | wo-20-admin-hub | – | merged 70e02d1 |
| WO-21 | Admin: agent | 4 | done | local_11de0a00 | wo-21-admin-agent | – | merged d2a50b6 |
| WO-21b | Approve-once keeps field ownership (D-027) | 4 | done | local_11de0a00 | wo-21b-approve-ownership | – | |
| WO-22 | Admin: web (+ onboarding wizard, readiness UI) | 4 | done | local_9fadb724 | wo-22-admin-web | – | merged 3070005 |
| WO-22b | Admin web polish (overflow, sync state, git read-only) | 4 | done | local_9fadb724 | wo-22b-admin-polish | – | |
| WO-25 | Onboarding, metrics sources, readiness | 4 | in-progress (phase 2) | local_14a53b46 | wo-25-onboarding-p2 | – | phase 1 merged 56424f9; D-026 |
| WO-24 | Admin: e2e | 4 | todo | – | wo-24-admin-e2e | WO-25 p2, WO-21b (shared spokes) | |

## Open questions for the user
- Token with GitHub `workflow` scope needed to activate `ci/github-workflows/` (user, morning).

## Integration log
- 2026-11-04 WO-00 verified (`make ci` exit 0 on branch and on merged main), squash-merged as 62156e3. `.github/workflows/ci.yaml` moved to `ci/github-workflows/` because the keychain token lacks the `workflow` scope.
- 2026-11-04 WO-01 verified by its session (all Verification commands, `make dev-verify` 11/11, peak 5.7 GiB); merged c5e7e20; root Makefile resolves `dev-up`. Hand-over notes copied to `docs/handover/`.
- 2026-11-04 WO-02, WO-03, WO-04, WO-06, WO-14 merged to main (--no-ff, go.mod/go.sum conflicts resolved by union + `go mod tidy`). `make vet test verify-generate lint-openapi` pass on merged main; `make lint` has 21 issues in tools/synth (WO-02 fixing on its branch). WO-04 added `db/migrations/0002_recommender_runs.sql` (additive, accepted).
- 2026-11-04 Agents on ro-spoke-1/2 connected to the hub via per-cluster dev token values (D-018); RSRs created in audit mode. Tilt now deploys `web` (context web/); NodePort 30090 documented as the web UI; VictoriaMetrics PV value raised to 10Gi for fresh installs (local-path does not enforce size, live PVC untouched).
- 2026-11-04 WO-02 lint fixes merged (3d284d9); `make lint` 0 issues on main.
- 2026-11-04 Merged WO-04b, WO-05, WO-07, WO-08, WO-09, WO-14c (clean merges). `make ci` exit 0 on main (lint 0 issues, all packages pass incl. internal/health and internal/webhook). Agent redeployed from main; dashboards provisioned to Grafana. Grafana limit raised to 1Gi (OOM at 384Mi). WO-09: `make e2e && make e2e` passed (scenario 3 skips on k3d swap; 8/10 pending WO-09b).
- 2026-11-04 Phase-3 work-orders WO-10..WO-13 written; WO-12 and WO-13 started.
- 2026-11-04 Merged WO-04c (hub wo04c-1 already live) and WO-08b; `make ci` exit 0. WO-09b first run: 8 and 10 pass; 11 broke when WO-12 redeployed the spoke-2 agent mid-run — spoke deploys frozen until the rerun ends. WO-10 and WO-11 started.
- 2026-11-04 Incident 04:30–04:43 UTC: WO-11 deployed the hub with a second migration 0003 (WO-12's 0003 already applied) → recommendations 500s; WO-11 rolled back to WO-12's image. Fix: orchestrator assigns migration numbers (0003 WO-12, 0004 WO-11, 0005 WO-10) and is the only one deploying the hub (CLAUDE.md). WO-12 merged (126d69e), make ci green.
- 2026-11-04 Merged WO-09b (e2e: 15 tests pass twice, 12–15 min), WO-11 (0004), WO-10 (0005; reconciler conflict with WO-11 resolved by hand), WO-14d. `make ci` with Postgres tests exit 0; web typecheck + 49 unit tests pass. Correction: the deploy loop at that point did not run (zsh does not word-split `make $t`); real deploy from main-a18528f followed (hub with 0004/0005, web, CRDs, spoke-2 agent, webhook; spoke-1 agent after a stuck WO-13 helm upgrade cleared). WO-10/WO-11 live checks pending (status review).
- 2026-11-04 WO-10 live check passed (deferred applied at window open 05:57:01, freeze blocked in-place); WO-10b fixes (skip reason persisted, dedup signature includes effective-policy digest → fixes TestHPACoordination). WO-11 live check passed on both spokes (100m→70m + HPA target 60→86 %, replicas stayed 2). WO-13 live check passed (no zero-Ready window). WO-04d (trend partial bucket, pricing fallback) merged. `make ci` with Postgres exit 0 on 6fbfeda. All components deployed from main-6fbfeda on ro-hub/ro-spoke-1/ro-spoke-2 (pricing unchanged since WO-03); endpoints 200. make/*.mk image tags now expand once (agent/web/pricing).
- 2026-11-04 Final e2e on main 6fbfeda: 1 test-side failure (WO-13 guard blocks 1-replica RestartContainer) fixed in WO-09c; with it g1/g2 PASS back to back (16 tests). WO-09c and WO-13b merged (93695ad), make ci exit 0, agent redeployed main-93695ad, pricing redeployed from main. UI checked in browser: overview, opportunities, workload detail render with live data ($10,015/mo fleet savings, 693 workloads incl. ~60 deleted test workloads → WO-04e).
- 2026-11-04 WO-04e merged (4dc9320), make ci exit 0, hub deployed main-4dc9320 (migration 0006 applied); deleted test workloads pruned after one agent pull (693 → 451 workloads, 0 ghosts). Morning report: docs/STATUS.md.
- 2026-11-04 WO-19 merged (59539ee): hub.yaml 19 → 90 operations, purely additive (no removed ops/props, no new required fields on existing schemas); migrations 0007–0010; shared validator api/v1alpha1/validate. `make ci` exit 0, web gen:api:check + typecheck pass. WO-20/21/22/25 started in parallel.
- 2026-11-04 WO-20, WO-21, WO-22 and WO-25 phase 1 merged (9325962; only go.mod overlapped). `make ci` exit 0 (lint 0 issues, Postgres tests), `make web-verify` exit 0 (25/25 Playwright). Deployed hub, agents (both spokes, CRDs re-applied) and web at main-9325962; migrations 0007–0010 applied, dev users seeded. Live: admin login via API and UI; hub policy → CR on ro-spoke-1 in 22 s, delete and pause/resume followed on the next pull; audit rows carry actor, reason, before/after. D-026, D-027 logged. Started WO-25 phase 2, WO-21b, WO-22b.
- 2026-11-04 Migration 0011 assigned to WO-25 p2 (0011_pricing_token_role: api_tokens.role CHECK adds 'pricing' for the D-026 proxy token). 0012+ unassigned.
- 2026-11-04 WO-21b (approve-once keeps field ownership, D-027; live Argo CD self-heal check on ro-spoke-1) and WO-22b (mode controls, /admin/clusters redirect, Git-only read-only, sync state) merged (cbc813c). `make ci` exit 0, `make web-verify` exit 0 (28/28). Agents and web deployed at main-cbc813c; hub unchanged (main-9325962). GitHub push failed (GitHub outage); a first deploy went out with an empty tag `main-` because the push failure short-circuited the SHA assignment — redeployed with the right tag within minutes.
