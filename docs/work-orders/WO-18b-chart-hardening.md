# WO-18b Production-grade component charts

- **Phase / wave:** 5 / 1 (parallel with WO-18a and WO-18c)
- **Owns:** `deploy/charts/{hub,web,pricing,agent,webhook}/**`, `api/crds/` copies inside the charts (not `api/crds/` itself), `make/charts.mk` (new).
- **Does not own:** the umbrella chart `deploy/charts/resource-optimizer` (WO-18d), Dockerfiles and release (WO-18c), app code. If a capability needs an app change (a flag, env var or file path), stop and report it.
- **Decisions:** D-028. Audit table: `docs/work-orders/WO-18-eks-packaging.md` section 1. Every ❌ and ⚠️ there is in scope unless listed under "Out of scope".

## Ground rules
- **Dev stays byte-compatible.** `values-dev.yaml` renders the same objects as today, apart from additive defaults that change nothing at runtime, such as labels. Prove it with a rendered diff (see Verification).
- **New features are off by default** where they need an add-on (ESO, ServiceMonitor, Ingress, NetworkPolicy, PrometheusRule).
- **Production behaviour comes from values,** never from a separate template branch.
- **The same value names in every chart,** so WO-18d can map them from one umbrella:
  `replicaCount`, `image.{registry,repository,tag,digest,pullPolicy}`, `imagePullSecrets`, `podLabels`, `podAnnotations`, `extraEnv`, `extraEnvFrom`, `extraVolumes`, `extraVolumeMounts`, `resources`, `nodeSelector`, `tolerations`, `affinity`, `topologySpreadConstraints`, `priorityClassName`, `podDisruptionBudget.{enabled,minAvailable,maxUnavailable}`, `autoscaling.{enabled,minReplicas,maxReplicas,targetCPUUtilizationPercentage,targetMemoryUtilizationPercentage}`, `serviceAccount.{create,name,annotations,automountServiceAccountToken}`, `podSecurityContext`, `securityContext`, `networkPolicy.{enabled,...}`, `serviceMonitor.{enabled,kind: ServiceMonitor|VMServiceScrape,interval,labels}`, `ingress.{enabled,className,annotations,hosts,tls}`.
  - Keep the old names that already exist (e.g. pricing's `replicas`, agent's `replicas`) working as aliases.
  - Support `global.imageRegistry` and `global.imagePullSecrets`.

## Per component
- **hub:**
  - Replicas with a PDB (on when `replicaCount > 1`).
  - Zone and node spread defaults (`ScheduleAnyway`) and an HPA on CPU.
  - A startupProbe that covers migrations.
  - Its own ServiceAccount with annotations (for IRSA or Pod Identity), with `automountServiceAccountToken: false`.
  - nodeSelector, tolerations and affinity.
  - Ingress for the API, with ALB example annotations in comments: internal scheme, ACM certificate ARN, `target-type: ip`, health-check path `/readyz`.
  - ExternalSecret options for the DB URL, the bootstrap admin password and the datasource encryption key.
    - DB URL: render it from an RDS-managed secret (JSON `username`/`password`) plus `database.{host,port,name,sslmode,sslrootcert}`.
    - Mount the RDS CA bundle from a ConfigMap or Secret for `verify-full`.
  - `cookieSecure` stays true by default.
- **web:**
  - PDB, spread, HPA, Ingress.
  - runAsNonRoot and readOnlyRootFilesystem, with emptyDirs for the nginx temp, cache and run paths. Check the image runs that way: the nginx image may need the unprivileged port and user, and the Dockerfile belongs to WO-18c, so report what's needed.
  - ExternalSecret for the `ui` token.
  - Add a `containerPort` value (default 80, today's port). WO-18c moves nginx to 8080 for non-root; the orchestrator sets 8080 when both are merged, so don't hard-code it.
- **pricing:**
  - Stays a singleton: document why in values.yaml.
  - IRSA example annotation and Pod Identity notes, ExternalSecret for the hub token.
  - `hub.caBundle` (mount a CA for HTTPS to the hub).
- **agent:**
  - `replicas: 2` with `leaderElect`, plus PDB and spread as a production example (comments).
  - `priorityClassName`, `hub.caBundle`, ExternalSecret for the hub token.
  - CRDs as templates with `helm.sh/resource-policy: keep`. Keep `crds/` out of the chart; a test keeps the copies in sync with `api/crds/`.
  - `installCRDs: true` toggle.
- **webhook:**
  - Configurable `containerPort` (default 9443).
  - The same CRD handling as the agent, without fighting it when both are installed (exactly one of them renders the CRDs: a value `crds.install`, default true on the agent and false on the webhook).
- **All charts:**
  - NetworkPolicy (off by default). Allow-lists:
    - hub: ingress from web, pricing, the ingress controller and agents (CIDR or namespaceSelector values); egress to Postgres, DNS and the datasources.
    - webhook: ingress from the API server (port value).
    - agent: egress to the API server, the hub and DNS.
  - ServiceMonitor or VMServiceScrape (off by default).
  - Keep the existing `prometheus.io/*` annotations.
- **Alert rules:** in the hub chart, `prometheusRule.{enabled,kind: PrometheusRule|VMRule}` with:
  - agent offline more than 10m
  - cluster readiness blocking > 0 for 30m
  - recommender run failing or no run for 2 intervals
  - no hub leader for 5m (`max(ro_hub_leader) == 0`; that metric arrives with WO-18a)
  - webhook admission errors
  - pricing series stale for 30m

  Use metric names from `docs/contracts/metrics.md` only.
- **Grafana dashboards:** in the hub chart, `dashboards.enabled` renders `dashboards/*.json` into ConfigMaps labelled `grafana_dashboard: "1"`. Copy them in with a sync target and a sync test, like the CRDs.

## Out of scope
The umbrella chart and `values.schema.json` (WO-18d). mTLS. OIDC. Image builds.

## Verification
```bash
make ci
make charts-lint     # new: helm lint + helm template + kubeconform (Kubernetes 1.35 schemas, CRD schemas for ServiceMonitor/VMServiceScrape/ExternalSecret/PrometheusRule via the datree catalog or vendored)
make charts-diff-dev # new: helm template with values-dev.yaml on main vs this branch; the diff must be empty or additive-only labels
```
- `make charts-lint` renders every chart three ways: defaults, `values-dev.yaml`, and a new `ci/values-prod.yaml` per chart that turns every new feature on (EKS-like annotations).
- On the shared fleet: before deploying, check that no e2e is running (see CLAUDE.md). Then `make deploy-agent` and `make deploy-webhook` with dev values; both spokes stay *Automation ready*. Hub, web and pricing aren't deployed by you; show the dev render diff instead.

## Rules
Branch `wo-18b-chart-hardening`; push the branch only. Write `docs/handover/WO-18b-handover.md`, with the final value names for WO-18d.
