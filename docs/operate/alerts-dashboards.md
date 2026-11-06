# Alerts and dashboards

The `kerf` chart ships eight alert rules and four Grafana dashboards. Both are off by default. They read the
`kerf_*` series that Kerf exports ([metrics contract](../contracts/metrics.md)).

## Turn them on

The alerts need the series of the whole fleet: the hub's own, the pricing service's, and the agent and webhook
series of every workload cluster. Evaluate them where those series land, in the hub's VictoriaMetrics (vmalert)
or in a Prometheus that reads it. The metrics must reach that store with the `cluster` label intact: the
ServiceMonitors keep `honorLabels: true` for this reason.

```yaml
hub:
  serviceMonitor:
    enabled: true
    kind: ServiceMonitor           # or VMServiceScrape
  prometheusRule:
    enabled: true
    kind: PrometheusRule           # or VMRule
    labels: {release: kube-prometheus-stack}   # whatever your rule selector expects
    additionalLabels: {team: sre}              # added to every alert
  dashboards:
    enabled: true
    namespace: monitoring          # where the Grafana sidecar looks
```

In each workload cluster, enable `agent.serviceMonitor` and `webhook.serviceMonitor` the same way
([example values](../../deploy/examples/eks/values-spoke.yaml)), and send those series to the hub's metrics store
with the cluster's name in the `cluster` label. Pricing has `pricing.serviceMonitor`.

The rules and ServiceMonitors need the Prometheus Operator CRDs, or the VictoriaMetrics operator with the
`kind` shown. Without them, leave the three blocks off. Keys: [hub values](../reference/values/hub.md).

## Alerts

All eight sit in one rule group, `kerf`. Each alert has its own block under
`hub.prometheusRule.alerts.<key>` with `enabled`, `for` and `severity`.

| Alert | Key | Default | Meaning | What to do |
|---|---|---|---|---|
| `KerfNoHubLeader` | `noLeader` | critical, 5m | No hub replica holds the leader lock (`max(kerf_hub_leader) == 0`). The recommender, readiness checks and sweeps stop. The API still answers. | [Hub HA and leader election](troubleshooting.md#hub-ha-and-leader-election). Usual cause: the database URL goes through a transaction-mode pooler or RDS Proxy. |
| `KerfAgentOffline` | `agentOffline` | warning, 10m | The hub has not had a config pull from a cluster's agent within 3 pull intervals (readiness check `agent.connected`). Nothing new is applied there. | [Agent not connecting](troubleshooting.md#agent-not-connecting). |
| `KerfReadinessBlocking` | `readinessBlocking` | warning, 30m | A blocking readiness check fails on a cluster, so it cannot reach *Automation ready*. | Open the cluster's readiness page and fix the named check: [readiness](readiness.md). |
| `KerfRecommenderFailing` | `recommenderFailing` | warning, 0m | A recommender run ended `failed` or `partial` within the last two recommender intervals. | `kubectl -n kerf logs -l app.kubernetes.io/name=hub \| grep "recommender run finished"`. Then check the metrics checks of the clusters involved. |
| `KerfRecommenderStale` | `recommenderStale` | warning, 15m | No successful run for a cluster for `staleAfter`: twice `config.recommender.interval` unless you set it. Recommendations are getting old. | Same as above. Start a run with `POST /v1/admin/recompute?cluster=<cluster>` once the cause is fixed. |
| `KerfWebhookErrors` | `webhookErrors` | warning, 15m | More than `ratio` (0.05) of a cluster's pod admissions ended in error over 10 minutes. Those pods start with their template resources, because the webhook's `failurePolicy` is `Ignore`. | `sum by (reason) (rate(kerf_webhook_admissions_total{result="error"}[10m]))` names the reason. [Webhook and cert-manager](troubleshooting.md#webhook-and-cert-manager). |
| `KerfPricingStale` | `pricingStale` | warning, 5m | No successful pricing refresh for `staleAfter` (30m), or the series is absent. Cost series go stale and savings fall back to default rates. | `kubectl -n kerf logs deploy/pricing` on the hub cluster. [Metrics source and pricing](troubleshooting.md#metrics-source-and-pricing). |
| `KerfUpdateAvailable` | `updateAvailable` | info, 1h | A newer stable release exists (`kerf_update_available == 1`). Exists only while `hub.updateCheck.enabled` is true. | [Upgrade](upgrade.md). |

Change a threshold in your values file, for example:

```yaml
hub:
  prometheusRule:
    alerts:
      webhookErrors: {ratio: 0.1}
      readinessBlocking: {for: 2h}
      updateAvailable: {enabled: false}
```

Add your own rules with `hub.prometheusRule.additionalRules`, a list of rule objects that join the `kerf` group:

```yaml
hub:
  prometheusRule:
    additionalRules:
      - alert: KerfRollback
        expr: sum by (cluster) (increase(kerf_actions_total{type="rolled_back"}[1h])) > 0
        labels: {severity: warning}
```

`kerf_actions_total` lives in the hub process and resets on restart, so read it with `increase()` or `rate()`.

## Metrics behind the alerts

| Series | Exported by | Used by |
|---|---|---|
| `kerf_hub_leader` | every hub replica | `KerfNoHubLeader` |
| `kerf_cluster_readiness{check,severity}` | the hub leader | `KerfAgentOffline`, `KerfReadinessBlocking` |
| `kerf_recommender_runs_total{status}` | every hub replica | `KerfRecommenderFailing` |
| `kerf_recommender_last_success_timestamp_seconds` | the hub leader | `KerfRecommenderStale` |
| `kerf_webhook_admissions_total{result,reason}` | the webhook of each workload cluster | `KerfWebhookErrors` |
| `kerf_pricing_last_success_timestamp_seconds` | the pricing service | `KerfPricingStale` |
| `kerf_update_available{current,latest}` | the hub leader | `KerfUpdateAvailable` |

Two more series are useful without an alert: `kerf_cluster_readiness_state{state}`, and
`kerf_pricing_nodes{status="skipped",reason}`, which names why a node is not priced.

## Dashboards

Four dashboards go into the Grafana folder `kerf`. They use PromQL only and query the data source with the uid
`victoriametrics` by default. Change it with the `datasource` variable at the top of each dashboard.

| Dashboard (uid) | Use it to |
|---|---|
| Overview (`ro-overview`) | See net and gross monthly savings by cluster, namespace and resource, usage against requests, workloads per mode, and recommender freshness. |
| Opportunities (`ro-opportunities`) | Rank containers by monthly savings, with confidence, risk flags, mode and phase. |
| Workload (`ro-workload`) | Understand one workload: usage percentiles, current and recommended values, throttling, restarts, OOM kills and automation events. |
| Automation (`ro-automation`) | Watch actions over time, rollbacks by reason, blocked and skipped reasons, and the health of the pipeline. |

Savings on every dashboard are estimates at current prices, not realized savings. The dashboards are generated
files: how to regenerate them is in [`dashboards/README.md`](../../dashboards/README.md).

## Related

- [Metrics contract](../contracts/metrics.md)
- [Cluster readiness](readiness.md)
- [Troubleshooting](troubleshooting.md)
- [Hub chart values](../reference/values/hub.md)
- [Maintenance and updates](../maintenance.md)
