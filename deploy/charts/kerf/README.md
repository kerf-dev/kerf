# Kerf (umbrella chart)

One chart for every cluster (WO-18d, D-028). `global.mode` picks what a release installs:

| `global.mode` | Components | Where |
|---|---|---|
| `hub` | hub, web, pricing | the hub cluster |
| `spoke` | agent, webhook, the CRDs | each workload cluster |
| `all` | everything | one cluster (evaluation, small setups) |

`<component>.enabled: true|false` overrides one component either way.

**Install guide:** [docs/install/eks.md](../../../docs/install/eks.md). **Examples:** [deploy/examples/eks](../../../deploy/examples/eks).

```bash
helm upgrade --install kerf oci://$REGISTRY/charts/kerf --version $VERSION \
  -n kerf --create-namespace -f values-hub.yaml
```

## How it works

- **Components** are the five component charts (`deploy/charts/{hub,web,pricing,agent,webhook}`),
  linked into `charts/` as symlinks. `helm package` (`make release`) stores real copies and sets their
  `version`/`appVersion` to the release version. Don't run `helm dependency update` here: it would
  replace the links.
- **Mode gate.** Helm evaluates a dependency's `condition:` before rendering any template, and the
  condition can't be derived from `mode`. So every component template is wrapped in
  `{{- if include "<component>.c.enabled" . -}}`, from the components' shared `_common.tpl`. The gate
  reads `.Values.enabled`, else `global.mode`. A component chart installed on its own has no
  `global.mode`, so it renders as before (`make charts-diff-dev`). That is also why the mode is
  `global.mode` and not a top-level `mode`: Helm passes only `global.*` to the components.
- **Wiring.** With `global.mode` set, the gate fills in what is left empty:
  - `web.hub.upstream`, and `pricing.hub.url` with its token Secret `<pricing>-hub-token`, point at this release's hub Service.
  - In mode `all`, `agent.hub.url` points at the hub Service too. In mode `spoke` it is required.
  - `clusterName` of agent, webhook and pricing comes from `global.clusterName`.
- **Names** (`templates/_names.tpl`):
  - Release `kerf` gives `hub`, `web`, `pricing`, `webhook` and `kerf-agent`.
    These are the dev release names, so moving from the component charts keeps every object name.
  - Any other release `R` gives `R-hub`, `R-web`, `R-pricing`, `R-webhook` and `R-agent`.
  - `<component>.fullnameOverride` wins.
- **Images.**
  - The reference is `global.imageRegistry` / `kerf/<component>` : the chart version, plus `@<digest>` when `<component>.image.digest` is set.
  - `global.imageRegistry` is required unless `dev: true`.
- **Schema** (`values.schema.json`):
  - It requires `global.mode`, plus `global.clusterName` in `spoke` and `all`, and `agent.hub.url` in `spoke`.
  - In `hub` and `all`, the hub needs a database Secret: `existingSecret` or `externalSecret`.
  - Unless `dev: true`, it rejects inline secrets (`hub.database.url`, `web.token.value`,
    `{pricing,agent}.hub.tokenSecret.create`/`.token`, `hub.auth.bootstrapAdmin.password`, `hub.devSeed.*`,
    passwords in user lists) and `hub.auth.cookieSecure: false`.

## Values

The umbrella's own values. Everything under `hub`, `web`, `pricing`, `agent` and `webhook` is that component chart's
values: see `deploy/charts/<component>/values.yaml` and the table in `docs/handover/WO-18b-handover.md`.

| Value | Default | Description |
|---|---|---|
| `global.mode` | – (required) | `hub`, `spoke` or `all` |
| `global.clusterName` | `""` | this cluster's name in the hub (DNS label); required in `spoke`/`all`; feeds agent, webhook, pricing `clusterName` |
| `global.imageRegistry` | `""` | registry host prefixed to every `image.repository` (the ECR host); required unless `dev` |
| `global.imagePullSecrets` | `[]` | pull secrets for every pod (names or `{name}`) |
| `global.externalSecrets.secretStoreRef` | `{}` | default store of every ExternalSecret, e.g. `{kind: ClusterSecretStore, name: aws-secrets-manager}` |
| `dev` | `false` | development: allows inline secrets, `cookieSecure: false`, an empty `imageRegistry` |
| `<component>.enabled` | unset | `true`/`false` overrides `global.mode` for that component |
| `<component>.fullnameOverride` | unset | name of that component's objects |
| `<component>.image.repository` | `kerf/<component>` | image repository under `global.imageRegistry` |
| `<component>.image.tag` | `""` | default: the chart's `appVersion` (the release version) |
| `<component>.image.digest` | `""` | optional `sha256:…` (from `bin/release/images.txt`) |
| `hub.database.url` | `""` | inline database URL; `dev` only (production: `database.externalSecret` or `existingSecret`) |
| `hub.auth.cookieSecure` | `true` | `false` only with `dev` |
| `web.hub.upstream` | `""` | empty: this release's hub Service |
| `web.token.value` | `""` | inline `ui` token; `dev` only (production: `token.externalSecret`) |
| `pricing.clusterName` | `""` | empty: `global.clusterName` |
| `pricing.provider` | `static` | `static` or `aws` (Pod Identity, `iam/pricing-policy.json`) |
| `pricing.hub.url` | `""` | empty: this release's hub Service |
| `pricing.hub.tokenSecret.name` | `""` | empty: `<pricing name>-hub-token` |
| `agent.clusterName` | `""` | empty: `global.clusterName` |
| `agent.hub.url` | `""` | required in `spoke` (the hub's ALB); empty in `all`: this release's hub Service |
| `agent.replicaCount` | `2` | production default here (component chart: 1) |
| `agent.leaderElection` | `true` | needed for more than one replica |
| `agent.health.metricsURL` | `""` | this cluster's Prometheus for the throttling rollback; empty disables it |
| `webhook.clusterName` | `""` | empty: `global.clusterName` |

Opt-in features (each needs its add-on, see `values.yaml` comments and the EKS examples):
`*.ingress` (AWS Load Balancer Controller), `*.networkPolicy`, `*.serviceMonitor` (Prometheus Operator
or VictoriaMetrics operator), `*.externalSecret(s)` (External Secrets Operator), `hub.prometheusRule`,
`hub.dashboards`.

## Development

```bash
make charts-lint            # includes charts-lint-umbrella: every mode x {defaults, EKS examples} through kubeconform
helm template ro deploy/charts/kerf --set global.mode=all --set dev=true \
  --set global.clusterName=dev --set hub.database.url=postgres://u:p@db/ro
```
