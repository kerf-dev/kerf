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
| b1 (03:59) | wo-09b-e2e | all pass except 11: another track's agent redeploy (wo12, 04:11) rewrote the token Secret mid-outage → outage now re-armed automatically | 17.9 min |
| c1 (04:19) | + outage hold | 11 pass (re-armed after a second redeploy at 04:27); 3 failed: hub answered 500 to agent pulls 04:28–04:43 (hub redeployed 04:43) | 24.7 min |
| c2 (04:44) | same | all pass except 3: WO-12 (merged meanwhile) makes `-Xmx` JVMs memory audit-only (`JVMFixedHeap`) | 19.4 min |
| d1 (05:04) | + main/WO-12, JVM via MaxRAMPercentage | Argo app stuck Progressing: all scenarios at once overcommitted the 8-core spoke-1 node (pods Pending) → stopped | – |
| **e1 (05:12)** | final (152fdd7): 250m test requests | **PASS** — all 15 tests (3 skip-by-design), recompute trigger live | **12.1 min** |
| **e2 (05:25)** | final, back to back | **PASS** | **15.3 min** |

e1 + e2 are the `make e2e && make e2e` check on main 0596898..ece1564 (WO-04b/07/08/12 merged, recompute deployed).

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
- **Recompute (D-019):** the suite calls `POST /v1/admin/recompute` (admin token from `hub-dev-tokens`,
  202) at most every 2 minutes while it waits; on a 404 it falls back to the periodic run. With it,
  all scenarios except 11 finish in 3–7 minutes. The suite's length (12–15 min) is now scenario 11's
  outage (`hubStaleAfter` 5m + 90 s), which no trigger can shorten.
- CPU budget: all scenarios start at once on spoke-1 (one 8-core node, ~2.6 cores of shared
  workloads), so over-provisioned test workloads request 250m (`overCPU`); at 500m pods went Pending.
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
- **Shared components changed under runs**: agent builds of WO-08 (~02:22, ~02:42) and WO-12 (04:11,
  04:27 UTC) were deployed during runs; each `helm upgrade` of the agent rewrites the token Secret
  (scenario 11 now re-arms its outage when that happens). Run 1's `TestArgoCD/release` timeout
  coincided with the first swap and has not reproduced since. CLAUDE.md now asks tracks to check for
  running e2e namespaces before deploying agent/webhook.
- **Hub incident 04:28–04:43 UTC**: the hub answered HTTP 500 to every agent pull until it was replaced
  by `hub:wo12-dd3a826` at 04:43:12 (likely a schema change applied under the old hub); spoke agents
  were offline (`HubUnreachable`) meanwhile.
- **WO-12** makes JVMs with a fixed `-Xmx` memory audit-only (`JVMFixedHeap`); scenario 3's JVM now
  sizes its heap with `-XX:MaxRAMPercentage`, and `TestJVMFixedHeapIsAuditOnly` covers the guard.
- Rollback timings seen live: OOM → `RolledBack` 70 s after the memory change (most of it ConfigMap
  propagation); 3 restarts → `RolledBack` 110 s after the crash switch (CrashLoopBackOff backoff).

## TODO

- CI: `ci/github-workflows/e2e.yaml` needs a token with `workflow` scope to move to `.github/workflows/`
  and an 8-core runner; not run in CI yet. The WO's DoD asks for 3 consecutive passes locally and in CI.
- Scenario 11 could drop to ~2 minutes if the agent's `hubStaleAfter` were configurable per test (it is
  a chart value shared by every track on spoke-2, so the suite does not change it).
