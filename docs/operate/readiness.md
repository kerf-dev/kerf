# Cluster readiness

Every cluster moves through five states. Each state needs the checks that gate it to pass:

```
Registered → Agent connected → Metrics OK → Ready for audit → Automation ready
```

A cluster's state is the highest one for which every blocking check that gates it, and every check of the earlier
states, has the status `pass`. Warning and info checks never gate. A blocking check that is `fail`, `warn` or
`unknown`, or has no result yet, holds the cluster back. Open the cluster in the UI, or run the following after
the sign-in of [the EKS guide](../install/eks.md#45-first-sign-in-and-the-pricing-token):

```bash
curl -sS -b cookies https://<ui-host>/v1/clusters/<cluster>/readiness \
  | jq -c '{state, nextState, blockedBy, failing: [.checks[] | select(.status != "pass") | {id, severity, status, message, remediation}]}'
```

Each failing check carries its `message` and a `remediation`: a command, a values snippet or a sentence.
Each check also shows when it ran (`checkedAt`, "checked 23 minutes ago" in the UI).

**After a fix, re-check.** The hub runs its metrics checks at registration, on a re-check, on a recompute, and
otherwise only every `hub.config.onboarding.checkInterval` (default `1h`). The agent runs its checks at start and
every 10 minutes. So a fixed scrape job can still show the old result for up to an hour. To run the checks now,
click **Re-check now** on the cluster's page (Overview or Readiness tab), or call:

```bash
curl -sS -b cookies -H "X-CSRF-Token: $CSRF" -X POST https://<ui-host>/v1/clusters/<cluster>/readiness/recheck
```

A re-check needs the role `operator`. The hub checks run at once; the agent answers with its next pull. Every
`warn` or `fail` remediation of a metrics check ends with this hint. Source of truth:
[readiness contract](../contracts/readiness.md).

## Registered

The cluster exists in the hub (**Clusters → Add cluster**). No check gates this state.

## Agent connected

| Check | Verifies | If it fails | Fix |
|---|---|---|---|
| `agent.connected` | The agent pulled its config at least once and within 3 pull intervals (3 minutes by default). | The agent never connected, or went quiet. Nothing new is applied there. | Install the agent with the cluster's install snippet and read its log: [Agent not connecting](troubleshooting.md#agent-not-connecting). |
| `agent.version` | The agent's version fits the hub's. | The agent is older than `config.onboarding.minAgentVersion`, or its major or minor version is higher than the hub's. | Upgrade the chart in that cluster, or the hub first: [Upgrade](upgrade.md#order-and-version-skew). |
| `agent.crds` | `RightsizingPolicy` and `RightsizingRecommendation` exist and have every field the agent knows. | CRDs are missing, or older than the agent. | Upgrade the chart in mode `spoke`, or `kubectl apply --server-side --force-conflicts -f api/crds/` from a checkout of your release. |
| `rbac.read` | The agent may get, list and watch pods, nodes, Deployments, StatefulSets, DaemonSets, ReplicaSets, CronJobs, Jobs, HPAs and PodDisruptionBudgets, read `rightsizingpolicies`, and manage `rightsizingrecommendations`. | The agent's ClusterRole is incomplete. | Upgrade the chart, which renders the full ClusterRole. The failing result lists the missing rules. |

## Metrics OK

These checks read the cluster's metrics source. They run in order, and a failure hides what follows: when
`datasource.reachable` fails, the rest show `unknown` ("not checked"). The same happens after
`metrics.cluster-label`. Fix them from the top.

| Check | Verifies | If it fails | Fix |
|---|---|---|---|
| `datasource.reachable` | The hub reaches the datasource's query API with its credentials. `details` holds `httpStatus` and `error`. | Wrong URL, TLS, credentials or tenant header, or no network path. | **Admin → Metrics sources**, then **Test**. With the hub NetworkPolicy on, a port other than 443, 8428 or 9090 needs adding to `hub.networkPolicy.egress.ports`. |
| `metrics.cluster-label` | Series match the cluster's binding (label, value, extra matchers) in the last hour. | Zero series. `details.suggestions` lists the values the label does have. | Set the binding on the cluster's page, or give the scraper the right external label. If no series carry the label, check its key (`k8s_cluster`, `cluster_name`, `eks_cluster`). |
| `metrics.cadvisor` | `container_cpu_usage_seconds_total` and `container_memory_working_set_bytes` exist with a `container` label. | cAdvisor is not scraped, or its labels were dropped. | Scrape the kubelet's `/metrics/cadvisor` and keep the container, pod and namespace labels. The remediation has a job for Prometheus and for Grafana Alloy: [Scraping cAdvisor](#scraping-cadvisor). |
| `metrics.ksm-owners` | `kube_pod_owner` exists, with `kube_replicaset_owner` and `kube_job_owner` for the owners pods refer to. | Pods of Deployments or CronJobs cannot be resolved to their workload. | Install kube-state-metrics and enable its replicasets and jobs collectors (`--resources`). |
| `metrics.ksm-resources` | `kube_pod_container_resource_requests` exists (limits are reported but optional). | No current values, so no savings. | Scrape kube-state-metrics with the pods collector on. |
| `metrics.freshness` | The newest sample of `container_memory_working_set_bytes` and of `kube_pod_owner` is under 5 minutes old. | Two cases. "no samples of … found in the last 1h" (`details` shows `absent`): the series does not exist, so its scrape job or exporter is missing. "last sample is … old": the data stopped, so scraping or remote-write broke. | Absent: add the cAdvisor scrape job or install and scrape kube-state-metrics. Stale: check the scraper's targets, its remote-write errors and the kube-state-metrics pod. |

## Ready for audit

| Check | Verifies | If it fails | Fix |
|---|---|---|---|
| `metrics.recommendations` | The recommender produced recommendations for the cluster's live workloads. | No recommendation yet, or no live workloads found. | Wait for the next run (once a day by default), or start one: `POST /v1/admin/recompute?cluster=<cluster>` (admin). A recompute also re-runs the hub's metrics checks of that cluster (of every cluster without `cluster=`), so they don't wait for the `1h` check interval. If it persists, fix the metrics checks, then read the hub log ([No recommendations](troubleshooting.md#no-recommendations)). |

## Automation ready

All six checks come from the agent's access reviews against its own ClusterRole. Each one needs the verbs in the
table. A failing result lists the missing ClusterRole rules.

| Check | The agent may |
|---|---|
| `rbac.resize` | `patch` `pods/resize` and `pods` |
| `rbac.rollout` | `patch` Deployments, StatefulSets, DaemonSets and CronJobs |
| `rbac.hpa` | `patch` HorizontalPodAutoscalers |
| `rbac.evict` | `create` `pods/eviction` |
| `rbac.policies` | `create`, `update` and `delete` `rightsizingpolicies` |
| `rbac.events` | `create` and `patch` Events (`core` and `events.k8s.io`) |

Fix: upgrade the chart. If the agent runs with `readOnly: true`, every write capability fails by design. Set
`agent.readOnly: false` to allow automation.

`auto-patch` needs this state. A policy, override or approve-once that newly enables `auto-patch` on a cluster
below it gets `409 cluster_not_automation_ready`. An admin can override with `overrideReadiness: true` and a reason,
which the audit log records. Existing `auto-patch` policies stay when a cluster drops out of the state.

## Checks that do not gate

They show as `warn` (or `unknown`) and name a reduced capability.

| Check | Severity | Means | Fix |
|---|---|---|---|
| `agent.clock-skew` | warning | The node clock differs from the hub's by more than 30 s. Windows and cooldowns drift. | Enable NTP on the nodes and the hub. |
| `metrics.owner-join` | warning | Under 95 % of cAdvisor pods match a `kube_pod_owner` series. | Make both carry the same `namespace` and `pod` labels: drop relabelling that renames them (`exported_namespace`) and set `honorLabels` on the kube-state-metrics scrape. |
| `metrics.scrape-interval` | warning | cAdvisor is scraped less often than about every 60 s. P95 and P99 are unreliable. The hub estimates the interval per series over the time the series covers in the last 30 minutes (newest minus oldest sample, divided by the gaps between samples), then takes the median. A job that started a few minutes ago gives the right value. A series needs 3 samples. | Set `scrape_interval: 30s` (60 s at most). |
| `metrics.history` | warning | History is shorter than the profile window. `details.enoughDataAt` says when it will cover it. Warns while a cluster is new. | Wait, or raise the store's retention. |
| `metrics.throttling` | warning | `container_cpu_cfs_*` are missing, so the throttling guardrail is blind. They exist only for containers with a CPU limit. | Keep them in the cAdvisor scrape. |
| `metrics.restarts-oom` | warning | Restart or OOM series are missing, so OOM and restart guardrails and rollbacks are blind. | Keep `kube_pod_container_status_restarts_total` and the last-terminated reason or `container_oom_events_total` (needs kubelet 1.26 or later). |
| `metrics.hpa` | warning | `unknown` without HPA series: the cluster has no HPA, or the collector is off. `warn` without `kube_horizontalpodautoscaler_spec_target_metric`. | Enable the `horizontalpodautoscalers` collector of kube-state-metrics (2.0 or later). |
| `metrics.node-labels` | warning | `kube_node_labels` lacks instance type, capacity type, zone or region on N of M nodes. Pricing uses default rates for those nodes. Each node is judged by its newest series, so a series from before an allowlist change does not count. The capacity type is `karpenter.sh/capacity-type` or `eks.amazonaws.com/capacityType` (exported as `label_eks_amazonaws_com_capacity_type`). | Add the labels to kube-state-metrics' `metricLabelsAllowlist` (below), then re-check. |
| `metrics.node-allocatable` | warning | `kube_node_status_allocatable` is missing, so the cost split per vCPU and GiB fails. | Enable the nodes collector. |
| `pricing.coverage` | warning | Fewer nodes are priced than seen. | `kerf_pricing_nodes{status="skipped"}` names the reason per node. See [pricing](troubleshooting.md#metrics-source-and-pricing). |
| `apply.in-place-resize` | warning | Kubernetes is older than 1.33, or `pods/resize` is not served. `in-place` falls back to `rollout`. | Upgrade the cluster, or set the apply strategy to `rollout`. |
| `webhook.healthy` | warning | The webhook is not installed, has no `caBundle`, has no ready endpoints, or a dry-run pod was not mutated. | `kubectl -n kerf get pods,certificate -l app.kubernetes.io/name=kerf-webhook` and `kubectl -n kerf logs deploy/webhook`. |
| `webhook.cert-manager` | info | The webhook uses cert-manager, but `cert-manager.io/v1` is not served. | Install cert-manager. |
| `nodes.in-place-memory` | info | Some nodes have swap or a static CPU or memory manager. Memory changes there use `rollout`. | Nothing, unless most workloads run there. |
| `integration.argocd` | warning | `argocd-cm` lacks `managedFieldsManagers: [kerf]`, so a self-healing Argo CD reverts the agent. | The patch in [step 6 of the EKS guide](../install/eks.md#6-turning-on-auto-patch). |
| `policy.namespace-coverage` | info | Some namespaces are excluded by policy or label. | Informational. |

The `metricLabelsAllowlist` for kube-state-metrics:

```yaml
kube-state-metrics:
  metricLabelsAllowlist:
    - nodes=[node.kubernetes.io/instance-type,karpenter.sh/capacity-type,eks.amazonaws.com/capacityType,topology.kubernetes.io/zone,topology.kubernetes.io/region]
```

## Scraping cAdvisor

The `metrics.cadvisor` remediation scrapes each node's cAdvisor endpoint through the API server proxy, with
TLS verified against the service-account CA (`ca_file`), not with `insecure_skip_verify`. The scraper's service
account needs these ClusterRole rules:

```yaml
- apiGroups: [""]
  resources: [nodes, nodes/proxy]
  verbs: [get, list, watch]
```

Prometheus (`scrape_configs`):

```yaml
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
    - source_labels: [__meta_kubernetes_node_name]
      target_label: instance
```

Grafana Alloy:

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
  rule {
    source_labels = ["__meta_kubernetes_node_name"]
    target_label  = "instance"
  }
}

prometheus.scrape "cadvisor" {
  job_name          = "cadvisor"
  targets           = discovery.relabel.cadvisor.output
  scrape_interval   = "30s"
  scheme            = "https"
  bearer_token_file = "/var/run/secrets/kubernetes.io/serviceaccount/token"
  tls_config {
    ca_file = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
  }
  forward_to = [prometheus.remote_write.default.receiver]
}
```

Use your own `prometheus.remote_write` component in `forward_to`. If the cluster's metrics binding lists other
cAdvisor job names (`jobNames.cadvisor`), the remediation uses that name.

## Related

- [Readiness contract](../contracts/readiness.md)
- [Troubleshooting](troubleshooting.md)
- [Alerts and dashboards](alerts-dashboards.md)
- [Install on Amazon EKS](../install/eks.md#54-readiness)
- [Guardrails](../concepts/guardrails.md)
