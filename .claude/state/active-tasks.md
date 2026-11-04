# Active tasks

Updated: 2026-11-04 by orchestrator. Status: `todo` · `in-progress` · `review` · `done` · `blocked`.

| WO | Title | Wave | Status | Track / session | Branch | Blocked by | Notes |
|---|---|---|---|---|---|---|---|
| WO-00 | Contracts and repo skeleton | 0 | done | local_b7f83517 | wo-00-contracts | – | Merged 62156e3; CI workflow parked |
| WO-01 | Local dev environment (k3d) | 0 | done | local_991f585a | wo-01-dev-env | – | Merged c5e7e20; clusters running |
| WO-02 | Synthetic metrics and backfill | 1 | done | local_038e4612 | wo-02-synth | – | Merged; live mode running; lint fixes merged 3d284d9 |
| WO-03 | Pricing service | 1 | done | local_14a53b46 | wo-03-pricing | – | Merged; ClusterIP only, 1m refresh in dev |
| WO-04 | Hub and recommender | 1 | done | local_9125207c | wo-04-hub | – | Merged; NodePort 30080 |
| WO-04b | Hub fixes + UI API additions (D-016) | 1 | in-progress | local_9125207c | wo-04b-hub-fixes | – | Trend vs summary, empty usage, facets, pricing, confidenceFactors |
| WO-05 | Grafana dashboards | 1 | in-progress | local_ed8cb3f5 | wo-05-dashboards | – | Resumed |
| WO-06 | Agent: policy resolution and apply | 1 | done | local_11de0a00 | wo-06-agent | – | Merged; agents connected (D-018) |
| WO-07 | Mutating admission webhook | 1 | in-progress | local_14a53b46 | wo-07-webhook | – | Reassigned to the pricing session |
| WO-14 | Web UI | 1 | done | local_9fadb724 | wo-14-web | – | Merged; http://localhost:19090; consumes D-016 after WO-04b |
| WO-08 | Health watch and rollback | 2 | in-progress | local_11de0a00 | wo-08-health | – | Includes D-017 event dedup |
| WO-09 | End-to-end suite incl. Argo CD | 2 | in-progress | local_038e4612 | wo-09-e2e | – | Webhook/rollback scenarios wait for WO-07/WO-08; no dev-reset in shared env |
| WO-14c | Web polish: deps, D-016 consumption | 1 | in-progress | local_9fadb724 | wo-14c-web-polish | WO-04b (part 3) | Dependabot: 2 critical, 5 moderate |

## Open questions for the user
- Token with GitHub `workflow` scope needed to activate `ci/github-workflows/` (user, morning).

## Integration log
- 2026-11-04 WO-00 verified (`make ci` exit 0 on branch and on merged main), squash-merged as 62156e3. `.github/workflows/ci.yaml` moved to `ci/github-workflows/` because the keychain token lacks the `workflow` scope.
- 2026-11-04 WO-01 verified by its session (all Verification commands, `make dev-verify` 11/11, peak 5.7 GiB); merged c5e7e20; root Makefile resolves `dev-up`. Hand-over notes copied to `docs/handover/`.
- 2026-11-04 WO-02, WO-03, WO-04, WO-06, WO-14 merged to main (--no-ff, go.mod/go.sum conflicts resolved by union + `go mod tidy`). `make vet test verify-generate lint-openapi` pass on merged main; `make lint` has 21 issues in tools/synth (WO-02 fixing on its branch). WO-04 added `db/migrations/0002_recommender_runs.sql` (additive, accepted).
- 2026-11-04 Agents on ro-spoke-1/2 connected to the hub via per-cluster dev token values (D-018); RSRs created in audit mode. Tilt now deploys `web` (context web/); NodePort 30090 documented as the web UI; VictoriaMetrics PV value raised to 10Gi for fresh installs (local-path does not enforce size, live PVC untouched).
- 2026-11-04 WO-02 lint fixes merged (3d284d9); `make lint` 0 issues on main.
