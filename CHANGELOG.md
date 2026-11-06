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
- **Notifications.** Admin → Notifications sends the events that matter (a change applied, rolled back, failed
  or blocked, a cluster losing readiness, automation paused or resumed) to **Slack**, **Microsoft Teams**
  (Workflows webhook) or a **generic webhook** signed with HMAC-SHA256 (`X-Kerf-Signature`, a versioned JSON payload
  documented in `docs/contracts/notifications.md`), routed by rules on event type, cluster and namespace patterns.
  A weekly digest (day, time and time zone are yours) sums up estimated savings, the top opportunities, the changes
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

- Signing in through the web UI kept users read-only: the web proxy looked for the old session cookie name
  and never forwarded `kerf_session`, so every request still went out with the read-only UI token.

### Security

- Sign-in rate limits per client address can no longer be dodged with a forged `X-Forwarded-For`: the web proxy
  sends the client it sees, and the hub uses the last entry. Behind an ALB or ingress controller, set
  `web.trustedProxies` so the limits count real clients.
