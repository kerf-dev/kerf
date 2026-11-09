# Use Dynatrace as the metrics source

> **Preview.** The native `dynatrace` datasource is new in 0.2.0 and has not yet been run against many Dynatrace
> environments. The dual-write path is the proven one. If the native datasource misreads your data, please
> [open an issue on GitHub](https://github.com/kerf-dev/kerf/issues) with the Test result.

Kerf recommends requests and limits from each container's CPU and memory usage, its current requests and limits,
the workload that owns it, and node labels for prices. With Prometheus these come from cAdvisor and
kube-state-metrics. If your clusters are monitored by Dynatrace, you have two ways to give Kerf the same data:

| | [A. Dual-write](#a-dual-write-to-a-prometheus-compatible-store) | [B. Native `dynatrace` datasource](#b-native-dynatrace-datasource) |
|---|---|---|
| What runs | An OpenTelemetry Collector or Grafana Alloy per cluster, and a Prometheus-compatible store (VictoriaMetrics, Prometheus, Mimir, Amazon Managed Prometheus) | Nothing new: the hub queries your Dynatrace environment with DQL |
| Data | The exact Prometheus series Kerf expects, also sent to Dynatrace | Dynatrace's Kubernetes metrics, mapped to Kerf's inputs |
| Gaps | None | No HPA data, no raw CFS throttling counters, no init-container or image data ([mapping](#what-kerf-reads-from-dynatrace)) |
| Choose it when | You want every Kerf feature, or Dynatrace's Kubernetes monitoring is not set up | You want no second pipeline and accept the gaps |

Both paths end the same way: the cluster's readiness reaches *Automation ready* once its agent is installed
([readiness](#readiness)).

## A. Dual-write to a Prometheus-compatible store

A collector in each workload cluster scrapes cAdvisor and kube-state-metrics once and writes the series twice: to
a Prometheus-compatible store that Kerf reads, and to Dynatrace. Install kube-state-metrics with the collectors and
node-label allowlist in [kube-state-metrics requirements](any-cluster.md#kube-state-metrics-requirements).

**The `cluster` label rule.** Every series must carry the cluster's registered Kerf name, by default in the label
`cluster`. Set it once in the writer (an external label), not per scrape job. If your series already carry another
label (for example `k8s_cluster`), keep it and set that label in the cluster's metrics binding instead (below).

### OpenTelemetry Collector

Use the contrib distribution (`otel/opentelemetry-collector-contrib`), which has the Prometheus receiver, the
Prometheus remote-write exporter and the `cumulativetodelta` processor. Dynatrace's OTLP endpoint takes delta
counters, so only the Dynatrace pipeline converts them; the store gets the original cumulative counters that
`rate()` needs. Give the collector's service account `get`, `list` and `watch` on `nodes` and `nodes/proxy`.

```yaml
receivers:
  prometheus:
    config:
      scrape_configs:
        - job_name: cadvisor
          scrape_interval: 30s
          scheme: https
          kubernetes_sd_configs: [{role: node}]
          bearer_token_file: /var/run/secrets/kubernetes.io/serviceaccount/token
          tls_config:
            ca_file: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
          relabel_configs:
            - target_label: __address__
              replacement: kubernetes.default.svc:443
            - source_labels: [__meta_kubernetes_node_name]
              target_label: __metrics_path__
              replacement: /api/v1/nodes/$1/proxy/metrics/cadvisor
            - source_labels: [__meta_kubernetes_node_name]
              target_label: node
        - job_name: kube-state-metrics
          scrape_interval: 30s
          honor_labels: true
          static_configs:
            - targets: [kube-state-metrics.kube-system.svc:8080]

processors:
  batch: {}
  cumulativetodelta: {}

exporters:
  prometheusremotewrite/kerf:
    endpoint: http://victoria-metrics.monitoring.svc:8428/api/v1/write
    external_labels:
      cluster: prod-eu1          # the cluster's name in Kerf
  otlphttp/dynatrace:
    endpoint: https://<environment-id>.live.dynatrace.com/api/v2/otlp
    headers:
      Authorization: "Api-Token ${env:DT_API_TOKEN}"   # scope metrics.ingest

service:
  pipelines:
    metrics/kerf:
      receivers: [prometheus]
      processors: [batch]
      exporters: [prometheusremotewrite/kerf]
    metrics/dynatrace:
      receivers: [prometheus]
      processors: [cumulativetodelta, batch]
      exporters: [otlphttp/dynatrace]
```

### Grafana Alloy

The same two scrape jobs, forwarded to a remote write for Kerf and, through the OpenTelemetry components, to
Dynatrace:

```alloy
discovery.kubernetes "nodes" {
  role = "node"
}
discovery.relabel "cadvisor" {
  targets = discovery.kubernetes.nodes.targets
  rule {
    target_label = "__address__"
    replacement  = "kubernetes.default.svc:443"
  }
  rule {
    source_labels = ["__meta_kubernetes_node_name"]
    target_label  = "__metrics_path__"
    replacement   = "/api/v1/nodes/$1/proxy/metrics/cadvisor"
  }
  rule {
    source_labels = ["__meta_kubernetes_node_name"]
    target_label  = "node"
  }
}
prometheus.scrape "cadvisor" {
  targets           = discovery.relabel.cadvisor.output
  scrape_interval   = "30s"
  scheme            = "https"
  bearer_token_file = "/var/run/secrets/kubernetes.io/serviceaccount/token"
  tls_config {
    ca_file = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
  }
  forward_to = [prometheus.remote_write.kerf.receiver, otelcol.receiver.prometheus.dynatrace.receiver]
}
prometheus.scrape "kube_state_metrics" {
  targets         = [{"__address__" = "kube-state-metrics.kube-system.svc:8080"}]
  scrape_interval = "30s"
  honor_labels    = true
  forward_to      = [prometheus.remote_write.kerf.receiver, otelcol.receiver.prometheus.dynatrace.receiver]
}

prometheus.remote_write "kerf" {
  external_labels = {cluster = "prod-eu1"}   // the cluster's name in Kerf
  endpoint {
    url = "http://victoria-metrics.monitoring.svc:8428/api/v1/write"
  }
}

otelcol.receiver.prometheus "dynatrace" {
  output {
    metrics = [otelcol.processor.cumulativetodelta.dynatrace.input]
  }
}
otelcol.processor.cumulativetodelta "dynatrace" {
  output {
    metrics = [otelcol.exporter.otlphttp.dynatrace.input]
  }
}
otelcol.exporter.otlphttp "dynatrace" {
  client {
    endpoint = "https://<environment-id>.live.dynatrace.com/api/v2/otlp"
    headers  = {"Authorization" = "Api-Token " + sys.env("DT_API_TOKEN")}   // scope metrics.ingest
  }
}
```

### Add the store to Kerf

If the store is the hub's `hub.config.prometheusURL`, it is the built-in datasource `local` and there is nothing to
add. Otherwise add it under **Admin → Metrics sources** or with the API (signed in as an admin, as in
[the readiness step](single-cluster.md#5-readiness)):

```bash
curl -sS -b cookies -H "X-CSRF-Token: $(cat csrf)" -H 'Content-Type: application/json' \
  -X POST "https://$UI_HOST/v1/admin/datasources" -d '{
    "name": "victoria-metrics",
    "type": "victoriametrics",
    "url": "http://victoria-metrics.monitoring.svc:8428",
    "reason": "Kubernetes metrics written next to Dynatrace"
  }' | jq '{id, name, type}'
```

Then pick it for the cluster in the cluster wizard, or set the binding: `clusterLabel` is the label that holds the
cluster name (`cluster` with the configs above) and `clusterLabelValue` its value (default: the cluster's Kerf name).

## B. Native `dynatrace` datasource

The hub reads Dynatrace's Kubernetes metrics (`dt.kubernetes.*`) and the Smartscape node labels with DQL through the
Grail query API, and maps them to what the recommender, the readiness checks and pricing need.

**Requirements.** Kubernetes monitoring for the cluster in Dynatrace: the Dynatrace Operator with a DynaKube whose
ActiveGate has the `kubernetes-monitoring` capability, so the Dynatrace Kubernetes app shows the cluster's
containers with CPU, memory, requests and limits. The throttling ratio comes from OneAgent container monitoring
(`cloudNativeFullStack` or `classicFullStack`). The environment must have Grail. The hub needs HTTPS access to the
environment, or to an ActiveGate or proxy that forwards to it.

### 1. A platform token in a Secret

Create a Dynatrace **platform token** (not a classic API token) with the scopes:

| Scope | For |
|---|---|
| `storage:metrics:read` | the container and node metrics |
| `storage:smartscape:read` | the node labels (Smartscape `K8S_NODE`) |
| `storage:buckets:read` | reading the metrics buckets |

Store it in the hub's namespace and mount it into the hub. Nothing is then stored in Kerf's database, and a
rotated Secret is picked up without a restart:

```bash
read -rs DT_TOKEN
kubectl -n kerf create secret generic kerf-dynatrace --from-literal=token="$DT_TOKEN"
unset DT_TOKEN
```

```yaml
# values-hub.yaml (umbrella chart; in the hub chart alone, without the hub: level)
hub:
  onboarding:
    datasourceSecrets:
      - {name: dynatrace, existingSecret: kerf-dynatrace}   # key token; /etc/hub/datasource-secrets/dynatrace/token
```

Run `helm upgrade` with the new values. You can also paste the token in the UI instead: it is then stored encrypted
with the datasource key, like other datasource credentials.

### 2. Add the datasource

Under **Admin → Metrics sources → New datasource**, choose the type **Dynatrace**, enter the environment URL
(`https://<environment-id>.apps.dynatrace.com`; a `.live.` URL is changed to `.apps.`), choose **Kubernetes Secret**
and enter `dynatrace`. Set **ActiveGate URL** only when the hub reaches Dynatrace through an ActiveGate or an egress
proxy; it must forward the platform query API (`/platform/storage/query/v1/`).

![The datasource form with the Dynatrace type](../screenshots/dynatrace-datasource-form-light.png#only-light)
![The datasource form with the Dynatrace type](../screenshots/dynatrace-datasource-form-dark.png#only-dark)

The same with the API:

```bash
curl -sS -b cookies -H "X-CSRF-Token: $(cat csrf)" -H 'Content-Type: application/json' \
  -X POST "https://$UI_HOST/v1/admin/datasources" -d '{
    "name": "dynatrace-prod",
    "type": "dynatrace",
    "url": "https://<environment-id>.apps.dynatrace.com",
    "auth": {"type": "bearer", "tokenSecret": "dynatrace"},
    "reason": "Kubernetes metrics from Dynatrace"
  }' | jq '{id, name, type, auth}'
```

A `400` names the problem, for example a `tokenSecret` that is not mounted.

### 3. Test it

Click **Test** on the datasource. Without a cluster it checks the token and searches the whole environment: the
**metric mapping** lists every input Kerf needs, the Dynatrace metric it reads it from, and whether it was found.
*Missing* names each metric without series; *not available* lists the inputs Dynatrace has no equivalent for. The
cluster names Dynatrace knows are listed as *cluster label values*.

![The Test result with the metric mapping](../screenshots/dynatrace-test-result-light.png#only-light)
![The Test result with the metric mapping](../screenshots/dynatrace-test-result-dark.png#only-dark)

### 4. Bind the cluster

In the cluster wizard (or the cluster's page), choose the Dynatrace datasource and enter the cluster's **Dynatrace
cluster name**: its `k8s.cluster.name`, as the Dynatrace Kubernetes app shows it. The default is the cluster's
Kerf name. Extra label matchers do not apply to Dynatrace. With the API:

```bash
ETAG=$(curl -sS -b cookies -o /dev/null -D - "https://$UI_HOST/v1/admin/clusters/prod-eu1" | awk -F'"' 'tolower($1) ~ /^etag/ {print $2}')
curl -sS -b cookies -H "X-CSRF-Token: $(cat csrf)" -H 'Content-Type: application/json' -H "If-Match: \"$ETAG\"" \
  -X PATCH "https://$UI_HOST/v1/admin/clusters/prod-eu1" -d '{
    "metrics": {"datasourceId": "<datasource id>", "clusterLabel": "cluster", "clusterLabelValue": "<k8s.cluster.name>"},
    "reason": "metrics from Dynatrace"
  }' | jq .metrics
```

`clusterLabel` is required by the API but not used for Dynatrace.

### Readiness

The hub runs the same readiness checks against Dynatrace, with Dynatrace remediation (no scrape jobs or
kube-state-metrics to fix). Expect:

- **Metrics OK** when the container metrics, requests and the workload dimensions are found and fresh.
- `metrics.hpa` stays **unknown**: Dynatrace has no HPA metrics, so Kerf does not detect HPA coupling. Review the
  recommendations of HPA-scaled workloads before you automate them, or exclude those workloads with a policy.
- `metrics.throttling` passes with the **derived** throttling ratio; recommendations say so in their CPU explanation.
- `metrics.node-labels` warns when nodes lack a label pricing needs. Both capacity-type labels are read
  (`karpenter.sh/capacity-type`, else `eks.amazonaws.com/capacityType`).
- `metrics.dcgm` is unknown: GPU metrics are not read from Dynatrace.

These are warnings or unknowns, not blocking checks: with the agent installed and its RBAC checks passing, the cluster
reaches **Ready for audit** after the first recommendations and then **Automation ready**.

![Readiness of a cluster read from Dynatrace](../screenshots/dynatrace-readiness-light.png#only-light)
![Readiness of a cluster read from Dynatrace](../screenshots/dynatrace-readiness-dark.png#only-dark)

### What Kerf reads from Dynatrace

Container metrics carry `k8s.cluster.name`, `k8s.namespace.name`, `k8s.workload.kind`, `k8s.workload.name`,
`k8s.pod.name` and `k8s.container.name`.

| Kerf input (Prometheus series) | Dynatrace source | Status |
|---|---|---|
| `container_cpu_usage_seconds_total` | `dt.kubernetes.container.cpu_usage` | Read. A gauge (no `rate()`), converted from the unit the API reports to cores. |
| `container_memory_working_set_bytes` | `dt.kubernetes.container.memory_working_set` | Read. Also gives the running pods per step. |
| `kube_pod_container_resource_requests` | `dt.kubernetes.container.requests_cpu`, `dt.kubernetes.container.requests_memory` | Read |
| `kube_pod_container_resource_limits` | `dt.kubernetes.container.limits_cpu`, `dt.kubernetes.container.limits_memory` | Read |
| `kube_pod_owner`, `kube_replicaset_owner`, `kube_job_owner` | the `k8s.workload.kind` and `k8s.workload.name` dimensions | Read. Dynatrace resolves the workload itself; kinds `deployment`, `statefulset`, `daemonset`, `cronjob`. |
| `container_cpu_cfs_throttled_periods_total`, `container_cpu_cfs_periods_total` | `dt.containers.cpu.throttling_ratio` | **Partly.** A derived ratio: no raw CFS period counters. Marked in recommendations. |
| `kube_pod_container_status_restarts_total` | `dt.kubernetes.container.restarts` | Read (summed over the risk window) |
| `container_oom_events_total` | `dt.kubernetes.container.oom_kills` | Read (summed over the risk window) |
| `kube_node_labels` | Smartscape `K8S_NODE` labels | Read from Smartscape, not a metric |
| `kube_node_status_allocatable` | `dt.kubernetes.node.cpu_allocatable`, `dt.kubernetes.node.memory_allocatable` | Read |
| `kube_horizontalpodautoscaler_*` | none | **Gap.** HPA coupling is not detected; readiness says so. |
| `kube_pod_init_container_*` | none | **Gap.** Init and native sidecar containers are not told apart from app containers. |
| `kube_pod_container_info` (images) | none | **Gap.** JVM heap flags are not detected. |
| `DCGM_FI_DEV_*` (GPU) | none | **Gap.** GPU usage is not read. |

### How the hub queries Dynatrace

- **Aggregation.** A bucket longer than one minute holds several samples. The hub first takes the average (CPU) or
  maximum (memory) per pod and container, then the maximum and the sum across pods, so a sum never adds up samples
  of one series.
- **Resolution.** Dynatrace stores one point per minute. The recommender reads 5-minute steps, like it does with
  Prometheus; `metrics.scrape-interval` checks the resolution.
- **One batch per cluster per recompute.** A query covers at most 1,440 buckets, so a 14-day window is three chunks.
  A recompute of one cluster runs about 16 queries for a 14-day window (25 for 28 days), and the hourly readiness run
  about 20, at most two at a time.
- **Rate limits.** On HTTP `429` (or `503`) the hub waits for `Retry-After` (or 1, 2, 4, 8 s) and retries up to four
  times; long queries are polled.
- **Pricing** reads the nodes through the hub (`GET /v1/internal/clusters/{cluster}/nodes`), not the query proxy.
- **Usage charts and realized savings** read Dynatrace too; the requested-capacity ratios of the node cost trend are
  left out.

### Limits

- **Missing series:** HPA, raw CFS throttling counters, init containers, images and GPU metrics (table above).
- **Lookback:** recommendations need the profile window of history (7, 14 or 28 days); `metrics.history` checks it
  against the retention of your Grail metrics bucket.
- **Token:** a platform token with the scopes above. A classic API token is rejected (`401`) by the query API.
- **API rate limits:** the query API's limits per environment were not measured. The hub keeps its load to the batches
  above and backs off on `429`. With many clusters on one environment, raise
  `hub.config.onboarding.checkInterval` if readiness runs hit the limits.
- **Units:** values are converted from the unit the API reports (millicores and cores, bytes and the binary and
  decimal multiples, percent and ratio). Without a unit, CPU is read as millicores, memory as bytes and the
  throttling ratio as percent.
- **Not verified against every environment:** that `dt.kubernetes.container.restarts` and `oom_kills` count events
  per interval (they are summed), that `dt.containers.cpu.throttling_ratio` carries the `k8s.*` dimensions, the exact
  shape of the Smartscape label field (the hub reads both the node's Kubernetes object and `tags:k8s.labels`), and
  routing the platform query API through an Environment ActiveGate.
