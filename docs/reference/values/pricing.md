<!-- Generated from deploy/charts/pricing/values.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# `pricing` values (pricing service)

The values of the `pricing` component chart. In the `kerf` chart they sit under `pricing:`; a key listed here as `image.tag` is then `pricing.image.tag`. Nested keys appear after their parent, with the parent's default shown in full or abbreviated.

| Key | Type | Default | Description |
|---|---|---|---|
| `clusterName` | string | `ro-hub` | Name of the cluster this runs in (the hub). Becomes the `cluster` label of the service's own kerf_pricing_* and Go runtime series; cost series carry the priced spoke's cluster instead. |
| `global` | object | see the keys below | Set by an umbrella chart; read by every kerf chart. |
| `global.imageRegistry` | string | `""` | prefixed to image.repository; image.registry wins over it |
| `global.imagePullSecrets` | array | `[]` | names or {name: ...} |
| `global.externalSecrets` | object | see the keys below |  |
| `global.externalSecrets.secretStoreRef` | object | `{}` | fallback SecretStore for every ExternalSecret |
| `image` | object | see the keys below |  |
| `image.registry` | string | `""` |  |
| `image.repository` | string | `ghcr.io/kerf-dev/kerf/pricing` |  |
| `image.tag` | string | `""` | defaults to the chart appVersion |
| `image.digest` | string | `""` | sha256:..., appended as @digest |
| `image.pullPolicy` | string | `IfNotPresent` |  |
| `imagePullSecrets` | array | `[]` |  |
| `provider` | string | `static` | static \| aws |
| `prices` | string | `""` | Static provider: inline price file (format of dev/prices.yaml). Empty = the file baked into the image (/etc/kerf/prices.yaml, a copy of dev/prices.yaml). |
| `aws` | object | see the keys below |  |
| `aws.pricingRegion` | string | `us-east-1` | AWS Pricing API endpoint region (us-east-1, eu-central-1 or ap-south-1). |
| `aws.region` | string | `""` | Region for SDK defaults (spot price calls use each node's region). |
| `cur` | object | see the keys below | Effective rates from the AWS Cost and Usage Report (CUR 2.0 through Data Exports, Parquet in S3), with Savings Plans and Reserved Instances amortised (docs/pricing.md). Off by default: nodes are priced at list (or static) prices. An instance type without usage in the report keeps its list price; an unreadable report falls back to list prices after maxAge. IAM: docs/install/eks.md. |
| `cur.source` | string | `""` | "" (off), athena (recommended), or s3 (reads the export's Parquet files directly; small accounts). |
| `cur.region` | string | `""` | Region of the Athena workgroup or the export bucket; empty = aws.region or the SDK default. |
| `cur.interval` | string | `24h` | How often the report is read. |
| `cur.window` | string | `720h` | The usage window rates are computed over. |
| `cur.maxAge` | string | `72h` | How long the last rates are kept while the report cannot be read; then list prices again. |
| `cur.athena` | object | see the keys below |  |
| `cur.athena.database` | string | `""` | Glue database of the CUR 2.0 table (for example kerf_cur). |
| `cur.athena.table` | string | `""` | e.g. data |
| `cur.athena.workgroup` | string | `primary` |  |
| `cur.athena.catalog` | string | `""` | empty = AwsDataCatalog |
| `cur.athena.outputLocation` | string | `""` | s3://<bucket>/<prefix>/ for query results; empty = the workgroup's |
| `cur.s3` | object | see the keys below |  |
| `cur.s3.bucket` | string | `""` |  |
| `cur.s3.prefix` | string | `""` | the export's data path: <S3 path prefix>/<export name>/data/ |
| `cur.s3.tmpSizeLimit` | string | `1Gi` | Scratch space for one Parquet file at a time (emptyDir at /tmp); raise resources.limits.memory for large files too. |
| `vmURL` | string | `http://victoria-metrics.monitoring.svc:8428` | Prometheus-compatible query URL holding the spokes' kube-state-metrics node series. Empty (the umbrella chart's default) with hub.url: the hub's own metrics datasource (hub.config.prometheusURL), read through the hub with the pricing token. When a query fails, the log names the URL and the value that set it. |
| `interval` | string | `5m` | Node discovery / pricing interval, and how long a fetched price is reused. |
| `priceTTL` | string | `24h` |  |
| `nodeLookback` | string | `10m` | A node is priced until this long after its last kube_node_labels sample. |
| `cpuMemoryWeight` | string | `0` | Weight of 1 vCPU in GiB of memory for the cost split; "0" derives it per instance family (fallback 9). |
| `clusters` | string | `""` | PromQL regex of cluster label values to price; empty = every cluster with node metrics. |
| `defaultRegion` | string | `""` | Region for nodes without topology.kubernetes.io/region or zone labels. |
| `logLevel` | string | `info` |  |
| `hub` | object | see the keys below | Clusters bound to other datasources are discovered through the hub's query proxy with a token of role pricing; empty url = only vmURL (clusters without a binding). |
| `hub.url` | string | `""` |  |
| `hub.tokenSecret` | object | see the keys below | The token of role pricing. Without it pricing logs "hub token missing: Secret <namespace>/<name> key <key>" on every refresh and is not Ready. Umbrella chart (global.mode hub and all): with name, create and externalSecret all unset, the token the hub chart generates (Secret <hub name>-local-tokens, key pricing); name set: your own Secret. |
| `hub.tokenSecret.name` | string | `""` | umbrella default: <pricing name>-hub-token for create / externalSecret |
| `hub.tokenSecret.key` | string | `token` |  |
| `hub.tokenSecret.create` | boolean | `false` | create: true makes the chart create the Secret from `token` (local development only). |
| `hub.tokenSecret.token` | string | `""` |  |
| `hub.tokenSecret.externalSecret` | object | see the keys below | External Secrets Operator: builds Secret `name` (key `key`) from Secrets Manager. |
| `hub.tokenSecret.externalSecret.enabled` | boolean | `false` |  |
| `hub.tokenSecret.externalSecret.remoteRef` | object | see the keys below |  |
| `hub.tokenSecret.externalSecret.remoteRef.key` | string | `""` | e.g. kerf/pricing/hub-token |
| `hub.tokenSecret.externalSecret.remoteRef.property` | string | `""` | e.g. token (JSON secret); empty = the whole secret string |
| `hub.tokenSecret.externalSecret.remoteRef.version` | string | `""` |  |
| `hub.tokenSecret.externalSecret.refreshInterval` | string | `""` |  |
| `hub.tokenSecret.externalSecret.secretStoreRef` | object | `{}` |  |
| `hub.caBundle` | object | see the keys below | CA that signed the hub's HTTPS certificate, when it is not publicly trusted (a private CA; an ACM public certificate needs nothing). Added to the system roots (SSL_CERT_DIR), mounted at /etc/kerf/hub-ca. One of existingConfigMap, existingSecret or pem (inline, rendered into ConfigMap <release>-hub-ca). |
| `hub.caBundle.existingConfigMap` | string | `""` |  |
| `hub.caBundle.existingSecret` | string | `""` |  |
| `hub.caBundle.key` | string | `ca.crt` |  |
| `hub.caBundle.pem` | string | `""` |  |
| `externalSecrets` | object | see the keys below | External Secrets Operator settings shared by every externalSecret of this chart. |
| `externalSecrets.apiVersion` | string | `external-secrets.io/v1` |  |
| `externalSecrets.refreshInterval` | string | `1h` |  |
| `externalSecrets.secretStoreRef` | object | `{}` | e.g. {kind: ClusterSecretStore, name: aws-secrets-manager} |
| `serviceAccount` | object | see the keys below | AWS access for provider aws: one IAM policy with pricing:GetProducts and ec2:DescribeSpotPriceHistory; with cur.source also Athena/Glue/S3 read access (docs/install/eks.md). - EKS Pod Identity (preferred): no annotation. Create the association for this ServiceAccount:<br>`aws eks create-pod-identity-association --cluster-name <cluster> --namespace <namespace> \`<br>`--service-account <serviceAccount name, default the release name> --role-arn <role>`<br>`(needs the eks-pod-identity-agent add-on).`<br>- IRSA (fallback): annotations: {eks.amazonaws.com/role-arn: arn:aws:iam::<account>:role/<role>} |
| `serviceAccount.create` | boolean | `true` |  |
| `serviceAccount.name` | string | `""` |  |
| `serviceAccount.annotations` | object | `{}` |  |
| `service` | object | see the keys below |  |
| `service.port` | integer | `9090` |  |
| `service.annotations` | object | see the keys below | Default annotations; replace the map to change them. Scraped by the hub vmagent's annotated-services job (honor_labels: true keeps `cluster`). |
| `service.annotations.prometheus.io/scrape` | string | `true` |  |
| `service.annotations.prometheus.io/port` | string | `9090` |  |
| `service.annotations.prometheus.io/path` | string | `/metrics` |  |
| `replicaCount` | integer | `1` | Singleton by design: every replica would publish every cost series again (duplicate kerf_*_hourly_cost_usd series), and there is no leader election. An outage only leaves cost figures stale for a few minutes (alert KerfPricingStale in the hub chart); recommendations and patching are unaffected. Hence no PDB, HPA or spread either. Values above 1 are rejected. |
| `replicas` | object | `null` | Deprecated alias of replicaCount (wins when set). |
| `priorityClassName` | string | `""` |  |
| `podLabels` | object | `{}` |  |
| `extraEnv` | array | `[]` |  |
| `extraEnvFrom` | array | `[]` |  |
| `extraVolumes` | array | `[]` |  |
| `extraVolumeMounts` | array | `[]` |  |
| `podSecurityContext` | object | see the keys below |  |
| `podSecurityContext.runAsNonRoot` | boolean | `true` |  |
| `podSecurityContext.runAsUser` | integer | `65532` |  |
| `podSecurityContext.runAsGroup` | integer | `65532` |  |
| `podSecurityContext.seccompProfile` | object | see the keys below |  |
| `podSecurityContext.seccompProfile.type` | string | `RuntimeDefault` |  |
| `securityContext` | object | see the keys below |  |
| `securityContext.allowPrivilegeEscalation` | boolean | `false` |  |
| `securityContext.readOnlyRootFilesystem` | boolean | `true` |  |
| `securityContext.capabilities` | object | see the keys below |  |
| `securityContext.capabilities.drop` | array | `["ALL"]` |  |
| `networkPolicy` | object | see the keys below | NetworkPolicy. Ingress to the metrics port from `ingress.metricsFrom` ([] = any source). Egress: DNS, TCP `egress.ports` to any destination (the hub 8080, VictoriaMetrics 8428, AWS APIs and an HTTPS hub on 443) and the raw rules in `egress.to`. |
| `networkPolicy.enabled` | boolean | `false` |  |
| `networkPolicy.ingress` | object | see the keys below |  |
| `networkPolicy.ingress.metricsFrom` | array | `[]` |  |
| `networkPolicy.egress` | object | see the keys below |  |
| `networkPolicy.egress.enabled` | boolean | `true` |  |
| `networkPolicy.egress.ports` | array | `[443, 8080, 8428]` |  |
| `networkPolicy.egress.to` | array | `[]` |  |
| `serviceMonitor` | object | see the keys below | Prometheus Operator ServiceMonitor or VictoriaMetrics VMServiceScrape. Keep honorLabels: cost series carry the priced spoke's `cluster` (docs/contracts/metrics.md). |
| `serviceMonitor.enabled` | boolean | `false` |  |
| `serviceMonitor.kind` | string | `ServiceMonitor` | or VMServiceScrape |
| `serviceMonitor.namespace` | string | `""` |  |
| `serviceMonitor.interval` | string | `30s` |  |
| `serviceMonitor.scrapeTimeout` | string | `""` |  |
| `serviceMonitor.honorLabels` | boolean | `true` |  |
| `serviceMonitor.labels` | object | `{}` |  |
| `serviceMonitor.relabelings` | array | `[]` |  |
| `serviceMonitor.metricRelabelings` | array | `[]` |  |
| `resources` | object | see the keys below |  |
| `resources.requests` | object | see the keys below |  |
| `resources.requests.cpu` | string | `10m` |  |
| `resources.requests.memory` | string | `32Mi` |  |
| `resources.limits` | object | see the keys below |  |
| `resources.limits.memory` | string | `128Mi` |  |
| `podAnnotations` | object | `{}` |  |
| `nodeSelector` | object | `{}` |  |
| `tolerations` | array | `[]` |  |
| `affinity` | object | `{}` |  |
