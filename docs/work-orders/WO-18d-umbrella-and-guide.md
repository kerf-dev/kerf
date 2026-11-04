# WO-18d One chart (`resource-optimizer`) and the EKS install guide

- **Phase / wave:** 5 / 2 (after WO-18a/b/c, all merged on main)
- **Owns:**
  - `deploy/charts/resource-optimizer/**` (new)
  - `deploy/examples/**` (new)
  - `docs/install/**` (new)
  - in `make/charts.mk` and `make/release.mk`: only the lines that add the umbrella (lint, diff, `RELEASE_CHARTS` default)
  - in `deploy/charts/{hub,web,pricing,agent,webhook}/templates`: only the enable gate described below (shared `_common.tpl` plus one `if` per template file)
- **Does not own:** app code, the component charts' features, or `values-dev.yaml`. Dev deploys (`make deploy-*`) and e2e keep using the component charts unchanged.
- **Read first:**
  - `DECISIONS.md` D-028; `docs/work-orders/WO-18-eks-packaging.md` (audit and decisions)
  - `docs/handover/WO-18a-handover.md` (hub HA: session-mode Postgres, `POD_NAME`, grace period)
  - `docs/handover/WO-18b-handover.md` (every value name)
  - `docs/handover/WO-18c-handover.md` (release and images)
  - `docs/release.md`
  - the agent and hub chart READMEs, and `docs/plans/cluster-onboarding.md` (wizard, readiness)
- **Changed after the 18b handover was written:** the hub chart now defaults to `replicaCount: 2`, RollingUpdate and `terminationGracePeriodSeconds: 30`, with `POD_NAME` set. The web chart defaults to `containerPort: 8080` and runs non-root with a read-only root filesystem.

## Deliverables

### 1. Umbrella chart `deploy/charts/resource-optimizer`
- Dependencies: the 5 component charts (`file://../<name>`), aliases `hub`, `web`, `pricing`, `agent`, `webhook`.
- **`mode: hub | spoke | all`** (required, no default) selects the components: hub = hub + web + pricing; spoke = agent + webhook; all = everything. `<component>.enabled: true|false` overrides one component.
  - Helm evaluates `condition:` before any template and can't derive it from `mode`. So gate inside the component charts: a shared helper in `_common.tpl` (synced by `make charts-sync`) returns enabled when `.Values.enabled` is explicitly set, otherwise whether `.Values.global.mode` (empty = standalone chart = enabled) includes this component. Wrap every template file in it.
  - Standalone installs of a component chart (dev) must render exactly as today: `make charts-diff-dev` stays empty.
  - It must work with plain `helm` and with Argo CD: no post-renderer, no `lookup`-only logic.
- **Wiring inside one release,** with no manual values needed:
  - `web.hub.upstream` → the hub Service of this release.
  - `pricing.hub.url` → the hub Service.
  - In `mode: all`, `agent.hub.url` → the hub Service.
  - In `mode: spoke`, `agent.hub.url` is required.
  - Resource names stay short and predictable (`<release>-hub`, …, or plain `hub`, … when the release is named `resource-optimizer`). Document which.
- **Images:**
  - `global.imageRegistry` is required unless `dev: true`.
  - Repositories default to `resource-optimizer/<component>` and tags to the chart `appVersion` (what `make release VERSION=` writes).
  - Digests are optional per component.
- **`clusterName`** (global, required in spoke and all) feeds the agent, webhook and pricing.
- **`values.schema.json`** for the umbrella:
  - required fields per mode
  - `mode` enum
  - inline secrets (`hub.database.url`, `*.token.value`, `*.tokenSecret.create`, `auth.bootstrapAdmin.password`, `auth.users[].password`, `devSeed.enabled`) rejected unless `dev: true`
  - `hub.auth.cookieSecure: false` rejected unless `dev: true`
- **Production defaults** where 18b left a feature off:
  - spread on (it already is)
  - hub PDB on; agent `replicaCount: 2` with `leaderElect: true`
  - webhook as is
  - NetworkPolicy, ServiceMonitor, ExternalSecrets, Ingress, PrometheusRule and dashboards stay opt-in, each with a commented example in values.yaml
- `NOTES.txt` per mode, with the next steps: the UI URL, "register this cluster in the wizard", and readiness.

### 2. Examples in `deploy/examples/eks/`
- `values-hub.yaml`: mode hub, RDS through ESO (`source: rds`) with `verify-full` and the RDS CA bundle, internal ALB for the API, ALB for the UI (scheme as a commented choice), ACM ARNs as placeholders, Pod Identity for pricing, ServiceMonitor or VMServiceScrape, PrometheusRule, dashboards, NetworkPolicy.
- `values-spoke.yaml`: mode spoke, `clusterName`, hub URL, agent token through ESO, `hub.caBundle` commented, agent with 2 replicas, NetworkPolicy, ServiceMonitor.
- `applicationset.yaml`: Argo CD ApplicationSet with a cluster generator.
  - A cluster label `resource-optimizer.oz.io/mode` (hub, spoke) and the per-cluster name and token path come from the cluster Secret's labels and annotations.
  - Includes the `ignoreDifferences` (D-006) for the patched workload fields, with `RespectIgnoreDifferences=true` and `ServerSideApply`, plus the managed-fields rule.
- `iam/pricing-policy.json` (`pricing:GetProducts`, `ec2:DescribeSpotPriceHistory`) and `iam/eso-policy.json` (read the `resource-optimizer/*` secrets), with Pod Identity association commands (`aws eks create-pod-identity-association …`) in the guide.
- `external-secrets/cluster-secret-store.yaml` for AWS Secrets Manager.
- **Secrets Manager layout:**
  - `resource-optimizer/hub/db` (RDS-managed JSON)
  - `resource-optimizer/hub/bootstrap-admin`
  - `resource-optimizer/hub/datasource-key`
  - `resource-optimizer/hub/ui-token`
  - `resource-optimizer/hub/pricing-token`
  - `resource-optimizer/clusters/<name>/agent-token`

### 3. `docs/install/eks.md`: the guide
Literal, copy-paste steps, each with a check command and its expected output:
1. **Prerequisites**, with versions:
   - EKS 1.35
   - Helm ≥ 3.17
   - AWS Load Balancer Controller
   - External Secrets Operator
   - cert-manager (the webhook's certificate)
   - Prometheus or VictoriaMetrics with kube-state-metrics and cAdvisor on every workload cluster (what the readiness checks need)
   - Argo CD (optional)
   - EKS Pod Identity Agent add-on
2. **Images and chart:** `make release REGISTRY=<ecr> VERSION=x.y.z` (refer to `docs/release.md`).
3. **AWS resources:**
   - RDS PostgreSQL 16 (≥ 12 required). A direct connection or a session-mode pooler only, never transaction pooling or RDS Proxy. Why: the leader lock.
   - Secrets Manager entries.
   - IAM and Pod Identity for pricing and ESO.
   - ACM certificates and DNS.
   - Security groups: the control plane must reach the nodes on the webhook port (9443); agents must reach the hub's internal ALB.
4. **Hub cluster:** `helm install` (or the Argo CD Application) with `values-hub.yaml`. Check: pods, `/readyz`, `ro_hub_leader` sum = 1, the UI reachable, first admin sign-in.
5. **Each workload cluster:**
   - Register it in the UI wizard, which shows the token once. Put the token into Secrets Manager.
   - Install with `values-spoke.yaml`, or label the Argo CD cluster for the ApplicationSet.
   - Check: the readiness checklist reaches *Ready (audit)* and then *Automation ready*. Every failing check names its fix; link `docs/contracts/readiness.md`.
6. **Turning on auto-patch:** policies, the readiness gate, Argo CD `ignoreDifferences`.
7. **Upgrade:**
   - CRDs upgrade with the chart.
   - Moving from the component charts or the old `crds/` chart: `--take-ownership` once.
   - The hub rolls with no downtime (RollingUpdate, leader handover).
8. **Uninstall:** CRDs and the datasource-key ExternalSecret are kept on purpose. How to remove them.
9. **Troubleshooting:**
   - webhook timeouts (security group on 9443, `failurePolicy: Ignore`)
   - no hub leader (pooler mode)
   - agent offline (token, CA, ALB)
   - stale metrics (cluster label binding)
   - ESO sync errors

Also: a short `deploy/charts/resource-optimizer/README.md` with the value reference generated from values.yaml (helm-docs style table is fine), and a link to the guide from the top `README.md` status line area (one line).

## Verification
```bash
make ci
make charts-lint            # now includes the umbrella × {mode hub, spoke, all} × {defaults+required, examples/eks values}
make charts-diff-dev        # empty: component charts render as before when standalone
helm template ro deploy/charts/resource-optimizer --set mode=hub   # fails with a schema error naming the missing required fields
helm template ro deploy/charts/resource-optimizer -f deploy/examples/eks/values-hub.yaml --set hub.database.url=postgres://x   # fails: inline secret without dev: true
RELEASE_CHARTS=resource-optimizer make release-chart REGISTRY=localhost:5005/ro-release VERSION=0.1.0-rc.1
helm template ro oci://localhost:5005/ro-release/charts/resource-optimizer --version 0.1.0-rc.1 --plain-http -f deploy/examples/eks/values-spoke.yaml | kubeconform -strict -kubernetes-version 1.35.0 -ignore-missing-schemas
```
- Render each mode and confirm the component sets: hub = hub, web, pricing; spoke = agent, webhook, CRDs; all = everything. Confirm web/pricing → hub wiring in the rendered output.
- Have someone who hasn't seen the work read the guide: run a subagent over `docs/install/eks.md` and the examples, looking for any value, secret, port or step the guide uses but doesn't define. Fix what it finds.
- No live install here: WO-18e installs it on a fresh k3d cluster by following the guide.

## Rules
Branch `wo-18d-umbrella`; work only in the worktree the orchestrator gives you. Push the branch only. Write `docs/handover/WO-18d-handover.md`, including what WO-18e must stub on k3d (ALB → ingress-nginx or traefik, Secrets Manager → ESO Fake provider, RDS → in-cluster Postgres with TLS, Pod Identity → static pricing provider).
