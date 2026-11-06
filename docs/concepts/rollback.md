# Rollback

After every automatic change, the agent watches the workload. If it becomes unhealthy, the agent restores the
previous values. The watch runs in the workload cluster and does not need the hub.

## The health watch

The watch starts when a change is applied and lasts `watchDuration` (default `30m`). It rolls back on the first of
these:

| Signal | Threshold (default) | Applies when | Restores |
|---|---|---|---|
| OOM kills | `oomKills`: `1` | The change touched memory | Memory |
| Container restarts, for any reason | `restarts`: `3` | The change touched CPU or memory | Every resource the change touched |
| CPU throttling ratio | `throttlingRatio`: `"0.25"` | The change touched CPU | CPU |

- OOM kills and restarts come from pod status. They are counted from the change, across all pods of the workload,
  and a breach is acted on as soon as the pod status changes.
- The throttling ratio is the share of CFS periods in which the container was throttled since the change. Kerf reads
  it with PromQL from a Prometheus-compatible endpoint (the agent value `health.metricsURL`). It starts to count
  two minutes after the change, and only once the window holds at least 300 CFS periods. With no endpoint
  configured, throttling does not trigger a rollback, unless you enable `health.cadvisorFallback`.

You set the thresholds in a policy:

```yaml
spec:
  guardrails:
    rollback:
      watchDuration: 1h
      oomKills: 1
      restarts: 3
      throttlingRatio: "0.2"
```

If no threshold is crossed during `watchDuration`, the watch ends and the `HealthWatch` condition reads `Healthy`.

## What a rollback does

1. The agent marks the `RightsizingRecommendation` as `RolledBack`, so it stops keeping the applied values live and the
   webhook stops giving them to new pods.
2. It restores the previous values of the breached resources with the same strategy that applied them: it resizes the
   pods back for `in-place`, and patches the pod template back for `rollout` and `deferred`. For a
   [coordinated HPA](hpa.md), the HPA target goes back with the requests.
3. It records a `rolled_back` event with the reason `OOMKills`, `Restarts` or `Throttling`, the old and new values,
   and the numbers that crossed the threshold.

Rollbacks are never blocked by a [freeze window or a pause](apply-strategies.md#freeze-windows), and they do not wait
for the hub: the event is buffered and sent when the hub is reachable.

After a rollback, the agent does not apply the same values again. It waits for a recommendation with different
values. Decreases of the rolled-back resource stay blocked until the [cooldown](guardrails.md) (default `24h`) has
passed since the rollback: memory after an OOM-kill rollback, CPU after a throttling rollback, both after a restart
rollback. The hub also never recommends a decrease for a resource that is throttled or OOM-killed.

A rollback restores only the breached resources, as the table above shows. If the restore fails, the agent retries it and records a `failed`
event with the reason `RollbackError`.

## See what happened

- **Activity** in the web UI lists events such as `applied` and `rolled_back`. The workload page shows a timeline of its
  changes and rollbacks, and marks the phase as rolled back.
- **On the cluster:** `kubectl get rsr -A` shows the phase of each workload. `kubectl describe rsr <name> -n <namespace>`
  shows the `HealthWatch` condition (`Watching`, `Healthy`, or the rollback reason) and the Kubernetes events. The
  agent records a `rolled_back` event as a `Warning`.
- **Through the API:** `GET /v1/audit?type=rolled_back` returns the events, and `GET /v1/workloads/{id}/history`
  returns the history of one workload ([hub API](../reference/api/README.md)).
- **In metrics:** the hub counts events in `kerf_actions_total{type="rolled_back"}`, with the reason and the
  resource as labels ([metrics](../contracts/metrics.md)).

```bash
kubectl get rsr -n payments
kubectl describe rsr checkout -n payments
```

## Related

- [Guardrails](guardrails.md)
- [Apply strategies](apply-strategies.md)
- [Events contract](../contracts/events.md)
- [Alerts and dashboards](../operate/alerts-dashboards.md)
- [Troubleshooting](../operate/troubleshooting.md)
