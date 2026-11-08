<!-- Generated from deploy/charts/agent/values.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# `agent` values (per-cluster agent)

The values of the `agent` component chart. In the `kerf` chart they sit under `agent:`; a key listed here as `image.tag` is then `agent.image.tag`. Nested keys appear after their parent, with the parent's default shown in full or abbreviated.

| Key | Type | Default | Description |
|---|---|---|---|
| `clusterName` | string | `""` | Cluster name as known to the hub (required). Tilt / dev scripts set it per cluster. |
| `global` | object | see the keys below | Set by an umbrella chart; read by every kerf chart. |
| `global.imageRegistry` | string | `""` | prefixed to image.repository; image.registry wins over it |
| `global.imagePullSecrets` | array | `[]` | names or {name: ...} |
| `global.externalSecrets` | object | see the keys below |  |
| `global.externalSecrets.secretStoreRef` | object | `{}` | fallback SecretStore for every ExternalSecret |
| `image` | object | see the keys below |  |
| `image.registry` | string | `""` |  |
| `image.repository` | string | `localhost:5005/kerf/agent` |  |
| `image.tag` | string | `dev` |  |
| `image.digest` | string | `""` | sha256:..., appended as @digest |
| `image.pullPolicy` | string | `IfNotPresent` |  |
| `imagePullSecrets` | array | `[]` |  |
| `crds` | object | see the keys below | The Kerf CRDs (copies of api/crds, `make charts-sync`), rendered as templates with helm.sh/resource-policy: keep: `helm upgrade` upgrades them and `helm uninstall` leaves them (and every RightsizingPolicy and RightsizingRecommendation) in place. Exactly one release per cluster renders them: this chart by default (the webhook chart has crds.install: false). |
| `crds.install` | boolean | `true` |  |
| `installCRDs` | object | `null` | Deprecated alias of crds.install (wins when set). |
| `hub` | object | see the keys below |  |
| `hub.url` | string | `http://hub-api.hub.svc:8080` | Hub base URL. Spokes reach the hub through a selector-less Service. |
| `hub.tokenSecret` | object | see the keys below | Bearer token for the agent role of this cluster, read from a Secret key and re-read on every request (rotation needs no restart). Without it the agent logs "hub token missing: Secret <namespace>/<name> key <key>" at error level and is not Ready. Umbrella chart, global.mode all: with name, create and externalSecret all unset, the agent uses the token the hub chart generates for its own cluster (Secret <hub name>-local-tokens, key agent). |
| `hub.tokenSecret.name` | string | `kerf-agent-token` | in mode all, this default name means "the generated token"; any other name is your Secret |
| `hub.tokenSecret.key` | string | `token` |  |
| `hub.tokenSecret.create` | boolean | `false` | create: true makes the chart create the Secret from `token` (local development only). |
| `hub.tokenSecret.token` | string | `""` |  |
| `hub.tokenSecret.externalSecret` | object | see the keys below | External Secrets Operator: builds Secret `name` (key `key`) from Secrets Manager, e.g. one secret per cluster: kerf/agents/<cluster>. |
| `hub.tokenSecret.externalSecret.enabled` | boolean | `false` |  |
| `hub.tokenSecret.externalSecret.remoteRef` | object | see the keys below |  |
| `hub.tokenSecret.externalSecret.remoteRef.key` | string | `""` |  |
| `hub.tokenSecret.externalSecret.remoteRef.property` | string | `""` | e.g. token (JSON secret); empty = the whole secret string |
| `hub.tokenSecret.externalSecret.remoteRef.version` | string | `""` |  |
| `hub.tokenSecret.externalSecret.refreshInterval` | string | `""` |  |
| `hub.tokenSecret.externalSecret.secretStoreRef` | object | `{}` |  |
| `hub.caBundle` | object | see the keys below | CA that signed the hub's HTTPS certificate, when it is not publicly trusted (a private CA; an ACM public certificate needs nothing). Added to the system roots (SSL_CERT_DIR), mounted at /etc/kerf/hub-ca. One of existingConfigMap, existingSecret or pem (inline, rendered into ConfigMap <fullname>-hub-ca). |
| `hub.caBundle.existingConfigMap` | string | `""` |  |
| `hub.caBundle.existingSecret` | string | `""` |  |
| `hub.caBundle.key` | string | `ca.crt` |  |
| `hub.caBundle.pem` | string | `""` |  |
| `externalSecrets` | object | see the keys below | External Secrets Operator settings shared by every externalSecret of this chart. |
| `externalSecrets.apiVersion` | string | `external-secrets.io/v1` |  |
| `externalSecrets.refreshInterval` | string | `1h` |  |
| `externalSecrets.secretStoreRef` | object | `{}` | e.g. {kind: ClusterSecretStore, name: aws-secrets-manager} |
| `clusterDefaults` | object | `{}` | Cluster default policy: a RightsizingPolicy spec without selector/priority. Unset fields inherit the global defaults (mode audit, strategy in-place, profile production, ...). |
| `freeze` | object | see the keys below | Cluster-wide freeze, in addition to every policy's apply.freezeWindows and not overridable by a policy. Nothing is changed or released while frozen; health-watch rollbacks still happen. `now: true` freezes until turned off; windows use the RightsizingPolicy TimeWindow format (schedule + duration [+ timeZone], or start + end). |
| `freeze.now` | boolean | `false` |  |
| `freeze.windows` | array | `[]` |  |
| `readOnly` | boolean | `false` | eadOnly caps every mode at audit and drops every write verb on workloads and pods from the ClusterRole. Use it for clusters where every policy is audit. |
| `configSync` | object | see the keys below | Hub-managed configuration: on every pull the agent fetches the cluster config, makes the RightsizingPolicy CRs labelled kerf.getkerf.dev/managed-by=hub equal to the hub's policies (never touching other CRs), applies the hub's pause flag like a cluster-wide freeze and applies approve-once grants. The last config is kept in the ConfigMap <fullname>-config-state, so a restart without the hub keeps the pause flag and approvals. With readOnly the CRs are not written; the inventory is still reported. Before downgrading the agent below the release that added hub-managed configuration, delete the hub CRs:<br>`kubectl delete rsp -l kerf.getkerf.dev/managed-by=hub` |
| `configSync.enabled` | boolean | `true` |  |
| `pullInterval` | string | `1m` |  |
| `hubStaleAfter` | string | `5m` |  |
| `reportInterval` | string | `10s` |  |
| `eventBuffer` | integer | `10000` |  |
| `tolerance` | string | `0.05` |  |
| `logLevel` | string | `info` |  |
| `unsupportedNodeSelectors` | array | `[]` | Label selectors of nodes where in-place resize must not be used (static CPU/Memory Manager policy, which the API does not expose). Nodes with swap are detected automatically. Example: ["kerf.getkerf.dev/no-in-place=true", "node.kubernetes.io/cpu-manager-static"] |
| `health` | object | see the keys below | Health watch and rollback after every automatic change. Thresholds and the watch duration come from guardrails.rollback of the effective policy. |
| `health.enabled` | boolean | `true` |  |
| `health.checkInterval` | string | `15s` |  |
| `health.metricsURL` | string | `http://victoria-metrics.hub.svc:8428` | Prometheus-compatible endpoint for the CPU throttling ratio (the hub's VictoriaMetrics). Empty disables throttling-based rollback unless cadvisorFallback is on. |
| `health.minThrottlingWindow` | string | `2m` |  |
| `health.clusterLabel` | string | `cluster` | The cluster's metrics binding at metricsURL: label clusterLabel = clusterLabelValue (default: clusterName; clusterLabel "" = no cluster matcher) plus extra PromQL matchers such as 'env="prod"'. The hub's install snippet fills these from the cluster's binding. |
| `health.clusterLabelValue` | string | `""` |  |
| `health.extraMatchers` | array | `[]` |  |
| `health.cadvisorFallback` | boolean | `false` | Read throttling from the kubelets' cAdvisor endpoints when metricsURL fails (offline hub). Adds `get nodes/proxy` to the ClusterRole: that permission reaches every kubelet API, so it is off by default. |
| `onDelete` | object | see the keys below | StatefulSets with updateStrategy OnDelete: after the rollout strategy patched the pod template, the agent evicts the outdated pods (eviction API, so PodDisruptionBudgets apply), highest ordinal first, at most maxPodDeletesPerRun at a time, and only once every pod has been Ready for `settle` (or the StatefulSet's minReadySeconds) and the health watch reports no breach. |
| `onDelete.maxPodDeletesPerRun` | integer | `1` |  |
| `onDelete.settle` | string | `30s` |  |
| `selfCheck` | object | see the keys below | Readiness self-checks: RBAC per capability, CRDs, in-place resize, webhook, Argo CD, nodes, clock skew. They run at start, every interval and when the hub asks for a re-check, and are reported with the workload status. |
| `selfCheck.enabled` | boolean | `true` |  |
| `selfCheck.interval` | string | `10m` |  |
| `selfCheck.webhookDryRun` | boolean | `false` | Create a pod with dryRun=All to see the webhook mutate it. Adds `create pods` to the ClusterRole (no pod is ever persisted, but the permission is broad), so it is off by default. |
| `selfCheck.webhookConfigName` | string | `""` | MutatingWebhookConfiguration of the webhook chart; empty = found by its webhook name. |
| `selfCheck.docsBaseURL` | string | `""` |  |
| `selfCheck.argocd` | object | see the keys below |  |
| `selfCheck.argocd.enabled` | boolean | `true` | Read argocd-cm (only that ConfigMap, a Role in the Argo CD namespace) to check that Argo CD ignores the agent's fields. The Role is created only when the namespace exists at install time (Helm lookup); set force: true when rendering without cluster access (e.g. Argo CD renders the chart) and the namespace exists. |
| `selfCheck.argocd.namespace` | string | `argocd` |  |
| `selfCheck.argocd.force` | boolean | `false` |  |
| `capacity` | object | see the keys below | Node capacity report for the node estimate and consolidation blockers on the cluster page. Read only: nodes, pods and PodDisruptionBudgets (already readable), Karpenter NodePools and NodeClaims (get/list on karpenter.sh), and the Cluster Autoscaler status ConfigMap (get on that one ConfigMap, a Role in its namespace). Kerf never creates, deletes or drains nodes. |
| `capacity.enabled` | boolean | `true` |  |
| `capacity.interval` | string | `5m` | How often the report is built; it is sent when it changed (at least every 30 minutes). |
| `capacity.nodePoolLabels` | array | `[]` | Node labels naming a node's pool when it is not a Karpenter node, tried in order. Empty = the built-in list (EKS, GKE, AKS, kOps and the Cluster Autoscaler KWOK provider labels). |
| `capacity.clusterAutoscaler` | object | see the keys below |  |
| `capacity.clusterAutoscaler.enabled` | boolean | `true` | Read the status ConfigMap the Cluster Autoscaler writes (--write-status-configmap=true). |
| `capacity.clusterAutoscaler.statusConfigMap` | object | see the keys below |  |
| `capacity.clusterAutoscaler.statusConfigMap.namespace` | string | `kube-system` |  |
| `capacity.clusterAutoscaler.statusConfigMap.name` | string | `cluster-autoscaler-status` |  |
| `replicaCount` | integer | `1` | More than 1 needs leaderElection: true (only the leader works; the others wait). Production example (also in ci/values-prod.yaml):<br>`replicaCount: 2`<br>`leaderElection: true`<br>`podDisruptionBudget: {enabled: true, minAvailable: 1}     # rendered when replicaCount > 1`<br>`topologySpread: {enabled: true}                           # node and zone spread`<br>`priorityClassName: system-cluster-critical                # or your own class` |
| `replicas` | object | `null` | Deprecated alias of replicaCount (wins when set). |
| `leaderElection` | boolean | `false` |  |
| `podDisruptionBudget` | object | see the keys below |  |
| `podDisruptionBudget.enabled` | boolean | `true` |  |
| `podDisruptionBudget.minAvailable` | integer | `1` |  |
| `podDisruptionBudget.maxUnavailable` | object | `null` |  |
| `topologySpread` | object | see the keys below | Default node and zone spread (ScheduleAnyway) when replicaCount > 1; topologySpreadConstraints replaces it. |
| `topologySpread.enabled` | boolean | `true` |  |
| `topologySpread.whenUnsatisfiable` | string | `ScheduleAnyway` |  |
| `topologySpreadConstraints` | array | `[]` |  |
| `priorityClassName` | string | `""` |  |
| `serviceAccount` | object | see the keys below |  |
| `serviceAccount.create` | boolean | `true` | false: use an existing ServiceAccount `name` (the ClusterRole is still bound to it). |
| `serviceAccount.name` | string | `""` | default: the chart's full name (kerf-agent) |
| `serviceAccount.annotations` | object | `{}` |  |
| `extraEnv` | array | `[]` |  |
| `extraEnvFrom` | array | `[]` |  |
| `extraVolumes` | array | `[]` |  |
| `extraVolumeMounts` | array | `[]` |  |
| `podSecurityContext` | object | see the keys below |  |
| `podSecurityContext.runAsNonRoot` | boolean | `true` |  |
| `podSecurityContext.seccompProfile` | object | see the keys below |  |
| `podSecurityContext.seccompProfile.type` | string | `RuntimeDefault` |  |
| `securityContext` | object | see the keys below |  |
| `securityContext.allowPrivilegeEscalation` | boolean | `false` |  |
| `securityContext.readOnlyRootFilesystem` | boolean | `true` |  |
| `securityContext.capabilities` | object | see the keys below |  |
| `securityContext.capabilities.drop` | array | `["ALL"]` |  |
| `resources` | object | see the keys below |  |
| `resources.requests` | object | see the keys below |  |
| `resources.requests.cpu` | string | `50m` |  |
| `resources.requests.memory` | string | `64Mi` |  |
| `resources.limits` | object | see the keys below |  |
| `resources.limits.memory` | string | `256Mi` |  |
| `podLabels` | object | `{}` |  |
| `podAnnotations` | object | `{}` |  |
| `nodeSelector` | object | `{}` |  |
| `tolerations` | array | `[]` |  |
| `affinity` | object | `{}` |  |
| `metrics` | object | see the keys below |  |
| `metrics.port` | integer | `8080` |  |
| `metrics.scrape` | boolean | `true` | Adds prometheus.io/* scrape annotations to the metrics Service (vmagent convention). |
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
| `networkPolicy` | object | see the keys below | NetworkPolicy. Ingress to the metrics port from `ingress.metricsFrom` ([] = any source). Egress: DNS, TCP `egress.ports` to any destination (the API server 443/6443, the hub 8080 or 443, health.metricsURL 8428) and the raw rules in `egress.to` (e.g. to narrow them to CIDRs, set ports: [] and list them here). |
| `networkPolicy.enabled` | boolean | `false` |  |
| `networkPolicy.ingress` | object | see the keys below |  |
| `networkPolicy.ingress.metricsFrom` | array | `[]` |  |
| `networkPolicy.egress` | object | see the keys below |  |
| `networkPolicy.egress.enabled` | boolean | `true` |  |
| `networkPolicy.egress.ports` | array | `[443, 6443, 8080, 8428]` |  |
| `networkPolicy.egress.to` | array | `[]` |  |
| `stubHub` | object | see the keys below | In-process stub of the hub's agent endpoints, for local development before the real hub exists. When enabled the agent talks to it instead of hub.url. |
| `stubHub.enabled` | boolean | `false` |  |
| `stubHub.recommendations` | object | `{}` | cluster name -> list of WorkloadRecommendation (api/openapi/hub.yaml). id, generatedAt, dataCoverage and riskFlags are filled in when missing. |
