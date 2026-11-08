# Kerf documentation

Kerf is open-source, self-hosted Kubernetes rightsizing: it recommends CPU and memory requests and limits from
usage history, prices the savings, and applies changes per policy with guardrails and automatic rollback. Start
with the [README](../README.md) for the overview; this page lists everything else. The same pages are published,
with search and a version switch, at [getkerf.dev/docs](https://getkerf.dev/docs/).

## Use Kerf

| Document | What it covers |
|---|---|
| [Quickstart on your laptop](quickstart.md) | One command on kind or k3d: Kerf with demo workloads and recommendations in minutes, what it installed, how to remove it and how to move to a real install |
| [Install on Amazon EKS](install/eks.md) | Prerequisites, AWS resources, the hub and workload clusters, readiness, turning on `auto-patch`, Argo CD, upgrade, uninstall, troubleshooting |
| [Install on any cluster](install/any-cluster.md) | The same chart on a conformant cluster, with a static price table instead of the AWS price list |
| [Concepts](concepts/README.md) | Modes, policies and precedence, apply strategies, guardrails, rollback, HPA coordination, JVM, approve once, GitOps and Argo CD |
| [Operate](operate/README.md) | Upgrade, backup, alerts and dashboards, readiness checks and their fixes, troubleshooting |
| [Reference](reference/README.md) | Generated pages: chart values, custom resources, hub API; plus metrics and events |
| [FAQ](faq.md) and [comparison](comparison.md) | Short answers, and how Kerf differs from VPA, Goldilocks, KRR and the commercial tools |
| [Example values](../deploy/examples/eks) | `values-hub.yaml`, `values-spoke.yaml`, IAM policies, External Secrets, an Argo CD ApplicationSet |
| [The `kerf` command-line tool](cli.md) | `kerf report`: recommendations and savings from your existing Prometheus, without installing the hub; install, authentication (including Amazon Managed Service for Prometheus), prices |
| [Helm chart reference](../deploy/charts/kerf/README.md) | `global.mode`, the umbrella values, opt-in features |
| [Component charts](../deploy/charts) | Values of `hub`, `web`, `pricing`, `agent`, `webhook` ([hub](../deploy/charts/hub/README.md), [agent](../deploy/charts/agent/README.md)) |
| [Grafana dashboards](../dashboards/README.md) | Overview, opportunities, automation and workload dashboards |
| [Notifications](notifications.md) | Slack, Teams and signed webhook channels, rules, the weekly digest, retries and metrics |
| [Pricing](pricing.md) | Where node rates come from (`cur`, `list`, `spot`, `static`), effective rates from the AWS Cost and Usage Report with Savings Plans and Reserved Instances amortised, the static price file, IAM access for the pricing service with Pod Identity or IRSA, its IAM policy and Data Exports settings |
| [Realized savings](realized-savings.md) | What applied changes actually saved, kept apart from estimates; the method, with SQL and PromQL to reproduce it |
| [Sign-in and access](auth.md) | Required sign-in, users and roles, sign-in protection, bearer tokens for automation, upgrading from anonymous read access |
| [Copy a recommendation as a patch](copy-as-patch.md) | Hand a recommendation over as a strategic merge patch, a `kubectl patch` command, a Kustomize patch or Helm values, for one workload or a selection |
| [GPU visibility](gpu.md) | Idle and under-used NVIDIA GPUs: prerequisites (DCGM exporter), the method, the cost, the readiness check |
| [Sign-in and access](auth.md) | Required sign-in, users and roles, sign-in with OpenID Connect (Okta, Entra ID, Google, Keycloak, Dex), sign-in protection, bearer tokens for automation, upgrading from anonymous read access |
| [Teams](teams.md) | Teams that own workloads (namespace selectors, owner label), team-scoped viewer and operator roles, identity-provider group mappings to teams, savings by team |
| [Node savings](node-savings.md) | Capacity, the node estimate after the recommendations, consolidation blockers with Karpenter and the Cluster Autoscaler |
| [Maintenance and updates](maintenance.md) | Component versions, the check for a new release, turning it off |
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
| [Notification webhook](contracts/notifications.md) | The signed JSON payload (version 1), its headers, retries, and how to verify the signature |
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
