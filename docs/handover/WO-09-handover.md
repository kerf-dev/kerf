# WO-09 hand-over (end-to-end suite)

Branch `wo-09-e2e` (on top of main 183fa69). Owns `test/e2e/`, `make/e2e.mk`, `ci/github-workflows/e2e.yaml`.
Suite docs: `test/e2e/README.md`.

## Run it

```bash
make e2e                       # 13–20 min against the existing clusters, all scenarios in parallel
make e2e E2E_RUN=TestArgoCD    # selected scenarios
make e2e-clean                 # leftovers of an interrupted run (namespaces, policies, Argo apps)
make e2e-unblock               # restore the spoke-2 agent token if a killed run left scenario 11's outage
```

Build tag `e2e`: `make test` / `go test ./...` never run it. Log: `bin/e2e.log`. Exit code is non-zero on failure.

## Results (shared environment, 2026-11-04 UTC)

| Run | Code | Result | Time |
|---|---|---|---|
| 1 (02:15) | first version | 9 pass, `TestArgoCD/release` timeout (agent swapped mid-run, see findings), `TestInPlaceFallback` wrong trigger | 16.5 min |
| 2 (02:54) | first version | `TestAudit` judged an immature first recommendation; `TestHubUnreachable` NetworkPolicy didn't cut the keep-alive connection | 16.2 min |
| 3 | – | stopped (obsolete outage method) | – |
| **4 (03:13)** | final (5273acd + docs) | **PASS**, all 11 runnable (3 skip-by-design, 8/10 pending) | **12.7 min** |
| **5 (03:26)** | final, back to back | **PASS**, same | **14.4 min** |

Runs 4 and 5 are the `make e2e && make e2e` flakiness check. No leftovers after them (namespaces, policies,
Argo apps), the spoke-2 agent token is intact. The killed run 3 left a directory `e2e/e2e-argocd-208b17/`
in the in-cluster git repo (not synced by anything; disappears when git-server restarts).

Scenario 3 skips on purpose (see findings); 8 and 10 are placeholders until WO-07/WO-08 merge.

## How it drives the system

- Real hub (WO-04) and real agents (WO-06); no stub hub (a second agent with a stub hub would act on
  the same workloads as the shared agent releases).
- Each scenario: namespace `e2e-<scenario>-<6 hex>` + cluster-scoped RightsizingPolicy of the same name
  selecting only it (priority 1000, `learningPeriod: 0s`, `minConfidence: "0"`, default value settings
  so no `RecommendationStale` round). Deleted at the end (`t.Cleanup`).
- Usage from `stress-ng` / a tiny JVM, images already on the nodes. The hub's dev run (every 5 min, data
  aligned to 5-min steps) gives a new workload a recommendation 3–15 min after it starts; the first can
  rest on a few samples, so applying scenarios start in audit and switch to auto-patch once the hub
  recommends a real decrease.
- Argo CD: per-scenario Application in `argocd` on `e2e/<namespace>/` of the in-cluster git repo (the
  main `workloads` app only syncs the repo root), D-006 settings copied from `argocd/workloads`.
- Scenario 4 forces a real kubelet `Infeasible` resize with the policy floor `guardrails.minCPU: 16`
  (nodes have 8 cores).
- Scenario 11 swaps the **ro-spoke-2** agent's hub token (`resource-optimizer/resource-optimizer-agent-token`)
  for an invalid one for ~8 min per run, then restores it; `make e2e` also restores it before/after
  from `hub-dev-tokens` if a killed run left it. This is the only object outside the suite's
  namespaces the suite touches. During that window the spoke-2 agent applies nothing.

## Findings for other tracks

- **make on macOS ignores `.SHELLFLAGS`** (GNU make 3.81): `make/common.mk`'s `-eu -o pipefail` is not in
  effect for any target on the dev Macs, so `cmd | tee` recipes report success on failure. `make e2e`
  sets `pipefail` itself; other `.mk` files may want the same (or require make ≥ 3.82).
- **NetworkPolicies don't cut existing connections** on k3s (kube-router accepts ESTABLISHED): the agent's
  keep-alive connection to the hub survives a policy that blocks the hub endpoint. Relevant for any
  "hub unreachable" testing (scenario 11 run 2 failed this way; the suite now swaps the token instead).
- **Swap blocks scenario 3** (JVM memory in-place / RestartContainer): k3d nodes report 1 GiB swap, so the
  agent (correctly, D-007) refuses in-place memory changes with `UnsupportedNode`. The test asserts that
  and that pods are untouched, then skips. Exercising RestartContainer needs a swap-free node (WO-01) or
  an agent option to ignore swap on dev clusters.
- **Shared agents changed under the runs**: WO-08 redeployed both spoke agents with its builds
  (`agent:wo08-43b3894` ~02:22, `wo08-3d7a5da` ~02:42 UTC) and WO-07 installed its webhook. In run 1,
  `TestArgoCD/release` timed out: the agent did not drop its field ownership within 3 min of the switch
  to audit. That coincided with the first swap and has not reproduced since (released in 5 s, 3 runs).
- **WO-08 agent build** logs `report to hub failed: HTTP 400: invalid_item: items[N]: recommendationID must
  be a UUID` every 30 s on ro-spoke-1 (seen 02:27–02:30 UTC).
- **Hub trigger (proposal for WO-04)**: an admin `POST /v1/admin/recompute` (or dev `interval: 1m` with a
  1-minute step) would cut the suite to ~5 minutes and remove its main timing dependency.

## TODO

- Scenario 8 (webhook, WO-07) and 10 (rollback, WO-08): `TestWebhook` / `TestRollback` are skipped
  placeholders with TODOs.
- CI: `ci/github-workflows/e2e.yaml` needs a token with `workflow` scope to move to `.github/workflows/`
  and an 8-core runner; not run in CI yet. The WO's DoD asks for 3 consecutive passes locally and in CI.
