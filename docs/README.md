# Kerf documentation

Kerf is open-source, self-hosted Kubernetes rightsizing: it recommends CPU and memory requests and limits from
usage history, prices the savings, and applies changes per policy with guardrails and automatic rollback. Start
with the [README](../README.md) for the overview; this page lists everything else.

## Use Kerf

| Document | What it covers |
|---|---|
| [Install on Amazon EKS](install/eks.md) | Prerequisites, AWS resources, the hub and workload clusters, readiness, turning on `auto-patch`, Argo CD, upgrade, uninstall, troubleshooting |
| [Example values](../deploy/examples/eks) | `values-hub.yaml`, `values-spoke.yaml`, IAM policies, External Secrets, an Argo CD ApplicationSet |
| [Helm chart reference](../deploy/charts/kerf/README.md) | `global.mode`, the umbrella values, opt-in features |
| [Component charts](../deploy/charts) | Values of `hub`, `web`, `pricing`, `agent`, `webhook` ([hub](../deploy/charts/hub/README.md), [agent](../deploy/charts/agent/README.md)) |
| [Grafana dashboards](../dashboards/README.md) | Overview, opportunities, automation and workload dashboards |
| [Changelog](../CHANGELOG.md) | What changed in each release and how to upgrade |
| [Security policy](../SECURITY.md) | How to report a vulnerability |

## Concepts and contracts

The interfaces between components. Changes to them need a design note (see [CONTRIBUTING.md](../CONTRIBUTING.md)).

| Document | What it covers |
|---|---|
| [CRDs](../api/v1alpha1) | `RightsizingPolicy` (modes, strategy, windows, guardrails, rollback) and `RightsizingRecommendation`, API group `kerf.getkerf.dev` |
| [Hub API](../api/openapi/hub.yaml) | The HTTP API used by the agents, the web UI and automation (OpenAPI 3) |
| [Metrics](contracts/metrics.md) | The `kerf_*` metrics and the PromQL the recommender runs |
| [Events](contracts/events.md) | The audit trail: what the agent decided and did, as sent to the hub |
| [Admin area](contracts/admin.md) | Users, roles, tokens, policies and settings through the API and UI |
| [Readiness](contracts/readiness.md) | Metrics sources (datasources) and the per-cluster readiness checks |
| [Database schema](../db/migrations) | PostgreSQL migrations of the hub |

## Develop Kerf

| Document | What it covers |
|---|---|
| [CONTRIBUTING.md](../CONTRIBUTING.md) | Local setup, checks, pull requests |
| [Local environment](../dev/README.md) | Three k3d clusters with metrics, Argo CD and sample workloads |
| [Web UI](../web/README.md) | The React app, the mock API, screenshots |
| [End-to-end suite](../test/e2e/README.md) | The scenarios and how to run them |
| [Synthetic metrics](../tools/synth/README.md) | Long usage histories for testing the recommender |
| [Releasing](release.md) | The release workflow, images, charts, signatures |

## Design

| Document | What it covers |
|---|---|
| [Architecture](architecture.md) | How the hub, agent, webhook and pricing fit together, and the main design choices |
| [Roadmap](../README.md#roadmap) | What comes next |
| [Brand guide](branding/brand.md) | Name, positioning, voice, logo, colours; the [comparison sources](branding/comparison.md) |
| [Screenshots](screenshots) | Every UI page in light, dark and tablet layouts (`make web-screenshots`) |
