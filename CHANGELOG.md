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
- **Install.** One Helm chart (`kerf`, `global.mode: hub | spoke | all`) with zone spread,
  autoscaling, External Secrets, Postgres over verified TLS, ingress, NetworkPolicies, alerts and Grafana
  dashboards. The hub runs highly available (leader election on Postgres).
- **Release.** Multi-arch images (`linux/amd64`, `linux/arm64`) with SBOM and provenance, signed with cosign,
  and OCI Helm charts, published to GitHub Container Registry.
- **Maintenance page and update check.** Admin → Maintenance lists the version of every component (each hub
  replica, the web UI, the pricing service, and the agent and webhook pods of every cluster) and flags
  mismatches. Once a day the hub checks the public GitHub releases of kerf-dev/kerf for a newer stable version
  and shows the release notes and `helm upgrade` commands; "Check now" runs it at once. It is on by default and
  sends nothing about the installation; turn it off with `hub.updateCheck.enabled: false`. Metric
  `kerf_update_available{current,latest}` and the alert `KerfUpdateAvailable` (severity `info`).
- **Realized savings.** What the applied changes actually saved since they were applied: the requests released
  by every applied change (rollbacks and later increases subtract) × the replicas that ran × the node rates of
  the time, computed hourly by the hub leader, including changes applied before the upgrade. Shown as
  "Realized since <date>" on the Overview (total and a monthly chart) and on the workload page, always apart
  from the estimated savings, which are now labelled "Estimated (if applied)". A node cost trend per cluster
  draws the hourly node cost and the requested/allocatable ratio next to the released requests (correlated, not
  caused). New API `GET /v1/savings/realized`, `GET /v1/workloads/{id}/savings/realized` and
  `GET /v1/savings/realized/node-cost`, metrics `kerf_hub_realized_*` and a Grafana row; the method is in
  [docs/realized-savings.md](docs/realized-savings.md). Database migration `0016_realized_savings`.
- **Copy as a patch.** A **Copy as…** menu on the workload page and on the Opportunities bulk bar turns the
  recommended values into a strategic merge patch, a `kubectl patch` command, a Kustomize `patches:` entry or a
  Helm values snippet, for one workload or a selection, so teams that keep their manifests in Git can make the
  change through their own pull request. Viewers can select rows and copy too. See
  [Copy a recommendation as a patch](docs/copy-as-patch.md).
- **Documentation site.** The documentation is published at [getkerf.dev/docs](https://getkerf.dev/docs/), built from
  `docs/` with search, a version switch (`dev`, and one version per minor release) and light and dark themes that
  match the site. New pages cover the concepts (modes, policies, apply strategies, guardrails, rollback, HPA, JVM,
  approve once, GitOps), operating Kerf (upgrade, backup, alerts and dashboards, readiness, troubleshooting),
  installing on any cluster, a comparison and an FAQ. The chart values, custom resources and hub API reference pages
  are generated from `values.schema.json` and the chart values, `api/v1alpha1` and `api/openapi/hub.yaml`
  (`make docs-generate`); CI fails when they are out of date.

- **Sign in with OpenID Connect.** Okta, Microsoft Entra ID, Google, Keycloak, Dex or any OpenID Connect
  provider (authorization code flow with PKCE). Groups map to roles (`hub.auth.oidc.roleMappings`, a default
  role or deny); the role is re-read from the provider every `refreshInterval`, so removing someone from a
  group removes the role. IdP users are created at their first sign-in, and the audit log records their IdP
  subject and e-mail. `hub.auth.local.enabled: false` keeps passwords only for one break-glass admin. The
  client secret comes from a Secret; refresh tokens are stored encrypted and no token is ever logged. Settings
  per provider: [Sign-in and access](docs/auth.md#sign-in-with-openid-connect).

### Changed

- **Breaking: signing in is always required.** Visitors without a session see only the sign-in page, and the hub
  answers `401` to every `/v1` request without a session or a bearer token, except sign-in. The UI checks the
  session before it shows anything, returns to the page you asked for after sign-in, and brings you back to
  sign-in with a notice when a session expires or is revoked. Bearer tokens for automation are unchanged. Give
  everyone who used the UI without signing in a user (*Admin → Access → Users*); see
  [Sign-in and access](docs/auth.md#upgrading-from-anonymous-read-access).
- Failed sign-ins get one answer whether or not the user exists, also for a locked user, and one message for
  every rate limit. Sign-out always clears the cookie, also after the session ended.

### Removed

- **Breaking:** the web chart's `auth.anonymousRead` and `token.*` (`web.auth.anonymousRead`, `web.token.*` in
  the kerf chart), with the token Secret and ExternalSecret. The chart fails with an upgrade message while they
  are set. Revoke the old `ui` token unless a script uses it.

### Fixed

- After signing in, the browser tab kept the title "Sign in · Kerf" on every page.
- Signing in through the web UI kept users read-only: the web proxy looked for the old session cookie name
  and never forwarded `kerf_session`, so every request still went out with the read-only UI token.

### Security

- Sign-in rate limits per client address can no longer be dodged with a forged `X-Forwarded-For`: the web proxy
  sends the client it sees, and the hub uses the last entry. Behind an ALB or ingress controller, set
  `web.trustedProxies` so the limits count real clients.
