<!-- Generated from deploy/charts/webhook/values.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# `webhook` values (mutating webhook)

The values of the `webhook` component chart. In the `kerf` chart they sit under `webhook:`; a key listed here as `image.tag` is then `webhook.image.tag`. Nested keys appear after their parent, with the parent's default shown in full or abbreviated.

| Key | Type | Default | Description |
|---|---|---|---|
| `global` | object | see the keys below | Set by an umbrella chart; read by every kerf chart. |
| `global.imageRegistry` | string | `""` | prefixed to image.repository; image.registry wins over it |
| `global.imagePullSecrets` | array | `[]` | names or {name: ...} |
| `global.externalSecrets` | object | see the keys below |  |
| `global.externalSecrets.secretStoreRef` | object | `{}` |  |
| `crds` | object | see the keys below | The Kerf CRDs as templates (helm.sh/resource-policy: keep). Off: the agent chart renders them, and exactly one release per cluster may (Helm refuses a CRD owned by another release). Turn it on only where the webhook is installed without the agent. |
| `crds.install` | boolean | `false` |  |
| `installCRDs` | object | `null` | Deprecated alias of crds.install (wins when set). |
| `clusterName` | string | `""` | Cluster name (informational; set by Tilt / dev scripts). |
| `fullnameOverride` | string | `""` | Names default to the release name (release "webhook" -> Deployment/Service "webhook"). |
| `image` | object | see the keys below |  |
| `image.registry` | string | `""` |  |
| `image.repository` | string | `localhost:5005/kerf/webhook` |  |
| `image.tag` | string | `dev` |  |
| `image.digest` | string | `""` | sha256:..., appended as @digest |
| `image.pullPolicy` | string | `IfNotPresent` |  |
| `imagePullSecrets` | array | `[]` |  |
| `replicaCount` | integer | `2` |  |
| `replicas` | object | `null` | Deprecated alias of replicaCount (wins when set). |
| `autoscaling` | object | see the keys below | Optional HPA on CPU (replicaCount is then ignored). |
| `autoscaling.enabled` | boolean | `false` |  |
| `autoscaling.minReplicas` | integer | `2` |  |
| `autoscaling.maxReplicas` | integer | `4` |  |
| `autoscaling.targetCPUUtilizationPercentage` | integer | `75` |  |
| `autoscaling.targetMemoryUtilizationPercentage` | object | `null` |  |
| `autoscaling.behavior` | object | `{}` |  |
| `containerPort` | integer | `9443` | Port the webhook server listens on (HTTPS). On EKS the control plane must reach the nodes on this port: EKS's cluster security group allows it, custom node security groups often do not (open it from the cluster security group, or use 443 where nothing else binds it). |
| `certManager` | object | see the keys below | Serving certificate from cert-manager. By default the chart creates a namespaced self-signed Issuer; set issuerRef to use an existing (Cluster)Issuer instead. cert-manager's CA injector writes the CA into the MutatingWebhookConfiguration. |
| `certManager.issuerRef` | object | `{}` |  |
| `certManager.duration` | string | `8760h` | kind: ClusterIssuer name: my-ca |
| `certManager.renewBefore` | string | `720h` |  |
| `webhook` | object | see the keys below |  |
| `webhook.failurePolicy` | string | `Ignore` | Ignore: a webhook outage never blocks pod creation (pods start with template values and the agent resizes them afterwards). |
| `webhook.timeoutSeconds` | integer | `2` | API server timeout (1-30s; the handler only reads informer caches). |
| `webhook.excludeNamespaces` | array | `[]` | Namespaces never sent to the webhook, in addition to kube-system and the release namespace. A namespace (or pod) labelled kerf.getkerf.dev/skip-webhook=true is skipped too. |
| `logLevel` | string | `info` |  |
| `resources` | object | see the keys below |  |
| `resources.requests` | object | see the keys below |  |
| `resources.requests.cpu` | string | `50m` |  |
| `resources.requests.memory` | string | `64Mi` |  |
| `resources.limits` | object | see the keys below |  |
| `resources.limits.memory` | string | `256Mi` |  |
| `podDisruptionBudget` | object | see the keys below |  |
| `podDisruptionBudget.enabled` | boolean | `true` |  |
| `podDisruptionBudget.minAvailable` | integer | `1` |  |
| `podDisruptionBudget.maxUnavailable` | object | `null` |  |
| `topologySpread` | object | see the keys below | Spread replicas across nodes and zones (best effort: ScheduleAnyway) when more than one pod can run; topologySpreadConstraints replaces it. |
| `topologySpread.enabled` | boolean | `true` |  |
| `topologySpread.whenUnsatisfiable` | string | `ScheduleAnyway` |  |
| `topologySpreadConstraints` | array | `[]` |  |
| `priorityClassName` | string | `""` |  |
| `nodeSelector` | object | `{}` |  |
| `tolerations` | array | `[]` |  |
| `affinity` | object | `{}` |  |
| `podLabels` | object | `{}` |  |
| `podAnnotations` | object | `{}` |  |
| `extraEnv` | array | `[]` |  |
| `extraEnvFrom` | array | `[]` |  |
| `extraVolumes` | array | `[]` |  |
| `extraVolumeMounts` | array | `[]` |  |
| `serviceAccount` | object | see the keys below |  |
| `serviceAccount.create` | boolean | `true` | false: use an existing ServiceAccount `name` (the ClusterRole is still bound to it). |
| `serviceAccount.name` | string | `""` | default: the release name |
| `serviceAccount.annotations` | object | `{}` |  |
| `podSecurityContext` | object | see the keys below |  |
| `podSecurityContext.runAsNonRoot` | boolean | `true` |  |
| `podSecurityContext.runAsUser` | integer | `65532` |  |
| `podSecurityContext.seccompProfile` | object | see the keys below |  |
| `podSecurityContext.seccompProfile.type` | string | `RuntimeDefault` |  |
| `securityContext` | object | see the keys below |  |
| `securityContext.allowPrivilegeEscalation` | boolean | `false` |  |
| `securityContext.readOnlyRootFilesystem` | boolean | `true` |  |
| `securityContext.capabilities` | object | see the keys below |  |
| `securityContext.capabilities.drop` | array | `["ALL"]` |  |
| `networkPolicy` | object | see the keys below | NetworkPolicy. Ingress to the webhook port from `ingress.apiServerFrom` ([] = any source: the API server's addresses are not pod addresses; on EKS the control-plane ENIs sit in the VPC, so the VPC CIDR as ipBlock narrows it) and to the metrics port from `ingress.metricsFrom` ([] = any). Egress: DNS, TCP `egress.ports` (the API server, for the informers) and `egress.to`. |
| `networkPolicy.enabled` | boolean | `false` |  |
| `networkPolicy.ingress` | object | see the keys below |  |
| `networkPolicy.ingress.apiServerFrom` | array | `[]` |  |
| `networkPolicy.ingress.metricsFrom` | array | `[]` |  |
| `networkPolicy.egress` | object | see the keys below |  |
| `networkPolicy.egress.enabled` | boolean | `true` |  |
| `networkPolicy.egress.ports` | array | `[443, 6443]` |  |
| `networkPolicy.egress.to` | array | `[]` |  |
| `serviceMonitor` | object | see the keys below | Prometheus Operator ServiceMonitor or VictoriaMetrics VMServiceScrape for the metrics Service. |
| `serviceMonitor.enabled` | boolean | `false` |  |
| `serviceMonitor.kind` | string | `ServiceMonitor` | or VMServiceScrape |
| `serviceMonitor.namespace` | string | `""` |  |
| `serviceMonitor.interval` | string | `30s` |  |
| `serviceMonitor.scrapeTimeout` | string | `""` |  |
| `serviceMonitor.honorLabels` | boolean | `false` |  |
| `serviceMonitor.labels` | object | `{}` |  |
| `serviceMonitor.relabelings` | array | `[]` |  |
| `serviceMonitor.metricRelabelings` | array | `[]` |  |
