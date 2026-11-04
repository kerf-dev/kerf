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
| WO-04c | Hub: action labels, recompute (D-019), usage step series, trend backfill | 2 | in-progress | local_9125207c | wo-04c-hub | – | |
| WO-05 | Grafana dashboards | 1 | done | local_ed8cb3f5 | wo-05-dashboards | – | Merged; 4 dashboards provisioned |
| WO-06 | Agent: policy resolution and apply | 1 | done | local_11de0a00 | wo-06-agent | – | Merged; agents connected (D-018) |
| WO-07 | Mutating admission webhook | 1 | done | local_14a53b46 | wo-07-webhook | – | Merged; deployed on both spokes |
| WO-08 | Health watch and rollback | 2 | done | local_11de0a00 | wo-08-health | – | Merged incl. D-017 |
| WO-08b | Agent fixes: dedup check, double release, non-UUID ids | 2 | in-progress | local_11de0a00 | wo-08b-agent-fixes | – | |
| WO-09 | End-to-end suite incl. Argo CD | 2 | done | local_038e4612 | wo-09-e2e | – | Merged; 2 consecutive passes (12.7/14.4 min) |
| WO-09b | E2E: webhook (8) and rollback (10) scenarios | 2 | in-progress | local_038e4612 | wo-09b-e2e | – | |
| WO-14 | Web UI | 1 | done | local_9fadb724 | wo-14-web | – | Merged; http://localhost:19090 |
| WO-14c | Web polish: deps, D-016 consumption | 1 | done | local_9fadb724 | wo-14c-web-polish | – | Merged; npm audit clean |
| WO-12 | JVM-aware memory | 3 | in-progress | local_14a53b46 | wo-12-jvm | – | |
| WO-13 | Single replica / OnDelete | 3 | in-progress | local_ed8cb3f5 | wo-13-single-replica | – | |
| WO-10 | Deferred strategy and windows | 3 | todo | – | – | WO-08b | |
| WO-11 | HPA coordination | 3 | todo | – | – | WO-04c | |

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
