<!-- Generated from deploy/charts/kerf/values.schema.json and values.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# Chart values

One chart, `kerf`, installs every component. `global.mode` chooses what a release installs: `hub`, `spoke` or `all`. Each component's own settings sit under its name (`hub`, `web`, `pricing`, `agent`, `webhook`) and are the values of the component chart: see the pages for [hub](hub.md), [web](web.md), [pricing](pricing.md), [agent](agent.md) and [webhook](webhook.md). The chart's `values.schema.json` checks the values on `helm install` and `helm template`.

## Umbrella values

Component sections are the component charts' values; this schema checks what the umbrella needs per global.mode and rejects inline secrets unless dev: true.

| Key | Type | Default | Description |
|---|---|---|---|
| `agent` | object | see the keys below | agent: one per workload cluster (mode spoke, all) |
| `agent.clusterName` | string | `""` | empty: global.clusterName |
| `agent.health` | object | see the keys below |  |
| `agent.hub` | object | see the keys below |  |
| `agent.image` | object | see the keys below |  |
| `agent.leaderElection` | boolean | `true` |  |
| `agent.replicaCount` | integer | `2` | `caBundle: {existingConfigMap: corp-root-ca, key: ca.crt}   # hub certificate from a private CA`<br>Production default here: two replicas with leader election (one works, one waits). |
| `dev` | boolean | `false` | Development: allows inline secrets, cookieSecure false, an empty global.imageRegistry, quickstart.enabled and webhook.tls.selfSigned. |
| `global` | object | see the keys below |  |
| `global.clusterName` | string | `""` | This cluster's name as registered in the hub (required in mode spoke and all). pattern `^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)?$` |
| `global.externalSecrets` | object | see the keys below | Default SecretStore of every ExternalSecret (External Secrets Operator), e.g.<br>`secretStoreRef: {kind: ClusterSecretStore, name: aws-secrets-manager}` |
| `global.externalSecrets.secretStoreRef` | object | `{}` |  |
| `global.imagePullSecrets` | array | `[]` | Pull secrets for every pod (names or {name: ...}); EKS nodes pull from ECR with their role. |
| `global.imageRegistry` | string | `""` | Registry host prefixed to every image repository (required unless dev: true). |
| `global.mode` | string | none (required) | hub = hub, web, pricing; spoke = agent, webhook, CRDs; all = everything. one of `hub`, `spoke`, `all` |
| `hub` | object | see the keys below | hub: API, recommender, readiness (mode hub, all) Object names: see templates/_names.tpl (release kerf -> hub, web, pricing, webhook, kerf-agent; another release R -> R-hub, R-web, ...). |
| `hub.auth` | object | see the keys below | Initial admin, created on an empty database. Production: from Secrets Manager<br>`auth.bootstrapAdmin.externalSecret: {enabled: true, remoteRef: {key: kerf/hub/bootstrap-admin, property: password}}` |
| `hub.database` | object | see the keys below | The database. Production: RDS through the External Secrets Operator (verify-full):<br>`database:`<br>`host: ro-hub.xxxxxxxx.eu-west-1.rds.amazonaws.com`<br>`caBundle: {existingConfigMap: rds-ca-bundle, key: global-bundle.pem}`<br>`externalSecret:`<br>`enabled: true`<br>`source: rds          # the RDS-managed secret (JSON username/password)`<br>`remoteRef: {key: "kerf/hub/db"}`<br>or your own Secret: database.existingSecret (key DATABASE_URL). A direct connection or a session-mode pooler only: the leader lock needs a session (no RDS Proxy, no transaction pooling). |
| `hub.image` | object | see the keys below | `enabled: true            # override global.mode for this component` |
| `pricing` | object | see the keys below | pricing: node prices and cost series (mode hub, all) |
| `pricing.clusterName` | string | `""` | empty: global.clusterName |
| `pricing.hub` | object | see the keys below |  |
| `pricing.image` | object | see the keys below |  |
| `pricing.provider` | string | `static` | static (the price file in the image) or aws (Pricing API + spot prices; IAM through EKS Pod Identity for ServiceAccount <pricing name>: docs/install/eks.md). |
| `quickstart` | object | see the keys below | The laptop quickstart (bundled PostgreSQL, VictoriaMetrics, demo workloads): enabled only with dev: true. |
| `quickstart.demo` | object | see the keys below | Demo workloads: each requests far more than it uses. `usage` is the generated history (tools/synth: pattern, base, diurnal, peakHour, weekend, spikesPerDay, noise, ...); the pods themselves only run a pause container. |
| `quickstart.demo.enabled` | boolean | `true` |  |
| `quickstart.demo.historyDays` | integer | `30` | At least the 28 days of the production profiles' window, so the readiness check for the history passes; at most 35. |
| `quickstart.demo.image` | string | `registry.k8s.io/pause:3.10` |  |
| `quickstart.demo.namespace` | string | `kerf-demo` |  |
| `quickstart.demo.workloads` | array | see values.yaml |  |
| `quickstart.enabled` | boolean | `false` |  |
| `quickstart.metrics` | object | see the keys below |  |
| `quickstart.metrics.image` | string | `victoriametrics/victoria-metrics:v1.155.0` |  |
| `quickstart.metrics.ksmImage` | string | `registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.20.0` |  |
| `quickstart.metrics.ksmResources` | object | see the keys below |  |
| `quickstart.metrics.ksmResources.limits` | object | see the keys below |  |
| `quickstart.metrics.ksmResources.limits.memory` | string | `192Mi` |  |
| `quickstart.metrics.ksmResources.requests` | object | see the keys below |  |
| `quickstart.metrics.ksmResources.requests.cpu` | string | `10m` |  |
| `quickstart.metrics.ksmResources.requests.memory` | string | `48Mi` |  |
| `quickstart.metrics.resources` | object | see the keys below |  |
| `quickstart.metrics.resources.limits` | object | see the keys below |  |
| `quickstart.metrics.resources.limits.memory` | string | `1Gi` |  |
| `quickstart.metrics.resources.requests` | object | see the keys below |  |
| `quickstart.metrics.resources.requests.cpu` | string | `50m` |  |
| `quickstart.metrics.resources.requests.memory` | string | `256Mi` |  |
| `quickstart.metrics.retention` | string | `35d` |  |
| `quickstart.metrics.scrapeInterval` | string | `30s` |  |
| `quickstart.metrics.storage` | string | `5Gi` |  |
| `quickstart.node` | object | see the keys below | kind and k3d nodes carry no cloud labels: kube_node_labels is relabelled with this node type, which the static price table prices, so the demo shows dollar amounts. Not a statement about your hardware. |
| `quickstart.node.instanceType` | string | `m6i.xlarge` |  |
| `quickstart.node.region` | string | `eu-west-1` |  |
| `quickstart.node.zone` | string | `eu-west-1a` |  |
| `quickstart.postgres` | object | see the keys below |  |
| `quickstart.postgres.image` | string | `postgres:18.6-alpine3.23` |  |
| `quickstart.postgres.resources` | object | see the keys below |  |
| `quickstart.postgres.resources.limits` | object | see the keys below |  |
| `quickstart.postgres.resources.limits.memory` | string | `512Mi` |  |
| `quickstart.postgres.resources.requests` | object | see the keys below |  |
| `quickstart.postgres.resources.requests.cpu` | string | `50m` |  |
| `quickstart.postgres.resources.requests.memory` | string | `128Mi` |  |
| `quickstart.postgres.storage` | string | `2Gi` |  |
| `web` | object | see the keys below | web: the UI (nginx, non-root on 8080) (mode hub, all) |
| `web.hub` | object | see the keys below |  |
| `web.image` | object | see the keys below |  |
| `webhook` | object | see the keys below | webhook: mutating admission webhook (mode spoke, all). Needs cert-manager. The control plane must reach the nodes on containerPort (9443): docs/install/eks.md, security groups. |
| `webhook.clusterName` | string | `""` | empty: global.clusterName |
| `webhook.image` | object | see the keys below |  |

## Rules the schema enforces

- mode spoke and all: global.clusterName is required.
- mode spoke: agent.hub.url is required (unless agent.enabled is false).
- mode hub and all: the hub needs a database Secret (hub.database.existingSecret or hub.database.externalSecret.enabled; hub.database.url only with dev: true).
- Production (dev is not true): no inline secrets, secure cookies, a registry.
