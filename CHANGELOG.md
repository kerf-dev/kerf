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

### Fixed

- Signing in through the web UI kept users read-only: the web proxy looked for the old session cookie name
  and never forwarded `kerf_session`, so every request still went out with the read-only UI token.
