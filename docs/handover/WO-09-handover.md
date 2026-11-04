# WO-09 hand-over (end-to-end suite)

Branches: `wo-09-e2e` (merged, scenarios 1–7, 9, 11) and `wo-09b-e2e` (scenarios 8 and 10, recompute
trigger; on top of main 0596898). Owns `test/e2e/`, `make/e2e.mk`, `ci/github-workflows/e2e.yaml`.
Suite docs: `test/e2e/README.md`.

## Run it

```bash
make e2e                       # 13–20 min against the existing clusters, all scenarios in parallel
make e2e E2E_RUN=TestArgoCD    # selected scenarios
make e2e-clean                 # leftovers of an interrupted run (namespaces, policies, Argo apps)
make e2e-unblock               # undo scenario 11's token swap / scenario 8's webhook scale-down after a killed run
```

Build tag `e2e`: `make test` / `go test ./...` never run it. Log: `bin/e2e.log`. Exit code is non-zero on failure.

## Results (shared environment, 2026-11-04 UTC)

| Run | Code | Result | Time |
|---|---|---|---|
| 1 (02:15) | wo-09-e2e, first version | 9 pass, `TestArgoCD/release` timeout (agent swapped mid-run, see findings), `TestInPlaceFallback` wrong trigger | 16.5 min |
| 2 (02:54) | wo-09-e2e, first version | `TestAudit` judged an immature first recommendation; `TestHubUnreachable` NetworkPolicy didn't cut the keep-alive connection | 16.2 min |
| 3 | – | stopped (obsolete outage method) | – |
| 4 (03:13) | wo-09-e2e final | PASS (1–7, 9, 11; 3 skip-by-design) | 12.7 min |
| 5 (03:26) | wo-09-e2e final, back to back | PASS | 14.4 min |
| 8+10 only (03:51) | wo-09b-e2e | PASS: `TestWebhook` (both subtests), `TestRollback` (OOM, restarts) | 7.9 min |
| B1_ROW |
| B2_ROW |

No leftovers after the runs (namespaces, policies, Argo apps); the spoke-2 agent token and the
webhook replicas are back to their originals. The killed run 3 left a directory `e2e/e2e-argocd-208b17/`
in the in-cluster git repo (not synced by anything; disappears when git-server restarts).

Scenario 3 skips on purpose (see findings).

## How it drives the system

- Real hub (WO-04), real agents (WO-06, with WO-08's health watch) and webhook (WO-07); no stub hub.
- Each scenario: namespace `e2e-<scenario>-<6 hex>` + cluster-scoped RightsizingPolicy of the same name
  selecting only it (priority 1000, `learningPeriod: 0s`, `minConfidence: "0"`, default value settings
  so no `RecommendationStale` round). Deleted at the end (`t.Cleanup`).
- Usage from `stress-ng`, a tiny JVM, and (scenario 10) a Python program whose held memory and crash
  switch come from a ConfigMap; images already on the nodes. The hub's dev run (every 5 min, data
  aligned to 5-min steps) gives a new workload a recommendation 3–15 min after it starts; the first can
  rest on a few samples, so applying scenarios start in audit and switch to auto-patch once the hub
  recommends a real decrease.
- **Recompute (D-019):** the suite calls `POST /v1/admin/recompute` (admin token from `hub-dev-tokens`)
  at most every 2 minutes while it waits, once the hub has it. Not on main yet (404): it then waits for
  the periodic run, as in the runs above. With the trigger the suite should take ~5–8 minutes.
- Argo CD: per-scenario Application in `argocd` on `e2e/<namespace>/` of the in-cluster git repo (the
  main `workloads` app only syncs the repo root), D-006 settings copied from `argocd/workloads`.
- Scenario 4 forces a real kubelet `Infeasible` resize with the policy floor `guardrails.minCPU: 16`
  (nodes have 8 cores).
- Objects outside the suite's namespaces, both restored at the end and by `make e2e-unblock`:
  - scenario 11 swaps the **ro-spoke-2** agent's hub token (`resource-optimizer/resource-optimizer-agent-token`)
    for an invalid one for ~8 min (the spoke-2 agent applies nothing meanwhile);
  - scenario 8 scales the **ro-spoke-1** webhook Deployment to 0 for ~15 s (new pods there get template
    values meanwhile; the agent's drift repair fixes Applied in-place workloads).

## Findings for other tracks

- **make on macOS ignores `.SHELLFLAGS`** (GNU make 3.81): `make/common.mk`'s `-eu -o pipefail` is not in
  effect for any target on the dev Macs, so `cmd | tee` recipes report success on failure. `make e2e`
  sets `pipefail` itself; other `.mk` files may want the same (or require make ≥ 3.82).
- **NetworkPolicies don't cut existing connections** on k3s (kube-router accepts ESTABLISHED): the agent's
  keep-alive connection to the hub survives a policy that blocks the hub endpoint (scenario 11 run 2).
- **Swap blocks scenario 3** (JVM memory in-place / RestartContainer) and forces scenario 10's memory case
  onto rollout: k3d nodes report 1 GiB swap, so the agent (correctly, D-007) refuses in-place memory
  changes with `UnsupportedNode`. Scenario 3 asserts that and that pods are untouched, then skips.
  Exercising RestartContainer needs a swap-free node (WO-01) or an agent option to ignore swap on dev
  clusters.
- **Shared agents changed under runs 1–2** (WO-08 builds deployed ~02:22 and ~02:42 UTC). Run 1's
  `TestArgoCD/release` timeout coincided with the first swap and has not reproduced since.
- Rollback timings seen live: OOM → `RolledBack` 70 s after the memory change (most of it ConfigMap
  propagation); 3 restarts → `RolledBack` 110 s after the crash switch (CrashLoopBackOff backoff).

## TODO

- CI: `ci/github-workflows/e2e.yaml` needs a token with `workflow` scope to move to `.github/workflows/`
  and an 8-core runner; not run in CI yet. The WO's DoD asks for 3 consecutive passes locally and in CI.
- Switch to the recompute trigger is automatic once D-019 lands; re-measure the suite time then.
