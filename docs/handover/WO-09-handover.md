# WO-09 hand-over (end-to-end suite)

Branch `wo-09-e2e` (on top of main 183fa69). Owns `test/e2e/`, `make/e2e.mk`, `ci/github-workflows/e2e.yaml`.
Suite docs: `test/e2e/README.md`.

## Run it

```bash
make e2e                       # ~17 min against the existing clusters, all scenarios in parallel
make e2e E2E_RUN=TestArgoCD    # selected scenarios
make e2e-clean                 # leftovers of an interrupted run (namespaces, policies, Argo apps)
```

Build tag `e2e`: `make test` / `go test ./...` never run it. Log: `bin/e2e.log`.

## Results (shared environment, 2026-11-04)

RUNS_PLACEHOLDER

## How it drives the system

- Real hub (WO-04) and real agents (WO-06); no stub hub. A second agent with a stub hub would act
  on the same workloads as the shared agent releases.
- Each scenario: namespace `e2e-<scenario>-<6 hex>` + cluster-scoped RightsizingPolicy of the same
  name selecting only it (priority 1000, `learningPeriod: 0s`, `minConfidence: "0"`, default value
  settings so no `RecommendationStale` round). Deleted at the end (`t.Cleanup`).
- Usage from `stress-ng` / a tiny JVM, images already on the nodes. The hub's dev run (every 5 min,
  data aligned to 5-min steps: `asOf` is the previous 5-minute boundary) gives a new workload its
  first recommendation 8–15 min after it starts. That dominates the suite time.
- Argo CD: per-scenario Application in `argocd` on `e2e/<namespace>/` of the in-cluster git repo
  (the main `workloads` app only syncs the repo root), D-006 settings copied from `argocd/workloads`.
- Scenario 4 forces a real `Infeasible` resize with the policy floor `guardrails.minCPU: 16` (node
  has 8 cores). A QoS-changing resize is not usable: the agent keeps Guaranteed pods Guaranteed.
- Scenario 11: temporary NetworkPolicy `resource-optimizer/e2e-hub-unreachable` on **ro-spoke-2**
  (agent pods, all egress except the hub-api endpoint) for ~7 min per run. `make e2e` deletes a
  leftover one before and after (`make e2e-unblock`). The only object the suite creates outside its
  own namespaces.

## Findings for other tracks

- **Swap blocks scenario 3** (JVM memory in-place / RestartContainer): k3d nodes report 1 GiB swap,
  so the agent (correctly, D-007) refuses in-place memory changes with `UnsupportedNode`. The test
  asserts that and pods untouched, then skips. Exercising RestartContainer needs a node without swap
  (WO-01: k3s `--kubelet-arg=fail-swap-on`/no swap on the Docker VM) or an agent option to ignore swap
  on dev clusters.
- **Run 1, TestArgoCD release**: after switching the policy to audit the agent did not drop its field
  ownership within 3 min. Not reproduced in the following runs (released after 5 s). In that run the
  spoke-1 agent had just been replaced by WO-08's build (`agent:wo08-43b3894`, deployed ~02:22 UTC).
- **WO-08 agent build** logs `report to hub failed: HTTP 400: invalid_item: items[N]: recommendationID
  must be a UUID` every 30 s on ro-spoke-1 since ~02:27 UTC (workload-status reports rejected).
- **Hub trigger (proposal for WO-04)**: an admin `POST /v1/admin/recompute` (or a dev `interval: 1m`
  with a 1-minute step) would cut the suite from ~17 to ~5 minutes and make it less timing-dependent.
- WO-07's webhook was deployed on ro-spoke-1 during these runs; scenario 8 stays skipped until the
  branch is merged.

## TODO

- Scenario 8 (webhook, WO-07) and 10 (rollback, WO-08): `TestWebhook` / `TestRollback` are skipped
  placeholders with TODOs.
- CI: `ci/github-workflows/e2e.yaml` needs a token with `workflow` scope to move to `.github/workflows/`,
  and an 8-core runner. Not run in CI yet.
