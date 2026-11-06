# Kerf Helm charts

| Chart | What it installs |
|---|---|
| [`kerf`](kerf/README.md) | The umbrella chart most installs use: `global.mode: hub \| spoke \| all` picks the components below |
| [`hub`](hub/README.md) | API, recommender, migrations, alerts and Grafana dashboards (central cluster) |
| `web` | The web UI, nginx serving static files and proxying `/api` to the hub (central cluster) |
| `pricing` | Node prices as metrics (central cluster) |
| [`agent`](agent/README.md) | The per-cluster agent and the CRDs (each workload cluster) |
| `webhook` | The mutating admission webhook (each workload cluster) |

Each chart's `values.yaml` documents its values and defaults; `ci/values-prod.yaml` switches every production
feature on (`make charts-lint` renders both).

## Value names shared by the component charts

The same names mean the same thing in every component chart that has them. In the umbrella chart they sit under
the component's key (`hub.replicaCount`, `agent.networkPolicy.enabled`, …), and `global.*` applies to all of them.

| Value | Meaning |
|---|---|
| `global.imageRegistry` | Prefixed to `image.repository`; `image.registry` wins over it |
| `global.imagePullSecrets`, `imagePullSecrets` | Names or `{name}`; the two lists are merged |
| `global.externalSecrets.secretStoreRef` | Fallback `{kind, name}` for every ExternalSecret (kind defaults to `ClusterSecretStore`) |
| `image.{registry,repository,tag,digest,pullPolicy}` | `repo:tag@digest` when `digest` is set; an empty tag means the chart's `appVersion` |
| `replicaCount` | Pod count (pricing runs one replica) |
| `podDisruptionBudget.{enabled,minAvailable,maxUnavailable}` | Rendered only when more than one pod can run; `maxUnavailable` wins when set |
| `autoscaling.{enabled,minReplicas,maxReplicas,targetCPUUtilizationPercentage,targetMemoryUtilizationPercentage,behavior}` | HPA (hub, web, webhook); the Deployment's `replicas` is omitted when on |
| `topologySpread.{enabled,whenUnsatisfiable,maxSkew}` | Default node and zone spread (`ScheduleAnyway`) when more than one pod can run |
| `topologySpreadConstraints` | An explicit list that replaces the default spread |
| `priorityClassName`, `nodeSelector`, `tolerations`, `affinity` | Pod scheduling |
| `serviceAccount.{create,name,annotations,automountServiceAccountToken}` | IRSA or Pod Identity annotations go here; `create: false` on agent or webhook needs `name` |
| `podLabels`, `podAnnotations` | Extra pod metadata |
| `extraEnv`, `extraEnvFrom`, `extraVolumes`, `extraVolumeMounts` | Extra container configuration |
| `podSecurityContext`, `securityContext`, `resources` | Pod and container settings |
| `ingress.{enabled,className,annotations,hosts,tls}` | hub and web; ALB examples in the values comments |
| `networkPolicy.enabled`, `.ingress.*`, `.egress.{enabled,ports,to}` | NetworkPolicies; DNS egress is always allowed |
| `serviceMonitor.{enabled,kind,…}` | `kind: ServiceMonitor` or `VMServiceScrape` (not web: nginx has no metrics) |
| `externalSecrets.{apiVersion,refreshInterval,secretStoreRef}` | Chart-wide External Secrets Operator settings |

Secrets that a chart can build with the External Secrets Operator each have a switch
`<secret>.externalSecret.{enabled,remoteRef{key,property,version},refreshInterval,secretStoreRef}`, exclusive with
the matching `existingSecret` or `create`:

| Chart | Value | Secret |
|---|---|---|
| hub | `database.externalSecret` (`source: rds \| url`) | The database URL; `rds` builds it from an RDS-managed secret (JSON `username`/`password`) and `database.{host,port,name,sslmode,params}` |
| hub | `auth.bootstrapAdmin.externalSecret` | The first admin's password |
| hub | `onboarding.secretsKey.externalSecret` | The datasource encryption key (kept on uninstall) |
| web | `token.externalSecret` | The web UI's hub token (nginx reads it at start: restart after a rotation) |
| pricing, agent | `hub.tokenSecret.externalSecret` | The hub token |
