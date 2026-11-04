# Active tasks

Updated: 2026-11-04 by orchestrator. Status: `todo` · `in-progress` · `review` · `done` · `blocked`.

| WO | Title | Wave | Status | Track / session | Branch | Blocked by | Notes |
|---|---|---|---|---|---|---|---|
| WO-00 | Contracts and repo skeleton | 0 | done | local_b7f83517 | wo-00-contracts | – | Merged 62156e3; CI workflow parked |
| WO-01 | Local dev environment (k3d) | 0 | done | local_991f585a | wo-01-dev-env | – | Merged c5e7e20; clusters running |
| WO-02 | Synthetic metrics and backfill | 1 | in-progress | local_038e4612 | wo-02-synth | – | synth-a/synth-b |
| WO-03 | Pricing service | 1 | in-progress | local_14a53b46 | wo-03-pricing | – | ClusterIP only (30090 → web) |
| WO-04 | Hub and recommender | 1 | in-progress | local_9125207c | wo-04-hub | – | Critical path; NodePort 30080 |
| WO-05 | Grafana dashboards | 1 | paused | local_ed8cb3f5 | wo-05-dashboards | session cap (5) | Resume when a slot frees |
| WO-06 | Agent: policy resolution and apply | 1 | in-progress | local_11de0a00 | wo-06-agent | – | Owns CRD install |
| WO-07 | Mutating admission webhook | 1 | todo | – | wo-07-webhook | session cap (5) | Start when a slot frees |
| WO-14 | Web UI | 1 | in-progress | local_9fadb724 | wo-14-web | – | NodePort 30090 → localhost:19090 |
| WO-08 | Health watch and rollback | 2 | todo | – | – | WO-06 | |
| WO-09 | End-to-end suite incl. Argo CD | 2 | todo | – | – | WO-01, WO-04, WO-06, WO-07 | |

## Open questions for the user
- Token with GitHub `workflow` scope needed to activate `ci/github-workflows/` (user, morning).

## Integration log
- 2026-11-04 WO-00 verified (`make ci` exit 0 on branch and on merged main), squash-merged as 62156e3. `.github/workflows/ci.yaml` moved to `ci/github-workflows/` because the keychain token lacks the `workflow` scope.
- 2026-11-04 WO-01 verified by its session (all Verification commands, `make dev-verify` 11/11, peak 5.7 GiB); merged c5e7e20; root Makefile resolves `dev-up`. Hand-over notes copied to `docs/handover/`.
