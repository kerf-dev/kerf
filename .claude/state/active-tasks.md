# Active tasks

Updated: 2026-11-04 by orchestrator. Status: `todo` · `in-progress` · `review` · `done` · `blocked`.

| WO | Title | Wave | Status | Track / session | Branch | Blocked by | Notes |
|---|---|---|---|---|---|---|---|
| WO-00 | Contracts and repo skeleton | 0 | done | local_b7f83517 | wo-00-contracts | – | Merged 62156e3; CI workflow parked |
| WO-01 | Local dev environment (k3d) | 0 | done | local_991f585a | wo-01-dev-env | – | Merged c5e7e20; clusters running |
| WO-02 | Synthetic metrics and backfill | 1 | todo | – | – | WO-00 | |
| WO-03 | Pricing service | 1 | todo | – | – | WO-00 | |
| WO-04 | Hub and recommender | 1 | todo | – | – | WO-00 | |
| WO-05 | Grafana dashboards | 1 | todo | – | – | WO-00 | |
| WO-06 | Agent: policy resolution and apply | 1 | todo | – | – | WO-00 | |
| WO-07 | Mutating admission webhook | 1 | todo | – | – | WO-00 | |
| WO-14 | Web UI | 1 | todo | – | – | – | D-014 |
| WO-08 | Health watch and rollback | 2 | todo | – | – | WO-06 | |
| WO-09 | End-to-end suite incl. Argo CD | 2 | todo | – | – | WO-01, WO-04, WO-06, WO-07 | |

## Open questions for the user
- Token with GitHub `workflow` scope needed to activate `ci/github-workflows/` (user, morning).

## Integration log
- 2026-11-04 WO-00 verified (`make ci` exit 0 on branch and on merged main), squash-merged as 62156e3. `.github/workflows/ci.yaml` moved to `ci/github-workflows/` because the keychain token lacks the `workflow` scope.
- 2026-11-04 WO-01 verified by its session (all Verification commands, `make dev-verify` 11/11, peak 5.7 GiB); merged c5e7e20; root Makefile resolves `dev-up`. Hand-over notes copied to `docs/handover/`.
