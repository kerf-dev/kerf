# WO-18 — Production packaging and EKS install

Status: **approved 2026-11-04** (D-028). Tracks WO-18a–18e **done 2026-11-05** (handovers in `docs/handover/`). Remaining: the real EKS install.

Goal: one Helm chart, `resource-optimizer`, that installs the hub side (`mode: hub`), a workload cluster (`mode: spoke`) or both (`mode: all`) on EKS 1.35 with production defaults. It ships with a step-by-step EKS guide, and the plan ends with a real EKS install.

## 1. Audit: what the charts and apps can do today

Legend: ✅ in place · ⚠️ partial · ❌ missing.

### Availability and scaling

| Capability | hub | web | pricing | agent | webhook |
|---|---|---|---|---|---|
| More than 1 replica | ❌ the app can't (see below) | ✅ 2 | ❌ singleton by design | ✅ via `leaderElect` | ✅ 2 |
| PodDisruptionBudget | ❌ | ❌ | – | ✅ | ✅ |
| Zone and node spread (`topologySpreadConstraints`) | ❌ | ❌ | – | ❌ | ✅ |
| HPA | ❌ | ❌ | – | – | ❌ (optional) |
| priorityClassName | ❌ | ❌ | ❌ | ❌ | ✅ |
| startupProbe | ❌ (migrations run at start) | – | – | – | – |

**The hub can't run 2 replicas today. This is an app change, not a chart change:**
- Each replica would run the recommender scheduler, the readiness checks and the sweeps, so the work would be done twice and recommendation ids would churn.
- Each replica runs the goose migrations at start, and nothing stops two from running them at the same time: there is no `WithSessionLocker`.
- "Recompute now" is queued in memory on whichever replica gets the request.

**Pricing** would publish every cost series twice with 2 replicas. Keep it at 1. An outage only leaves cost figures stale for a few minutes; recommendations and patching are unaffected.

### Scheduling and security

| Capability | hub | web | pricing | agent | webhook |
|---|---|---|---|---|---|
| nodeSelector, tolerations, affinity | ❌ | ✅ | ✅ | ✅ | ✅ |
| runAsNonRoot, readOnlyRootFilesystem | ✅ | ❌ (nginx needs writable temp: use emptyDir) | ✅ | ✅ | ✅ |
| Own ServiceAccount (needed for IRSA or Pod Identity) | ❌ | ⚠️ | ✅ | ✅ | ✅ |
| `automountServiceAccountToken: false` where no API access is needed | ❌ | ❌ | – | – | – |
| imagePullSecrets, global registry, image digest | ❌ | ❌ | ❌ | ❌ | ❌ |
| podLabels, podAnnotations, extraEnv, extraVolumes | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ |
| NetworkPolicy | ❌ | ❌ | ❌ | ❌ | ❌ |

### Secrets

Every secret can already come from an existing Kubernetes Secret: the DB URL, the first admin's password, the datasource encryption key, the web `ui` token, the agent's hub token and the pricing token. What's missing:
- **Secrets Manager integration:** no ExternalSecret (External Secrets Operator) templates that build those Secrets from AWS Secrets Manager.
- **RDS:** nothing turns an RDS-managed master secret (JSON with username and password) into the hub's DB URL; nothing sets `sslmode=verify-full` with the RDS CA bundle; there are no pool settings.
- **No guard against dev settings in production:** `create: true` and inline token values are accepted with any values file. There is no `values.schema.json`.

### Exposure and networking

- **Ingress:** ❌ for both the hub API and the web UI.
  - Agents in other clusters need to reach the hub, and so do users' browsers.
  - Needed: Ingress templates for the AWS Load Balancer Controller (ALB, ACM certificate, internal or internet-facing) and a generic `className` for nginx or traefik.
- **Hub TLS for agents and pricing:** ❌ there's no setting for a custom CA. An ACM public certificate works with the system roots; a private CA needs a `caBundle` setting.
- **Webhook port:** it listens on 9443. On EKS, node security groups must allow the control plane to reach the nodes on 9443. EKS's own cluster security group allows it, but custom node security groups often don't. The port needs to be configurable and the guide must cover it.

### Observability

- **Scraping:** today it relies only on `prometheus.io/*` annotations. Missing: optional `ServiceMonitor` (Prometheus Operator) and `VMServiceScrape` (VictoriaMetrics operator).
- **Alerts:** ❌ no PrometheusRule (or `VMRule`) for: agent offline, cluster readiness blocking, hub recommender failing, webhook errors, pricing stale.
- **Grafana dashboards:** the 4 dashboards aren't packaged. Missing: ConfigMaps with the Grafana sidecar label.

### Build and release

- **Single-architecture images:** they're built for the build machine's architecture (arm64 on this Mac), so they won't run on x86 EKS nodes. Missing: multi-arch builds (amd64 + arm64, which also covers Graviton) with `buildx`.
- **Nothing is published:** images exist only in the local registry and no chart is published. Needed: images and the chart pushed to ECR (OCI), with versioned tags.
- **CRD upgrades:** Helm installs `crds/` once and never upgrades it. Argo CD does upgrade it.
  - Fix: ship the CRDs as templates with `helm.sh/resource-policy: keep`, so plain Helm upgrades them too and an uninstall never deletes them.

## 2. Decisions proposed (to be logged as D-028)

| Topic | Proposal |
|---|---|
| Chart layout | Umbrella chart `deploy/charts/resource-optimizer`, `mode: hub \| spoke \| all`. The 5 component charts become its subcharts, so dev `make deploy-*` and e2e stay unchanged. |
| Secrets | External Secrets Operator with AWS Secrets Manager, optional per secret. A plain `existingSecret` keeps working. Inline secrets are rejected by the schema unless `dev: true`. |
| Database | RDS PostgreSQL 16 (or Aurora PG), `sslmode=verify-full`. Credentials come from the RDS-managed secret through ESO. |
| Exposure | AWS Load Balancer Controller: the hub API on an **internal** ALB (agents, pricing), the web UI on an ALB whose scheme you choose. TLS with ACM. |
| Agent → hub authentication | HTTPS plus the per-cluster bearer token (as today). mTLS moves to a later WO, because it needs a CA and client-certificate rotation per cluster. |
| AWS access for pricing | EKS Pod Identity (association documented), with an IRSA annotation as the fallback. One IAM policy: `pricing:GetProducts` and `ec2:DescribeSpotPriceHistory`. |
| Hub HA | 2 replicas. A Postgres advisory-lock leader runs the background work; every replica serves the API. Migrations run under goose's Postgres session locker. "Recompute now" is stored in the DB. |
| GitOps | An Argo CD ApplicationSet with a cluster generator. A cluster label picks `mode`; each cluster's values file holds only its name, the hub URL and the token's secret path. |

## 3. Tracks

| Track | Owns | Scope | Parallel? |
|---|---|---|---|
| **WO-18a Hub HA** | `internal/hub/**`, `cmd/hub/**`, `db/migrations/0012_*` (assigned) | Advisory-lock leader for the recommender, readiness, sweeps and pruning; goose session locker; recompute requests stored in the DB; `/readyz` unaffected by leadership; metric `ro_hub_leader`. Tests: two hubs on one DB do each run once, and leadership fails over in under 30 s. | yes |
| **WO-18b Chart hardening** | `deploy/charts/{hub,web,pricing,agent,webhook}/**` | Every ❌ and ⚠️ in section 1, per component: PDB, spread, HPA, priorityClass, scheduling, securityContext, ServiceAccount, Ingress, NetworkPolicy, ServiceMonitor and VMServiceScrape, PrometheusRule, ExternalSecret, caBundle, webhook port, CRDs as templates, extras. Defaults stay dev-compatible. | yes |
| **WO-18c Build and release** | `make/release.mk`, `cmd/*/Dockerfile`, `web/Dockerfile` | Multi-arch `buildx` (amd64 + arm64), `REGISTRY=` and `VERSION=` parameters, `make release` pushes the images and the OCI chart; SBOM; digests written into the chart. | yes |
| **WO-18d Umbrella and guide** | `deploy/charts/resource-optimizer/**`, `deploy/examples/**`, `docs/install/**` | The umbrella chart, `values.schema.json`, `values-hub-eks.yaml` and `values-spoke-eks.yaml` examples, the ApplicationSet example, the IAM policy JSON, the Secrets Manager layout. Writes `docs/install/eks.md`: prerequisites → hub cluster → each workload cluster → Argo CD → readiness checklist → upgrade and uninstall. | after 18b |
| **WO-18e Staging verification** | `test/install/**` | A fresh k3d cluster `ro-staging` (3 nodes, zone labels) with `mode: all`. Uses ESO with its Fake provider standing in for Secrets Manager, ingress-nginx standing in for the ALB, and its own Postgres. Install from the published OCI chart by following the guide literally, then onboard and reach *Automation ready*. Kill the hub leader and check failover. Also `helm template` + kubeconform for the EKS values. | after 18a, 18c, 18d |

Then the **EKS install** (orchestrator plus you), following the guide on a real cluster.

## 4. What the EKS step needs from you

None of this is needed until the EKS step; the build runs on k3d.
- An AWS account and region, and the EKS cluster or clusters (the hub cluster, plus one workload cluster to start).
- An ECR repository prefix for the images and the chart; access to push to it.
- RDS PostgreSQL (or permission for me to describe what to create), and its secret in Secrets Manager.
- An ACM certificate and DNS names for the hub API and the web UI. Should the UI be internal or internet-facing?
- Whether the AWS Load Balancer Controller, External Secrets Operator, cert-manager and a Prometheus or VictoriaMetrics operator are already installed. Which of those do you use?

## 5. Verification (done means)

- `make ci`, `make web-verify` and `make e2e` stay green on the dev fleet.
- WO-18a: the two-hub tests pass, and on k3d a killed leader's work resumes on the other replica within 30 s.
- `helm lint` and `helm template` + kubeconform (Kubernetes 1.35 schemas) pass for `mode: hub`, `spoke` and `all`, with the dev and EKS example values. The schema rejects inline secrets without `dev: true`.
- WO-18e: following `docs/install/eks.md` on `ro-staging` reaches *Automation ready*, with no step that isn't in the guide.
- The images run on both amd64 and arm64 (`docker manifest inspect` lists both).
