# End-to-end suite (WO-09)

Go tests (build tag `e2e`) against the running k3d environment (WO-01) with the deployed hub
(WO-04) and agents (WO-06). Nothing is mocked: test pods burn known amounts of CPU/memory, the spoke
vmagent ships usage to VictoriaMetrics, the hub's periodic run recommends, the agent applies.

```bash
make e2e                         # all scenarios in parallel, 13–20 min
make e2e E2E_RUN=TestRollout     # one scenario (Go -run regexp)
make e2e E2E_FLAGS=-short        # (no-op today) extra go test flags
make e2e-clean                   # remove leftovers of an interrupted run
make e2e-vet                     # compile/vet the suite without a cluster
```

`go test ./...` / `make test` never run it (build tag). The log of the last run is `bin/e2e.log`.

## Scenarios

| # | Test | Cluster | What is asserted |
|---|---|---|---|
| 1 | `TestAudit` | spoke-1 | hub recommends a decrease; RSR reason `ModeAudit`; pods (UID, resources, restarts) and Deployment generation unchanged; never `Applied` |
| 2 | `TestInPlaceCPU` | spoke-1 | RSR `Applied`; both pods resized in place to the RSR value (spec and kubelet-reported status), same UIDs, no restarts; Deployment generation and template unchanged; hub `applied` event, strategy in-place |
| 3 | `TestInPlaceJVMMemory` | spoke-1 | JVM pod with memory `resizePolicy: RestartContainer`, memory auto-patch. Node **with swap** (k3d on Docker Desktop reports 1 GiB): agent blocks with `UnsupportedNode` and pods stay untouched, then the test is **skipped** with that reason. Node without swap: memory decreased, exactly one container restart, same pod UID |
| 4 | `TestInPlaceFallback` | spoke-1 | policy floor `guardrails.minCPU: 16` (twice the node) makes the agent resize to 16 cores; the kubelet answers `PodResizePending/Infeasible`; the agent falls back: hub `fallback` event (`ResizeInfeasible`), RSR `spec.strategy: rollout`, template cpu 16 (the new pod stays Pending, the old one keeps running) |
| 5 | `TestRollout` | spoke-1 | RSR `Applied`; template cpu = recommendation (a decrease); field manager `resource-optimizer` owns `…containers[app].resources.requests.cpu`; new ReplicaSet, pods at the new value; hub `applied` event, strategy rollout |
| 6 | `TestArgoCD/hard_sync_keeps_values` | spoke-1 | workload deployed by its own Argo CD Application from the in-cluster git server (`e2e/<namespace>/`), D-006 settings; after the rollout patch a hard refresh + explicit sync keeps the value, the app is Synced, and 75 s of self-heal don't revert it |
| 7 | `TestArgoCD/release_restores_Git_values` | spoke-1 | policy switched to audit → the agent drops its field ownership → hard refresh + sync restores the Git value (500m), app Synced |
| 8 | `TestWebhook` | – | **skipped** until WO-07 lands |
| 9 | `TestGuardrails/*` | spoke-1 | throttled workload (limit 200m, wants a core): hub flags `throttled` and raises; applied value > current, never below. HPA-coupled (CPU-utilization HPA): RSR reason `HPACoupled`, nothing changed. `maxChangePercent: 20`: applied decrease is within 20% (400m–500m) |
| 10 | `TestRollback` | – | **skipped** until WO-08 lands |
| 11 | `TestHubUnreachable` | spoke-2 | with one workload applied and one waiting in audit, the agent is cut off from the hub (invalid hub token) for `hubStaleAfter` + 90 s; switching the waiting workload to auto-patch gives `HubUnreachable` and nothing changes for 60 s; the applied workload is not reverted; after reconnecting, the pending change is applied |

## How the hub is driven

The real hub, not the agent's stub-hub mode: the agents are shared Helm releases (WO-06) and a
second agent with a stub hub would fight them over the same workloads.

- Every scenario creates namespace `e2e-<scenario>-<6 hex>` and a cluster-scoped
  `RightsizingPolicy` of the same name selecting only that namespace (priority 1000), with
  `guardrails.learningPeriod: 0s` and `minConfidence: "0"` so minutes of data are enough. Other
  value settings stay at the defaults, so the hub's recommendation settings match at once (no
  `RecommendationStale` round).
- The hub (dev chart) runs every 5 minutes over data aligned to 5-minute steps, so a new workload
  gets a recommendation 3–15 minutes after it starts; the agent pulls every 30 s. Timeouts are sized
  for that (`E2E_APPLY_TIMEOUT`, default 18m) and all scenarios run in parallel.
- The first recommendation can be based on a handful of samples (e.g. "keep 500m"). Scenarios that
  apply therefore start their policy with CPU in audit, wait until the hub recommends a real
  decrease, and only then switch CPU to auto-patch (`armed`). Otherwise an immature first
  recommendation could be applied and then held by the 24 h cooldown.
- Usage: `stress-ng --cpu 1 --cpu-load N` (5% of a core against a 500m request = clearly
  over-provisioned; 100% against a 200m limit = throttled); a small JVM program for scenario 3.
  Images are the ones the sample workloads already pulled, so nothing is downloaded.
- Argo CD: each Argo scenario pushes its manifests to `e2e/<namespace>/` of
  `git://localhost:19418/workloads.git` (the main `argocd/workloads` Application syncs only the repo
  root, non-recursively) and creates its own Application in `argocd`, removed (cascading) at the end,
  together with the git directory.
- Hub outage (scenario 11): the spoke-2 agent's hub token (`resource-optimizer/resource-optimizer-agent-token`)
  is replaced by an invalid one for ~8 minutes. Every pull fails with 401, which the agent treats like
  an unreachable hub, and it re-reads the token on each request. The original is put back at the end.
  If a run was killed, `make e2e` restores it from the hub's `hub-dev-tokens` Secret before and after
  the run (`make e2e-unblock`). A NetworkPolicy does not work: the k3s policy controller keeps
  ESTABLISHED connections, and the agent's keep-alive connection to the hub never goes idle.

Nothing outside the suite's namespaces is modified, except that temporary token swap.

## Configuration (environment)

| Variable | Default |
|---|---|
| `E2E_SPOKE1_CONTEXT` / `E2E_SPOKE2_CONTEXT` / `E2E_HUB_CONTEXT` | `k3d-ro-spoke-1` / `k3d-ro-spoke-2` / `k3d-ro-hub` |
| `E2E_SPOKE1` / `E2E_SPOKE2` | hub cluster names `ro-spoke-1` / `ro-spoke-2` |
| `E2E_HUB_URL` | `http://localhost:18080` |
| `E2E_HUB_UI_TOKEN` | read from Secret `resource-optimizer/hub-dev-tokens` (key `ui`) on ro-hub |
| `E2E_GIT_URL` / `E2E_GIT_IN_CLUSTER_URL` | `git://localhost:19418/workloads.git` / `git://git-server.git-server.svc.cluster.local/workloads.git` |
| `E2E_APPLY_TIMEOUT` | `18m` (hub run + data alignment + apply) |
| `E2E_HUB_STALE_AFTER` | `5m` (the agent's `hubStaleAfter`) |
| `E2E_INFEASIBLE_CPU` | `16` (scenario 4's CPU floor; must exceed the node's capacity) |
| `E2E_KEEP` | set to keep namespaces/policies/apps for debugging (`make e2e-clean` later) |

Prerequisites: the agents have their hub token Secret (WO-06 hand-over) and are connected.

## CI

`ci/github-workflows/e2e.yaml` (to be moved to `.github/workflows/`): creates the clusters
(`make dev-up`), deploys hub, pricing and agents, creates the agent tokens, runs `make e2e`, and on
failure uploads `bin/e2e.log`, `kubectl cluster-info dump` of all clusters and the rightsizing
objects.
