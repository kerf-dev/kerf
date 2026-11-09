# Changelog

All notable changes to Kerf are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow [Semantic Versioning](https://semver.org/).
Until 1.0.0, a minor version may change the chart values or the CRDs; the notes say how to upgrade.

To release: run `make changelog VERSION=X.Y.Z` for the changes since the previous release, then move the
`Unreleased` notes under a new `## [X.Y.Z] - YYYY-MM-DD` heading and curate them (this file is written by hand; the
command only prints a draft), commit, and push the tag `vX.Y.Z` (see
[docs/release.md](docs/release.md#releasing-on-github)). The release workflow fails for a final version without its
section here. The GitHub Release lists the same section as its highlights, followed by the changes grouped by type.

## [Unreleased]

### Added

- **Dynatrace as a metrics source** ([#66](https://github.com/kerf-dev/kerf/issues/66)). A new datasource type
  `dynatrace` reads a cluster's Kubernetes metrics from a Dynatrace environment with DQL: container CPU and memory,
  requests and limits, the workload of each container, restarts and OOM kills, node allocatable and the Smartscape
  node labels (both capacity-type labels). The recommender, the readiness checks, pricing, the usage charts and
  realized savings use it. The **Test** button shows the metric mapping and names each missing metric. Readiness gives
  Dynatrace remediations; HPA coupling is reported as unknown, and recommendations say when the throttling input is
  Dynatrace's derived ratio. Queries run as one batch per cluster per recompute and back off on HTTP 429.
- **Datasource tokens from a Kubernetes Secret.** A bearer datasource can name `auth.tokenSecret` instead of storing a
  token: the hub chart mounts the Secrets listed in `onboarding.datasourceSecrets`, and a rotated Secret is picked up
  without a restart.

### Documentation

- **Use Dynatrace as the metrics source** (`docs/install/dynatrace.md`): dual-write with the OpenTelemetry Collector
  or Grafana Alloy, or the native datasource, with the mapping from Kerf's series to Dynatrace metric keys, the gaps
  and the limits.
## [0.1.2] - 2026-11-09

### Documentation

- **Search engines can find the docs.** `robots.txt` now names a sitemap for the documentation of the latest release
  (`https://getkerf.dev/docs/sitemap.xml`, written by the Pages workflow) next to the homepage sitemap; the dev build
  and older versions are not advertised. The homepage's "Read the docs" button opens the documentation site, and the
  README opens with a link to [getkerf.dev](https://getkerf.dev).

## [0.1.1] - 2026-11-08

This release fixes what a first install on EKS showed ([#59](https://github.com/kerf-dev/kerf/issues/59)): Graviton
nodes priced, installs that need no hand-made token Secrets, readiness checks that tell the truth and can be re-run,
a bootstrap admin that must change its password, and install guides that match the chart.

**Upgrading from 0.1.0.** Nothing is required, but three defaults change for an existing install, and one note:

- `pricing.vmURL` now defaults to empty: pricing reads the node series through the hub
  (`hub.config.prometheusURL`) instead of `http://victoria-metrics.monitoring.svc:8428`. A value you set keeps working.
- In `global.mode: all` the chart generates the pricing and agent tokens (Secret `<hub name>-local-tokens`; in `hub`
  mode only the pricing token) and uses them, so the Secrets you created for 0.1.0 are no longer read unless you name them: `pricing.hub.tokenSecret.name`
  for pricing (any name), and `agent.hub.tokenSecret.name` for the agent with a name other than `kerf-agent-token`
  (in `mode: all` that name means the generated token; copy your token to a Secret with another name). An
  ExternalSecret setting keeps working. The old tokens stay valid until you revoke them in *Admin → Access*. Mode
  `spoke` is unchanged: the agent still reads `kerf-agent-token`.
- A generated bootstrap admin password must be changed at first sign-in. This applies to a new database only (the
  admin is created once, on an empty one); an admin that exists, or a password you supply, is not forced.
  `hub.auth.bootstrapAdmin.generate: false` keeps the old behaviour (no admin without a password of yours).
- `web.token` was already removed in 0.1.0. If you copied it from an install guide, delete it from your values; the
  guides no longer show it.

### Added

- **Graviton prices in the default static price table.** The price file baked into the pricing image now prices the
  `c7g`, `c8g`, `m7g`, `m8g`, `r7g`, `r8g` and `t4g` families (up to `4xlarge`; `t4g` up to `2xlarge`) in `eu-west-1`
  and `us-east-1`, on-demand and spot. Before, a cluster on Graviton nodes had every node skipped as `no-price`.
- **Re-check now.** The cluster page has a **Re-check now** button on the Overview card and the Readiness tab, and
  every readiness check shows when it ran ("checked 23 minutes ago"). A recompute (`POST /v1/admin/recompute`)
  also re-runs the hub's readiness checks, and every failing metrics check's remediation says how to re-check, so a
  fix no longer waits for the hourly check interval.
- **IRSA in the pricing guide.** `docs/pricing.md` explains how to give the pricing service its AWS role with IRSA
  (IAM policy, OIDC trust policy and the `pricing.serviceAccount.annotations` annotation), and when to choose it over
  EKS Pod Identity: organizations that block Pod Identity associations (`Cross-account pass role is not allowed`).

### Changed

- **`global.mode: all` installs need no token.** The chart generates the pricing token (modes `hub` and `all`)
  and the local agent's token (mode `all`) once, in the Secret `<hub name>-local-tokens` (kept across upgrades),
  and the hub accepts them at start-up. Pricing and the agent use them unless their `hub.tokenSecret` names
  another source: an ExternalSecret, `create`, or your own Secret (`name`; for the agent another name than
  `kerf-agent-token`). Revoking one in *Admin → Access* is final; delete the Secret and upgrade for new ones. With
  Argo CD or `helm template` (no Helm `lookup`), give pricing and the agent their own Secrets.
- **The generated bootstrap admin must change its password.** Without a password of yours, the chart generates
  one (Secret `<hub name>-bootstrap-admin`, kept) and the admin must change it at first sign-in
  (`mustChangePassword: true`). A password you supply (`existingSecret`, `externalSecret`) is not forced.
  `hub.auth.bootstrapAdmin.generate: false` keeps the old behaviour (no admin without a password).
- **`pricing.vmURL` defaults to the hub's metrics.** Empty (the new default of the `kerf` chart): pricing reads
  the node series from `hub.config.prometheusURL`, through the hub. A failed query names the URL and the setting
  that set it.
- **Install NOTES.** They mention Secrets Manager and ExternalSecrets only when the External Secrets Operator is
  configured, print the `kubectl create secret` command for every token Secret you have to create, and show
  how to read the generated admin password.

### Fixed

- **A missing token no longer fails silently.** An agent or pricing pod without its token Secret starts, logs
  `hub token missing: Secret <namespace>/<name> key <key>` at error level, and is not Ready until the token is
  there. Before, the agent ran without a token and pricing waited in `ContainerCreating`.
- **`metrics.freshness` tells absent series from stale ones.** A series without samples reads "no samples of … found
  in the last 1h" (`details`: `absent`), with a remediation for the missing scrape job or exporter, instead of
  "last sample is over 1h old".
- **`metrics.node-labels` judges each node by its newest `kube_node_labels` series.** After a kube-state-metrics
  allowlist change, the node's old series without the capacity type no longer fails it for up to an hour. Pricing
  node discovery uses a node's current series the same way. The readiness contract shows the same allowlist as the
  check (with `eks.amazonaws.com/capacityType`).
- **`metrics.scrape-interval` reports the real interval for new scrape jobs.** It is estimated over the time each
  series covers, not the whole 30-minute window: a job started 12 minutes ago at 30 s no longer reads as about 1m18s.
- **The `metrics.cadvisor` remediation keeps TLS verification on.** It scrapes cAdvisor through the API server proxy
  with the service-account CA (`ca_file`) instead of `insecure_skip_verify`, names the `nodes` and `nodes/proxy`
  RBAC the scraper needs, and shows the job for Prometheus and for Grafana Alloy.

### Documentation

- **Install guides.** The install guide for any cluster no longer tells you to create a `ui` token or set `web.token`,
  which the chart rejects; it sets `global.imageRegistry` and the web UI needs no token. A new page,
  [Install on an existing cluster](docs/install/single-cluster.md), covers one namespace with an in-cluster
  PostgreSQL, and one list of the kube-state-metrics collectors and node labels Kerf reads
  ([docs/install/any-cluster.md](docs/install/any-cluster.md#kube-state-metrics-requirements)).

## [0.1.0] - 2026-11-07

First public version.

### Added

- **Recommendations and savings (audit).** The hub reads container CPU and memory usage from Prometheus or
  VictoriaMetrics, recommends requests and limits per container, and prices the difference per node type
  (pricing service: AWS price list or a static price table).
- **Automation (`auto-patch`).** A per-cluster agent applies recommendations through `RightsizingPolicy`
  resources: in-place pod resize or a rollout, with guardrails, maintenance and freeze windows, and automatic
  rollback when a workload becomes unhealthy. A mutating webhook gives new pods the recommended resources.
- **Smarter recommendations.** HPA coordination (requests and the HPA target change together), JVM heap-aware
  memory, and zero-downtime handling of single-replica workloads and StatefulSets with `OnDelete`.
- **`kerf report`.** A command-line tool that runs the recommender against an existing Prometheus, VictoriaMetrics or
  Amazon Managed Service for Prometheus, without installing the hub: workloads sorted by monthly saving as a table, CSV,
  JSON or Markdown, with the hub's profiles, confidence and risk flags. Prices come from a static table or the AWS
  Pricing API, and a kubeconfig adds HPAs and JVM settings. Signed binaries for Linux and macOS (amd64, arm64) are
  attached to each release ([docs/cli.md](docs/cli.md)).
- **Web UI and admin area.** Workloads, recommendations, savings, policies, audit log; users and tokens;
  approve, apply and roll back from the UI.
- **Cluster onboarding.** An add-cluster wizard, per-cluster metrics sources and readiness checks
  (Registered → Agent connected → Metrics OK → Ready for audit → Automation ready).
- **Sign-in and access.** Signing in is always required: visitors without a session see only the sign-in page, and the
  hub answers `401` to every `/v1` request without a session or a bearer token (bearer tokens serve automation).
  Users sign in with a local password or with **OpenID Connect**: Okta, Microsoft Entra ID, Google, Keycloak, Dex or any
  OpenID Connect provider (authorization code flow with PKCE). Failed sign-ins get one answer whether or not the user
  exists, and the rate limits count real clients (see Security). Groups map to roles
  (`hub.auth.oidc.roleMappings`, a default role or deny); the role is re-read from the provider every
  `refreshInterval`, so removing someone from a group removes the role. IdP users are created at their first sign-in, and the audit log records their IdP
  subject and e-mail. `hub.auth.local.enabled: false` keeps passwords only for one break-glass admin. The
  client secret comes from a Secret; refresh tokens are stored encrypted and no token is ever logged. Settings
  per provider: [Sign-in and access](docs/auth.md#sign-in-with-openid-connect).
- **Teams and team-scoped access.** Admin → Teams defines teams by namespace selectors (names, globs, namespace
  labels, clusters) and an owner label on the workload or its namespace (`team` by default,
  `hub.config.teams.ownerLabel`); the workload's own label wins, and workloads in no team are "Unassigned". Users
  get `viewer` or `operator` on one or more teams in Admin → Access, and identity-provider users from group
  mappings with `teams:`; `admin` stays fleet-wide. The hub filters every list, count, total, detail and write by
  the caller's teams: other teams' workloads are not found, and approving or overriding them answers
  `403 scope_forbidden`. The audit log records the team that allowed each action (`actor.teams`). Opportunities
  filters and groups by team, the Overview shows savings by team, the workload page shows its team, and the weekly
  digest has a savings-by-team section. API `/v1/admin/teams`, `team` filters and `groupBy=team` on workloads,
  facets, the savings trend and realized savings. The agent reports workload and namespace labels and reads
  namespaces (RBAC). Database migration `0021_teams`. See [Teams](docs/teams.md).
- **Realized savings.** What the applied changes actually saved since they were applied: the requests released
  by every applied change (rollbacks and later increases subtract) × the replicas that ran × the node rates of
  the time, computed hourly by the hub leader. Shown as
  "Realized since <date>" on the Overview (total and a monthly chart) and on the workload page, always apart
  from the estimated savings, which are labelled "Estimated (if applied)". A node cost trend per cluster
  draws the hourly node cost and the requested/allocatable ratio next to the released requests (correlated, not
  caused). API `GET /v1/savings/realized`, `GET /v1/workloads/{id}/savings/realized` and
  `GET /v1/savings/realized/node-cost`, metrics `kerf_hub_realized_*` and a Grafana row; the method is in
  [docs/realized-savings.md](docs/realized-savings.md). Database migration `0016_realized_savings`.
- **Node savings estimate.** The cluster page has a Capacity tab: requested vs allocatable CPU and memory now
  and after the open recommendations, an estimate of the nodes each node pool needs (first-fit-decreasing bin
  packing of pod requests, priced per instance type), and the consolidation blockers that keep nodes from
  going away (PodDisruptionBudgets at zero, `karpenter.sh/do-not-disrupt` and `safe-to-evict=false` pods,
  local storage, node pool minimums, Karpenter `WhenEmpty` and zero disruption budgets, nodes with
  `scale-down-disabled`), each naming its object with a fix hint. The agent reads nodes, Karpenter
  `NodePool`/`NodeClaim` and the Cluster Autoscaler status ConfigMap read-only (RBAC: `get`/`list` on
  `karpenter.sh` node pools and node claims, `get` on that one ConfigMap); turn it off with
  `capacity.enabled: false`. API: `GET /v1/clusters/{cluster}/capacity`. The node cost trend on the Overview
  shows the estimated hourly node cost as a dashed "Estimate" level. See
  [docs/node-savings.md](docs/node-savings.md).
- **Effective AWS prices from the Cost and Usage Report.** The pricing service can read your CUR 2.0 export
  (AWS Data Exports, Parquet in S3), through Athena or directly from S3, and price nodes at the rate you paid
  over the last 30 days, with Savings Plans and Reserved Instances amortised. Instance types without usage keep
  the list price; an unreadable report falls back to list prices with a `pricing.coverage` readiness warning.
  The workload's savings estimate and Admin → Pricing show where each rate comes from (`cur`, `list`, `spot`,
  `static`) and the period it covers. Off by default (`pricing.cur.source`); see docs/pricing.md for the IAM
  policy and the Data Exports settings. The cost series carry a `source` label.
- **GPU visibility.** Workloads that request `nvidia.com/gpu` show their GPU utilisation and memory (from the
  NVIDIA DCGM exporter), what the GPUs cost (the price of each GPU node times the workload's share of its GPUs) and a
  status: idle (95th percentile utilisation under 5 %) or under-used (under 40 %, or under 30 % of the GPU memory),
  with a direction: time-slicing or MIG, a smaller GPU type, or scale to zero. Opportunities has a GPU filter, the
  workload page a GPU panel, and the readiness check `metrics.dcgm` says exactly which DCGM series are missing.
  Visibility only: Kerf changes no GPU request. Needs database migration `0020` (the hub applies it) and, for GPU
  prices, the pricing service on a version that exports `kerf_gpu_hourly_cost_usd`. See [docs/gpu.md](docs/gpu.md).
- **Notifications.** Admin → Notifications sends the events that matter (a change applied, rolled back, failed
  or blocked, a cluster losing readiness, automation paused or resumed) to **Slack**, **Microsoft Teams**
  (Workflows webhook) or a **generic webhook** signed with HMAC-SHA256 (`X-Kerf-Signature`, a versioned JSON payload
  documented in `docs/contracts/notifications.md`), routed by rules on event type, cluster and namespace patterns.
  A weekly digest (day, time and time zone are yours) sums up estimated savings, the week's realized savings, the top opportunities, the changes
  and the clusters that need attention. Webhook URLs and secrets are encrypted at rest and never returned; the hub
  leader sends with retries and exponential backoff for an hour, a per-channel rate limit, a "Send test" button and
  the last delivery status per channel. Channels can also come from the chart (`hub.notifications`). Metrics
  `kerf_notification_deliveries_total` and `kerf_notification_last_success_timestamp_seconds`, and the alert
  `KerfNotificationDeliveryFailing`.
- **Copy as a patch.** A **Copy as…** menu on the workload page and on the Opportunities bulk bar turns the
  recommended values into a strategic merge patch, a `kubectl patch` command, a Kustomize `patches:` entry or a
  Helm values snippet, for one workload or a selection, so teams that keep their manifests in Git can make the
  change through their own pull request. Viewers can select rows and copy too. See
  [Copy a recommendation as a patch](docs/copy-as-patch.md).
- **Maintenance page and update check.** Admin → Maintenance lists the version of every component (each hub
  replica, the web UI, the pricing service, and the agent and webhook pods of every cluster) and flags
  mismatches. Once a day the hub checks the public GitHub releases of kerf-dev/kerf for a newer stable version
  and shows the release notes and `helm upgrade` commands; "Check now" runs it at once. It is on by default and
  sends nothing about the installation; turn it off with `hub.updateCheck.enabled: false`. Metric
  `kerf_update_available{current,latest}` and the alert `KerfUpdateAvailable` (severity `info`).
- **Install.** One Helm chart (`kerf`, `global.mode: hub | spoke | all`) with zone spread,
  autoscaling, External Secrets, Postgres over verified TLS, ingress, NetworkPolicies, alerts and Grafana
  dashboards. The hub runs highly available (leader election on Postgres).
- **One-command quickstart.** `hack/quickstart.sh` creates a kind or k3d cluster, installs Kerf with demo workloads and
  a generated usage history, and prints the URL and the admin password of a UI that shows priced recommendations
  (`hack/quickstart.sh down` removes everything). It uses the chart values `values-quickstart.yaml`
  (`quickstart.enabled`: a bundled dev-only PostgreSQL, VictoriaMetrics and kube-state-metrics, the demo workloads
  and their history) and `webhook.tls.selfSigned` (a webhook certificate made by the chart, no cert-manager).
  The values schema refuses both unless `dev: true`. See [docs/quickstart.md](docs/quickstart.md).
- **Release.** Multi-arch images (`linux/amd64`, `linux/arm64`) with SBOM and provenance, signed with cosign,
  and OCI Helm charts, published to GitHub Container Registry.
- **Artifact Hub.** The chart carries the metadata Artifact Hub shows (images, CRDs with examples, the changes of the
  release and the cosign signature, see [Verify the signatures](docs/release.md#verify-the-signatures)). Every
  release pushes `artifacthub-repo.yml` as the `artifacthub.io` tag of the chart repository, and CI runs `ah lint`
  (`make charts-ah-lint`).
- **Documentation site.** The documentation is published at [getkerf.dev/docs](https://getkerf.dev/docs/), built from
  `docs/` with search, a version switch (`dev`, and one version per minor release) and light and dark themes that
  match the site. Pages cover the concepts (modes, policies, apply strategies, guardrails, rollback, HPA, JVM,
  approve once, GitOps), operating Kerf (upgrade, backup, alerts and dashboards, readiness, troubleshooting),
  installing on any cluster, a comparison and an FAQ. The chart values, custom resources and hub API reference pages
  are generated from `values.schema.json` and the chart values, `api/v1alpha1` and `api/openapi/hub.yaml`
  (`make docs-generate`); CI fails when they are out of date.

### Security

- The web image now runs on `nginx:1.30-alpine-slim` (the stable nginx line) instead of `nginx:1.29-alpine`, which no
  longer receives fixes. This removes about fifty high-severity and one critical finding in OS packages that nginx does
  not need. Every image's base is pinned by digest, so Dependabot proposes the update when a base image is rebuilt.
- Updated `golang.org/x/net` to 0.62.0, which fixes a high-severity vulnerability in the Go images.
- The release workflow scans every image with Trivy and stops before publishing when it finds a critical or high
  vulnerability that has a fix.
- Sign-in rate limits per client address can no longer be dodged with a forged `X-Forwarded-For`: the web proxy
  sends the client it sees, and the hub uses the last entry. Behind an ALB or ingress controller, set
  `web.trustedProxies` so the limits count real clients.
- Updated `github.com/go-jose/go-jose/v4` to 4.1.6, which fixes a high-severity vulnerability in the JOSE and JWT
  handling used by OIDC sign-in.
