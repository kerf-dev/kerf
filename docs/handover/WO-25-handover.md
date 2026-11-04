# WO-25 hand-over (cluster onboarding, metrics sources, readiness)

Phase 1 (self-contained packages) is merged into main 9325962. Phase 2 is on branch
`wo-25-onboarding-p2` (pushed, no PR). Decisions D-025 and D-026. Migration **0011** (approved).
The hub is not deployed (the orchestrator deploys it). The agent `wo25p2-efe6be9` runs on
ro-spoke-1.

## What is wired
**Hub**
- **Datasources:** `/v1/admin/datasources` CRUD.
  - If-Match and ETag. Secrets are write-only and encrypted. The built-in `local` datasource is
    upserted at start-up.
  - `409 builtin` for `local`, and `409 datasource_in_use` when a cluster is bound to it.
  - `POST …/test` runs reachability only, or every metrics check for an unsaved binding. `found` is
    absent when nothing matched.
  - Changes are recorded in the config audit (`DatasourceCreated/Updated/Deleted`).
- **Bindings:** `PATCH /v1/admin/clusters/{c}` validates `metrics` and normalises extra matchers.
  A binding change triggers the cluster's checks.
- **Recommender, usage endpoint, health watch and pricing** read a bound cluster's metrics from its
  datasource with its matchers, relabelled to the registered name.
  - Clusters without a binding get byte-identical queries.
  - Pricing series are always read from `local`.
- **Readiness**
  - **Endpoints:** `GET /v1/clusters/{c}/readiness`, `POST …/recheck` (202), `GET …/history`.
  - **Metrics checks** run hourly (`onboarding.checkInterval`), at registration, on recheck and on a
    binding or datasource change.
  - **`agent.connected` and `metrics.recommendations`** are re-evaluated every minute.
  - **Agent checks** come from the status batch (`checks[]`, `capabilities[]`). The agent's set
    replaces the previous one, and the catalog fields always win.
  - **Re-check flag:** the config pull carries `flags.recheck`. It is cleared when newer agent checks
    arrive, and expires after 15 minutes without an answer.
  - **Version check:** `hubVersion` (chart appVersion) and `minAgentVersion` are sent to the agent.
  - **Outputs:** `ClusterInfo` and `ClusterAdmin` carry the readiness summary, plus the gauges
    `ro_cluster_readiness{cluster,check,severity}` and `ro_cluster_readiness_state`.
  - **Audit:** every state change writes a history entry and a `ReadinessChanged` config-audit row
    (actor system/readiness).
- **Auto-patch gating:** `409 cluster_not_automation_ready` is returned when a request newly enables
  auto-patch on a cluster that is not AutomationReady.
  - It applies to policy create, policy update (only when auto-patch is newly set, or when the policy
    reaches new clusters), overrides, and every approve-once.
  - A global default or a policy without clusters is checked against every cluster.
  - An admin can send `overrideReadiness: true`, which is recorded as `after.overrideReadiness`.
    An operator who sends it gets `403 admin_required`.
- **Install:** `GET /v1/admin/clusters/{c}/install` returns `format` helm, values or argocd.
  - For a bound cluster it adds the `health.*` matchers, plus `health.metricsURL` when the
    datasource has no auth.
  - It answers `409 install_not_configured` when `onboarding.agentChart` is not set.
- **Pricing proxy (D-026):** new token role `pricing` (migration 0011, enum in hub.yaml).
  - `GET /v1/internal/metrics-bindings`.
  - `GET|POST /v1/internal/datasources/{id}/api/v1/query[_range]`: the hub adds the datasource's
    auth, forwards only query, time, start, end, step and timeout, and passes the response through.
  - Pricing tokens reach nothing else (`TestPricingTokenScope`).

**Agent**
- The self-checks run 15 s after start, every `selfCheck.interval` (10m) and on the recheck flag.
- Results go in the next status batch and in every full batch. The log line `self-checks done`
  lists the checks that are not passing.
- Clock skew comes from the hub's `Date` header. Namespace coverage comes from the reported
  workload modes.
- The throttling health watch uses `--metrics-cluster-label`, `-label-value` and
  `-extra-matcher` (chart `health.clusterLabel`, `clusterLabelValue`, `extraMatchers`).

**Pricing:** `--hub-url` and `--hub-token-file` (chart `hub.url`, `hub.tokenSecret`). The
phase-1 DB and key-file mode is removed.

**Charts**
- **Hub:**
  - `onboarding.secretsKey`: generated once and kept across upgrades through lookup and
    `resource-policy: keep`, or taken from an existing Secret. It is mounted as
    `/etc/hub/datasource-key/key`.
  - `config.onboarding.*` holds docsBaseURL, checkInterval, agentPullInterval, minAgentVersion,
    hubURL, agentChart, webhookChart and chartVersion.
  - The dev values seed a `pricing` token.
- **Agent:**
  - The ClusterRole adds get on CRDs and get/list on MutatingWebhookConfigurations.
  - A Role lists EndpointSlices in the release namespace.
  - With `selfCheck.argocd.enabled` (default true), a Role in `argocd` gets `argocd-cm` only. It is
    created when that namespace exists (Helm lookup) or when `force: true` is set.
  - With `selfCheck.webhookDryRun` (default off), the agent may `create pods`.
  - New values: `selfCheck.*` and `health.clusterLabel/clusterLabelValue/extraMatchers`.
- **Pricing:** `hub.url`, plus a token Secret (create only in dev).

## Verification
- `make ci` on the branch: build, vet, golangci-lint (0 issues), all tests including DB and envtest,
  verify-generate and redocly. `web`: `npm run gen:api` (schema regenerated), `gen:api:check`,
  `typecheck`.
- **New tests:**
  - hub: `TestDatasourcesAPI`, `TestReadinessAndGating`, `TestInstallSnippetAPI`, `TestPricingTokenScope`
  - recommender: `TestSourceBinder`
  - agent: `TestReporterSelfChecks`, `TestHubClock`
  - health: `TestThrottlingQuerySelector`
  - pricing: `TestHubTargets`
- **Migration 0011:** checked up, re-run, down (removes pricing tokens; inserts refused) and up
  again, on postgres.
- **Live** (a local hub from this branch on a scratch database, querying ro-hub's VictoriaMetrics
  at localhost:18428): the wizard test against `local`
  - ro-spoke-1: `ok: true`, found 58 container series, 53 pods, 1 node.
  - ro-spoke-2: `ok: true`, 47 / 42 / 1.
  - The only non-pass is `metrics.history` warn (under a day of data against the 28d window).
  - This run found and fixed a real bug: the `metrics.cluster-label` query had two selectors.
- **Live agent (ro-spoke-1):** 16 self-checks. Every blocking check passes: RBAC for all
  capabilities, version, CRDs. The only non-pass is `nodes.in-place-memory=warn` (info).

## Not done / open
- **AutomationReady for ro-spoke-1:** needs the hub built from this branch, because the deployed
  hub (main-9325962) ignores the agent's checks. Once it runs this branch:
  ```
  curl -s -H "Authorization: Bearer $UI_TOKEN" localhost:18080/v1/clusters/ro-spoke-1/readiness | jq '.state, [.checks[] | select(.status!="pass")]'
  ```
- **KSM scaled to 0 on ro-spoke-2:** scaling the deployment was denied by the session's permission
  classifier (shared cluster mutation), so this was not run. After a hub deploy it needs someone
  with permission to scale it:
  ```
  kubectl --context k3d-ro-spoke-2 -n monitoring scale deploy/kube-state-metrics --replicas=0
  ```
  Then `POST /v1/clusters/ro-spoke-2/readiness/recheck`. After 5 minutes `metrics.freshness`
  fails (blocking) and the state drops to AgentConnected. Scale back to 1 afterwards.
- **Web vs hub (not edited):**
  1. Datasource edit dialog: shown for `local`, but the hub answers `409 builtin`.
  2. `summary.blocking` excludes blocking checks with status `unknown`, which are counted in
     `summary.unknown`. The banner (`blocking > 0`) does not fire for them.
  3. `blockedBy` also lists blocking checks that never reported.
  4. Problem codes differ from the mocks:
     - `not_found` (the mocks use `datasource_not_found`)
     - `invalid_datasource` (the mocks use `invalid_binding`)
     - `invalid_metrics` (the mocks use `invalid_cluster`)
  5. The install endpoint can answer `409 install_not_configured`.
  6. History `to` is inclusive.
  7. Gating applies only when auto-patch is newly enabled, and not on policy revert.
- `revertPolicy` is not gated by readiness.
