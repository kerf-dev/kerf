# End-to-end suite

Go tests (build tag `e2e`) against the running k3d environment with the deployed hub
and agents. Nothing is mocked: test pods burn known amounts of CPU/memory, the spoke
vmagent ships usage to VictoriaMetrics, the hub's periodic run recommends, the agent applies.

```bash
make e2e                         # full suite, all scenarios in parallel (goal: <= 8 min)
make e2e-smoke                   # smoke tier: one scenario per area (goal: <= 4 min)
make e2e E2E_RUN=TestRollout     # one scenario (Go -run regexp)
make e2e E2E_FLAGS=-short        # (no-op today) extra go test flags
make e2e-clean                   # remove leftovers of an interrupted run
make e2e-vet                     # compile/vet the suite without a cluster
```

`go test ./...` / `make test` never run it (build tag). The log of the last run is `bin/e2e.log`
(`bin/e2e-smoke.log` for the smoke tier), the raw `go test -json` stream `bin/e2e.json`.

## Tiers

| Tier | Target | Tests | Where |
|---|---|---|---|
| smoke | `make e2e-smoke` | `E2E_SMOKE_TESTS` = `TestAudit`, `TestInPlaceCPU`, `TestWebhook`, `TestRollback`, `TestAdminPolicy`, `TestReadiness`, `TestMaintenance`, `TestWebSignIn`: audit, in-place apply, webhook, rollback, admin policy, readiness, component versions, sign-in through the web proxy | pull requests touching `cmd/**`, `internal/**`, `deploy/**`, `test/e2e/**` (CI, once a runner can host the fleet) |
| full | `make e2e` | every test | nightly (CI), and locally before merging suite or timing changes |

At the end of every run the log shows a per-test duration table, the setup time (`make e2e` start
until the first test starts) and the wall time, and, for the five slowest tests, every wait they
made (`<what> after <duration>` of `eventually`, `held for` of `consistently`, the spoke-2 lock,
`slept`). `test/e2e/e2esummary` turns the `go test -json` stream into that log.

## Timing

The suite is fast because the dev charts use short timers and the suite waits for observed signals,
not fixed durations. Production defaults are unchanged.

| Setting | Production default | Dev/e2e value | Where | Effect |
|---|---|---|---|---|
| hub `config.recommender.endAlignment` | the step (5m) | `30s` | `deploy/charts/hub/values-dev.yaml` | a run (and a recompute) sees usage up to 30 s ago instead of the last full 5-minute step: a new workload gets its first recommendation ~1 min after it starts instead of ~5.5 min |
| agent `capacity.interval` | `5m` | `30s` | `deploy/charts/agent/values-dev.yaml` | `TestNodeCapacity` sees its blockers within a minute |
| agent `pullInterval` | `1m` | `15s` (was 30s) | `deploy/charts/agent/values-dev.yaml` | recommendations, policies, pause and approvals reach the agent sooner |
| agent `hubStaleAfter` | `5m` | `1m` | same | `TestHubUnreachable` waits 1 min plus the cut-off, not 6.5 min |
| agent `reportInterval` | `10s` | `5s` (unchanged) | same | events reach the hub |
| agent `health.checkInterval` | `15s` | `5s` | same | health-watch re-evaluation (pod events act at once anyway) |
| suite `E2E_RECOMPUTE_EVERY` | – | `30s` | env | per-cluster `POST /v1/admin/recompute?cluster=` at most this often per cluster while a test waits |

- Recompute requests name the cluster: a run of one dev spoke takes a few seconds (all four
  clusters, with the 30-day synthetic ones, ~40 s). Holds (`holdRecompute`) are per cluster.
- `TestHubUnreachable` reads `--hub-stale-after` and `--pull-interval` from the agent's Deployment
  args, waits until the hub stops recording the agent's heartbeats (the token swap is in effect), then
  `hubStaleAfter` plus the hub's heartbeat granularity (15 s). It nudges the agent pod (annotation
  `e2e.kerf.getkerf.dev/token-nudge`, removed at the end and by `make e2e-unblock`): a pod update
  makes the kubelet refresh the mounted token Secret at once instead of at its next periodic sync.
- `setControl` nudges the test pods the same way, so the ConfigMap that makes them OOM or crash
  arrives at once.

## Scenarios

| # | Test | Cluster | What is asserted |
|---|---|---|---|
| 1 | `TestAudit` | spoke-1 | hub recommends a decrease; RSR reason `ModeAudit`; pods (UID, resources, restarts) and Deployment generation unchanged; never `Applied` |
| 2 | `TestInPlaceCPU` | spoke-1 | RSR `Applied`; both pods resized in place to the RSR value (spec and kubelet-reported status), same UIDs, no restarts; Deployment generation and template unchanged; hub `applied` event, strategy in-place |
| 3 | `TestInPlaceJVMMemory` | spoke-1 | 2 JVM pods (heap `-XX:MaxRAMPercentage=50`, so it follows the limit) with memory `resizePolicy: RestartContainer`, memory auto-patch. Node **with swap** (k3d on Docker Desktop reports 1 GiB): agent blocks with `UnsupportedNode` and pods stay untouched, then the test is **skipped** with that reason. Node without swap: memory decreased, exactly one container restart, same pod UID |
| 3b | `TestJVMFixedHeapIsAuditOnly` | spoke-1 | JVM with a fixed `-Xmx` and memory auto-patch: agent blocks with `JVMFixedHeap`, pods and Deployment unchanged |
| 3c | `TestSingleReplicaRestartIsBlocked` | spoke-1 | single-replica JVM whose in-place memory resize would restart its container (RestartContainer): agent blocks with `SingleReplicaUnsafe`, pod and Deployment unchanged |
| 4 | `TestInPlaceFallback` | spoke-1 | policy floor `guardrails.minCPU: 16` (twice the node) makes the agent resize to 16 cores; the kubelet answers `PodResizePending/Infeasible`; the agent falls back: hub `fallback` event (`ResizeInfeasible`), RSR `spec.strategy: rollout`, template cpu 16 (the new pod stays Pending, the old one keeps running) |
| 5 | `TestRollout` | spoke-1 | RSR `Applied`; template cpu = recommendation (a decrease); field manager `kerf` owns `…containers[app].resources.requests.cpu`; new ReplicaSet, pods at the new value; hub `applied` event, strategy rollout |
| 6 | `TestArgoCD/hard_sync_keeps_values` | spoke-1 | workload deployed by its own Argo CD Application from the in-cluster git server (`e2e/<namespace>/`), with Argo CD set to ignore the fields owned by `kerf`; after the rollout patch a hard refresh + explicit sync keeps the value, the app is Synced, and 75 s of self-heal don't revert it |
| 7 | `TestArgoCD/release_restores_Git_values` | spoke-1 | policy switched to audit → the agent drops its field ownership → hard refresh + sync restores the Git value (250m), app Synced |
| 8 | `TestWebhook/scale-out_gets_the_applied_values` | spoke-1 | in-place CPU applied (RSR `Applied`, status observed its spec); scaling 1 → 2 gives new pods the applied CPU at admission (`injected-resources` annotation, no restarts); template unchanged |
| 8 | `TestWebhook/webhook_down_does_not_block_pod_creation` | spoke-1 | webhook Deployment scaled to 0 (original replicas recorded in an annotation, restored at the end): scaling 2 → 3 still creates a Ready pod within a minute, not mutated; after the webhook is back, the agent's drift repair resizes that pod to the applied value |
| 9 | `TestGuardrails/*` | spoke-1 | throttled workload (limit 200m, wants a core): hub flags `throttled` and raises; applied value > current, never below. HPA-coupled (CPU-utilization HPA): RSR reason `HPACoupled`, nothing changed. `maxChangePercent: 20`: applied decrease is within 20% (200m–250m) |
| 10 | `TestRollback/OOM_after_a_memory_decrease` | spoke-1 | Python workload whose held memory is set through a ConfigMap; memory decrease applied by rollout (in-place memory is refused on swap nodes); raising the held memory past the new limit OOM-kills it → RSR `RolledBack` with `rolledBackAt`, template back at 256Mi, hub `rolled_back` event reason `OOMKills` |
| 10 | `TestRollback/restarts_after_an_in-place_CPU_change` | spoke-1 | in-place CPU decrease applied; the container is then made to crash-loop (ConfigMap) → ≥ 3 restarts → RSR `RolledBack`, pods resized back to 250m in place, template untouched, hub `rolled_back` event reason `Restarts` |
| 11 | `TestHubUnreachable` | spoke-2 | with one workload applied and one waiting in audit, the agent is cut off from the hub (invalid hub token) until it counts the hub as unreachable (`hubStaleAfter` after its last request, as recorded by the hub); switching the waiting workload to auto-patch gives `HubUnreachable` and nothing changes for 2 agent pulls (at least 30 s); the applied workload is not reverted; after reconnecting, the pending change is applied |
| 12 | `TestRealizedSavings` | spoke-1 | in-place CPU decrease applied (2 replicas); the hub's realized savings for ro-spoke-1 are computed up to the last hour or so; applied changes of earlier runs older than two hours have a realized saving by workload id (also for deleted namespaces); the node cost trend (`relation: correlated`) has hourly points with node cost and requested/allocatable CPU. With `E2E_REALIZED_WAIT` (e.g. `75m`; the hub counts complete hours only): the CPU released now is (250m − applied) × 2 replicas, within 5 % |
| 13 | `TestNodeCapacity` | spoke-1 | a PodDisruptionBudget with `minAvailable` = replicas and a `karpenter.sh/do-not-disrupt` pod → within a few agent capacity reports (`capacity.interval` 30s in dev) `GET /v1/clusters/{cluster}/capacity` lists both as blockers naming their objects, with message and fix; totals, pools and a `first-fit-decreasing` estimate with its method link |
| 14 | `TestFreshInstall` | hub cluster (`E2E_INSTALL_CLUSTER_CONTEXT`) | `helm install` of the umbrella chart from this tree, `global.mode: all`, in its own namespace and release, with no token or admin password supplied (bundled PostgreSQL and VictoriaMetrics, the fleet's images, read-only agent, no webhook): the chart created the agent and pricing tokens (`<release>-hub-local-tokens`); the agent and pricing are Ready; the agent's heartbeats reach the new hub (`connected`) with no manual step; the generated bootstrap admin signs in with `mustChangePassword: true`. Needs `helm`; uninstalls and deletes the namespace at the end |

### Admin area

These drive the hub API like the web UI: logged in as the dev users `viewer`, `operator` and `admin`
(values-dev.yaml; `E2E_<USER>_PASSWORD` overrides) with the `kerf_session` cookie and `X-CSRF-Token`.

| # | Test | Cluster | What is asserted |
|---|---|---|---|
| A1, A4 | `TestAdminPolicy` | spoke-1 | admin creates a namespace policy (audit) → hub-labelled CR with id/version/type annotations; update to auto-patch (`If-Match`) → CR v2, workload `Applied`; `config_changed` rows `PolicyCreated`/`PolicyUpdated` name the admin; revert to v1 → v3, CR back to audit, agent resolves cpu audit, `PolicyReverted` row; delete → CR removed |
| A2 | `TestAdminPause` | spoke-2 | operator pauses ro-spoke-2 (after the agent applied the paused config) → a workload switched to auto-patch gets exactly one `blocked/Paused` and stays unchanged; an applied workload made to crash is still `RolledBack`; operator resume → the pending change is `Applied`; `AutomationPaused`/`Resumed` rows name the operator |
| A3 | `TestApproveOnce` | spoke-1 | operator approves the latest recommendation of an audit workload → `applied/ApprovedOnce`, pods resized in place, approval `consumed`, `WorkloadApproved` row; the next recommendation (new id) is not applied |
| A5 | `TestGitPolicyUntouched` | spoke-1 | a hub policy with the name of a Git CR (no hub label): the CR is never modified or deleted (also after the hub policy is deleted); the inventory lists it as `source: git` and the hub policy as invalid |
| A6 | `TestAdminRoles` | registered test cluster | viewer: 403 `role_forbidden` on policy create, pause, recheck; operator: 403 on global defaults, policies and recommender settings, 403 `csrf_invalid` without the token, pause + resume of a non-prod cluster OK and audited |
| A7 | `TestApproveOnceArgoCD` | spoke-1 | approve once on an Argo CD app with self-heal (rollout): the agent keeps the field ownership, a hard sync and 75 s of self-heal keep the approved value, app Synced |
| A8 | `TestOnboarding` | new cluster `e2e-onb-*` | datasource test of `local` with ro-spoke-1's label finds series; registered with that binding; recheck runs the metrics checks; state stays `Registered`; auto-patch policy → 409 `cluster_not_automation_ready`; operator `overrideReadiness` → 403; admin override → 201 with `after.overrideReadiness: true`; cluster deleted |
| A9 | `TestReadiness/*` | both spokes | after a recheck (answered by the agent) the state is `AutomationReady`, no blocking failures, every capability granted |
| A10 | `TestReadinessRBAC` | spoke-2 | the agent's `pods/resize` rule removed from its ClusterRole → recheck → `ReadyAudit`, `rbac.resize` fail, `ReadinessChanged` row (actor system/readiness), auto-patch policy refused (409); rule restored (also on failure, and by `make e2e-unblock`) → `AutomationReady` again |
| A11 | `TestReadinessKSMDown` | spoke-2 | **skipped unless `KERF_E2E_DISRUPTIVE=1`** (needs the user's OK): kube-state-metrics scaled to 0 → `metrics.freshness` fails (blocking), state below `MetricsOK`, `summary.blocking > 0` (banner); scaled back at the end |
| A12 | `TestReadinessRecheck` | spoke-1 | a recompute (`POST /v1/admin/recompute?cluster=`) and then a re-check each re-run the hub's metrics checks within 2 minutes (`metrics.freshness` `checkedAt` after the request), instead of after the 1h check interval; on the fresh results `metrics.scrape-interval` passes with an interval of 65 s or less, `metrics.freshness` never reports `1h0m0s`, and every failing hub metrics check's remediation names the re-check endpoint and never `insecure_skip_verify` |

### GPU visibility

| Test | Cluster | What is asserted |
|---|---|---|
| `TestGPUVisibility` | `synth-gpu` (synthetic) | read-only: after a recompute, `GET /v1/workloads?cluster=synth-gpu&gpu=any` lists the five GPU workloads of `tools/synth/scenarios/fleet-gpu.yaml` with status `idle` (+ `scale-to-zero`, the whole cost when priced), `under-used` (+ `time-slicing`; also the 4-GPU one), `ok` and `no-data`; the `gpu` filter and the `gpu` facet count them; `recommendation.gpu` of the detail matches; readiness check `metrics.dcgm` is `pass` |

It needs the cluster `synth-gpu` in the hub (`make synth-backfill-gpu`, then register it with the `local` datasource) and
**skips** with that hint when the hub doesn't know it. It is not part of the smoke tier.

Spoke-2 is shared by scenarios that disturb its agent (hub outage, pause, RBAC removal, KSM) or
need it undisturbed while they apply. A suite-wide lock (`lockSpoke2`) serialises those phases;
setup (deploying, waiting for recommendations) still runs in parallel.

Run marker: while `make e2e` runs, namespace `e2e-run-<id>` exists on both spokes (so the
pre-deploy check `kubectl get ns | grep e2e-` sees every run, including scenarios that create no
namespace); a killed run's marker is deleted by the next run or `make e2e-clean`.

Field ownership: the suite writes with field manager `kerf-e2e`. After restoring a
Helm-owned object (agent ClusterRole, agent token Secret, webhook Deployment, kube-state-metrics)
it drops its managedFields entry, so the next `helm upgrade` (server-side apply as `helm`) does not
conflict; `make e2e-unblock` drops leftover entries (also the `e2e.test` ones of older versions).

Hub objects: every test deletes the policies and clusters it created and resumes what it paused.
Before the scenarios (and in `make e2e-clean`) the suite sweeps what an interrupted run left:
hub policies, clusters and teams named `e2e-*`, and pauses whose reason starts with `e2e `.

### Notifications

| Test | Cluster | What is asserted |
|---|---|---|
| `TestNotifications` | spoke-1 (events only) | A fake signed-webhook receiver runs on the host (the hub reaches it as `host.k3d.internal`; the dev hub values set `notifications.allowInsecureHTTP`). As admin: a webhook channel with a signing secret (never returned by the API), **Send test** arrives signed (`X-Kerf-Signature` verified with `notify.Verify`), a second channel nobody listens on shows its error (without its URL) on **Send test** and on the channel. A rule for one namespace of spoke-1 (`rolled_back`, `blocked`); events posted with spoke-1's agent token (namespaces `e2e-notify-*`; the audit log is append-only, so they stay): the rollback arrives within a minute with a link to the workload page and without the credential in its text, the rollback of another namespace and repeats of one block produce nothing more (one `blocked` message), the history lists both deliveries, `kerf_notification_deliveries_total` is exported. The rule and channels are deleted at the end. |

### Teams

| Test | Cluster | What is asserted |
|---|---|---|
| `TestTeamScopedAccess` | spoke-1 | Two namespaces `e2e-teama-*` and `e2e-teamb-*`, each with a small audit workload; team A owns its namespace by selector, team B its workload by the owner label `team` (reported by the agent). A local user bound to team A as operator only (scope `teams`): `GET /v1/workloads` and `GET /v1/summary` hold team A's workloads only (total and fleet count agree, `teams` lists A only); team B's workload is `404` to read and `403 scope_forbidden` to approve; `GET /v1/admin/policies` is `403`; approving team A's workload succeeds and its `WorkloadApproved` row has `actor.teams = [team A]`. Teams and the user are deleted at the end. |

## How the hub is driven

The real hub, not the agent's stub-hub mode: the agents are shared Helm releases and a
second agent with a stub hub would fight them over the same workloads.

- Every scenario creates namespace `e2e-<scenario>-<6 hex>` and a cluster-scoped
  `RightsizingPolicy` of the same name selecting only that namespace (priority 1000), with
  `guardrails.learningPeriod: 0s` and `minConfidence: "0"` so minutes of data are enough. Other
  value settings stay at the defaults, so the hub's recommendation settings match at once (no
  `RecommendationStale` round).
- The hub (dev chart) runs every 5 minutes; with `endAlignment: 30s` and the suite's per-cluster
  recomputes a new workload gets a recommendation about a minute after it starts (a hub without
  `endAlignment` aligns to 5-minute steps: 5–6 minutes). The agent pulls every 15 s. Timeouts
  (`E2E_APPLY_TIMEOUT`, default 18m) still allow for the slow case; all scenarios run in parallel.
- The first recommendation can be based on a handful of samples (e.g. "keep 500m"). Scenarios that
  apply therefore start their policy with CPU in audit, wait until the hub recommends a real
  decrease, and only then switch CPU to auto-patch (`armed`). Otherwise an immature first
  recommendation could be applied and then held by the 24 h cooldown.
- Usage: `stress-ng --cpu 1 --cpu-load N` (5% of a core against a 250m request (`overCPU`; small because all scenarios share one 8-core node) = clearly
  over-provisioned; 100% against a 200m limit = throttled); a small JVM program for scenario 3.
  Images are the ones the sample workloads already pulled, so nothing is downloaded.
- Argo CD: each Argo scenario pushes its manifests to `e2e/<namespace>/` of
  `git://localhost:19418/workloads.git` (the main `argocd/workloads` Application syncs only the repo
  root, non-recursively) and creates its own Application in `argocd`, removed (cascading) at the end,
  together with the git directory.
- Hub outage (scenario 11): the spoke-2 agent's hub token (`kerf/kerf-agent-token`)
  is replaced by an invalid one for ~3 minutes (`hubStaleAfter` 1m in the dev values). Every pull fails with 401, which the agent treats like
  an unreachable hub, and it re-reads the token on each request. The original is put back at the end.
  If a run was killed, `make e2e` restores it from the hub's `hub-dev-tokens` Secret before and after
  the run (`make e2e-unblock`). A NetworkPolicy does not work: the k3s policy controller keeps
  ESTABLISHED connections, and the agent's keep-alive connection to the hub never goes idle.

- Webhook down (scenario 8): the webhook Deployment on spoke-1 is scaled to 0 for about a minute,
  with its replicas recorded in annotation `e2e.kerf.getkerf.dev/scaled-from`, then scaled
  back. `make e2e-unblock` scales it back after a killed run.
- Recompute: when the hub offers `POST /v1/admin/recompute`, the suite triggers it for the
  cluster it waits on, at most every `E2E_RECOMPUTE_EVERY` (30 s) per cluster, using the admin token from `hub-dev-tokens` (`E2E_HUB_ADMIN_TOKEN`
  overrides; `E2E_HUB_RECOMPUTE=off` disables it). Without it (404), it waits for the periodic run.

Nothing outside the suite's namespaces is modified, except that temporary token swap, the webhook
scale-down, the spoke-2 pause and `pods/resize` rule removal and, only with
`KERF_E2E_DISRUPTIVE=1`, the kube-state-metrics scale-down; `make e2e-unblock` undoes the cluster-side ones.

## Configuration (environment)

| Variable | Default |
|---|---|
| `E2E_SPOKE1_CONTEXT` / `E2E_SPOKE2_CONTEXT` / `E2E_HUB_CONTEXT` | `k3d-ro-spoke-1` / `k3d-ro-spoke-2` / `k3d-ro-hub` |
| `E2E_SPOKE1` / `E2E_SPOKE2` | hub cluster names `ro-spoke-1` / `ro-spoke-2` |
| `E2E_HUB_URL` | `http://localhost:18080` |
| `E2E_WEB_URL` | `http://localhost:19090` (the web UI's nginx; `TestWebSignIn`) |
| `E2E_HUB_UI_TOKEN` | read from Secret `kerf/hub-dev-tokens` (key `ui`) on ro-hub |
| `E2E_GIT_URL` / `E2E_GIT_IN_CLUSTER_URL` | `git://localhost:19418/workloads.git` / `git://git-server.git-server.svc.cluster.local/workloads.git` |
| `E2E_APPLY_TIMEOUT` | `18m` (hub run + data alignment + apply) |
| `E2E_HUB_STALE_AFTER` | the agent's `--hub-stale-after` (Deployment args) |
| `E2E_RECOMPUTE_EVERY` | `30s` (per cluster) |
| `E2E_REALIZED_WAIT` | `0` = off; how long `TestRealizedSavings` may wait for the applied change's first complete hour |
| `E2E_AGENT_DEPLOYMENT` | `kerf-agent` (scenario 11 reads its args) |
| `E2E_INFEASIBLE_CPU` | `16` (scenario 4's CPU floor; must exceed the node's capacity) |
| `E2E_NOTIFY_HOST` | `host.k3d.internal` (how the hub pod reaches the suite's fake receiver; `TestNotifications`) |
| `E2E_KEEP` | set to keep namespaces/policies/apps for debugging (`make e2e-clean` later) |
| `E2E_VIEWER_PASSWORD` / `E2E_OPERATOR_PASSWORD` / `E2E_ADMIN_PASSWORD` | dev users of values-dev.yaml |
| `E2E_AGENT_CLUSTERROLE` | `kerf-agent` (scenario A10) |
| `KERF_E2E_DISRUPTIVE` | unset: A11 skipped; `1` runs it |
| `E2E_KSM_NAMESPACE` / `E2E_KSM_DEPLOYMENT` | `monitoring` / `kube-state-metrics` (A11) |

Prerequisites: the agents have their hub token Secret and are connected.

## CI

`.github/workflows/e2e.yaml`: creates the clusters
(`make dev-up`), deploys hub, pricing and agents, creates the agent tokens, runs `make e2e`
(nightly, manual dispatch with tier `full`) or `make e2e-smoke` (pull requests, manual tier
`smoke`), and on failure uploads the e2e logs, `kubectl cluster-info dump` of all clusters and the
rightsizing objects. Pull requests run it only when the repository variable
`E2E_ON_PULL_REQUESTS` is `true` (set it once a runner can host the fleet; `E2E_RUNNER` picks the
runner label, default `ubuntu-latest`).
