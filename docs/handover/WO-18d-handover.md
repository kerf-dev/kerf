# WO-18d handover: one chart (`resource-optimizer`) and the EKS install guide

Branch `wo-18d-umbrella`, cut from main eca7475. D-028. Nothing was deployed (per the work-order);
WO-18e installs the chart on a fresh k3d cluster by following `docs/install/eks.md`.

## What changed

| Path | What |
|---|---|
| `deploy/charts/resource-optimizer/` (new) | The umbrella chart: `Chart.yaml` (5 dependencies, `file://../<name>`, aliases = names, no `condition:`), `charts/<name>` → `../../<name>` **symlinks**, `values.yaml`, `values.schema.json`, `templates/_names.tpl`, `templates/NOTES.txt`, `README.md` (value reference), `.helmignore`, `umbrella_test.go` |
| `deploy/charts/hub/templates/_common.tpl` (+ the 4 synced copies) | Appended: `<c>.c.enabled` (the gate), `<c>.c.umbrellaName` (naming rule), `<c>.c.umbrellaWiring` (derived values). Nothing else changed. |
| `deploy/charts/{hub,web,pricing,agent,webhook}/templates/*.yaml` | Each file wrapped in `{{- if include "<c>.c.enabled" . -}}` … `{{ end -}}` (byte-identical output when standalone). |
| `deploy/examples/eks/` (new) | `values-hub.yaml`, `values-spoke.yaml`, `applicationset.yaml`, `argocd-ecr-repo.yaml`, `external-secrets/cluster-secret-store.yaml`, `iam/{pricing-policy,eso-policy,pod-identity-trust,argocd-ecr-pull-policy}.json` |
| `docs/install/eks.md` (new) | The guide: variables, prerequisites, release, RDS, Secrets Manager, IAM/Pod Identity, ACM/DNS, security groups (incl. 9443), hub install, first sign-in and tokens, spoke install, readiness, auto-patch, Argo CD, upgrade (incl. `--take-ownership` migration), uninstall, troubleshooting. |
| `make/charts.mk` | `charts-lint` now also runs `charts-lint-umbrella`; `charts-diff-dev` also diffs the umbrella (mode all, dev) once it exists on the base branch. |
| `make/release.mk` | `RELEASE_CHARTS` default = `resource-optimizer hub pricing agent webhook web`; a chart with a `charts/` directory is packaged from a staged copy with the links resolved and every subchart's `version`/`appVersion` set to `VERSION`. |
| `README.md` | One line linking the guide. |

## Design decisions (and one deviation from the work-order)

- **`global.mode`, not top-level `mode`.** Helm passes only `global.*` to subcharts, values can't be derived
  from other values, and the parent's templates render *after* the subcharts' (Helm sorts by path depth). So a
  top-level `mode` can never reach the component gate the work-order specifies (`keyed on global.mode`). The
  user-facing switch is `global.mode`. The schema rejects a top-level `mode` with
  `at '/mode': value must be '(not used: set global.mode instead of mode)'`. **The work-order's verification
  line `helm template ro … --set mode=hub` therefore fails with that message plus the missing
  `global.mode`**; the equivalent check is `--set global.mode=hub` (output below).
- **The gate** (`<c>.c.enabled`): `.Values.enabled` when set (`<component>.enabled` in the umbrella), else
  whether `global.mode` includes the chart (`hub`: hub, web, pricing; `spoke`: agent, webhook; `all`), else (no
  mode = standalone) enabled. Uses `.Chart.Name` (aliases equal names). No `lookup`, no post-renderer: works with
  plain helm and Argo CD. `umbrella_test.go` fails if a component template lacks the gate.
- **Wiring** happens inside the gate (it runs first in every template file): with `global.mode` set it `set`s
  empty values on the subchart's `.Values`: `clusterName` ← `global.clusterName`; web `hub.upstream` and
  pricing `hub.url` ← `<hub>.<ns>.svc.cluster.local:8080`; pricing `hub.tokenSecret.name` ← `<pricing>-hub-token`;
  agent `hub.url` ← the hub Service in mode `all`, `fail` in mode `spoke` (the schema catches it first). The
  hub port is assumed to be 8080 (the hub chart default).
- **Names**: the component charts name objects after the release, which collides in one release. The umbrella
  redefines `<c>.fullname` in `templates/_names.tpl` (a parent's `define` wins over a subchart's: same template
  namespace, parsed after). Release `resource-optimizer` → `hub`, `web`, `pricing`, `webhook`,
  `resource-optimizer-agent` (= the dev release names, so the `--take-ownership` migration keeps object names);
  another release `R` → `R-hub`, …, `R-agent`. The agent always keeps the prefix (cluster-scoped RBAC).
  `<c>.fullnameOverride` wins. The label `app.kubernetes.io/instance` becomes the umbrella release, which changes
  Deployment selectors: migrating from component releases is **uninstall then install `--take-ownership`**
  (the guide's "Moving from the component charts").
- **Images**: the umbrella sets `image.repository: resource-optimizer/<c>` and `tag: ""`; the components' existing
  `<c>.c.image` defaults the tag to the subchart's `appVersion`. `release-chart-package` stamps the subcharts'
  `appVersion` with `VERSION`, so the tag is the release version (checked: `…/agent:0.1.0-rc.1`).
- **Symlinks** in `charts/` keep in-tree renders on the current component charts (no stale archives, no
  `helm dependency build`). Helm 4 logs `level=INFO msg="found symbolic link in path"` on stderr for in-tree
  renders; packaged charts contain real copies. Don't run `helm dependency update` in the umbrella.
- **Production defaults** in the umbrella: agent `replicaCount: 2` + `leaderElection: true` (the work-order's
  `leaderElect` is the chart's `leaderElection`); hub PDB and spread are already chart defaults (hub 2 replicas);
  agent `health.metricsURL: ""` (the dev default `victoria-metrics.hub.svc` is meaningless on EKS). Everything
  needing an add-on stays opt-in, with commented examples.
- **Argo CD**: `applicationset.yaml` uses a cluster generator (label `resource-optimizer.oz.io/mode`,
  annotations for cluster name, token key, VPC CIDR), multi-source (chart from ECR + values from Git),
  `ServerSideApply` + `RespectIgnoreDifferences`, `ignoreDifferences` for `managedFieldsManagers: [resource-optimizer]`,
  the webhook `caBundle` and the generated datasource key, `preserveResourcesOnDeletion` and no resources
  finalizer (Argo CD ignores `helm.sh/resource-policy: keep`; the CRDs hold every policy).

## For WO-18e: what to stub on k3d

Follow `docs/install/eks.md` literally where possible; replace the AWS pieces like this:

| Guide step | AWS piece | k3d stand-in |
|---|---|---|
| 0, 3.1, 3.6 | EKS, VPC, security groups, `aws eks update-kubeconfig` | A fresh k3d cluster on network `ro-dev` with `--registry-use k3d-ro-registry:5005`. Contexts `k3d-<name>`. The 9443 rule is a no-op (k3s reaches pods directly). Skip `aws` network lookups; set `HUB_VPC_CIDR`/`SPOKE_VPC_CIDR` to the k3d pod CIDR (`10.42.0.0/16`) so the NetworkPolicy `ipBlock`s admit traefik. |
| 1 | Pod Identity agent, LB controller, VPC CNI NetworkPolicy | Not needed. k3s enforces NetworkPolicies itself (kube-router). Install ESO (≥ 0.17), cert-manager, and the Prometheus Operator CRDs (or set `serviceMonitor`/`prometheusRule`/`dashboards` to `enabled: false`), plus a Prometheus/VictoriaMetrics with kube-state-metrics (node label allowlist) and cAdvisor. |
| 2 | ECR, `make release` | `make release REGISTRY=localhost:5005/resource-optimizer VERSION=0.1.0-rc.N` (then `global.imageRegistry: localhost:5005` and the default repositories match), or keep `localhost:5005/ro-release` and set `<c>.image.repository: ro-release/<c>`. Chart: `oci://localhost:5005/…/charts/resource-optimizer` with `--plain-http`. |
| 3.2, 4.2 | RDS PostgreSQL 16 with verify-full | In-cluster Postgres with TLS whose server certificate matches the host name, e.g. CloudNativePG (TLS on by default; CA in Secret `<cluster>-ca`, key `ca.crt`): `hub.database.host: <cluster>-rw.<ns>.svc`, `hub.database.caBundle: {existingSecret: <cluster>-ca, key: ca.crt}` instead of the `rds-ca-bundle` ConfigMap. Or Postgres plus a cert-manager CA-issued certificate with that DNS name. Keep `sslmode: verify-full`. The psql setup (role `ro`, database `resource_optimizer` owned by it) applies as written. |
| 3.3, 3.4 | Secrets Manager, ESO IAM, Pod Identity | ESO **Fake** provider: a `ClusterSecretStore` named `aws-secrets-manager` with `spec.provider.fake.data: [{key: resource-optimizer/hub/db, value: '{"username":"ro","password":"…"}'}, …]` with the same keys and JSON as the guide's table (3.3). Add the `ui-token`, `pricing-token` and `clusters/<c>/agent-token` entries by patching the store when the guide creates them (4.5, 5.2), then `force-sync`. Skip IAM. |
| 3.4 | Pricing through Pod Identity | `pricing.provider: static` (the price file in the image); drop `pricing.aws`. |
| 3.5, 4.4 | ACM, Route 53, ALB | k3d's traefik (or ingress-nginx): `ingress.className: traefik`, drop the `alb.ingress.kubernetes.io/*` annotations, TLS from a cert-manager self-signed CA (ClusterIssuer) in `ingress.tls[].secretName`. Agents then need `agent.hub.caBundle` with that CA; `curl --cacert`. Resolve `HUB_HOST`/`UI_HOST` through `/etc/hosts` / k3d port mappings, and inside the clusters through CoreDNS (`NodeHosts` / a `hosts` entry) or the Service name. `cookieSecure: true` needs the HTTPS UI. |
| 7 | Argo CD with ECR credentials | Optional; skip `argocd-ecr-repo.yaml` (no ECR) and point the ApplicationSet at the local OCI registry with `--plain-http`-equivalent repo settings (`insecure: true`). |

`global.mode=all` on a single k3d cluster exercises everything at once; a hub cluster plus one spoke matches the guide.

## Verification (2026-11-05)

See the "Verification" section of the final report in the session; summarised here after the run.

## Follow-ups (not owned here)

1. **Hub configmap:** `secretsKeyFile` is set only when `onboarding.secretsKey.create` or `existingSecret` is set
   (`deploy/charts/hub/templates/configmap.yaml`), not for `externalSecret` alone. With `create: false` +
   `externalSecret` the hub would ignore the key. The examples keep `create: true` (default), so they work; the
   condition should use `hub.datasourceKeyEnabled`.
2. **Wizard snippets** (`internal/hub/install`) render the component charts (`agent`, `webhook`) with their value
   names. The guide tells users to take only the token and install the umbrella. A later WO could render umbrella
   snippets (`global.mode: spoke`, `agent.` prefixed values).
3. **Agent self-check remediation text** (`internal/agent/selfcheck/selfcheck.go:461`) uses
   `-l app.kubernetes.io/name=webhook` and `deploy/webhook`; the webhook's label is `resource-optimizer-webhook`.
4. `docs/release.md` still says the umbrella comes "after WO-18d"; its table could now list it first.
5. The wiring assumes the hub Service port 8080; a changed `hub.service.port` needs `web.hub.upstream` /
   `pricing.hub.url` set explicitly.
